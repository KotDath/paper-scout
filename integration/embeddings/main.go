// T06 executable HTTP integration checks. Fixture failures are not live evidence.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"time"

	"paper-scout/internal/config"
	"paper-scout/internal/embeddings"
)

func main() {
	live := flag.Bool("live", false, "use existing local Ollama; no downloads or paid requests")
	envFile := flag.String("env-file", "", "optional dotenv path, loaded only by Go in live mode")
	flag.Parse()
	var err error
	if *live {
		err = liveCheck(*envFile)
	} else if *envFile != "" {
		err = errors.New("env-file requires live mode")
	} else {
		err = fixtureChecks()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
}

func checkVectors(r embeddings.Result, count int) error {
	if len(r.Vectors) != count || len(r.InputHashes) != count || r.Identity.Dimensions != 1024 || r.Identity.EncodingVersion != embeddings.EncodingVersion {
		return errors.New("result metadata/count mismatch")
	}
	for _, v := range r.Vectors {
		if len(v) != 1024 {
			return errors.New("wrong dimensions")
		}
		norm := 0.0
		for _, x := range v {
			if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
				return errors.New("nonfinite vector")
			}
			norm += float64(x) * float64(x)
		}
		if math.Abs(norm-1) > 1e-6 {
			return errors.New("vector is not L2 normalized")
		}
	}
	return nil
}

func liveCheck(envFile string) error {
	if _, err := config.Load(envFile); err != nil {
		return err
	}
	cfg, err := embeddings.ConfigFromEnv()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	client, err := embeddings.New(ctx, cfg)
	if err != nil {
		return err
	}
	id := client.Identity()
	fmt.Printf("PASS: live model=%s digest=%s dimensions=%d encoding=%s\n", id.Model, id.Digest, id.Dimensions, id.EncodingVersion)
	docs := []string{
		embeddings.DocumentText("Agent memory consolidation", "Method", "An autonomous agent stores experiences in long-term memory and consolidates episodic observations into semantic knowledge for future tasks."),
		embeddings.DocumentText("Stellar spectroscopy", "Method", "We measure the absorption lines and atmospheric temperature of distant stars using an optical telescope."),
	}
	documents, err := client.Embed(ctx, embeddings.Document, docs)
	if err != nil {
		return err
	}
	if err := checkVectors(documents, 2); err != nil {
		return err
	}
	queries, err := client.Embed(ctx, embeddings.Query, []string{"How do agents consolidate long-term memories?", "Как агенты консолидируют долговременную память?"})
	if err != nil {
		return err
	}
	if err := checkVectors(queries, 2); err != nil {
		return err
	}
	if documents.Identity != queries.Identity {
		return errors.New("live identities differ")
	}
	for i, q := range queries.Vectors {
		scores := [2]float64{}
		for d, v := range documents.Vectors {
			for j, x := range v {
				scores[d] += float64(x) * float64(q[j])
			}
		}
		if scores[0] <= scores[1] {
			return errors.New("live semantic smoke check ranked unrelated passage first")
		}
		fmt.Printf("PASS: live query %d memory cosine=%.6f unrelated cosine=%.6f\n", i+1, scores[0], scores[1])
	}
	fmt.Printf("PASS: real document batch and EN/RU query batch; four finite normalized 1024-vectors; prompt tokens document=%d query=%d\n", documents.PromptTokens, queries.PromptTokens)
	// 40,000 ordinary tokens exceed the model's 32,768 context, while the
	// 80,000-byte string stays below our input byte cap. No hidden shortening.
	r, err := client.Embed(ctx, embeddings.Document, []string{strings.Repeat("a ", 40000)})
	var failure *embeddings.Error
	if !errors.As(err, &failure) || failure.Code != "context_limit" || failure.HTTPStatus != 400 || len(r.Vectors) != 0 {
		return errors.New("live over-context input did not fail explicitly")
	}
	fmt.Println("PASS: live 40,000-token input rejected by Ollama HTTP 400 as context_limit with truncate=false; no vectors returned")
	return nil
}

func fixtureChecks() error {
	ctx := context.Background()
	digest := strings.Repeat("a", 64)
	var mu sync.Mutex
	scenario := "ok"
	tagCount, embedCount, allCount := 0, 0, 0
	protocolFailure := false
	redirected := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		redirected = true
		mu.Unlock()
		w.WriteHeader(500)
	}))
	defer target.Close()
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		allCount++
		s := scenario
		if r.Header.Get("Content-Type") != "application/json" {
			protocolFailure = true
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if s == "wait-tags" && r.URL.Path == "/api/tags" {
			<-r.Context().Done()
			return
		}
		switch r.URL.Path {
		case "/api/tags":
			mu.Lock()
			tagCount++
			n := tagCount
			mu.Unlock()
			if r.Method != "GET" {
				mu.Lock()
				protocolFailure = true
				mu.Unlock()
			}
			d := digest
			if s == "bad-digest" {
				d = "invalid"
			}
			if s == "changed" || (s == "changed-during" && n >= 2) || (s == "changed-new" && n >= 2) {
				d = strings.Repeat("b", 64)
			}
			if s == "missing" {
				fmt.Fprint(w, `{"models":[]}`)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"models": []any{map[string]string{"name": embeddings.Model, "digest": d}}})
		case "/api/show":
			var b map[string]string
			if r.Method != "POST" || json.NewDecoder(r.Body).Decode(&b) != nil || b["model"] != embeddings.Model {
				mu.Lock()
				protocolFailure = true
				mu.Unlock()
			}
			dims := 1024
			if s == "show-dimensions" {
				dims = 768
			}
			capabilities := []string{"embedding"}
			if s == "show-capabilities" {
				capabilities = []string{"completion"}
			}
			remote := ""
			if s == "show-remote" {
				remote = "https://example.invalid"
			}
			json.NewEncoder(w).Encode(map[string]any{"capabilities": capabilities, "remote_host": remote, "model_info": map[string]any{"general.architecture": "qwen3", "qwen3.embedding_length": dims, "qwen3.context_length": 32768}})
		case "/api/embed":
			mu.Lock()
			embedCount++
			mu.Unlock()
			var b struct {
				Model      string         `json:"model"`
				Input      []string       `json:"input"`
				Truncate   *bool          `json:"truncate"`
				Options    map[string]int `json:"options"`
				Dimensions *int           `json:"dimensions"`
			}
			if r.Method != "POST" || json.NewDecoder(r.Body).Decode(&b) != nil || b.Model != embeddings.Model || b.Truncate == nil || *b.Truncate || b.Options["num_ctx"] != 32768 || b.Dimensions != nil {
				mu.Lock()
				protocolFailure = true
				mu.Unlock()
				w.WriteHeader(400)
				return
			}
			if s == "query" && (len(b.Input) != 2 || b.Input[0] != embeddings.QueryInstruction+"memory?" || b.Input[1] != embeddings.QueryInstruction+"память?") {
				mu.Lock()
				protocolFailure = true
				mu.Unlock()
			}
			if s == "document" && (len(b.Input) != 2 || b.Input[0] != embeddings.DocumentText("Title", "Method", "passage") || b.Input[1] != "unchanged\ntext") {
				mu.Lock()
				protocolFailure = true
				mu.Unlock()
			}
			if s == "wait" {
				<-r.Context().Done()
				return
			}
			if s == "redirect" {
				w.Header().Set("Location", target.URL)
				w.WriteHeader(307)
				return
			}
			if s == "oversized" {
				fmt.Fprint(w, strings.Repeat("x", (8<<20)+1))
				return
			}
			if s == "malformed" {
				fmt.Fprint(w, `{"private-input":`)
				return
			}
			if strings.HasPrefix(s, "status-") {
				status := 0
				fmt.Sscanf(s, "status-%d", &status)
				w.Header().Set("Retry-After", "2")
				w.WriteHeader(status)
				fmt.Fprint(w, `{"error":"private-input"}`)
				return
			}
			if s == "context" {
				w.WriteHeader(400)
				fmt.Fprint(w, `{"error":"private-input exceeds the context length"}`)
				return
			}
			vectors := make([][]any, len(b.Input))
			for i := range vectors {
				vectors[i] = make([]any, 1024)
				for j := range vectors[i] {
					vectors[i][j] = float64(j + 1 + i)
				}
			}
			switch s {
			case "dimensions":
				vectors[0] = vectors[0][:1023]
			case "count":
				vectors = vectors[:0]
			case "zero":
				for j := range vectors[0] {
					vectors[0][j] = 0
				}
			case "null":
				vectors[0][0] = nil
			case "overflow":
				vectors[0][0] = 1e100
			case "nonfinite":
				fmt.Fprint(w, `{"model":"qwen3-embedding:0.6b","embeddings":[[1e999]],"prompt_eval_count":2}`)
				return
			}
			model := embeddings.Model
			if s == "model" {
				model = "wrong"
			}
			var tokens any = 17
			if s == "no-tokens" {
				tokens = nil
			}
			json.NewEncoder(w).Encode(map[string]any{"model": model, "embeddings": vectors, "prompt_eval_count": tokens})
		default:
			w.WriteHeader(404)
		}
	}))
	defer fixture.Close()
	set := func(s string) { mu.Lock(); scenario = s; tagCount = 0; mu.Unlock() }
	newClient := func(timeout time.Duration) (*embeddings.EmbeddingClient, error) {
		return embeddings.New(ctx, embeddings.Config{BaseURL: fixture.URL, Timeout: timeout})
	}
	client, err := newClient(0)
	if err != nil {
		return err
	}
	for _, mode := range []embeddings.Mode{embeddings.Document, embeddings.Query} {
		texts := []string{embeddings.DocumentText("Title", "Method", "passage"), "unchanged\ntext"}
		if mode == embeddings.Query {
			set("query")
			texts = []string{"memory?", "память?"}
		} else {
			set("document")
		}
		r, err := client.Embed(ctx, mode, texts)
		if err != nil {
			return err
		}
		if err := checkVectors(r, 2); err != nil {
			return err
		}
		for i, text := range texts {
			if mode == embeddings.Query {
				text = embeddings.QueryInstruction + text
			}
			sum := sha256.Sum256([]byte(text))
			if r.InputHashes[i] != hex.EncodeToString(sum[:]) {
				return errors.New("wrong exact input hash")
			}
			norm := 0.0
			for j := range 1024 {
				norm += float64((j + 1 + i) * (j + 1 + i))
			}
			if math.Abs(float64(r.Vectors[i][0])-float64(i+1)/math.Sqrt(norm)) > 1e-8 {
				return errors.New("HTTP batch vector ordering failed")
			}
		}
		if r.Identity != client.Identity() || r.Mode != mode || r.PromptTokens != 17 {
			return errors.New("result identity/mode/usage mismatch")
		}
	}
	fmt.Println("PASS: HTTP model preflight, batch order, exact document/query encoding, truncate=false, full context, no dimension reduction, input hashes, normalized float32 vectors")
	set("ok")
	concurrent := make(chan error, 4)
	for range 4 {
		go func() {
			r, err := client.Embed(ctx, embeddings.Query, []string{"concurrent question"})
			if err == nil {
				err = checkVectors(r, 1)
			}
			concurrent <- err
		}()
	}
	for range 4 {
		if err := <-concurrent; err != nil {
			return err
		}
	}
	fmt.Println("PASS: four concurrent encoding requests share one immutable client identity")
	for s, code := range map[string]string{"dimensions": "embedding_incompatible", "count": "invalid_response", "zero": "invalid_vector", "null": "invalid_vector", "overflow": "invalid_vector", "nonfinite": "invalid_response", "model": "embedding_incompatible", "no-tokens": "invalid_response", "malformed": "invalid_response", "oversized": "response_too_large", "redirect": "embedding_error", "context": "context_limit", "status-400": "invalid_request", "status-404": "model_unavailable", "status-429": "embedding_rate_limited", "status-503": "embedding_unavailable"} {
		set(s)
		mu.Lock()
		before := embedCount
		mu.Unlock()
		r, err := client.Embed(ctx, embeddings.Document, []string{"private-input"})
		var failure *embeddings.Error
		if !errors.As(err, &failure) || failure.Code != code || len(r.Vectors) != 0 || strings.Contains(err.Error(), "private-input") {
			return fmt.Errorf("HTTP %s rejection failed", s)
		}
		if s == "status-429" || s == "status-503" {
			if !failure.Retryable || failure.RetryAfter != 2*time.Second {
				return errors.New("retry classification failed")
			}
		}
		mu.Lock()
		once := embedCount == before+1
		mu.Unlock()
		if !once {
			return errors.New("hidden retry")
		}
	}
	fmt.Println("PASS: HTTP malformed/count/dimension/model/null/nonfinite/zero/overflow rejection; bounded responses; context/400/404/429/503; sanitized errors; no retries/redirects")
	for _, s := range []string{"bad-digest", "missing", "show-dimensions", "show-capabilities", "show-remote", "changed-new"} {
		set(s)
		_, err := newClient(0)
		if err == nil {
			return fmt.Errorf("preflight %s accepted", s)
		}
	}
	set("ok")
	_, err = embeddings.New(ctx, embeddings.Config{BaseURL: fixture.URL, ExpectedDigest: strings.Repeat("b", 64)})
	if err == nil {
		return errors.New("expected digest pin ignored")
	}
	for _, s := range []string{"changed", "changed-during"} {
		set(s)
		r, err := client.Embed(ctx, embeddings.Document, []string{"passage"})
		var failure *embeddings.Error
		if !errors.As(err, &failure) || failure.Code != "embedding_incompatible" || len(r.Vectors) != 0 {
			return errors.New("digest drift not rejected")
		}
	}
	fmt.Println("PASS: missing/bad digest, wrong metadata/capabilities, remote model, expected digest mismatch, tag change during preflight/before/after batch rejected")
	set("ok")
	short, err := newClient(80 * time.Millisecond)
	if err != nil {
		return err
	}
	for _, s := range []string{"wait-tags", "wait"} {
		for _, cancelled := range []bool{false, true} {
			set(s)
			callCtx, cancel := context.WithCancel(ctx)
			if cancelled {
				time.AfterFunc(20*time.Millisecond, cancel)
			}
			r, err := short.Embed(callCtx, embeddings.Document, []string{"passage"})
			cancel()
			want := context.DeadlineExceeded
			if cancelled {
				want = context.Canceled
			}
			if !errors.Is(err, want) || len(r.Vectors) != 0 {
				return errors.New("in-flight cancellation/deadline failed")
			}
		}
	}
	set("ok")
	mu.Lock()
	before := allCount
	mu.Unlock()
	for _, cfg := range []embeddings.Config{
		{BaseURL: "http://192.0.2.1:11434"}, {BaseURL: fixture.URL + "/unexpected"},
		{BaseURL: fixture.URL + "?secret=private-input"}, {BaseURL: "http://user:private-input@127.0.0.1"},
		{BaseURL: fixture.URL, Model: "wrong"}, {BaseURL: fixture.URL, Dimensions: 768},
		{BaseURL: fixture.URL, ExpectedDigest: "bad"}, {BaseURL: fixture.URL, Timeout: -time.Second},
	} {
		if _, err := embeddings.New(ctx, cfg); err == nil || strings.Contains(err.Error(), "private-input") {
			return errors.New("invalid configuration not rejected safely before HTTP")
		}
	}
	for _, input := range []struct {
		mode  embeddings.Mode
		texts []string
	}{
		{embeddings.Query, nil}, {"invalid", []string{"x"}}, {embeddings.Document, []string{" "}},
		{embeddings.Document, []string{string([]byte{0xff})}}, {embeddings.Document, make([]string, 33)},
		{embeddings.Document, []string{strings.Repeat("x", embeddings.MaxInputBytes+1)}},
		{embeddings.Document, []string{strings.Repeat("x", embeddings.MaxInputBytes), strings.Repeat("x", embeddings.MaxInputBytes), strings.Repeat("x", embeddings.MaxInputBytes), strings.Repeat("x", embeddings.MaxInputBytes), "x"}},
	} {
		if _, err := client.Embed(ctx, input.mode, input.texts); err == nil {
			return errors.New("invalid input sent")
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := client.Embed(cancelled, embeddings.Document, []string{"x"}); !errors.Is(err, context.Canceled) {
		return errors.New("pre-cancellation failed")
	}
	mu.Lock()
	noRequests := allCount == before
	bad := protocolFailure || redirected
	mu.Unlock()
	if !noRequests || bad {
		return errors.New("protocol violation, redirect, or pre-validation network request")
	}
	closed := httptest.NewServer(http.NotFoundHandler())
	closedURL := closed.URL
	closed.Close()
	_, err = embeddings.New(ctx, embeddings.Config{BaseURL: closedURL})
	var failure *embeddings.Error
	if !errors.As(err, &failure) || failure.Code != "embedding_unavailable" || strings.Contains(err.Error(), closedURL) {
		return errors.New("refused connection classification failed")
	}
	fmt.Println("PASS: in-flight preflight/embedding cancellation and deadline, pre-cancelled/invalid/oversized input sends no HTTP, refused connection; all servers use ephemeral loopback ports")
	return nil
}

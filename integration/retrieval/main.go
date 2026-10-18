// T08 live integration executable. The small labeled corpus is synthetic public
// fixture content; inference, auth, SQLite, extraction and retrieval are real.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/bcrypt"
	"paper-scout/internal/auth"
	"paper-scout/internal/config"
	"paper-scout/internal/embeddings"
	"paper-scout/internal/httpapi"
	"paper-scout/internal/retrieval"
	"paper-scout/internal/store"
)

func main() {
	runtime := flag.String("runtime", "", "temporary directory inside assigned worktree")
	envFile := flag.String("env-file", "", "optional dotenv path loaded only by application config")
	artifact := flag.String("artifact", "", "sanitized JSON evaluation path")
	flag.Parse()
	if err := run(*runtime, *envFile, *artifact); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
}

type evaluation struct {
	Name     string                 `json:"name"`
	Expected string                 `json:"expected_section"`
	Result   retrieval.SearchResult `json:"result"`
}

func run(runtime, envFile, artifact string) error {
	if runtime == "" {
		return errors.New("runtime is required")
	}
	if _, err := config.Load(envFile); err != nil {
		return err
	}
	cfg, err := embeddings.ConfigFromEnv()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	// The proxy forwards all healthy requests to actual Ollama. An explicit 503
	// switch gives deterministic outage evidence without stopping shared Ollama.
	backend := cfg.BaseURL
	if backend == "" {
		backend = "http://127.0.0.1:11434"
	}
	target, err := url.Parse(backend)
	if err != nil {
		return errors.New("invalid live backend configuration")
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	proxy.Transport = transport
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) { w.WriteHeader(503) }
	var outage atomic.Bool
	var calls atomic.Int32
	var beforeEmbed atomic.Pointer[func()]
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if outage.Load() {
			w.WriteHeader(503)
			return
		}
		if r.URL.Path == "/api/embed" {
			calls.Add(1)
			if callback := beforeEmbed.Swap(nil); callback != nil {
				(*callback)()
			}
		}
		proxy.ServeHTTP(w, r)
	}))
	defer gateway.Close()
	cfg.BaseURL = gateway.URL
	client, err := embeddings.New(ctx, cfg)
	if err != nil {
		return fmt.Errorf("live Ollama preflight: %w", err)
	}
	fmt.Printf("PASS live Ollama: model=%s digest=%s dimensions=%d encoding=%s\n", client.Identity().Model, client.Identity().Digest, client.Identity().Dimensions, client.Identity().EncodingVersion)
	dbPath := filepath.Join(runtime, "retrieval.sqlite")
	db, err := store.Open(ctx, dbPath)
	if err != nil {
		return err
	}
	defer func() {
		if db != nil {
			db.Close()
		}
	}()
	password := "integration-only-password"
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		return err
	}
	accounts := []store.SeedAccount{}
	for _, id := range []string{"owner_a", "owner_b"} {
		accounts = append(accounts, store.SeedAccount{User: store.User{ID: id, Login: id, CreatedAt: time.Now().UTC().Format(time.RFC3339)}, PasswordHash: string(hash)})
	}
	if err = db.SeedAccounts(ctx, accounts); err != nil {
		return err
	}
	docs, err := importCorpus(ctx, db, runtime)
	if err != nil {
		return err
	}
	service := retrieval.New(db.DB, client)
	for i, d := range docs {
		owner := "owner_a"
		if i == 1 {
			owner = "owner_b"
		}
		if err = service.AddPaper(ctx, owner, d.PaperVersionID); err != nil {
			return err
		}
		status, err := service.Index(ctx, owner, d.ID)
		if err != nil || status.Status != "ready" || status.IndexedChunks != len(d.Chunks) {
			return fmt.Errorf("live index not ready: %+v %v", status, err)
		}
	}
	before := calls.Load()
	cached, err := service.Index(ctx, "owner_a", docs[0].ID)
	if err != nil || cached.CacheHits != len(docs[0].Chunks) || calls.Load() != before {
		return fmt.Errorf("document embedding cache did not avoid inference: %+v %v", cached, err)
	}
	fmt.Printf("PASS full corpus indexed: %d + %d chunks; repeat index reused %d vectors without inference\n", len(docs[0].Chunks), len(docs[1].Chunks), cached.CacheHits)
	server := httptest.NewServer(httpapi.New(db, service))
	defer func() { server.Close() }()
	tokens := map[string]string{}
	for _, owner := range []string{"owner_a", "owner_b"} {
		var login auth.LoginResult
		code, err := request(server.URL, "POST", "/api/v1/auth/login", "", map[string]string{"login": owner, "password": password}, &login)
		if err != nil || code != 200 || login.SessionToken == "" {
			return errors.New("real HTTP login failed")
		}
		tokens[owner] = login.SessionToken
	}
	search := func(owner string, req retrieval.SearchRequest) (retrieval.SearchResult, int, error) {
		var result retrieval.SearchResult
		code, err := request(server.URL, "POST", "/api/v1/library/search", tokens[owner], req, &result)
		return result, code, err
	}
	cases := []struct{ name, query, terms, expected string }{
		{"exact", "episodic consolidation", "", "Memory consolidation"},
		{"paraphrase", "How can an autonomous assistant transform past experiences into reusable knowledge for future tasks?", "", "Memory consolidation"},
		{"russian", "Как агент объединяет прошлый опыт в долговременную память для будущих задач?", "", "Memory consolidation"},
		{"russian_with_terms", "Как агент объединяет прошлый опыт в долговременную память?", "episodic consolidation", "Memory consolidation"},
		{"unrelated", "Medieval ceramic glazing recipes kiln", "", ""},
	}
	evaluations := []evaluation{}
	for _, c := range cases {
		result, code, err := search("owner_a", retrieval.SearchRequest{Query: c.query, LexicalTerms: c.terms})
		if err != nil || code != 200 || result.RetrievalMode != "hybrid" || result.IndexCoverage != 1 || len(result.DenseCandidates) == 0 {
			return fmt.Errorf("%s hybrid search failed: HTTP %d mode=%s err=%v", c.name, code, result.RetrievalMode, err)
		}
		if err = validateRanks(result); err != nil {
			return err
		}
		relevant := c.expected == ""
		for _, h := range result.Hits {
			if h.Section == c.expected {
				relevant = true
			}
		}
		if !relevant {
			return fmt.Errorf("%s: labeled passage absent from top8", c.name)
		}
		if c.name == "exact" && len(result.LexicalCandidates) == 0 {
			return errors.New("exact query did not execute BM25")
		}
		if c.name == "unrelated" && len(result.LexicalCandidates) != 0 {
			return errors.New("unrelated query unexpectedly matched lexical corpus")
		}
		for _, h := range append(append(result.Hits, result.LexicalCandidates...), result.DenseCandidates...) {
			if h.PaperVersionID != docs[0].PaperVersionID {
				return errors.New("cross-owner candidate leaked")
			}
			var evidence retrieval.Hit
			code, err := request(server.URL, "GET", "/api/v1/evidence/"+h.EvidenceID, tokens["owner_a"], nil, &evidence)
			if err != nil || code != 200 || evidence.Text != h.Text || evidence.CharStart != h.CharStart || evidence.CharEnd != h.CharEnd {
				return errors.New("evidence HTTP resolution differs from ranked passage")
			}
		}
		evaluations = append(evaluations, evaluation{Name: c.name, Expected: c.expected, Result: result})
		fmt.Printf("PASS query=%s lexical=%d dense=%d top8=%d has_relevance_label=%t\n", c.name, len(result.LexicalCandidates), len(result.DenseCandidates), len(result.Hits), c.expected != "")
	}
	// Owner B has just one source. Both direct IDs and scope arguments must remain
	// incapable of granting access to A, even when a public cache entry exists.
	result, code, err := search("owner_b", retrieval.SearchRequest{Query: "episodic consolidation", PaperVersionIDs: []string{docs[0].PaperVersionID}})
	if err != nil || code != 200 || result.TotalChunks != 0 || len(result.Hits) != 0 || len(result.LexicalCandidates) != 0 || len(result.DenseCandidates) != 0 {
		return errors.New("foreign scope leaked candidates")
	}
	var missing map[string]any
	foreignCode, err := request(server.URL, "GET", "/api/v1/evidence/"+docs[0].Chunks[0].ID, tokens["owner_b"], nil, &missing)
	if err != nil || foreignCode != 404 {
		return errors.New("foreign evidence not hidden")
	}
	code, err = request(server.URL, "GET", "/api/v1/evidence/missing", tokens["owner_b"], nil, &missing)
	if err != nil || code != foreignCode {
		return errors.New("missing and foreign evidence differ")
	}
	if _, err = service.Index(ctx, "owner_b", docs[0].ID); !errors.Is(err, retrieval.ErrNotFound) {
		return errors.New("foreign index allowed")
	}
	result, code, err = search("owner_b", retrieval.SearchRequest{Query: "episodic consolidation"})
	if err != nil || code != 200 || result.TotalChunks != len(docs[1].Chunks) {
		return errors.New("B library not isolated")
	}
	for _, h := range append(result.DenseCandidates, result.LexicalCandidates...) {
		if h.PaperVersionID != docs[1].PaperVersionID {
			return errors.New("B branch leaked A")
		}
	}
	code, err = request(server.URL, "POST", "/api/v1/library/search", "", retrieval.SearchRequest{Query: "memory"}, &missing)
	if err != nil || code != 401 {
		return errors.New("search without auth allowed")
	}
	for _, q := range []string{`" OR * NEAR(foo) title:bar; DROP TABLE users; --`, `***`, `память AND NOT "`} {
		if _, code, err = search("owner_a", retrieval.SearchRequest{Query: q}); err != nil || code != 200 {
			return fmt.Errorf("unsafe lexical syntax escaped literal tokenization: %v HTTP %d", err, code)
		}
	}
	for _, req := range []retrieval.SearchRequest{{Query: ""}, {Query: "memory", Limit: 9}, {Query: strings.Repeat("x", 4001)}} {
		if _, code, err = search("owner_a", req); err != nil || code != 400 {
			return errors.New("invalid search accepted")
		}
	}
	fmt.Println("PASS two real accounts: both branches scoped before top-k; foreign index/evidence denied; safe FTS syntax and input limits")
	// Stored incompatibility/corruption must be excluded even with matching length.
	if _, err = db.DB.ExecContext(ctx, `UPDATE chunk_embeddings SET model_digest=? WHERE chunk_id IN (SELECT id FROM chunks WHERE document_id=?)`, strings.Repeat("0", 64), docs[0].ID); err != nil {
		return err
	}
	result, code, err = search("owner_a", retrieval.SearchRequest{Query: "episodic consolidation"})
	if err != nil || code != 200 || result.RetrievalMode != "hybrid_partial" || result.IndexCoverage != 0 || len(result.DenseCandidates) != 0 || len(result.LexicalCandidates) == 0 {
		return errors.New("incompatible vectors silently compared")
	}
	if _, err = db.DB.ExecContext(ctx, `UPDATE chunk_embeddings SET model_digest=? WHERE chunk_id IN (SELECT id FROM chunks WHERE document_id=?)`, client.Identity().Digest, docs[0].ID); err != nil {
		return err
	}
	if _, err = db.DB.ExecContext(ctx, `UPDATE chunk_embeddings SET vector_blob=zeroblob(4096) WHERE chunk_id=?`, docs[0].Chunks[0].ID); err != nil {
		return err
	}
	result, code, err = search("owner_a", retrieval.SearchRequest{Query: "episodic consolidation"})
	if err != nil || code != 200 || result.RetrievalMode != "hybrid_partial" || result.IndexedChunks != len(docs[0].Chunks)-1 {
		return errors.New("corrupt normalized vector was compared")
	}
	if _, err = service.Index(ctx, "owner_a", docs[0].ID); err != nil {
		return err
	}
	// Verify the rest of the embedding identity and chunk input are enforced,
	// independently of model digest and vector length.
	for _, column := range []string{"model_id", "encoding_version", "chunking_version", "input_hash"} {
		var original string
		if err = db.DB.QueryRowContext(ctx, `SELECT `+column+` FROM chunk_embeddings WHERE chunk_id=?`, docs[0].Chunks[0].ID).Scan(&original); err != nil {
			return err
		}
		changed := "incompatible"
		if column == "input_hash" {
			changed = strings.Repeat("0", 64)
		}
		if _, err = db.DB.ExecContext(ctx, `UPDATE chunk_embeddings SET `+column+`=? WHERE chunk_id=?`, changed, docs[0].Chunks[0].ID); err != nil {
			return err
		}
		result, code, err = search("owner_a", retrieval.SearchRequest{Query: "episodic consolidation"})
		if err != nil || code != 200 || result.RetrievalMode != "hybrid_partial" || result.IndexedChunks != len(docs[0].Chunks)-1 {
			return fmt.Errorf("incompatible %s was compared", column)
		}
		if _, err = db.DB.ExecContext(ctx, `UPDATE chunk_embeddings SET `+column+`=? WHERE chunk_id=?`, original, docs[0].Chunks[0].ID); err != nil {
			return err
		}
	}
	outage.Store(true)
	if _, err = db.DB.ExecContext(ctx, `UPDATE chunk_embeddings SET vector_blob=zeroblob(4096) WHERE chunk_id=?`, docs[0].Chunks[0].ID); err != nil {
		return err
	}
	partial, partialErr := service.Index(ctx, "owner_a", docs[0].ID)
	if partialErr == nil || partial.Status != "partial" || partial.IndexedChunks != len(docs[0].Chunks)-1 || partial.LastError != "embedding_unavailable" {
		return errors.New("index did not retain partial cache progress on outage")
	}
	outage.Store(false)
	if _, err = service.Index(ctx, "owner_a", docs[0].ID); err != nil {
		return err
	}
	outage.Store(true)
	result, code, err = search("owner_a", retrieval.SearchRequest{Query: "episodic consolidation"})
	if err != nil || code != 200 || result.RetrievalMode != "lexical_only" || !slices.Contains(result.Reasons, "embedding_unavailable") || len(result.LexicalCandidates) == 0 || len(result.DenseCandidates) != 0 {
		return errors.New("live backend outage not explicit lexical_only")
	}
	// Force a new exact input so an outage cannot be hidden by an index cache hit.
	if _, err = db.DB.ExecContext(ctx, `UPDATE paper_versions SET title=title||' changed' WHERE id=?`, docs[0].PaperVersionID); err != nil {
		return err
	}
	var invalidated string
	if err = db.DB.QueryRowContext(ctx, `SELECT status FROM document_indexes WHERE document_id=?`, docs[0].ID).Scan(&invalidated); err != nil || invalidated != "pending" {
		return errors.New("public title mutation retained stale ready status")
	}
	failed, err := service.Index(ctx, "owner_a", docs[0].ID)
	if err == nil || failed.Status != "failed" || failed.LastError != "embedding_unavailable" {
		return errors.New("index outage falsely ready")
	}
	if _, err = db.DB.ExecContext(ctx, `UPDATE paper_versions SET title=? WHERE id=?`, corpusTitle, docs[0].PaperVersionID); err != nil {
		return err
	}
	outage.Store(false)
	if _, err = service.Index(ctx, "owner_a", docs[0].ID); err != nil {
		return err
	}
	fmt.Println("PASS actual backend outage via HTTP 503 injection, stale model digest, corrupt vector, title/input hash invalidation, and index recovery")
	// An additional SQL fixture references exactly the same source range.
	// Public cache reuse must avoid inference, and context must deduplicate it.
	if _, err = db.DB.ExecContext(ctx, `INSERT INTO chunks SELECT 'duplicate_evidence',document_id,999,text,char_start,char_end,page_start,page_end,section FROM chunks WHERE id=?`, docs[0].Chunks[0].ID); err != nil {
		return err
	}
	before = calls.Load()
	duplicateIndex, err := service.Index(ctx, "owner_a", docs[0].ID)
	if err != nil || calls.Load() != before || duplicateIndex.CacheHits != len(docs[0].Chunks)+1 {
		return errors.New("exact public input cache not reused for another evidence ID")
	}
	result, code, err = search("owner_a", retrieval.SearchRequest{Query: "episodic consolidation"})
	duplicates := 0
	for _, h := range result.Hits {
		if h.Section == "Memory consolidation" {
			duplicates++
		}
	}
	if err != nil || code != 200 || duplicates != 1 {
		return errors.New("strongly overlapping evidence duplicated in context")
	}
	if _, err = db.DB.ExecContext(ctx, `DELETE FROM chunks WHERE id='duplicate_evidence'`); err != nil {
		return err
	}
	if _, err = service.Index(ctx, "owner_a", docs[0].ID); err != nil {
		return err
	}
	// Change metadata from inside the real inference HTTP boundary. This also
	// proves indexing holds no database transaction across its network call.
	if _, err = db.DB.ExecContext(ctx, `UPDATE paper_versions SET title=title||' indexing' WHERE id=?`, docs[0].PaperVersionID); err != nil {
		return err
	}
	mutationError := make(chan error, 1)
	mutate := func() {
		_, mutationErr := db.DB.ExecContext(ctx, `UPDATE paper_versions SET title=title||' changed_during_inference' WHERE id=?`, docs[0].PaperVersionID)
		mutationError <- mutationErr
	}
	beforeEmbed.Store(&mutate)
	stale, staleErr := service.Index(ctx, "owner_a", docs[0].ID)
	if staleErr == nil || stale.Status != "partial" || stale.LastError != "source_changed" {
		return errors.New("source changed during inference but index published ready")
	}
	select {
	case mutationErr := <-mutationError:
		if mutationErr != nil {
			return mutationErr
		}
	default:
		return errors.New("source mutation callback did not execute")
	}
	if _, err = db.DB.ExecContext(ctx, `UPDATE paper_versions SET title=? WHERE id=?`, corpusTitle, docs[0].PaperVersionID); err != nil {
		return err
	}
	if _, err = service.Index(ctx, "owner_a", docs[0].ID); err != nil {
		return err
	}
	fmt.Println("PASS model/encoding/chunking/input compatibility, partial progress, public-input cache reuse and overlapping evidence deduplication")
	fmt.Println("PASS concurrent source mutation during actual inference cannot publish stale ready status")
	if err = scopeLimit(ctx, db, service, docs[0]); err != nil {
		return err
	}
	// Restart storage and HTTP, retaining real opaque sessions and vectors.
	server.Close()
	if err = db.Close(); err != nil {
		return err
	}
	db = nil
	db, err = store.Open(ctx, dbPath)
	if err != nil {
		return err
	}
	service = retrieval.New(db.DB, client)
	server = httptest.NewServer(httpapi.New(db, service))
	before = calls.Load()
	cached, err = service.Index(ctx, "owner_a", docs[0].ID)
	if err != nil || calls.Load() != before || cached.CacheHits != len(docs[0].Chunks) {
		return errors.New("cache did not survive reopen")
	}
	result, code, err = search("owner_a", retrieval.SearchRequest{Query: "episodic consolidation"})
	if err != nil || code != 200 || result.RetrievalMode != "hybrid" || result.IndexCoverage != 1 {
		return errors.New("hybrid/session did not survive reopen")
	}
	for _, h := range result.Hits {
		found := false
		for _, prior := range evaluations[0].Result.Hits {
			if h.EvidenceID == prior.EvidenceID {
				found = true
			}
		}
		if !found {
			return errors.New("evidence/ranking changed after reopen")
		}
	}
	if artifact != "" {
		data, _ := json.MarshalIndent(evaluations, "", "  ")
		if err = os.WriteFile(artifact, data, 0600); err != nil {
			return err
		}
	}
	fmt.Println("PASS persisted hybrid index, membership, opaque sessions, stable evidence/ranks survive SQLite + HTTP reopen; no analysis coverage claimed")
	return nil
}

func request(base, method, path, token string, body, output any) (int, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, base+path, reader)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := http.Client{Timeout: 125 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return 0, errors.New("integration HTTP request failed")
	}
	defer res.Body.Close()
	if output != nil {
		err = json.NewDecoder(res.Body).Decode(output)
	}
	return res.StatusCode, err
}

func validateRanks(result retrieval.SearchResult) error {
	scores := map[string]float64{}
	for i, h := range result.LexicalCandidates {
		if h.LexicalRank != i+1 {
			return errors.New("BM25 ranks not ordinal")
		}
		scores[h.EvidenceID] += 1 / float64(61+i)
	}
	for i, h := range result.DenseCandidates {
		if h.DenseRank != i+1 {
			return errors.New("dense ranks not ordinal")
		}
		scores[h.EvidenceID] += 1 / float64(61+i)
	}
	prior := math.Inf(1)
	size := 0
	for _, h := range result.Hits {
		if math.Abs(h.RRFScore-scores[h.EvidenceID]) > 1e-12 || h.RRFScore > prior {
			return errors.New("RRF score/order incorrect")
		}
		prior = h.RRFScore
		size += len([]rune(h.Text))
	}
	if len(result.Hits) > 8 || size > retrieval.ContextRunes {
		return errors.New("retrieval context budget exceeded")
	}
	return nil
}

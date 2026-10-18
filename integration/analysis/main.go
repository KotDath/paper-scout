// Integration executable. Real HTTP clients, extraction and SQLite; fixtures
// replace ONLY external source/provider endpoints. Live mode replaces neither.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"paper-scout/internal/deepseek"
	"paper-scout/internal/documents"
	"paper-scout/internal/research"
	"paper-scout/internal/store"
)

func main() {
	runtime := flag.String("runtime", "", "worktree-local disposable directory")
	live := flag.Bool("live", false, "real arXiv and paid DeepSeek, at most 8 requests of 1536 output tokens")
	env := flag.String("env-file", "", "dotenv path loaded only by live process")
	paper := flag.String("paper", "2304.03442v1", "exact public arXiv ID for live reading")
	segmentRunes := flag.Int("segment-runes", 36000, "article runes per live request")
	flag.Parse()
	var err error
	if *runtime == "" {
		err = errors.New("runtime required")
	} else if *live {
		err = liveCheck(*runtime, *env, *paper, *segmentRunes)
	} else if *env != "" {
		err = errors.New("env-file requires live")
	} else {
		err = localCheck(*runtime)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
}

type providerFixture struct {
	mu    sync.Mutex
	mode  string
	calls int
}

func (p *providerFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	p.calls++
	n, mode := p.calls, p.mode
	p.mu.Unlock()
	var req struct {
		Messages  []deepseek.Message `json:"messages"`
		Tools     json.RawMessage    `json:"tools"`
		MaxTokens int                `json:"max_tokens"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil || len(req.Messages) != 2 || req.Messages[1].Content == nil || len(req.Tools) > 0 || req.MaxTokens < 1 {
		http.Error(w, "bad contract", 400)
		return
	}
	if mode == "failure" && n == 2 {
		http.Error(w, "private provider diagnostics", 503)
		return
	}
	if mode == "cancel" && n == 2 {
		<-r.Context().Done()
		return
	}
	var payload struct {
		Packet struct {
			Passages []research.Evidence `json:"passages"`
		} `json:"article_data"`
		Observations []research.Observations `json:"observations"`
	}
	if json.Unmarshal([]byte(*req.Messages[1].Content), &payload) != nil {
		http.Error(w, "bad data", 400)
		return
	}
	id := ""
	if len(payload.Packet.Passages) > 0 {
		id = payload.Packet.Passages[0].ID
	} else if len(payload.Observations) > 0 && len(payload.Observations[0].Methods) > 0 {
		id = payload.Observations[0].Methods[0].EvidenceIDs[0]
	}
	if mode == "foreign" || mode == "bad-synthesis" && len(payload.Observations) > 0 {
		id = "ev_not_in_input"
	}
	content, _ := json.Marshal(research.Observations{Methods: []research.Claim{{Text: "Авторы описывают механизм сохранения памяти.", EvidenceIDs: []string{id}}}, Results: []research.Claim{}, Limitations: []research.Claim{}})
	finish := "stop"
	if mode == "length" && n == 2 {
		finish = "length"
	}
	var usage any = deepseek.Usage{PromptTokens: 100, CompletionTokens: 40, TotalTokens: 140}
	if mode == "unknown" && n == 2 {
		usage = nil
	}
	json.NewEncoder(w).Encode(map[string]any{"id": "fixture", "model": "fixture", "choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": string(content)}, "finish_reason": finish}}, "usage": usage})
}
func (p *providerFixture) reset(mode string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.mode = mode
	p.calls = 0
}

// This integration gateway represents the required shared run budget boundary.
// The requests field can start with calls already spent by an enclosing agent. It
// uses the actual single production DeepSeekClient and never fabricates usage.
type gateway struct {
	client                                            *deepseek.DeepSeekClient
	requests, tokens, unknown, maxRequests, maxTokens int
}

func (g *gateway) complete(ctx context.Context, req deepseek.Request) (deepseek.Response, error) {
	estimate := req.MaxTokens
	for _, m := range req.Messages {
		if m.Content != nil {
			estimate += len([]rune(*m.Content))
		}
	}
	if g.requests >= g.maxRequests || g.tokens+estimate > g.maxTokens {
		return deepseek.Response{}, research.ErrAnalysisBudget
	}
	response, err := g.client.Complete(ctx, req)
	if response.Attempted {
		g.requests++
		if response.Usage == nil {
			g.unknown++
		}
	}
	if response.Usage != nil {
		g.tokens += response.Usage.TotalTokens
	}
	if g.tokens > g.maxTokens {
		return response, research.ErrAnalysisBudget
	}
	return response, err
}

type sourceTransport struct{ target *url.URL }

func (t sourceTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != "arxiv.org" || r.URL.Scheme != "https" {
		return nil, errors.New("unexpected source")
	}
	clone := r.Clone(r.Context())
	u := *r.URL
	u.Host = t.target.Host
	u.Scheme = t.target.Scheme
	clone.URL = &u
	response, err := http.DefaultTransport.RoundTrip(clone)
	if response != nil {
		response.Request = r
	}
	return response, err
}

func seed(ctx context.Context, db *store.Store) error {
	for _, owner := range []string{"alice", "bob"} {
		if _, err := db.DB.ExecContext(ctx, `INSERT INTO users(id,login,password_hash,created_at) VALUES (?,?,?,'')`, owner, owner, "not-an-authentication-fixture"); err != nil {
			return err
		}
	}
	return nil
}
func run(ctx context.Context, db *store.Store) (store.Run, error) {
	c, err := db.CreateConversation(ctx, "alice", "analysis")
	if err != nil {
		return store.Run{}, err
	}
	r, _, err := db.CreateRun(ctx, "alice", c.ID, "request", "Какие методы, результаты и ограничения?")
	if err != nil {
		return r, err
	}
	return db.StartRun(ctx, "alice", r.ID)
}
func register(ctx context.Context, db *store.Store, id string) error {
	_, err := db.DB.ExecContext(ctx, `INSERT INTO paper_versions(id,arxiv_id,version,title,authors_json,abstract,abstract_url,html_url,pdf_url,published_at,updated_at,fetched_at) VALUES (?,?,1,'Authored integration source','[]','Abstract','','','','','','')`, id, strings.TrimSuffix(id, "v1"))
	return err
}

func localCheck(runtime string) error {
	ctx := context.Background()
	path := filepath.Join(runtime, "analysis.sqlite")
	db, err := store.Open(ctx, path)
	if err != nil {
		return err
	}
	defer func() { db.Close() }()
	if err = seed(ctx, db); err != nil {
		return err
	}
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/html/2601.00001v1" {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<article class="ltx_document"><h1>Память агентов</h1><section><h2>Methods</h2><p>`+strings.Repeat("Memory consolidation сохраняет наблюдения. ", 65)+`</p></section><section><h2>Results</h2><p>`+strings.Repeat("Retrieval сохраняет исходные координаты. ", 65)+`</p><p>Limitations include incomplete measurements and lack of independent replication.</p></section></article>`)
		} else if r.URL.Path == "/pdf/2601.00003v1" {
			w.Header().Set("Content-Type", "application/pdf")
			w.Write(pdf())
		} else {
			http.NotFound(w, r)
		}
	}))
	defer source.Close()
	target, _ := url.Parse(source.URL)
	docs, err := documents.New(db.DB, documents.Config{CacheDir: filepath.Join(runtime, "cache"), HTTPClient: &http.Client{Transport: sourceTransport{target}}})
	if err != nil {
		return err
	}
	imported := []documents.Document{}
	for _, id := range []string{"2601.00001v1", "2601.00002v1", "2601.00003v1", "2601.00004v1"} {
		if err = register(ctx, db, id); err != nil {
			return err
		}
		abstract := "Only abstract: memory consolidation method is proposed. No reported experiments."
		if id == "2601.00004v1" {
			abstract = ""
		}
		doc, e := docs.Import(ctx, documents.Source{PaperVersionID: id, ArxivID: id, Abstract: abstract})
		if e != nil {
			return e
		}
		imported = append(imported, doc)
	}
	if imported[0].ExtractionStatus != "full_text" || imported[1].ExtractionStatus != "abstract_only" || imported[2].Format != "pdf" {
		return errors.New("source HTTP ingestion did not exercise full text/abstract/PDF")
	}
	provider := &providerFixture{}
	server := httptest.NewServer(provider)
	defer server.Close()
	client, err := deepseek.New(deepseek.Config{BaseURL: server.URL, Model: "fixture", APIKey: "fixture"})
	if err != nil {
		return err
	}
	analyzer, err := research.NewAnalyzer(db, docs, research.AnalyzerConfig{MaxSegmentRunes: 2200, OutputTokens: 512})
	if err != nil {
		return err
	}
	var saved research.Analysis
	for _, scenario := range []struct {
		mode string
		max  int
		want string
		code string
	}{
		{"success", 32, "complete", ""}, {"failure", 32, "partial", "provider_unavailable"}, {"length", 32, "partial", "output_limit"}, {"foreign", 32, "partial", "invalid_evidence"}, {"unknown", 32, "partial", "invalid_response"}, {"success", 1, "partial", "budget_exceeded"}, {"bad-synthesis", 32, "partial", "invalid_evidence"}, {"cancel", 32, "partial", "cancelled"}, {"committed-cancel", 32, "partial", "cancelled"}, {"context-cap", 32, "partial", "context_limit"}, {"local-cap", 32, "partial", "budget_exceeded"}, {"already-spent", 32, "partial", "budget_exceeded"}, {"token-cap", 32, "partial", "budget_exceeded"},
	} {
		provider.reset(scenario.mode)
		r, e := run(ctx, db)
		if e != nil {
			return e
		}
		if e = analyzer.SelectSource(ctx, "alice", r.ID, imported[0].PaperVersionID, "fixture selection"); e != nil {
			return e
		}
		// Sharing a public cache entry alone is not permission to analyze it.
		g := &gateway{client: client, maxRequests: scenario.max, maxTokens: 250000}
		if scenario.mode == "already-spent" {
			g.requests = 31
		}
		if scenario.mode == "token-cap" {
			g.maxTokens = 1
		}
		current := analyzer
		if scenario.mode == "context-cap" || scenario.mode == "local-cap" {
			cfg := research.AnalyzerConfig{MaxSegmentRunes: 2200, OutputTokens: 512}
			if scenario.mode == "context-cap" {
				cfg.MaxSynthesisRunes = 256
			} else {
				cfg.MaxRequests = 1
			}
			current, e = research.NewAnalyzer(db, docs, cfg)
			if e != nil {
				return e
			}
		}
		complete := g.complete
		if scenario.mode == "committed-cancel" {
			complete = func(c context.Context, request deepseek.Request) (deepseek.Response, error) {
				response, callErr := g.complete(c, request)
				if g.requests == 2 {
					if _, cancelErr := db.CancelRun(ctx, "alice", r.ID); cancelErr != nil {
						return response, cancelErr
					}
				}
				return response, callErr
			}
		}
		if _, e = analyzer.Analyze(ctx, "bob", r.ID, imported[0].ID, "question", g.complete); !errors.Is(e, store.ErrNotFound) {
			return errors.New("foreign run was accessible")
		}
		if _, e = analyzer.Analyze(ctx, "alice", r.ID, imported[1].ID, "question", g.complete); !errors.Is(e, store.ErrNotFound) {
			return errors.New("unselected cached document was accessible")
		}
		callCtx := ctx
		cancel := func() {}
		if scenario.mode == "cancel" {
			callCtx, cancel = context.WithCancel(ctx)
			go func() {
				for {
					provider.mu.Lock()
					n := provider.calls
					provider.mu.Unlock()
					if n >= 2 {
						cancel()
						return
					}
					time.Sleep(5 * time.Millisecond)
				}
			}()
		}
		out, callErr := current.Analyze(callCtx, "alice", r.ID, imported[0].ID, "Методы и ограничения?", complete)
		cancel()
		if out.Status != scenario.want || out.ErrorCode != scenario.code || (callErr != nil) != (scenario.code != "") {
			return fmt.Errorf("%s status=%s code=%s err=%v", scenario.mode, out.Status, out.ErrorCode, callErr)
		}
		got, e := analyzer.Analysis(ctx, "alice", out.ID)
		if e != nil || !reflect.DeepEqual(got, out) {
			return fmt.Errorf("durable result differs: %v", e)
		}
		if _, e = analyzer.Analysis(ctx, "bob", out.ID); !errors.Is(e, store.ErrNotFound) {
			return errors.New("foreign analysis was accessible")
		}
		if e = verifyTrace(ctx, db, imported[0], out); e != nil {
			return fmt.Errorf("%s: %w", scenario.mode, e)
		}
		if scenario.mode == "success" && scenario.max == 32 {
			saved = out
			if out.CoveredRunes != out.TotalRunes || len(out.SuccessfulRanges) < 2 || out.Synthesis == nil || len(out.Evidence) == 0 {
				return errors.New("sequential reading/synthesis/evidence incomplete")
			}
		}
		if scenario.mode == "context-cap" && out.CoveredRunes != out.TotalRunes {
			return errors.New("synthesis context limit discarded successful reading")
		}
		if scenario.mode == "already-spent" && g.requests != 32 {
			return errors.New("nested analyzer bypassed enclosing request count")
		}
		if scenario.mode == "token-cap" && g.requests != 0 {
			return errors.New("token reservation rejection reached provider")
		}
		if scenario.mode == "unknown" && g.unknown != 1 {
			return errors.New("unknown usage counted as zero")
		}
		if _, e = db.FinishRun(ctx, "alice", r.ID, "partial", "", nil); e != nil {
			return e
		}
	}
	fmt.Println("PASS: HTTP HTML ingestion → sequential exact Unicode ranges → real DeepSeek HTTP client → durable observations/synthesis; provider errors, output limit, foreign evidence, unknown usage, shared request limit, synthesis failure/context cap, local cap, prior shared spending, token reservation, committed/in-flight cancellation remain partial; cross-owner/unselected source access denied")
	// Whole-text, abstract-only and PDF source coordinates through the same path.
	whole, err := research.NewAnalyzer(db, docs, research.AnalyzerConfig{MaxSegmentRunes: 20000, OutputTokens: 512})
	if err != nil {
		return err
	}
	for _, doc := range imported {
		provider.reset("success")
		r, e := run(ctx, db)
		if e != nil {
			return e
		}
		if e = whole.SelectSource(ctx, "alice", r.ID, doc.PaperVersionID, "whole document"); e != nil {
			return e
		}
		g := &gateway{client: client, maxRequests: 32, maxTokens: 250000}
		out, e := whole.Analyze(ctx, "alice", r.ID, doc.ID, "Что подтверждает источник?", g.complete)
		if doc.ExtractionStatus == "failed" {
			if e == nil || out.Status != "none" || out.ErrorCode != "text_unusable" || g.requests != 0 {
				return errors.New("empty source gained analysis coverage or reached provider")
			}
			if _, e = db.FinishRun(ctx, "alice", r.ID, "partial", "text_unusable", nil); e != nil {
				return e
			}
			continue
		}
		if e != nil {
			return e
		}
		want := "complete"
		if doc.ExtractionStatus == "abstract_only" {
			want = "abstract_only"
		}
		if out.Status != want || g.requests != 1 || out.CoveredRunes != out.TotalRunes || len(out.Warnings) == 0 {
			return errors.New("whole/abstract path misreported coverage or extraction warnings")
		}
		if doc.Format == "pdf" && (out.Evidence[0].PageStart != 1 || !strings.HasSuffix(out.Evidence[0].SourceURL, "#page=1")) {
			return errors.New("PDF original page link missing")
		}
		if _, e = db.FinishRun(ctx, "alice", r.ID, "completed", "", nil); e != nil {
			return e
		}
	}
	fmt.Println("PASS: empty failed extraction remains none without model calls; fitting document uses one request; abstract remains abstract_only with source-unavailable reasons; real Poppler PDF extraction yields original URL with page and exact evidence")
	// Simulate hard process interruption by leaving the actual persisted pending
	// request after a successful prefix, then invoke production run recovery.
	provider.reset("success")
	r, err := run(ctx, db)
	if err != nil {
		return err
	}
	if err = analyzer.SelectSource(ctx, "alice", r.ID, imported[0].PaperVersionID, "restart"); err != nil {
		return err
	}
	g := &gateway{client: client, maxRequests: 1, maxTokens: 250000}
	partial, _ := analyzer.Analyze(ctx, "alice", r.ID, imported[0].ID, "restart", g.complete)
	_, err = db.DB.ExecContext(ctx, `UPDATE paper_analyses SET result_json=NULL,finished_at=NULL,status='none' WHERE id=?`, partial.ID)
	if err != nil {
		return err
	}
	_, err = db.DB.ExecContext(ctx, `INSERT INTO analysis_segments(id,analysis_id,ordinal,kind,char_start,char_end,input_hash,input_json,evidence_ids_json,status,attempted) VALUES ('crash',?,100,'reading',?,?, 'uncertain','{}','[]','pending',1)`, partial.ID, partial.CoveredRunes, partial.TotalRunes)
	if err != nil {
		return err
	}
	if err = db.Close(); err != nil {
		return err
	}
	db, err = store.Open(ctx, path)
	if err != nil {
		return err
	}
	if err = db.RecoverRuns(ctx); err != nil {
		return err
	}
	docs, err = documents.New(db.DB, documents.Config{CacheDir: filepath.Join(runtime, "cache")})
	if err != nil {
		return err
	}
	analyzer, err = research.NewAnalyzer(db, docs, research.AnalyzerConfig{})
	if err != nil {
		return err
	}
	got, err := analyzer.Analysis(ctx, "alice", saved.ID)
	if err != nil || !reflect.DeepEqual(got, saved) {
		return errors.New("completed analysis changed after SQLite restart")
	}
	got, err = analyzer.Analysis(ctx, "alice", partial.ID)
	if err != nil || got.Status != "partial" || got.ErrorCode != "run_interrupted" || got.CoveredRunes != partial.CoveredRunes {
		return errors.New("pending interrupted range earned coverage or lost prefix")
	}
	// Duplicate successful ranges must not increase coverage.
	_, err = db.DB.ExecContext(ctx, `INSERT INTO analysis_segments(id,analysis_id,ordinal,kind,char_start,char_end,input_hash,input_json,evidence_ids_json,status,attempted,analysis_json,usage_json) SELECT 'duplicate',analysis_id,101,kind,char_start,char_end,input_hash,input_json,evidence_ids_json,status,attempted,analysis_json,usage_json FROM analysis_segments WHERE analysis_id=? AND status='succeeded' LIMIT 1`, partial.ID)
	if err != nil {
		return err
	}
	duplicate, err := analyzer.Analysis(ctx, "alice", partial.ID)
	if err != nil || duplicate.CoveredRunes != got.CoveredRunes {
		return errors.New("duplicate range inflated coverage")
	}
	fmt.Println("PASS: completed results/evidence survive SQLite reopen; production interrupted recovery preserves successful prefix, excludes pending steps, does no paid replay; duplicate ranges do not inflate coverage")
	return nil
}

func verifyTrace(ctx context.Context, db *store.Store, doc documents.Document, out research.Analysis) error {
	rows, err := db.DB.QueryContext(ctx, `SELECT input_hash,input_json,status,kind,char_start,char_end,usage_json,attempted FROM analysis_segments WHERE analysis_id=? ORDER BY ordinal`, out.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	cursor, covered := 0, 0
	for rows.Next() {
		var hash, input, status, kind string
		var start, end, attempted int
		var usage *string
		if err = rows.Scan(&hash, &input, &status, &kind, &start, &end, &usage, &attempted); err != nil {
			return err
		}
		if hash != fmt.Sprintf("%x", sha256.Sum256([]byte(input))) {
			return errors.New("request input hash mismatch")
		}
		if kind != "reading" {
			continue
		}
		var request deepseek.Request
		if err = json.Unmarshal([]byte(input), &request); err != nil {
			return err
		}
		var payload struct {
			Packet struct {
				Start    int                 `json:"char_start"`
				End      int                 `json:"char_end"`
				Passages []research.Evidence `json:"passages"`
			} `json:"article_data"`
		}
		if err = json.Unmarshal([]byte(*request.Messages[1].Content), &payload); err != nil {
			return err
		}
		if start != cursor || payload.Packet.Start != start || payload.Packet.End != end {
			return errors.New("reading skipped a range")
		}
		var text strings.Builder
		for _, e := range payload.Packet.Passages {
			text.WriteString(e.Text)
		}
		if text.String() != string([]rune(doc.Text)[start:end]) {
			return errors.New("transmitted passages differ from exact original range")
		}
		cursor = end
		if status == "succeeded" {
			covered += end - start
			if usage == nil || attempted != 1 {
				return errors.New("successful step missing usage/attempt")
			}
		}
	}
	if covered != out.CoveredRunes {
		return errors.New("coverage differs from durable successful ranges")
	}
	return rows.Err()
}

// Authored text PDF, parsed by actual pdfinfo/pdftotext.
func pdf() []byte {
	stream := "BT /F1 10 Tf 30 750 Td (" + strings.Repeat("Memory consolidation method with results and limitations. ", 8) + ") Tj ET\n"
	objects := []string{"<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Count 1 /Kids [3 0 R] >>", "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 2000 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>", "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>", fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(stream), stream)}
	body := "%PDF-1.4\n"
	offsets := []int{}
	for i, o := range objects {
		offsets = append(offsets, len(body))
		body += fmt.Sprintf("%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := len(body)
	body += fmt.Sprintf("xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, offset := range offsets {
		body += fmt.Sprintf("%010d 00000 n \n", offset)
	}
	body += fmt.Sprintf("trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return []byte(body)
}

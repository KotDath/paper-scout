// T19 integration: synthetic persisted research, real SQLite/auth/HTTP/Qwen3.
// No DeepSeek calls, source fetches, unit tests or invented live scientific claims.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"golang.org/x/crypto/bcrypt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"paper-scout/internal/auth"
	"paper-scout/internal/embeddings"
	"paper-scout/internal/httpapi"
	"paper-scout/internal/research"
	"paper-scout/internal/researchmemory"
	"paper-scout/internal/retrieval"
	"paper-scout/internal/store"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

func main() {
	dir := flag.String("runtime", "", "temporary path within assigned worktree")
	flag.Parse()
	if err := run(*dir); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
}
func require(ok bool, message string) error {
	if !ok {
		return errors.New(message)
	}
	return nil
}
func run(dir string) error {
	if dir == "" {
		return errors.New("--runtime required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	db, err := store.Open(ctx, filepath.Join(dir, "memory.sqlite"))
	if err != nil {
		return err
	}
	defer func() { db.Close() }()
	pass := "integration-password-only"
	pw, err := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.MinCost)
	if err != nil {
		return err
	}
	accounts := []store.SeedAccount{}
	for _, id := range []string{"owner_a", "owner_b"} {
		accounts = append(accounts, store.SeedAccount{User: store.User{ID: id, Login: id, CreatedAt: "2026-01-01T00:00:00Z"}, PasswordHash: string(pw)})
	}
	if err = db.SeedAccounts(ctx, accounts); err != nil {
		return err
	}
	for _, id := range []string{"a", "b"} {
		if err = seedResearch(ctx, db, id); err != nil {
			return err
		}
	}
	// Forward all healthy requests to the real installed model. Only failures and
	// race timing are injected here; no substitute vectors or fake model metadata.
	target, _ := url.Parse("http://127.0.0.1:11434")
	proxy := httputil.NewSingleHostReverseProxy(target)
	var outage atomic.Bool
	var hook atomic.Pointer[func()]
	var calls atomic.Int32
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if outage.Load() {
			w.WriteHeader(503)
			return
		}
		if r.URL.Path == "/api/embed" {
			calls.Add(1)
			if f := hook.Swap(nil); f != nil {
				(*f)()
			}
		}
		proxy.ServeHTTP(w, r)
	}))
	defer gateway.Close()
	client, err := embeddings.New(ctx, embeddings.Config{BaseURL: gateway.URL})
	if err != nil {
		return err
	}
	memory := researchmemory.New(db.DB, client)
	server := httptest.NewServer(httpapi.New(db, retrieval.New(db.DB, client)))
	defer func() { server.Close() }()
	tokens := map[string]string{}
	for _, id := range []string{"owner_a", "owner_b"} {
		login, e := auth.New(db).Login(ctx, id, pass)
		if e != nil {
			return e
		}
		tokens[id] = login.SessionToken
	}
	search := func(owner, query string) (researchmemory.SearchResult, error) {
		var out researchmemory.SearchResult
		code, e := request(server.URL, "POST", "/api/v1/research-memory/search", tokens[owner], researchmemory.SearchRequest{Query: query}, &out)
		if e != nil {
			return out, e
		}
		if code != 200 {
			return out, fmt.Errorf("search HTTP %d", code)
		}
		return out, nil
	}
	first, err := search("owner_a", "episodic memory consolidation forgetting")
	if err != nil {
		return err
	}
	if err = require(first.RetrievalMode == "hybrid" && len(first.LexicalCandidates) > 0 && len(first.DenseCandidates) > 0 && len(first.Hits) > 0 && first.IndexCoverage == 1, "initial real hybrid/index failed"); err != nil {
		return err
	}
	var claims, facets int
	if err = db.DB.QueryRowContext(ctx, `SELECT count(*),count(DISTINCT facet) FROM research_memories WHERE kind='claim'`).Scan(&claims, &facets); err != nil {
		return err
	}
	if err = require(claims == 6 && facets == 3, "typed atomic claim facets missing"); err != nil {
		return err
	}
	var claimID, episodeID string
	if err = db.DB.QueryRowContext(ctx, `SELECT id FROM research_memories WHERE kind='claim' AND facet='method' AND paper_id='paper_a'`).Scan(&claimID); err != nil {
		return err
	}
	if err = db.DB.QueryRowContext(ctx, `SELECT id FROM research_memories WHERE kind='episode' AND facet='report_a'`).Scan(&episodeID); err != nil {
		return err
	}
	noop, err := memory.Capture(ctx, "owner_a", "report_a")
	if err != nil {
		return err
	}
	if err = require(noop.Noop == 1 && noop.Added == 0, "capture not NOOP"); err != nil {
		return err
	}
	// A second episode with exact same scientific claims adds origins, not cards.
	if err = seedDuplicate(ctx, db); err != nil {
		return err
	}
	if _, err = memory.Capture(ctx, "owner_a", "report_duplicate"); err != nil {
		return err
	}
	var originCount int
	if err = db.DB.QueryRowContext(ctx, `SELECT count(*) FROM research_memory_origins WHERE memory_id=?`, claimID).Scan(&originCount); err != nil {
		return err
	}
	if err = require(originCount == 2, "NOOP lost additional provenance"); err != nil {
		return err
	}
	for _, h := range first.Hits {
		score := 0.
		if h.LexicalRank > 0 {
			score += 1 / float64(60+h.LexicalRank)
		}
		if h.DenseRank > 0 {
			score += 1 / float64(60+h.DenseRank)
		}
		if math.Abs(score-h.RRFScore) > 1e-12 {
			return errors.New("RRF is not actual rank fusion")
		}
	}
	fmt.Println("PASS real Qwen3/BM25/RRF; capture atomic facets; exact dedup retains both episode origins; conflicting papers coexist")
	b, err := search("owner_b", "episodic memory consolidation")
	if err != nil {
		return err
	}
	if err = require(len(b.Hits) == 0 && len(b.DenseCandidates) == 0 && len(b.LexicalCandidates) == 0, "foreign candidates leaked"); err != nil {
		return err
	}
	for _, method := range []string{"GET", "PATCH"} {
		var body any
		if method == "PATCH" {
			body = map[string]any{"expected_revision": 1, "excluded": true}
		}
		for _, id := range []string{claimID, "missing"} {
			code, e := request(server.URL, method, "/api/v1/research-memory/"+id, tokens["owner_b"], body, nil)
			if e != nil {
				return e
			}
			if err = require(code == 404, "foreign/missing ID not hidden"); err != nil {
				return err
			}
		}
	}
	code, err := request(server.URL, "POST", "/api/v1/research-memory/search", "", map[string]any{"query": "memory"}, nil)
	if err != nil {
		return err
	}
	if err = require(code == 401, "anonymous memory accepted"); err != nil {
		return err
	}
	for _, input := range []any{map[string]any{"query": "", "limit": 1}, map[string]any{"query": "memory", "limit": 6}, map[string]any{"query": "memory", "owner": "owner_b"}, map[string]any{"query": "memory", "exclude_run_id": "x"}} {
		code, err = request(server.URL, "POST", "/api/v1/research-memory/search", tokens["owner_a"], input, nil)
		if err != nil {
			return err
		}
		if err = require(code == 400, "invalid/owner input accepted"); err != nil {
			return err
		}
	}
	fmt.Println("PASS authenticated API; both branches owner-filtered; foreign/missing GET/PATCH=404; invalid/unknown fields=400")
	for _, input := range []any{map[string]any{"expected_revision": 1, "correction": ""}, map[string]any{"expected_revision": 1, "correction": strings.Repeat("я", 4001)}, map[string]any{"excluded": true}, map[string]any{"expected_revision": 1, "excluded": true, "correction": "x"}} {
		code, err = request(server.URL, "PATCH", "/api/v1/research-memory/"+claimID, tokens["owner_a"], input, nil)
		if err != nil {
			return err
		}
		if err = require(code == 400, "invalid patch accepted"); err != nil {
			return err
		}
	}
	// Note is a deterministic attachment even without query terms.
	note := "For me prioritize evaluation in long conversations, personal criterion."
	if _, err = db.UpdatePaper(ctx, "owner_a", "paper_a", store.PaperPatch{Note: &note}); err != nil {
		return err
	}
	result, err := search("owner_a", "episodic memory consolidation")
	if err != nil {
		return err
	}
	if err = contains(result, note, true); err != nil {
		return err
	}
	// T22 live reproducer: a highly ranked note must not consume its paper's
	// only group and suppress all scientific provenance, even at limit=1.
	noteFirst, err := memory.Search(ctx, "owner_a", researchmemory.SearchRequest{Query: note, Limit: 1})
	if err != nil {
		return err
	}
	if err = require(len(noteFirst.LexicalCandidates) > 0 && noteFirst.LexicalCandidates[0].Kind == "user_note" && len(noteFirst.DenseCandidates) > 0 && noteFirst.DenseCandidates[0].Kind == "user_note", "note-first reproducer did not rank note first in both real branches"); err != nil {
		return err
	}
	if err = require(len(noteFirst.Hits) >= 2 && noteFirst.Hits[0].Kind == "claim" && noteFirst.Hits[0].ReportID != "" && len(noteFirst.Hits[0].EvidenceIDs) > 0, "top note displaced scientific origin from one-paper recall"); err != nil {
		return err
	}
	if err = contains(noteFirst, note, true); err != nil {
		return err
	}
	fmt.Println("PASS T22 note ranks first in BM25 and real Qwen3; limit=1 retains scientific origin/evidence plus exact note")
	correction := "This historical method claim is disputed: verify the original passage before comparison."
	var patched researchmemory.Entry
	code, err = request(server.URL, "PATCH", "/api/v1/research-memory/"+claimID, tokens["owner_a"], researchmemory.Patch{ExpectedRevision: 1, Correction: &correction}, &patched)
	if err != nil {
		return err
	}
	if err = require(code == 200 && patched.Revision == 2 && patched.State == "disputed" && len(patched.Annotations) > 0, "correction did not persist"); err != nil {
		return err
	}
	code, err = request(server.URL, "PATCH", "/api/v1/research-memory/"+claimID, tokens["owner_a"], map[string]any{"expected_revision": 1, "excluded": true}, nil)
	if err != nil {
		return err
	}
	if err = require(code == 409, "stale edit accepted"); err != nil {
		return err
	}
	result, err = search("owner_a", "episodic consolidation forgetting")
	if err != nil {
		return err
	}
	for _, h := range result.Hits {
		if h.MemoryID == claimID {
			if err = contains(result, correction, true); err != nil {
				return err
			}
		}
		if h.MemoryID == episodeID {
			return errors.New("correction bypassed through raw episode")
		}
	}
	// GET historical episode contains current correction and exact immutable report.
	entry, err := memory.Get(ctx, "owner_a", episodeID)
	if err != nil {
		return err
	}
	found := false
	for _, a := range entry.Annotations {
		found = found || a.Text == correction
	}
	if err = require(found, "historical episode missing current correction"); err != nil {
		return err
	}
	// Exclusion and correction survive full projection rebuild without DeepSeek.
	exclude := true
	if _, err = memory.Patch(ctx, "owner_a", claimID, researchmemory.Patch{ExpectedRevision: 2, Excluded: &exclude}); err != nil {
		return err
	}
	if err = memory.Rebuild(ctx, "owner_a"); err != nil {
		return err
	}
	if _, err = memory.Refresh(ctx, "owner_a"); err != nil {
		return err
	}
	result, err = search("owner_a", "episodic consolidation forgetting")
	if err != nil {
		return err
	}
	for _, h := range result.Hits {
		if h.MemoryID == claimID || h.MemoryID == episodeID {
			return errors.New("exclusion resurrected by rebuild")
		}
	}
	exclude = false
	if _, err = memory.Patch(ctx, "owner_a", claimID, researchmemory.Patch{ExpectedRevision: 3, Excluded: &exclude}); err != nil {
		return err
	}
	// Invalidate a vector, then change note *during real inference*. Publication
	// cannot insert the just-deleted passage or expose the old note afterward.
	var noteID string
	if err = db.DB.QueryRowContext(ctx, `SELECT id FROM research_memories WHERE kind='user_note'`).Scan(&noteID); err != nil {
		return err
	}
	if _, err = db.DB.ExecContext(ctx, `DELETE FROM research_memory_vectors`); err != nil {
		return err
	}
	updated := "Updated personal criterion: prioritize reproducible benchmark protocols."
	var mutationErr error
	mutate := func() { _, mutationErr = db.UpdatePaper(ctx, "owner_a", "paper_a", store.PaperPatch{Note: &updated}) }
	hook.Store(&mutate)
	indexed, err := memory.Index(ctx, "owner_a", researchmemory.SearchRequest{})
	if err != nil {
		return err
	}
	if mutationErr != nil {
		return mutationErr
	}
	if err = require(indexed.Published < indexed.Attempted, "stale note vector published"); err != nil {
		return err
	}
	var stale int
	if err = db.DB.QueryRowContext(ctx, `SELECT count(*) FROM research_memory_vectors v JOIN research_memory_passages p ON p.id=v.passage_id WHERE p.memory_id=?`, noteID).Scan(&stale); err != nil {
		return err
	}
	if err = require(stale == 0, "stale ready note retained"); err != nil {
		return err
	}
	result, err = search("owner_a", "episodic consolidation")
	if err != nil {
		return err
	}
	if err = contains(result, note, false); err != nil {
		return err
	}
	if err = contains(result, updated, true); err != nil {
		return err
	}
	empty := ""
	if _, err = db.UpdatePaper(ctx, "owner_a", "paper_a", store.PaperPatch{Note: &empty}); err != nil {
		return err
	}
	result, err = search("owner_a", "episodic consolidation")
	if err != nil {
		return err
	}
	if err = contains(result, updated, false); err != nil {
		return err
	}
	// Long current note is delivered whole. If mandatory annotation would not
	// fit, ordinary candidates are dropped; an orphan disputed claim is forbidden.
	long := strings.Repeat("personal criterion ", 500)
	if _, err = db.UpdatePaper(ctx, "owner_a", "paper_a", store.PaperPatch{Note: &long}); err != nil {
		return err
	}
	result, err = search("owner_a", "episodic consolidation")
	if err != nil {
		return err
	}
	if err = contains(result, long, true); err != nil {
		return err
	}
	for _, h := range result.Hits {
		if h.MemoryID == claimID {
			if err = contains(result, correction, true); err != nil {
				return err
			}
		}
	}
	if _, err = db.UpdatePaper(ctx, "owner_a", "paper_a", store.PaperPatch{Note: &empty}); err != nil {
		return err
	}
	if _, err = memory.Refresh(ctx, "owner_a"); err != nil {
		return err
	}
	var history int
	if err = db.DB.QueryRowContext(ctx, `SELECT count(*) FROM research_memory_note_history WHERE owner='owner_a' AND paper_id='paper_a'`).Scan(&history); err != nil {
		return err
	}
	if err = require(history == 5, "explicit note edit history incomplete"); err != nil {
		return err
	}
	fmt.Println("PASS correction/revision409, exclusion/restore, audit and origins survive rebuild; note edit/clear and real-inference stale publication race")
	outage.Store(true)
	result, err = search("owner_a", "episodic consolidation")
	if err != nil {
		return err
	}
	if err = require(result.RetrievalMode == "lexical_only" && len(result.LexicalCandidates) > 0 && len(result.DenseCandidates) == 0, "Ollama outage not explicit lexical_only"); err != nil {
		return err
	}
	outage.Store(false)
	old := researchmemory.SearchRequest{Query: "episodic consolidation", Before: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	past, err := memory.Search(ctx, "owner_a", old)
	if err != nil {
		return err
	}
	if err = require(len(past.Hits) == 0 && len(past.DenseCandidates) == 0, "future data leaked past cutoff"); err != nil {
		return err
	}
	bounded, err := memory.Search(ctx, "owner_a", researchmemory.SearchRequest{Query: "episodic consolidation", ExcludeRunID: "run_a"})
	if err != nil {
		return err
	}
	for _, h := range bounded.Hits {
		for _, o := range h.Origins {
			if o.RunID == "run_a" {
				return errors.New("excluded current run origin leaked")
			}
		}
	}
	cancelled, cancelNow := context.WithCancel(ctx)
	cancelNow()
	if _, err = memory.Search(cancelled, "owner_a", researchmemory.SearchRequest{Query: "memory"}); !errors.Is(err, context.Canceled) {
		return errors.New("cancellation ignored")
	}
	// Reopen database and HTTP with persisted sessions; no write-pass generation.
	server.Close()
	if err = db.Close(); err != nil {
		return err
	}
	db, err = store.Open(ctx, filepath.Join(dir, "memory.sqlite"))
	if err != nil {
		return err
	}
	memory = researchmemory.New(db.DB, client)
	server = httptest.NewServer(httpapi.New(db, retrieval.New(db.DB, client)))
	entry, err = memory.Get(ctx, "owner_a", claimID)
	if err != nil {
		return err
	}
	if err = require(entry.State == "disputed" && entry.Revision == 4 && !entry.Excluded, "revision lost after restart"); err != nil {
		return err
	}
	result, err = search("owner_a", "episodic consolidation")
	if err != nil {
		return err
	}
	if err = contains(result, updated, false); err != nil {
		return err
	}
	var audits int
	if err = db.DB.QueryRowContext(ctx, `SELECT count(*) FROM research_memory_revisions WHERE memory_id=?`, claimID).Scan(&audits); err != nil {
		return err
	}
	if err = require(audits == 4, "revision audit incomplete"); err != nil {
		return err
	}
	if err = fallback(ctx, db, memory); err != nil {
		return err
	}
	if err = review1(ctx, db, memory, server.URL, tokens["owner_a"], &hook); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(first, "", "  ")
	if err = os.WriteFile(filepath.Join(dir, "hybrid.json"), data, 0600); err != nil {
		return err
	}
	fmt.Printf("PASS explicit live-backend outage, cutoff/current-run/cancel bounds; SQLite+HTTP restart; sanitized hybrid ranks in runtime; real embedding requests=%d\n", calls.Load())
	return nil
}
func contains(r researchmemory.SearchResult, text string, want bool) error {
	found := false
	size := 0
	for _, h := range r.Hits {
		found = found || strings.Contains(h.Text, text)
		size += len([]rune(h.Text))
	}
	if found != want {
		return fmt.Errorf("annotation presence got %t want %t", found, want)
	}
	if size > researchmemory.ContextRunes {
		return errors.New("context exceeds 12000 runes")
	}
	return nil
}
func request(base, method, path, token string, body, output any) (int, error) {
	var reader io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, base+path, reader)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	if output != nil {
		err = json.NewDecoder(res.Body).Decode(output)
	}
	return res.StatusCode, err
}
func seedResearch(ctx context.Context, db *store.Store, key string) error {
	exec := func(q string, args ...any) error { _, err := db.DB.ExecContext(ctx, q, args...); return err }
	stamp := "2026-01-02T00:00:00Z"
	paper := "paper_" + key
	run := "run_" + key
	conv := "conv_" + key
	doc := "doc_" + key
	ev := "ev_" + key
	source := "Synthetic episodic memory consolidation method uses forgetting and recurrent review."
	if key == "b" {
		source = "Synthetic episodic memory consolidation does not improve the measured result under this condition."
	}
	if err := exec(`INSERT INTO paper_versions VALUES(?,?,1,?,'[]',?,'https://arxiv.org/abs/2305.10250','https://arxiv.org/html/2305.10250v1','https://arxiv.org/pdf/2305.10250v1',?,?,?)`, paper, "synthetic-"+key, "Synthetic memory paper "+key, source, stamp, stamp, stamp); err != nil {
		return err
	}
	if err := exec(`INSERT INTO documents VALUES(?,?,'html',?,'fixture-v1','fixture-v1','full_text','https://arxiv.org/html/2305.10250v1','',?,'[]','[]',?)`, doc, paper, strings.Repeat(key, 64), source, stamp); err != nil {
		return err
	}
	if err := exec(`INSERT INTO chunks(id,document_id,ordinal,text,char_start,char_end,section) VALUES(?,?,0,?,0,?,'Methods')`, ev, doc, source, len([]rune(source))); err != nil {
		return err
	}
	if err := exec(`INSERT INTO user_papers(user_id,paper_version_id,added_at) VALUES('owner_a',?,?)`, paper, stamp); err != nil {
		return err
	}
	if err := exec(`INSERT INTO conversations VALUES(?,'owner_a',?,?)`, conv, "Synthetic memory research "+key, stamp); err != nil {
		return err
	}
	if err := exec(`INSERT INTO runs(id,user_id,conversation_id,client_request_id,request_message,status,created_at,budget_json) VALUES(?,'owner_a',?,?,?,'completed',?,'{}')`, run, conv, key, "Study episodic memory consolidation", stamp); err != nil {
		return err
	}
	if err := exec(`INSERT INTO run_sources VALUES(?,?,'synthetic integration source','complete')`, run, paper); err != nil {
		return err
	}
	obs := research.Observations{Methods: []research.Claim{{Text: "Episodic consolidation uses forgetting and recurrent review " + key, EvidenceIDs: []string{ev}}}, Results: []research.Claim{{Text: source, EvidenceIDs: []string{ev}}}, Limitations: []research.Claim{{Text: "Synthetic evaluation limitations require independent verification " + key, EvidenceIDs: []string{ev}}}}
	a := research.Analysis{ID: "analysis_" + key, RunID: run, DocumentID: doc, PaperVersionID: paper, Status: "complete", ExtractionStatus: "full_text", TotalRunes: len([]rune(source)), CoveredRunes: len([]rune(source)), SuccessfulRanges: []research.Range{{Start: 0, End: len([]rune(source))}}, Synthesis: &obs}
	if err := exec(`INSERT INTO paper_analyses(id,run_id,document_id,question,status,result_json,created_at,finished_at) VALUES(?,?,?,'synthetic','complete',?,?,?)`, a.ID, run, doc, mustJSON(a), stamp, stamp); err != nil {
		return err
	}
	sources := []store.ReportSource{{Paper: store.Paper{ID: paper, Title: "Synthetic memory paper " + key}, AnalysisStatus: "complete", Coverage: []store.Coverage{}}}
	return exec(`INSERT INTO reports(id,user_id,run_id,markdown,citations_json,sources_json,coverage_json,limitations_json,created_at) VALUES(?,'owner_a',?,?,'[]',?,'[]','[]',?)`, "report_"+key, run, "Synthetic report: "+source, mustJSON(sources), stamp)
}
func seedDuplicate(ctx context.Context, db *store.Store) error {
	for _, q := range []string{
		`INSERT INTO conversations SELECT 'conv_duplicate',user_id,title,created_at FROM conversations WHERE id='conv_a'`,
		`INSERT INTO runs(id,user_id,conversation_id,client_request_id,request_message,status,created_at,budget_json) SELECT 'run_duplicate',user_id,'conv_duplicate','duplicate',request_message,status,created_at,budget_json FROM runs WHERE id='run_a'`,
		`INSERT INTO run_sources SELECT 'run_duplicate',paper_version_id,selection_reason,analysis_status FROM run_sources WHERE run_id='run_a'`,
		`INSERT INTO paper_analyses SELECT 'analysis_duplicate','run_duplicate',document_id,question,status,result_json,error_code,created_at,finished_at FROM paper_analyses WHERE id='analysis_a'`,
		`INSERT INTO reports(id,user_id,run_id,markdown,citations_json,sources_json,coverage_json,limitations_json,created_at) SELECT 'report_duplicate',user_id,'run_duplicate',markdown,citations_json,sources_json,coverage_json,limitations_json,created_at FROM reports WHERE id='report_a'`,
	} {
		if _, err := db.DB.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}
func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

// Successful persisted reading steps are the only fallback when synthesis is
// absent. A large episode also exercises bounded capture and one index batch.
func fallback(ctx context.Context, db *store.Store, memory *researchmemory.Service) error {
	queries := []string{
		`INSERT INTO conversations VALUES('conv_fallback','owner_b','Partial episode','2026-01-03T00:00:00Z')`,
		`INSERT INTO runs(id,user_id,conversation_id,client_request_id,request_message,status,created_at,budget_json) VALUES('run_fallback','owner_b','conv_fallback','fallback','episodic consolidation','partial','2026-01-03T00:00:00Z','{}')`,
		`INSERT INTO run_sources VALUES('run_fallback','paper_b','partial fixture','partial')`,
		`INSERT INTO paper_analyses(id,run_id,document_id,question,status,created_at,finished_at) VALUES('analysis_fallback','run_fallback','doc_b','partial fixture','partial','2026-01-03T00:00:00Z','2026-01-03T00:00:00Z')`,
	}
	for _, q := range queries {
		if _, err := db.DB.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	if _, err := db.DB.ExecContext(ctx, `INSERT INTO chunks(id,document_id,ordinal,text,char_start,char_end,section) VALUES('ev_b_second','doc_b',1,'Second source range',20,40,'Results')`); err != nil {
		return err
	}
	for i, status := range []string{"succeeded", "succeeded", "failed"} {
		text := "Successful persisted fallback method"
		if status == "failed" {
			text = "DO_NOT_CAPTURE_FAILED_OBSERVATION"
		}
		ev := "ev_b"
		if i == 1 {
			ev = "ev_b_second"
		}
		obs := research.Observations{Methods: []research.Claim{{Text: text, EvidenceIDs: []string{ev}}}}
		if _, err := db.DB.ExecContext(ctx, `INSERT INTO analysis_segments(id,analysis_id,ordinal,kind,char_start,char_end,input_hash,input_json,evidence_ids_json,status,attempted,analysis_json) VALUES(?,'analysis_fallback',?,'reading',?,?,?,'{}',?, ?,1,?)`, fmt.Sprintf("step_%d", i), i, i*20, (i+1)*20, strings.Repeat("c", 64), mustJSON([]string{ev}), status, mustJSON(obs)); err != nil {
			return err
		}
	}
	source := []store.ReportSource{{Paper: store.Paper{ID: "paper_b"}, AnalysisStatus: "partial"}}
	if _, err := db.DB.ExecContext(ctx, `INSERT INTO reports(id,user_id,run_id,markdown,citations_json,sources_json,coverage_json,limitations_json,created_at) VALUES('report_fallback','owner_b','run_fallback',?,'[]',?,'[]','["partial synthetic source"]','2026-01-03T00:00:00Z')`, strings.Repeat("episodic consolidation long synthetic episode. ", 2000), mustJSON(source)); err != nil {
		return err
	}
	captured, err := memory.Capture(ctx, "owner_b", "report_fallback")
	if err != nil {
		return err
	}
	if captured.Added != 2 {
		return errors.New("fallback did not capture exactly episode+successful claim")
	}
	var id string
	if err = db.DB.QueryRowContext(ctx, `SELECT id FROM research_memories WHERE owner='owner_b' AND kind='claim'`).Scan(&id); err != nil {
		return err
	}
	entry, err := memory.Get(ctx, "owner_b", id)
	if err != nil {
		return err
	}
	if len(entry.Origins) != 1 || entry.Origins[0].AnalysisStatus != "partial" || !strings.Contains(string(entry.Origins[0].Coverage), `"covered_runes":40`) {
		return errors.New("fallback lost partial range coverage")
	}
	if len(entry.EvidenceIDs) != 2 {
		return errors.New("fallback NOOP lost evidence from another successful segment")
	}
	result, err := memory.Search(ctx, "owner_b", researchmemory.SearchRequest{Query: "episodic consolidation"})
	if err != nil {
		return err
	}
	if result.RetrievalMode != "hybrid_partial" || result.IndexCoverage >= 1 {
		return errors.New("bounded incomplete index silently ready")
	}
	if len(result.Refresh.Reasons) == 0 {
		return errors.New("episode truncation not explicit")
	}
	fmt.Println("PASS successful-step-only fallback with exact partial ranges; oversized episode flagged; one 32-passage batch reports hybrid_partial")
	return nil
}

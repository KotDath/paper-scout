// Executable integration: production loop/modules/SQLite and real Ollama.
// HTTP fixtures replace external source/provider boundaries only, never modules.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"paper-scout/internal/agent"
	"paper-scout/internal/arxiv"
	"paper-scout/internal/config"
	"paper-scout/internal/deepseek"
	"paper-scout/internal/documents"
	"paper-scout/internal/embeddings"
	"paper-scout/internal/research"
	"paper-scout/internal/researchtools"
	"paper-scout/internal/retrieval"
	"paper-scout/internal/store"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}
func check(ok bool, label string) {
	if !ok {
		panic(label)
	}
}
func decode(raw string, target any) { must(json.Unmarshal([]byte(raw), target)) }

var ctx = context.Background()

func main() {
	runtime := flag.String("runtime", "", "temporary directory in this worktree")
	live := flag.Bool("live", false, "real arXiv, DeepSeek and Ollama")
	allowPartial := flag.Bool("allow-partial", false, "live tool smoke only: permit honestly labeled partial analysis with a validated reading prefix")
	env := flag.String("env-file", "", "dotenv loaded only by Go process in live mode")
	artifact := flag.String("artifact", "", "optional sanitized result JSON path in this worktree")
	flag.Parse()
	check(*runtime != "", "runtime required")
	check(!*allowPartial || *live, "allow-partial requires live mode")
	if !*live {
		check(*env == "", "env-file requires live mode")
	} else {
		_, err := config.Load(*env)
		must(err)
	}
	db, err := store.Open(ctx, filepath.Join(*runtime, "tools.sqlite"))
	must(err)
	defer func() { db.Close() }()
	must(db.SeedAccounts(ctx, []store.SeedAccount{
		{User: store.User{ID: "alice", Login: "tools-alice", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}, PasswordHash: "integration-only"},
		{User: store.User{ID: "bob", Login: "tools-bob", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}, PasswordHash: "integration-only"},
	}))
	ecfg, err := embeddings.ConfigFromEnv()
	must(err)
	preflight, cancel := context.WithTimeout(ctx, 30*time.Second)
	embed, err := embeddings.New(preflight, ecfg)
	cancel()
	must(err)
	fmt.Println("PASS: real installed Qwen3 embedding preflight; 1024 dimensions/digest pinned")
	search := retrieval.New(db.DB, embed)
	endpoint := ""
	var sourceClient *http.Client
	provider := deepseek.ConfigFromEnv()
	closeFixture := func() {}
	if *live {
		check(provider.BaseURL == "" || strings.TrimRight(provider.BaseURL, "/") == "https://api.deepseek.com" || strings.TrimRight(provider.BaseURL, "/") == "https://api.deepseek.com/v1", "live requires official DeepSeek HTTPS")
		check(provider.Thinking == "" || provider.Thinking == "disabled", "bounded live requires thinking disabled")
	} else {
		endpoint, sourceClient, provider, closeFixture = fixtures(db)
	}
	defer closeFixture()
	catalog, err := arxiv.New(db.DB, endpoint)
	must(err)
	docs, err := documents.New(db.DB, documents.Config{CacheDir: filepath.Join(*runtime, "cache"), HTTPClient: sourceClient})
	must(err)
	analyzer, err := research.NewAnalyzer(db, docs, research.AnalyzerConfig{MaxSegmentRunes: 12000, MaxRequests: 6, OutputTokens: 2048})
	must(err)
	registry, err := researchtools.New(db, catalog, docs, search, analyzer)
	must(err)
	check(len(registry.Definitions()) == 5, "expected five tools")
	client, err := deepseek.New(provider)
	must(err)
	runs := research.New(ctx, db)
	defer runs.Close()
	limits := agent.DefaultLimits()
	limits.Requests = 12
	limits.ToolCalls = 10
	limits.Tokens = 180000
	limits.Retries = 0
	limits.OutputTokens = 2048
	runner, err := agent.New(db, runs, client, registry, limits)
	must(err)
	conversation, err := db.CreateConversation(ctx, "alice", "Tools integration")
	must(err)
	prompt := "T11_PRIMARY: Call search_papers first with query ti:memory, limit 1. Import its first candidate using import_paper with its exact arxiv_id and version; then call analyze_paper using the returned paper_version_id and question 'What memory mechanism, evaluation and limitations do the authors describe?'. Finish with a short Russian answer containing at least one evidence ID from the analysis and the actual coverage status. Do not repeat tools."
	if *live {
		prompt = `Call search_papers FIRST with query ti:"Efficient Estimation of Word Representations in Vector Space", limit 1. Then import_paper arxiv_id "1301.3781v1" with a selection_reason. Call analyze_paper using the paper_version_id RETURNED BY IMPORT (not search) and question "Какие модели и оценки предлагают авторы и какие ограничения признают?". After analysis finish with a short Russian paragraph citing at least one exact evidence ID returned by analyze_paper and stating its actual coverage status. Do not repeat tools or search_library in this turn.`
	}
	run, _, err := runs.Create(ctx, "alice", conversation.ID, "primary", prompt)
	must(err)
	result, err := runner.Run(ctx, "alice", conversation.ID, run.ID)
	must(err)
	check(result.Run.Status == "completed", "primary not completed")
	primary := inspect(ctx, db, result, []string{"search_papers", "import_paper", "analyze_paper"})
	var imported struct {
		Paper           arxiv.Paper           `json:"paper"`
		DocumentID      string                `json:"document_id"`
		Extraction      string                `json:"extraction_status"`
		Cached          bool                  `json:"document_cache_hit"`
		PreviouslyKnown bool                  `json:"previously_known"`
		Index           retrieval.IndexStatus `json:"index"`
	}
	decode(primary["import_paper"], &imported)
	check(imported.Extraction == "full_text" && imported.Index.Status == "ready" && !imported.PreviouslyKnown && !imported.Cached, "fresh source/index status invalid")
	var searchProvenance struct {
		Cached   bool `json:"cache_hit"`
		Attempts int  `json:"attempts"`
	}
	decode(primary["search_papers"], &searchProvenance)
	check(!searchProvenance.Cached && searchProvenance.Attempts > 0, "fresh search not proven")
	var analyzed struct {
		ID      string `json:"analysis_id"`
		Status  string `json:"analysis_status"`
		Covered int    `json:"covered_runes"`
		Total   int    `json:"total_runes"`
	}
	decode(primary["analyze_paper"], &analyzed)
	fmt.Printf("ANALYSIS: status=%s covered=%d total=%d\n", analyzed.Status, analyzed.Covered, analyzed.Total)
	if *artifact != "" {
		data, e := json.MarshalIndent(map[string]any{"live": *live, "primary_usage": result.Usage, "primary_tools": primary, "primary_answer": result.Answer, "analysis_response_diagnostics": analysisDiagnostics(ctx, db, run.ID)}, "", "  ")
		must(e)
		must(os.WriteFile(*artifact, data, 0600))
	}
	complete := analyzed.Status == "complete" && analyzed.Covered == analyzed.Total && analyzed.Total > 0
	if !complete {
		check(*allowPartial && analyzed.Status == "partial" && analyzed.Covered > 0 && analyzed.Covered <= analyzed.Total, "no complete tool analysis")
	}

	analysis, err := analyzer.Analysis(ctx, "alice", analyzed.ID)
	must(err)
	check(len(analysis.Evidence) > 0 && len(analysis.SuccessfulRanges) > 0, "no validated real reading")
	if complete {
		check(len(analysis.SuccessfulRanges) >= 2, "sequential full analysis not proven")
	} else {
		check(analysis.ErrorCode != "", "partial result lacks reason")
		fmt.Printf("LIVE GAP: complete coverage not confirmed; partial reason=%s\n", analysis.ErrorCode)
	}
	cited := false
	for _, ev := range analysis.Evidence {
		if strings.Contains(result.Answer, ev.ID) {
			cited = true
		}
	}
	check(cited, "primary answer lacks analyzed evidence")
	var steps int
	must(db.DB.QueryRowContext(ctx, `SELECT count(*) FROM analysis_segments WHERE analysis_id=?`, analysis.ID).Scan(&steps))
	var outer int
	must(db.DB.QueryRowContext(ctx, `SELECT count(*) FROM messages WHERE run_id=? AND role='assistant'`, run.ID).Scan(&outer))
	check(result.Usage.Requests == outer+steps, "nested requests not in runner usage")
	fmt.Printf("PASS: AgentRunner search/import/sequential analysis; source=%s format status=%s reading_parts=%d coverage=%d/%d requests=%d tokens=%d\n", imported.Paper.AbstractURL, imported.Extraction, len(analysis.SuccessfulRanges), analyzed.Covered, analyzed.Total, result.Usage.Requests, result.Usage.TotalTokens)
	follow := "T11_FOLLOWUP: Use search_library for query 'Как работает консолидация памяти?', lexical_terms 'memory consolidation', limit 2. Then use read_passages for exactly one evidence_id returned by search_library. Finish in Russian with that exact evidence ID and what the passage supports. Do not import or analyze again."
	if *live {
		follow = `Use search_library for query "Какие модели векторных представлений слов сравниваются?", lexical_terms "continuous bag of words skip gram", limit 2. Then use read_passages with exactly one evidence_id from its hits. Finish with one short Russian paragraph explaining that passage and citing its exact evidence_id. Do not search_papers/import/analyze again.`
	}
	next, _, err := runs.Create(ctx, "alice", conversation.ID, "followup", follow)
	must(err)
	followResult, err := runner.Run(ctx, "alice", conversation.ID, next.ID)
	must(err)
	outputs := inspect(ctx, db, followResult, []string{"search_library", "read_passages"})
	var found struct {
		Mode     string          `json:"retrieval_mode"`
		Coverage float64         `json:"index_coverage"`
		Hits     []retrieval.Hit `json:"hits"`
	}
	decode(outputs["search_library"], &found)
	check(found.Mode == "hybrid" && found.Coverage == 1 && len(found.Hits) > 0, "followup not hybrid")
	var passages struct {
		Hits []retrieval.Hit `json:"passages"`
	}
	decode(outputs["read_passages"], &passages)
	check(len(passages.Hits) == 1 && strings.Contains(followResult.Answer, passages.Hits[0].EvidenceID), "followup answer lacks original evidence")
	var diagnostics string
	must(db.DB.QueryRowContext(ctx, `SELECT private_payload_json FROM run_events WHERE run_id=? AND event_type='retrieval_completed'`, next.ID).Scan(&diagnostics))
	var traced retrieval.SearchResult
	decode(diagnostics, &traced)
	check(len(traced.LexicalCandidates) > 0 && len(traced.DenseCandidates) > 0, "both branches not traced")
	check(followResult.Usage.Requests == 3, "followup used unexpected nested requests")
	_, err = search.Evidence(ctx, "bob", passages.Hits[0].EvidenceID)
	check(errors.Is(err, retrieval.ErrNotFound), "foreign evidence leaked")
	bob, err := search.Search(ctx, "bob", retrieval.SearchRequest{Query: "memory", PaperVersionIDs: []string{imported.Paper.ID}})
	must(err)
	check(len(bob.Hits) == 0 && bob.TotalChunks == 0, "foreign retrieval leaked")
	fmt.Println("PASS: same-session follow-up → both real BM25/Qwen3 branches + RRF → original passage → cited answer; second owner isolated")
	if !*live {
		adversarial(ctx, db, runs, client, registry, catalog, docs, analyzer, imported.Paper.ID, passages.Hits[0].EvidenceID)
	}
	must(db.Close())
	db, err = store.Open(ctx, filepath.Join(*runtime, "tools.sqlite"))
	must(err)
	docs, err = documents.New(db.DB, documents.Config{CacheDir: filepath.Join(*runtime, "cache")})
	must(err)
	analyzer, err = research.NewAnalyzer(db, docs, research.AnalyzerConfig{})
	must(err)
	restored, err := analyzer.Analysis(ctx, "alice", analysis.ID)
	must(err)
	check(restored.Status == analysis.Status && restored.CoveredRunes == analyzed.Covered, "coverage lost on reopen")
	messages, err := db.Messages(ctx, "alice", conversation.ID)
	must(err)
	check(len(messages) > 10, "history lost on reopen")
	var integrity string
	must(db.DB.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity))
	check(integrity == "ok", "SQLite integrity")
	document, err := docs.Get(ctx, analysis.DocumentID)
	must(err)
	record := map[string]any{"document_format": document.Format, "content_hash": document.ContentHash, "search_provenance": json.RawMessage(primary["search_papers"]), "import_provenance": json.RawMessage(primary["import_paper"]), "complete_analysis_confirmed": complete, "live": *live, "model": provider.Model, "embedding_identity": embed.Identity(), "source": imported.Paper.AbstractURL, "document_cache_hit": imported.Cached, "primary_usage": result.Usage, "followup_usage": followResult.Usage, "analysis_id": analysis.ID, "analysis_status": analysis.Status, "covered_runes": analysis.CoveredRunes, "total_runes": analysis.TotalRunes, "ranges": analysis.SuccessfulRanges, "warnings": analysis.Warnings, "source_hash_document_id": analysis.DocumentID, "retrieval": traced, "read_passages": passages.Hits, "primary_answer": result.Answer, "followup_answer": followResult.Answer, "finished_at": time.Now().UTC().Format(time.RFC3339)}
	if *artifact != "" {
		data, err := json.MarshalIndent(record, "", "  ")
		must(err)
		must(os.WriteFile(*artifact, data, 0600))
	}
	fmt.Println("PASS: persisted analysis status/coverage/history after SQLite reopen; integrity_check=ok; no unit tests")
}

// Verify real durable assistant/tool pairs, not a separate dispatcher.
func inspect(ctx context.Context, db *store.Store, result agent.Result, expected []string) map[string]string {
	messages, err := db.Messages(ctx, "alice", result.Run.ConversationID)
	must(err)
	names := map[string]string{}
	out := map[string]string{}
	for _, m := range messages {
		if m.RunID != result.Run.ID {
			continue
		}
		var calls []deepseek.ToolCall
		must(json.Unmarshal(m.ToolCalls, &calls))
		for _, c := range calls {
			names[c.ID] = c.Function.Name
		}
		if m.Role == "tool" {
			check(m.Content != nil, "empty tool result")
			name, ok := names[m.ToolCallID]
			check(ok, "orphan tool result")
			out[name] = *m.Content
			check(!strings.Contains(*m.Content, `"error":{"code"`), "tool error: "+name+" "+*m.Content)
		}
	}
	for _, name := range expected {
		check(out[name] != "", "missing tool: "+name)
	}
	check(result.Run.Status == "completed", "turn did not complete")
	return out
}

// Inspect only normal assistant content shape/JSON errors, never reasoning.
func analysisDiagnostics(ctx context.Context, db *store.Store, run string) []map[string]any {
	rows, err := db.DB.QueryContext(ctx, `SELECT private_payload_json FROM run_events WHERE run_id=? AND event_type='model_request_finished' ORDER BY seq`, run)
	must(err)
	defer rows.Close()
	var diagnostics []map[string]any
	for rows.Next() {
		var raw string
		must(rows.Scan(&raw))
		var event struct {
			Response struct {
				Message      struct{ Content *string }
				FinishReason string
			} `json:"response"`
		}
		decode(raw, &event)
		if event.Response.Message.Content == nil {
			continue
		}
		text := strings.TrimSpace(*event.Response.Message.Content)
		if strings.HasPrefix(text, "```json\n") && strings.HasSuffix(text, "```") {
			text = strings.TrimSuffix(strings.TrimPrefix(text, "```json\n"), "```")
		}
		var observations research.Observations
		parseErr := json.Unmarshal([]byte(text), &observations)
		detail := map[string]any{"finish_reason": event.Response.FinishReason, "content_bytes": len(text), "valid_json": json.Valid([]byte(text)), "methods_nil": observations.Methods == nil, "results_nil": observations.Results == nil, "limitations_nil": observations.Limitations == nil}
		if parseErr != nil {
			detail["json_error"] = fmt.Sprintf("%T", parseErr)
		}
		diagnostics = append(diagnostics, detail)
	}
	must(rows.Err())
	return diagnostics
}

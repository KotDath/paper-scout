package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"paper-scout/internal/agent"
	"paper-scout/internal/arxiv"
	"paper-scout/internal/deepseek"
	"paper-scout/internal/documents"
	"paper-scout/internal/research"
	"paper-scout/internal/researchtools"
	"paper-scout/internal/retrieval"
	"paper-scout/internal/store"
)

func adversarial(ctx context.Context, db *store.Store, runs *research.Service, client *deepseek.DeepSeekClient, registry *agent.Registry, catalog *arxiv.Client, docs *documents.Service, analyzer *research.Analyzer, paperID, evID string) {
	limits := agent.DefaultLimits()
	limits.Retries = 0
	execute := func(owner, prompt string, reg *agent.Registry, l agent.Limits) agent.Result {
		conv, err := db.CreateConversation(ctx, owner, "Boundary integration")
		must(err)
		r, _, err := runs.Create(ctx, owner, conv.ID, fmt.Sprint(time.Now().UnixNano()), prompt)
		must(err)
		runner, err := agent.New(db, runs, client, reg, l)
		must(err)
		out, _ := runner.Run(ctx, owner, conv.ID, r.ID)
		return out
	}
	outputs := func(r agent.Result, owner string) []string {
		m, err := db.Messages(ctx, owner, r.Run.ConversationID)
		must(err)
		var out []string
		for _, row := range m {
			if row.RunID == r.Run.ID && row.Role == "tool" {
				out = append(out, *row.Content)
			}
		}
		return out
	}
	r := execute("alice", "T11_INVALID "+paperID, registry, limits)
	bad := outputs(r, "alice")
	check(r.Run.Status == "completed" && len(bad) == 6, "validation exchange failed")
	for i, b := range bad {
		code := "invalid_arguments"
		if i >= 4 {
			code = "not_found"
		}
		check(strings.Contains(b, code), "wrong validation/selection error")
	}
	var count int
	must(db.DB.QueryRowContext(ctx, `SELECT count(*) FROM run_sources WHERE run_id=?`, r.Run.ID).Scan(&count))
	check(count == 0, "invalid arguments changed selection")
	r = execute("bob", "T11_FOREIGN "+evID+" "+paperID, registry, limits)
	foreign := outputs(r, "bob")
	check(len(foreign) == 3 && strings.Contains(foreign[0], "not_found") && strings.Contains(foreign[1], "not_found"), "foreign read/analysis leaked")
	var empty struct {
		Hits []retrieval.Hit `json:"hits"`
	}
	decode(foreign[2], &empty)
	check(len(empty.Hits) == 0, "foreign scoped search leaked")
	fmt.Println("PASS: real runner rejects owner injection, dates/limits/URLs, unselected public-cache analysis, and foreign evidence/analysis/search")
	limits.Requests = 3
	r = execute("alice", "T11_BUDGET", registry, limits)
	check(r.Run.Status == "partial" && r.Run.ErrorCode == "budget_exceeded" && r.Usage.Requests == 3, "shared budget bypassed")
	budgetOutputs := outputs(r, "alice")
	check(len(budgetOutputs) == 2, "budget tool pairs missing")
	var partial struct {
		ID      string `json:"analysis_id"`
		Status  string `json:"analysis_status"`
		Error   string `json:"error_code"`
		Covered int    `json:"covered_runes"`
		Total   int    `json:"total_runes"`
	}
	decode(budgetOutputs[1], &partial)
	check(partial.Status == "partial" && partial.Error == "budget_exceeded" && partial.Covered > 0 && partial.Covered < partial.Total, "budget result lost partial coverage")
	var succeeded, failed, attempted int
	must(db.DB.QueryRowContext(ctx, `SELECT sum(status='succeeded'),sum(status='failed'),sum(attempted) FROM analysis_segments WHERE analysis_id=?`, partial.ID).Scan(&succeeded, &failed, &attempted))
	check(succeeded == 1 && failed == 1 && attempted == 1, "unadmitted analysis request sent to provider")
	fmt.Println("PASS: outer and nested model requests share actual AgentRunner cap; stopped before HTTP, retained successful prefix and failed unattempted range")
	limits = agent.DefaultLimits()
	limits.Retries = 0
	_, err := db.DB.ExecContext(ctx, `UPDATE user_papers SET saved=1,read=1,note='PRIVATE_NOTE_NOT_FOR_MODEL' WHERE user_id='alice' AND paper_version_id=?`, paperID)
	must(err)
	r = execute("alice", "T11_IMPORTS", registry, limits)
	imports := outputs(r, "alice")
	check(len(imports) == 5, "four-source/abstract exchange failed")
	check(strings.Contains(imports[0], `"previously_known":true`) && strings.Contains(imports[0], `"saved":true`) && strings.Contains(imports[0], `"read":true`), "known source not marked")
	check(strings.Contains(imports[1], `"extraction_status":"abstract_only"`) && strings.Contains(imports[4], `"analysis_status":"abstract_only"`), "abstract fallback misrepresented")
	check(strings.Contains(imports[3], "budget_exceeded"), "fourth source accepted")
	must(db.DB.QueryRowContext(ctx, `SELECT count(*) FROM run_sources WHERE run_id=?`, r.Run.ID).Scan(&count))
	check(count == 3, "source cap not durable")
	must(db.DB.QueryRowContext(ctx, `SELECT count(*) FROM user_papers up JOIN paper_versions p ON p.id=up.paper_version_id WHERE up.user_id='alice' AND p.arxiv_id='2301.00004'`).Scan(&count))
	check(count == 0, "fourth import granted membership")
	for _, out := range imports {
		check(!strings.Contains(out, "PRIVATE_NOTE_NOT_FOR_MODEL"), "private note in public cache/tool context")
	}
	var saved, read bool
	var note string
	must(db.DB.QueryRowContext(ctx, `SELECT saved,read,note FROM user_papers WHERE user_id='alice' AND paper_version_id=?`, paperID).Scan(&saved, &read, &note))
	check(saved && read && note == "PRIVATE_NOTE_NOT_FOR_MODEL", "import changed library state")
	fmt.Println("PASS: idempotent known import preserves saved/read/note; three-source cap blocks fourth membership; unavailable HTML/PDF yields explicit analyzed abstract_only")
	degraded := retrieval.New(db.DB, nil)
	reg, err := researchtools.New(db, catalog, docs, degraded, analyzer)
	must(err)
	r = execute("alice", "T11_DEGRADED", reg, limits)
	out := inspect(ctx, db, r, []string{"import_paper", "search_library"})
	check(strings.Contains(out["import_paper"], `"last_error":"embedding_unavailable"`) && strings.Contains(out["import_paper"], `"status":"failed"`), "degraded import hid failure")
	check(strings.Contains(out["search_library"], `"retrieval_mode":"lexical_only"`), "degradation called hybrid")
	fmt.Println("PASS: missing embedding backend preserves import and explicit failed index/lexical_only mode; no false hybrid or analysis promotion")
}

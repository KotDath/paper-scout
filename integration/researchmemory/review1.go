package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	"paper-scout/internal/researchmemory"
	"paper-scout/internal/store"
)

// Review regressions cross the real auth/HTTP/SQLite/Ollama boundaries. Only
// synthetic stored research and the timing of a real query request are controlled.
func review1(ctx context.Context, db *store.Store, memory *researchmemory.Service, base, token string, hook *atomic.Pointer[func()]) error {
	query := "Episodic consolidation uses forgetting and recurrent review a"
	search := func() (int, researchmemory.SearchResult, string, error) {
		var response struct {
			researchmemory.SearchResult
			Error struct {
				Code      string `json:"code"`
				Retryable bool   `json:"retryable"`
			} `json:"error"`
		}
		code, err := request(base, "POST", "/api/v1/research-memory/search", token, researchmemory.SearchRequest{Query: query}, &response)
		if err == nil && code == 503 && !response.Error.Retryable {
			return code, response.SearchResult, response.Error.Code, errors.New("pending response must be retryable")
		}
		return code, response.SearchResult, response.Error.Code, err
	}
	patch := func(id string, revision int, text string) (researchmemory.Entry, error) {
		var out researchmemory.Entry
		code, err := request(base, "PATCH", "/api/v1/research-memory/"+id, token, researchmemory.Patch{ExpectedRevision: revision, Correction: &text}, &out)
		if err != nil {
			return out, err
		}
		if code != 200 {
			return out, fmt.Errorf("review correction HTTP %d", code)
		}
		return out, nil
	}
	var claimID, noteID string
	if err := db.DB.QueryRowContext(ctx, `SELECT id FROM research_memories WHERE owner='owner_a' AND kind='claim' AND paper_id='paper_a' AND facet='method'`).Scan(&claimID); err != nil {
		return err
	}
	setNote := func(text string) error {
		_, err := db.UpdatePaper(ctx, "owner_a", "paper_a", store.PaperPatch{Note: &text})
		return err
	}
	note := "Personal preference: focus on reproducible benchmarks."
	if err := setNote(note); err != nil {
		return err
	}
	if _, err := memory.Refresh(ctx, "owner_a"); err != nil {
		return err
	}
	if err := db.DB.QueryRowContext(ctx, `SELECT id FROM research_memories WHERE owner='owner_a' AND kind='user_note' AND paper_id='paper_a'`).Scan(&noteID); err != nil {
		return err
	}
	entry, err := memory.Get(ctx, "owner_a", noteID)
	if err != nil {
		return err
	}
	correction := strings.Repeat("Use the corrected personal criterion. ", 90)
	if _, err = patch(noteID, entry.Revision, correction); err != nil {
		return err
	}
	assertGroup := func(r researchmemory.SearchResult, current string) error {
		if err := contains(r, current, true); err != nil {
			return err
		}
		if err := contains(r, correction, true); err != nil {
			return errors.New("attached disputed note lost its correction")
		}
		found := false
		for _, h := range r.Hits {
			if h.Kind == "claim" && h.PaperVersionID == "paper_a" {
				found = true
			}
		}
		if !found {
			return errors.New("corrected note was not recalled through a scientific claim")
		}
		return nil
	}
	code, result, _, err := search()
	if err != nil {
		return err
	}
	if code != 200 {
		return fmt.Errorf("corrected note search HTTP %d", code)
	}
	if err = assertGroup(result, note); err != nil {
		return err
	}
	// Both note and its correction are mandatory. Their combined size exceeds
	// 12000; the associated scientific hit must be dropped as an entire group.
	long := strings.Repeat("personal criterion ", 500)
	if err = setNote(long); err != nil {
		return err
	}
	code, result, _, err = search()
	if err != nil {
		return err
	}
	if code != 200 {
		return fmt.Errorf("annotation budget HTTP %d", code)
	}
	for _, h := range result.Hits {
		if h.PaperVersionID == "paper_a" {
			return errors.New("over-budget group published without its whole annotation closure")
		}
	}
	if err = contains(result, correction, false); err != nil {
		return err
	}
	if err = setNote(note); err != nil {
		return err
	}
	if _, err = memory.Refresh(ctx, "owner_a"); err != nil {
		return err
	}
	fmt.Println("PASS review P1: claim-attached disputed note includes correction; whole mandatory group budgeted before publication")

	// Sixteen earlier note jobs consume exactly one bounded refresh, leaving the
	// relevant edit pending. Search must signal retryable pending, never 404 or a
	// successful claim without its mandatory annotation.
	for i := 0; i < 16; i++ {
		id := fmt.Sprintf("aaa_pending_%02d", i)
		if _, err = db.DB.ExecContext(ctx, `INSERT INTO paper_versions SELECT ?,?,version,title,authors_json,abstract,abstract_url,html_url,pdf_url,published_at,updated_at,fetched_at FROM paper_versions WHERE id='paper_b'`, id, "synthetic-"+id); err != nil {
			return err
		}
		if _, err = db.DB.ExecContext(ctx, `INSERT INTO user_papers(user_id,paper_version_id,note,added_at) VALUES('owner_a',?,'Unrelated personal annotation','2026-01-02T00:00:00Z')`, id); err != nil {
			return err
		}
	}
	updated := "New personal preference after queued edits."
	if err = setNote(updated); err != nil {
		return err
	}
	code, _, errorCode, err := search()
	if err != nil {
		return err
	}
	if code != 503 || errorCode != "memory_pending" {
		return fmt.Errorf("pending note should give retryable memory_pending, got HTTP %d/%s", code, errorCode)
	}
	code, result, _, err = search()
	if err != nil {
		return err
	}
	if code != 200 {
		return fmt.Errorf("recovered backlog HTTP %d", code)
	}
	if err = assertGroup(result, updated); err != nil {
		return err
	}
	if err = contains(result, note, false); err != nil {
		return err
	}
	// Fill any bounded indexing backlog, so the injected edit occurs during query
	// inference rather than document inference (the original integration covers it).
	for i := 0; i < 4; i++ {
		indexed, e := memory.Index(ctx, "owner_a", researchmemory.SearchRequest{})
		if e != nil {
			return e
		}
		if indexed.Attempted == 0 {
			break
		}
		if i == 3 {
			return errors.New("review index setup backlog exceeded bounded fixture")
		}
	}
	changedDuringQuery := "Latest personal preference written during query inference."
	var mutationErr error
	var mutated bool
	mutate := func() { mutated = true; mutationErr = setNote(changedDuringQuery) }
	hook.Store(&mutate)
	code, _, errorCode, err = search()
	if err != nil {
		return err
	}
	if mutationErr != nil {
		return mutationErr
	}
	if !mutated {
		return errors.New("query inference race hook did not execute")
	}
	if code != 503 || errorCode != "memory_pending" {
		return fmt.Errorf("query-race note should be pending, got HTTP %d/%s", code, errorCode)
	}
	code, result, _, err = search()
	if err != nil {
		return err
	}
	if code != 200 {
		return fmt.Errorf("query-race recovery HTTP %d", code)
	}
	if err = assertGroup(result, changedDuringQuery); err != nil {
		return err
	}
	if err = contains(result, updated, false); err != nil {
		return err
	}
	fmt.Println("PASS review P2: >16 note jobs and edit during real query inference return explicit pending503, then current corrected note on retry")

	// Consolidate the same claim into seven distinct reports. Correct the last
	// episode too: discovery must still find that mandatory correction even when
	// its origin is outside the five-origin context budget.
	for i := 0; i < 5; i++ {
		conv := fmt.Sprintf("conv_review_%d", i)
		run := fmt.Sprintf("run_review_%d", i)
		report := fmt.Sprintf("report_review_%d", i)
		analysis := fmt.Sprintf("analysis_review_%d", i)
		statements := []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO conversations SELECT ?,user_id,title,created_at FROM conversations WHERE id='conv_a'`, []any{conv}},
			{`INSERT INTO runs(id,user_id,conversation_id,client_request_id,request_message,status,created_at,budget_json) SELECT ?,user_id,?,?,request_message,status,created_at,budget_json FROM runs WHERE id='run_a'`, []any{run, conv, run}},
			{`INSERT INTO run_sources SELECT ?,paper_version_id,selection_reason,analysis_status FROM run_sources WHERE run_id='run_a'`, []any{run}},
			{`INSERT INTO paper_analyses SELECT ?,?,document_id,question,status,result_json,error_code,created_at,finished_at FROM paper_analyses WHERE id='analysis_a'`, []any{analysis, run}},
			{`INSERT INTO reports SELECT ?,user_id,?,markdown,citations_json,sources_json,coverage_json,limitations_json,created_at FROM reports WHERE id='report_a'`, []any{report, run}},
		}
		for _, st := range statements {
			if _, err = db.DB.ExecContext(ctx, st.sql, st.args...); err != nil {
				return err
			}
		}
		if _, err = memory.Capture(ctx, "owner_a", report); err != nil {
			return err
		}
	}
	var episodeID string
	if err = db.DB.QueryRowContext(ctx, `SELECT id FROM research_memories WHERE kind='episode' AND facet='report_review_4'`).Scan(&episodeID); err != nil {
		return err
	}
	episodeCorrection := "This seventh episode also requires original-source verification."
	if _, err = patch(episodeID, 1, episodeCorrection); err != nil {
		return err
	}
	code, result, _, err = search()
	if err != nil {
		return err
	}
	if code != 200 {
		return fmt.Errorf("seven-origin search HTTP %d", code)
	}
	if err = assertGroup(result, changedDuringQuery); err != nil {
		return err
	}
	if err = contains(result, episodeCorrection, true); err != nil {
		return err
	}
	reports := map[string]bool{}
	for _, h := range result.Hits {
		if h.ReportID != "" {
			reports[h.ReportID] = true
		}
		for _, o := range h.Origins {
			reports[o.ReportID] = true
		}
	}
	if len(reports) != 5 {
		return fmt.Errorf("whole recall group has %d origins, expected 5", len(reports))
	}
	entry, err = memory.Get(ctx, "owner_a", claimID)
	if err != nil {
		return err
	}
	if len(entry.Origins) != 7 {
		return errors.New("context origin budget destroyed durable provenance")
	}
	fmt.Println("PASS review P2: seven durable origins, five across all published hits/annotations; omitted-origin correction remains mandatory")
	return nil
}

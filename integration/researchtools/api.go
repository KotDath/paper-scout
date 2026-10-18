package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	"paper-scout/internal/agent"
	"paper-scout/internal/httpapi"
	"paper-scout/internal/research"
	"paper-scout/internal/researchapp"
	"paper-scout/internal/retrieval"
	"paper-scout/internal/store"
)

type apiClient struct{ url, token string }

func (c apiClient) request(method, path string, body any, code int, out any) []byte {
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		must(err)
	}
	req, err := http.NewRequest(method, c.url+path, bytes.NewReader(data))
	must(err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	must(err)
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	must(err)
	check(response.StatusCode == code, fmt.Sprintf("HTTP %s %s: got %d expected %d", method, path, response.StatusCode, code))
	if out != nil {
		must(json.Unmarshal(raw, out))
	}
	return raw
}
func (c apiClient) create(conversation, key, message string) string {
	var out struct {
		ID          string `json:"run_id"`
		ProgressURL string `json:"progress_url"`
	}
	c.request("POST", "/api/v1/conversations/"+conversation+"/runs", map[string]string{"message": message, "client_request_id": key}, 202, &out)
	check(out.ID != "" && out.ProgressURL == "/api/v1/runs/"+out.ID, "run admission contract")
	return out.ID
}
func (c apiClient) poll(id string) store.Run {
	deadline := time.Now().Add(8 * time.Minute)
	for time.Now().Before(deadline) {
		var r store.Run
		c.request("GET", "/api/v1/runs/"+id, nil, 200, &r)
		if r.Status != "queued" && r.Status != "running" {
			return r
		}
		time.Sleep(50 * time.Millisecond)
	}
	panic("HTTP polling deadline")
}
func (c apiClient) events(id string) []store.RunEvent {
	out := []store.RunEvent{}
	after := int64(0)
	for {
		var page struct {
			Items []store.RunEvent `json:"items"`
			Next  int64            `json:"next_after_seq"`
			More  bool             `json:"has_more"`
		}
		c.request("GET", fmt.Sprintf("/api/v1/runs/%s/events?after_seq=%d&limit=3", id, after), nil, 200, &page)
		for _, e := range page.Items {
			check(e.Seq > after, "events out of order")
			after = e.Seq
			out = append(out, e)
		}
		check(page.Next == after, "event cursor")
		if !page.More {
			return out
		}
	}
}
func login(url, name string) apiClient {
	c := apiClient{url: url}
	var result struct {
		Token string `json:"session_token"`
	}
	c.request("POST", "/api/v1/auth/login", map[string]string{"login": name, "password": "api-integration-password"}, 200, &result)
	c.token = result.Token
	check(c.token != "", "no login session")
	return c
}

func verifyAPI(runtime string, db *store.Store, runs *research.Service, runner *agent.AgentRunner, search *retrieval.Service, live bool, artifact string, identity any) {
	// Existing integration users have deliberately unusable hashes. Set a public
	// integration-only password in this disposable DB; authentication stays real.
	hash, err := bcrypt.GenerateFromPassword([]byte("api-integration-password"), bcrypt.DefaultCost)
	must(err)
	_, err = db.DB.Exec(`UPDATE users SET password_hash=? WHERE id IN ('alice','bob')`, string(hash))
	must(err)
	researchapp.Attach(db, runs, runner, search)
	server := httptest.NewServer(httpapi.NewWithRuns(db, runs, search))
	defer server.Close()
	alice := login(server.URL, "tools-alice")
	bob := login(server.URL, "tools-bob")
	var conv store.Conversation
	alice.request("POST", "/api/v1/conversations", map[string]string{"title": "T12 HTTP research"}, 201, &conv)
	primary := "T11_PRIMARY: Research memory mechanisms with sources."
	follow := "T11_FOLLOWUP: Explain memory consolidation using original evidence."
	if live {
		primary = `Call search_papers FIRST with query ti:"Efficient Estimation of Word Representations in Vector Space", limit 1. Then import_paper arxiv_id "1301.3781v1" with selection_reason. Call analyze_paper using paper_version_id RETURNED BY IMPORT (not search) and question "Какие модели и оценки предлагают авторы и какие ограничения признают?". Finish with a short Russian report citing at least one exact evidence ID returned by analysis and its actual coverage status. Do not repeat tools or search_library in this turn.`
		follow = `Use search_library for query "Какие модели векторных представлений слов сравниваются?", lexical_terms "continuous bag of words skip gram", limit 2. Then read_passages for exactly one returned evidence_id. Finish in Russian explaining the passage and citing that exact evidence_id. Do not search_papers/import/analyze again.`
	}
	id := alice.create(conv.ID, "primary", primary)
	repeated := alice.create(conv.ID, "primary", primary)
	check(repeated == id, "idempotent admission created another run")
	alice.request("POST", "/api/v1/conversations/"+conv.ID+"/runs", map[string]string{"message": "changed", "client_request_id": "primary"}, 409, nil)
	run := alice.poll(id)
	check(run.Status == "completed" && run.ReportID != nil, "HTTP primary did not save report: "+run.Status+"/"+run.ErrorCode)
	var report store.Report
	public := alice.request("GET", "/api/v1/reports/"+*run.ReportID, nil, 200, &report)
	check(len(report.Sources) == 1 && report.Markdown != "", "report missing source or markdown")
	source := report.Sources[0]
	paper := source.Paper.ID
	check(len(source.Coverage) == 1, "no analysis coverage")
	coverage := source.Coverage[0]
	fmt.Printf("HTTP ANALYSIS: status=%s covered=%d total=%d\n", coverage.Status, coverage.CoveredRunes, coverage.TotalRunes)
	// A partial live result is valid API data, never evidence of complete reading.
	check(coverage.Status == "complete" && coverage.CoveredRunes == coverage.TotalRunes && coverage.TotalRunes > 0, "API verification requires complete analysis")
	var citations []retrieval.Hit
	must(json.Unmarshal(report.Citations, &citations))
	check(len(citations) > 0, "report citation missing")
	evidence := citations[0].EvidenceID
	alice.request("GET", "/api/v1/evidence/"+evidence, nil, 200, nil)
	for _, private := range []string{"reasoning_content", "private_payload_json", "provider_json", "local_path", "api-integration-password", "integration-only"} {
		check(!bytes.Contains(public, []byte(private)), "report leaks private field")
	}
	events := alice.events(id)
	saved, finished := false, false
	for _, e := range events {
		if e.Type == "report_saved" {
			saved = true
		}
		if e.Type == "run_finished" {
			check(saved, "finished before report commit")
			finished = true
		}
	}
	check(finished, "no terminal event")
	alice.request("GET", "/api/v1/runs/"+id, nil, 200, nil)
	for _, path := range []string{"/conversations/" + conv.ID, "/runs/" + id, "/runs/" + id + "/events", "/reports/" + report.ID, "/papers/" + paper, "/evidence/" + evidence} {
		bob.request("GET", "/api/v1"+path, nil, 404, nil)
	}
	bob.request("PATCH", "/api/v1/library/"+paper, map[string]any{"saved": true, "note": "foreign"}, 404, nil)
	var library struct {
		Items []store.Paper `json:"items"`
		Next  string        `json:"next_cursor"`
	}
	bob.request("GET", "/api/v1/library", nil, 200, &library)
	check(len(library.Items) == 0, "foreign library leaked")
	alice.request("PATCH", "/api/v1/library/"+paper, map[string]any{"saved": true, "read": true, "note": "T12 persistent personal note"}, 200, nil)
	alice.request("PATCH", "/api/v1/library/"+paper, map[string]any{}, 400, nil)
	alice.request("PATCH", "/api/v1/library/"+paper, map[string]any{"note": strings.Repeat("я", 10001)}, 400, nil)
	alice.request("PATCH", "/api/v1/library/"+paper, map[string]any{"owner": "bob", "saved": true}, 400, nil)
	alice.request("GET", "/api/v1/library?limit=101", nil, 400, nil)
	alice.request("GET", "/api/v1/reports/"+report.ID+"?owner=bob", nil, 400, nil)
	check(alice.create(conv.ID, "primary", primary) == id, "terminal retry created another report")
	var cancelledCompleted store.Run
	alice.request("POST", "/api/v1/runs/"+id+"/cancel", nil, 200, &cancelledCompleted)
	check(cancelledCompleted.Status == "completed" && cancelledCompleted.ReportID != nil, "terminal cancel changed report")
	if !live {
		verifyAPIFailures(db, alice, conv.ID)
	}
	fmt.Println("PASS: HTTP admission/polling/report_saved + terminal commit; complete coverage/citations; auth ownership and library validation")
	followID := alice.create(conv.ID, "followup", follow)
	followRun := alice.poll(followID)
	check(followRun.Status == "completed" && followRun.ReportID != nil, "followup missing report")
	var followReport store.Report
	alice.request("GET", "/api/v1/reports/"+*followRun.ReportID, nil, 200, &followReport)
	must(json.Unmarshal(followReport.Citations, &citations))
	check(len(citations) > 0 && len(followReport.Sources) > 0, "followup lacks cited original source")
	hybrid := false
	for _, e := range alice.events(followID) {
		if e.Type == "retrieval_completed" {
			var payload struct {
				Mode    string `json:"retrieval_mode"`
				Lexical int    `json:"lexical_candidates"`
				Dense   int    `json:"dense_candidates"`
			}
			must(json.Unmarshal(e.Payload, &payload))
			hybrid = payload.Mode == "hybrid" && payload.Lexical > 0 && payload.Dense > 0
		}
	}
	check(hybrid, "HTTP followup not hybrid")
	fmt.Println("PASS: HTTP same-conversation followup uses real BM25/Qwen3/RRF and persists cited source/coverage")
	// A deliberately queued row is recovery input, not model evidence. No paid
	// replay is attached after reopen. Completed reports must remain byte stable.
	var interruptedConv store.Conversation
	alice.request("POST", "/api/v1/conversations", map[string]string{"title": "Recovery"}, 201, &interruptedConv)
	interrupted, _, err := db.CreateRun(ctx, "alice", interruptedConv.ID, "interrupted", "Do not replay this queued run")
	must(err)
	server.Close()
	runs.Close()
	runs.Wait()
	must(db.Close())
	reopened, err := store.Open(ctx, filepath.Join(runtime, "tools.sqlite"))
	must(err)
	defer reopened.Close()
	must(reopened.RecoverRuns(ctx))
	restoredRuns := research.New(context.Background(), reopened)
	defer restoredRuns.Close()
	restarted := httptest.NewServer(httpapi.NewWithRuns(reopened, restoredRuns, retrieval.New(reopened.DB, nil)))
	defer restarted.Close()
	alice.url = restarted.URL
	bob.url = restarted.URL
	restored := alice.request("GET", "/api/v1/reports/"+report.ID, nil, 200, nil)
	check(bytes.Equal(public, restored), "report snapshot changed across restart")
	alice.request("GET", "/api/v1/reports/"+followReport.ID, nil, 200, nil)
	var restoredPaper store.Paper
	alice.request("GET", "/api/v1/papers/"+paper, nil, 200, &restoredPaper)
	check(restoredPaper.Saved && restoredPaper.Read && restoredPaper.Note == "T12 persistent personal note", "library mutation lost")
	alice.request("GET", "/api/v1/library?limit=1", nil, 200, &library)
	check(len(library.Items) == 1 && library.Items[0].ID == paper, "library lost")
	var recovery store.Run
	alice.request("GET", "/api/v1/runs/"+interrupted.ID, nil, 200, &recovery)
	check(recovery.Status == "interrupted", "queued run not recovered")
	alice.request("GET", "/api/v1/evidence/"+evidence, nil, 200, nil)
	bob.request("GET", "/api/v1/reports/"+report.ID, nil, 404, nil)
	var history struct {
		Messages []store.Message `json:"messages"`
	}
	alice.request("GET", "/api/v1/conversations/"+conv.ID, nil, 200, &history)
	check(len(history.Messages) >= 4, "history lost")
	var integrity string
	must(reopened.DB.QueryRow("PRAGMA integrity_check").Scan(&integrity))
	check(integrity == "ok", "SQLite integrity")
	fmt.Println("PASS: HTTP listener + SQLite restart preserves reports/sources/evidence/history/notes/auth isolation; queued run interrupted without replay; integrity_check=ok")
	if artifact != "" {
		data, err := json.MarshalIndent(map[string]any{"live": live, "primary_run": run, "primary_report": report, "followup_run": followRun, "followup_report": followReport, "events": events, "embedding_identity": identity, "restart_verified": true, "complete_analysis_confirmed": true, "finished_at": time.Now().UTC().Format(time.RFC3339)}, "", "  ")
		must(err)
		must(os.WriteFile(artifact, data, 0600))
	}
}

// These checks cross HTTP, the real worker/provider client and SQLite. Only the
// external provider fixture is controlled; production modules are not mocked.
func verifyAPIFailures(db *store.Store, alice apiClient, conversation string) {
	var other, third store.Conversation
	alice.request("POST", "/api/v1/conversations", map[string]string{"title": "Concurrent worker"}, 201, &other)
	alice.request("POST", "/api/v1/conversations", map[string]string{"title": "Capacity"}, 201, &third)
	first := alice.create(conversation, "slow", "T12_SLOW")
	second := alice.create(other.ID, "slow", "T12_SLOW")
	alice.request("POST", "/api/v1/conversations/"+conversation+"/runs", map[string]string{"message": "conflict", "client_request_id": "conflict"}, 409, nil)
	alice.request("POST", "/api/v1/conversations/"+third.ID+"/runs", map[string]string{"message": "capacity", "client_request_id": "capacity"}, 429, nil)
	for _, id := range []string{first, second} {
		alice.request("POST", "/api/v1/runs/"+id+"/cancel", nil, 200, nil)
		r := alice.poll(id)
		check(r.Status == "cancelled" && r.ReportID == nil, "cancel created report")
		for _, e := range alice.events(id) {
			check(e.Type != "report_saved", "cancelled report_saved")
		}
	}
	_, err := db.DB.Exec(`CREATE TRIGGER t12_integration_report_failure BEFORE INSERT ON reports WHEN new.markdown='T12_FAIL_REPORT' BEGIN SELECT RAISE(ABORT,'integration insert failure'); END`)
	must(err)
	failed := alice.create(conversation, "fail-report", "T12_FAIL_REPORT")
	r := alice.poll(failed)
	check(r.Status == "failed" && r.ErrorCode == "finalization_error" && r.ReportID == nil, "report transaction failure misreported")
	for _, e := range alice.events(failed) {
		check(e.Type != "report_saved", "failed report transaction left event")
	}
	var count int
	must(db.DB.QueryRow(`SELECT count(*) FROM reports WHERE run_id=?`, failed).Scan(&count))
	check(count == 0, "failed transaction retained report")
	_, err = db.DB.Exec(`DROP TRIGGER t12_integration_report_failure`)
	must(err)
	pending := alice.create(conversation, "pending-analysis", "T12_PENDING")
	deadline := time.Now().Add(30 * time.Second)
	covered := false
	for time.Now().Before(deadline) {
		var state struct {
			store.Run
			Sources []store.ReportSource `json:"sources"`
		}
		alice.request("GET", "/api/v1/runs/"+pending, nil, 200, &state)
		if len(state.Sources) > 0 && len(state.Sources[0].Coverage) > 0 {
			c := state.Sources[0].Coverage[0]
			if c.RunID == pending && c.CoveredRunes > 0 {
				check(c.CoveredRunes < c.TotalRunes && c.Status == "partial" && c.ErrorCode == "analysis_incomplete", "pending coverage misrepresented")
				covered = true
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	check(covered, "pending analysis never exposed persisted reading prefix")
	alice.request("POST", "/api/v1/runs/"+pending+"/cancel", nil, 200, nil)
	check(alice.poll(pending).Status == "cancelled", "pending analysis not cancelled")
	fmt.Println("PASS: worker cancellation/two-run pool; report transaction rollback; polling exposes persisted partial reading prefix")
}

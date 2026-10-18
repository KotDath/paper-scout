package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"

	"paper-scout/internal/research"
	"paper-scout/internal/store"
)

// Only the explicitly enabled UI fault chapter uses these synthetic records.
// No global service is stopped, and no scientific/live acceptance is claimed.
func videoFallbacks(ctx context.Context, db *store.Store, next http.Handler) http.Handler {
	for _, key := range []string{"partial", "abstract", "pending", "unavailable"} {
		must(seedResearch(ctx, db, key))
		label := "T22 CONTROLLED FIXTURE: " + key
		_, err := db.DB.ExecContext(ctx, `UPDATE conversations SET title=? WHERE id=?`, label, "conv_"+key)
		must(err)
		var raw string
		must(db.DB.QueryRowContext(ctx, `SELECT result_json FROM paper_analyses WHERE id=?`, "analysis_"+key).Scan(&raw))
		var a research.Analysis
		must(json.Unmarshal([]byte(raw), &a))
		if key == "partial" {
			a.Status, a.ErrorCode = "partial", "provider_unavailable"
			a.CoveredRunes = a.TotalRunes / 2
			a.SuccessfulRanges = []research.Range{{Start: 0, End: a.CoveredRunes}}
		}
		if key == "abstract" {
			a.Status, a.ExtractionStatus = "abstract_only", "abstract_only"
		}
		_, err = db.DB.ExecContext(ctx, `UPDATE paper_analyses SET status=?,result_json=? WHERE id=?`, a.Status, mustJSON(a), a.ID)
		must(err)
		_, err = db.DB.ExecContext(ctx, `UPDATE documents SET extraction_status=? WHERE id=?`, a.ExtractionStatus, a.DocumentID)
		must(err)
		coverage := store.Coverage{ID: a.ID, RunID: a.RunID, DocumentID: a.DocumentID, Status: a.Status, TotalRunes: a.TotalRunes, CoveredRunes: a.CoveredRunes, ErrorCode: a.ErrorCode, SuccessfulRanges: []store.CoverageRange{{Start: 0, End: a.CoveredRunes}}}
		sources := []store.ReportSource{{Paper: store.Paper{ID: a.PaperVersionID, Title: label}, AnalysisStatus: a.Status, SelectionReason: "Explicit synthetic fallback presentation, no paid provider", Coverage: []store.Coverage{coverage}}}
		_, err = db.DB.ExecContext(ctx, `UPDATE reports SET markdown=?,sources_json=?,coverage_json=? WHERE id=?`, label+"\n\nSynthetic UI fallback. Not a real article analysis or provider result.", mustJSON(sources), mustJSON([]store.Coverage{coverage}), "report_"+key)
		must(err)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Preserve real auth/owner checks before substituting fault responses.
		rec := httptest.NewRecorder()
		next.ServeHTTP(rec, r)
		if rec.Code == http.StatusOK && r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/api/v1/research-memory/") {
			fault, _ := os.ReadFile(os.Getenv("SCOUT_VIDEO_FAULT_FILE"))
			code := strings.TrimSpace(string(fault))
			if code == "memory_pending" || code == "memory_unavailable" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusServiceUnavailable)
				must(json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code, "message": "T22 CONTROLLED HTTP FAULT FIXTURE", "retryable": true}}))
				return
			}
		}
		if rec.Code == http.StatusOK && r.Method == "GET" && strings.Contains(r.URL.Path, "/reports/report_") {
			key := strings.TrimPrefix(r.URL.Path, "/api/v1/reports/report_")
			if key == "pending" || key == "unavailable" {
				var report map[string]any
				must(json.Unmarshal(rec.Body.Bytes(), &report))
				report["research_memory"] = map[string]any{"retrieval_mode": "unavailable", "reasons": []string{"T22_CONTROLLED_HTTP_FIXTURE", "memory_" + key}, "recalled": []any{}, "referenced": []any{}}
				w.Header().Set("Content-Type", "application/json")
				must(json.NewEncoder(w).Encode(report))
				return
			}
		}
		for k, values := range rec.Header() {
			w.Header()[k] = values
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	})
}

// Package researchapp wires the HTTP run lifecycle to the single agent harness.
package researchapp

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"paper-scout/internal/agent"
	"paper-scout/internal/research"
	"paper-scout/internal/researchmemory"
	"paper-scout/internal/retrieval"
	"paper-scout/internal/store"
)

var evidenceID = regexp.MustCompile(`ev_[A-Za-z0-9_]+`)

func Attach(db *store.Store, runs *research.Service, runner *agent.AgentRunner, search *retrieval.Service, memories ...*researchmemory.Service) {
	var memory *researchmemory.Service
	if len(memories) > 0 {
		memory = memories[0]
		runner.WithResearchMemory(memory)
	}
	runs.SetExecutor(func(ctx context.Context, run store.Run) {
		_, err := runner.RunWithFinalizer(ctx, run.UserID, run.ConversationID, run.ID, func(ctx context.Context, result agent.Result) error {
			report, err := materialize(ctx, db, search, result)
			if err != nil {
				return err
			}
			usage, err := json.Marshal(result.Usage)
			if err != nil {
				return err
			}
			return db.CompleteReport(ctx, run.UserID, run.ID, report, usage)
		})
		if err != nil {
			// Log only server IDs and a fixed message; provider errors/results stay private.
			slog.Warn("research worker stopped without completed report", "run_id", run.ID)
		}
		// Capture is separate from terminal/report commit. The T19 trigger has
		// already queued recovery if this process dies or projection fails.
		if memory != nil {
			captureCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
			saved, readErr := db.Run(captureCtx, run.UserID, run.ID)
			if readErr == nil && saved.ReportID != nil {
				projection, captureErr := memory.Capture(captureCtx, run.UserID, *saved.ReportID)
				state := "captured"
				if captureErr != nil {
					state = "pending"
				}
				payload, _ := json.Marshal(map[string]any{"status": state, "processed": projection.Processed, "added": projection.Added, "noop": projection.Noop, "reasons": projection.Reasons})
				eventCtx, finishEvent := context.WithTimeout(context.Background(), 3*time.Second)
				if err := db.MemoryCaptureEvent(eventCtx, run.UserID, run.ID, payload); err != nil {
					slog.Warn("memory capture diagnostic persistence failed", "run_id", run.ID)
				}
				finishEvent()
				slog.Info("research memory write", "run_id", run.ID, "status", state, "processed", projection.Processed, "added", projection.Added, "noop", projection.Noop)
			}
			stop()
		}
		// Admission/persistence errors must not leave a queued run occupying capacity.
		persist, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		current, readErr := db.Run(persist, run.UserID, run.ID)
		if readErr == nil && (current.Status == "queued" || current.Status == "running") {
			if current.Status == "queued" {
				_, _ = db.StartRun(persist, run.UserID, run.ID)
			}
			_, _ = runs.Finish(persist, run.UserID, run.ID, "failed", "worker_error", nil)
		}
	})
}

func materialize(ctx context.Context, db *store.Store, search *retrieval.Service, result agent.Result) (store.Report, error) {
	r := result.Run
	sources, err := db.RunSources(ctx, r.UserID, r.ID)
	if err != nil {
		return store.Report{}, err
	}
	report := store.Report{Markdown: result.Answer, Sources: sources, Limitations: []string{}, Grounded: result.Grounded, GroundingRepairs: result.GroundingRepairs, HistoryReplay: result.HistoryReplay}
	citations := []retrieval.Hit{}
	seen := map[string]bool{}
	papers := map[string]bool{}
	for _, s := range sources {
		papers[s.Paper.ID] = true
	}
	ids := evidenceID.FindAllString(result.Answer, -1)
	if result.Grounded {
		ids = result.EvidenceIDs
	}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		hit, err := search.Evidence(ctx, r.UserID, id)
		if errors.Is(err, retrieval.ErrNotFound) {
			if result.Grounded {
				return report, errors.New("accepted evidence unavailable")
			}
			report.Limitations = append(report.Limitations, "unresolved_citation: "+id)
			continue
		}
		if err != nil {
			return report, err
		}
		citations = append(citations, hit)
		if !papers[hit.PaperVersionID] {
			source, err := db.Source(ctx, r.UserID, r.ID, hit.PaperVersionID)
			if err != nil {
				return report, err
			}
			report.Sources = append(report.Sources, source)
			papers[hit.PaperVersionID] = true
		}
	}
	if len(citations) == 0 {
		report.Limitations = append(report.Limitations, "no_addressable_citations")
	}
	coverage := map[string][]store.Coverage{}
	for _, source := range report.Sources {
		coverage[source.Paper.ID] = source.Coverage
		if source.AnalysisStatus != "complete" {
			report.Limitations = append(report.Limitations, source.Paper.ID+": analysis_"+source.AnalysisStatus)
		}
		for _, doc := range source.Paper.Documents {
			var warnings []struct {
				Code string `json:"code"`
			}
			if err = json.Unmarshal(doc.Warnings, &warnings); err != nil {
				return report, err
			}
			for _, warning := range warnings {
				report.Limitations = append(report.Limitations, doc.ID+": "+warning.Code)
			}
			if doc.IndexStatus != "ready" {
				report.Limitations = append(report.Limitations, doc.ID+": index_"+doc.IndexStatus)
			}
		}
	}
	// Retrieval degradation is a separate axis from reading coverage. Persist only
	// the server-generated public diagnostics, never private provider payloads.
	events, err := db.RunEvents(ctx, r.UserID, r.ID, 0, 1000)
	if err != nil {
		return report, err
	}
	for _, event := range events {
		if event.Type == "retrieval_completed" {
			var state struct {
				Mode    string   `json:"retrieval_mode"`
				Reasons []string `json:"reasons"`
			}
			if err = json.Unmarshal(event.Payload, &state); err != nil {
				return report, err
			}
			if state.Mode != "hybrid" {
				report.Limitations = append(report.Limitations, "retrieval_"+state.Mode+": "+strings.Join(state.Reasons, ", "))
			}
		}
	}
	if err = materializeMemory(ctx, db, r.UserID, r.ID, &report, result.MemoryIDs); err != nil {
		return report, err
	}
	report.Citations, err = json.Marshal(citations)
	if err != nil {
		return report, err
	}
	report.Coverage, err = json.Marshal(coverage)
	return report, err
}

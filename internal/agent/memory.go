package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"paper-scout/internal/research"
	"paper-scout/internal/researchmemory"
	"paper-scout/internal/store"
)

// WithResearchMemory enables automatic recall without changing legacy callers.
// Configure once, before the runner is attached to the executor.
func (a *AgentRunner) WithResearchMemory(memory *researchmemory.Service) *AgentRunner {
	a.memory = memory
	return a
}

const memoryContextRunes = 12000
const memoryTimeout = 30 * time.Second

func (a *AgentRunner) recall(ctx context.Context, run store.Run, point, query string) error {
	previous, err := a.db.MemorySnapshots(ctx, run.UserID, run.ID)
	if err != nil {
		return err
	}
	if len(previous) >= 2 {
		return store.ErrRunState
	}
	cap := memoryContextRunes
	if point == "question" {
		cap = memoryContextRunes / 2
	}
	snap := store.MemorySnapshot{Point: point, Query: truncateQuery(query), RetrievalMode: "unavailable", Reasons: []string{}, Hits: []store.MemoryHit{}}
	if utf8.RuneCountInString(query) > 4000 {
		snap.Reasons = append(snap.Reasons, "memory_query_truncated")
	}
	// Budget the exact JSON before retrieval, so the recorded query is also the
	// query actually searched. Reserve room for bounded diagnostics added later.
	if err := fitRecallQuery(previous, &snap, cap-256); err != nil {
		return err
	}
	cutoff, err := time.Parse(time.RFC3339Nano, run.CreatedAt)
	if err != nil {
		return err
	}
	readCtx, cancel := context.WithTimeout(ctx, memoryTimeout)
	started := time.Now()
	found, searchErr := a.memory.Search(readCtx, run.UserID, researchmemory.SearchRequest{Query: snap.Query, Limit: 5, ExcludeRunID: run.ID, Before: cutoff})
	cancel()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if searchErr != nil {
		reason := "memory_unavailable"
		switch {
		case errors.Is(searchErr, researchmemory.ErrPending):
			reason = "memory_pending"
		case errors.Is(searchErr, researchmemory.ErrScope):
			reason = "memory_scope_limit"
		case errors.Is(searchErr, context.DeadlineExceeded):
			reason = "memory_timeout"
		}
		snap.Reasons = append(snap.Reasons, reason)
	} else {
		snap.RetrievalMode = found.RetrievalMode
		snap.Reasons = append(snap.Reasons, found.Reasons...)
		// The service retains complete per-base annotation closures even when
		// public Hits deduplicates a correction shared by several groups.
		groups := found.Groups
		already := map[string]bool{}
		origins := map[string]bool{}
		for _, p := range previous {
			for _, h := range p.Hits {
				already[fmt.Sprintf("%s:%d:%d", h.MemoryID, h.Revision, h.CharStart)] = true
				for _, o := range h.Origins {
					origins[o.ReportID] = true
				}
			}
		}
		for _, group := range groups {
			duplicate := true
			for _, h := range group {
				if !already[fmt.Sprintf("%s:%d:%d", h.MemoryID, h.Revision, h.CharStart)] {
					duplicate = false
				}
			}
			if duplicate {
				continue
			}
			nextOrigins := map[string]bool{}
			for id := range origins {
				nextOrigins[id] = true
			}
			for _, h := range group {
				for _, o := range h.Origins {
					nextOrigins[o.ReportID] = true
				}
			}
			if len(nextOrigins) > 5 {
				snap.Reasons = appendUnique(snap.Reasons, "origin_budget")
				continue
			}
			candidate := snap
			candidate.Hits = append(append([]store.MemoryHit{}, snap.Hits...), group...)
			raw, _ := json.Marshal(append(append([]store.MemorySnapshot{}, previous...), candidate))
			if utf8.RuneCount(raw) > cap-256 {
				snap.Reasons = appendUnique(snap.Reasons, "memory_prompt_budget")
				continue
			}
			snap = candidate
			origins = nextOrigins
		}
	}
	// Diagnostics are bounded too, even with a large historical job backlog.
	raw, err := json.Marshal(append(append([]store.MemorySnapshot{}, previous...), snap))
	if err != nil {
		return err
	}
	if utf8.RuneCount(raw) > cap {
		snap.Hits = []store.MemoryHit{}
		snap.Reasons = []string{"memory_prompt_budget", "memory_diagnostics_omitted"}
		if snap.Query != query {
			snap.Reasons = append(snap.Reasons, "memory_query_truncated")
		}
	}
	// Recheck the final serialized representation after every reduction. Query
	// and diagnostics can expand in JSON even when there are no remaining hits.
	if err := fitRecallQuery(previous, &snap, cap); err != nil {
		return err
	}
	slog.Info("research memory read", "run_id", run.ID, "point", point, "mode", snap.RetrievalMode, "hits", len(snap.Hits), "refresh_processed", found.Refresh.Processed, "refresh_pending", found.Refresh.Pending, "duration_ms", time.Since(started).Milliseconds())
	return a.db.SaveMemorySnapshot(ctx, run.UserID, run.ID, snap)
}

// fitRecallQuery uses the same encoding as AgentSession's actual context string.
// Rune-safe prefix selection accounts for JSON expansion (e.g. & -> \u0026),
// not just the decoded query length. A context that cannot fit is never saved.
func fitRecallQuery(previous []store.MemorySnapshot, snap *store.MemorySnapshot, cap int) error {
	size := func() (int, error) {
		raw, err := json.Marshal(append(append([]store.MemorySnapshot{}, previous...), *snap))
		return utf8.RuneCount(raw), err
	}
	n, err := size()
	if err != nil || n <= cap {
		return err
	}
	snap.Reasons = appendUnique(snap.Reasons, "memory_query_truncated")
	query := []rune(snap.Query)
	low, high := 0, len(query)
	for low < high {
		mid := (low + high + 1) / 2
		snap.Query = string(query[:mid])
		n, err = size()
		if err != nil {
			return err
		}
		if n <= cap {
			low = mid
		} else {
			high = mid - 1
		}
	}
	snap.Query = string(query[:low])
	n, err = size()
	if err != nil {
		return err
	}
	if n > cap {
		return &ToolError{Code: "memory_context_limit"}
	}
	return nil
}

func appendUnique(values []string, v string) []string {
	for _, s := range values {
		if s == v {
			return values
		}
	}
	return append(values, v)
}
func truncateQuery(s string) string {
	r := []rune(s)
	if len(r) > 4000 {
		return string(r[:4000])
	}
	return s
}

// No extra generation: derive the second query only from successful analysis
// observations and the already available paper metadata, never provider reasoning.
func (a *AgentRunner) contentQuery(ctx context.Context, owner string, output json.RawMessage) string {
	var payload struct {
		Paper        string          `json:"paper_version_id"`
		Observations json.RawMessage `json:"observations"`
	}
	if json.Unmarshal(output, &payload) != nil || len(payload.Observations) == 0 {
		return ""
	}
	var one research.Observations
	var all []research.Observations
	if json.Unmarshal(payload.Observations, &one) == nil {
		all = []research.Observations{one}
	} else if json.Unmarshal(payload.Observations, &all) != nil {
		return ""
	}
	parts := []string{}
	for _, o := range all {
		for _, claims := range [][]research.Claim{o.Methods, o.Results, o.Limitations} {
			for _, c := range claims {
				if strings.TrimSpace(c.Text) != "" {
					parts = append(parts, c.Text)
				}
			}
		}
	}
	if len(parts) == 0 {
		return ""
	}
	if p, err := a.db.Paper(ctx, owner, payload.Paper); err == nil {
		parts = append([]string{p.Title}, parts...)
	}
	return truncateQuery(strings.Join(parts, "\n"))
}

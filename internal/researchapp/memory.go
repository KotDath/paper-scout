package researchapp

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"

	"paper-scout/internal/store"
)

var memoryLink = regexp.MustCompile(`\[memory:([^\]\r\n]*)\]`)

// Only actual run snapshots authorize links. A matching title, another run's
// snapshot, or even an owned but unrecalled entry cannot authorize a reference.
func materializeMemory(ctx context.Context, db *store.Store, owner, run string, report *store.Report) error {
	snapshots, err := db.MemorySnapshots(ctx, owner, run)
	if err != nil {
		return err
	}
	issued := map[string]store.MemoryHit{}
	if len(snapshots) > 0 {
		report.ResearchMemory = &store.ResearchMemory{RetrievalMode: "hybrid", Reasons: []string{}, Recalled: []store.MemoryHit{}, Referenced: []store.MemoryHit{}}
		seen := map[string]bool{}
		rank := map[string]int{"hybrid": 0, "hybrid_partial": 1, "lexical_only": 2, "unavailable": 3}
		for _, snap := range snapshots {
			if rank[snap.RetrievalMode] > rank[report.ResearchMemory.RetrievalMode] {
				report.ResearchMemory.RetrievalMode = snap.RetrievalMode
			}
			for _, reason := range snap.Reasons {
				report.ResearchMemory.Reasons = uniqueReason(report.ResearchMemory.Reasons, reason)
			}
			for _, h := range snap.Hits {
				key := fmt.Sprintf("%s:%d:%d", h.MemoryID, h.Revision, h.CharStart)
				if !seen[key] {
					report.ResearchMemory.Recalled = append(report.ResearchMemory.Recalled, h)
					seen[key] = true
				}
				issued[h.MemoryID] = h
			}
		}
		if report.ResearchMemory.RetrievalMode != "hybrid" {
			report.Limitations = append(report.Limitations, "research_memory_"+report.ResearchMemory.RetrievalMode)
		}
	}
	referenced := map[string]bool{}
	var failure error
	report.Markdown = memoryLink.ReplaceAllStringFunc(report.Markdown, func(marker string) string {
		id := memoryLink.FindStringSubmatch(marker)[1]
		h, ok := issued[id]
		if ok {
			var exists int
			err := db.DB.QueryRowContext(ctx, `SELECT 1 FROM research_memories WHERE id=? AND owner=?`, id, owner).Scan(&exists)
			if errors.Is(err, sql.ErrNoRows) {
				ok = false
			} else if err != nil {
				failure = err
				ok = false
			}
		}
		if !ok {
			report.Limitations = uniqueReason(report.Limitations, "unresolved_memory_reference")
			return "[unresolved_memory_reference]"
		}
		if !referenced[id] {
			report.ResearchMemory.Referenced = append(report.ResearchMemory.Referenced, h)
			referenced[id] = true
		}
		return marker
	})
	return failure
}
func uniqueReason(values []string, v string) []string {
	for _, s := range values {
		if s == v {
			return values
		}
	}
	return append(values, v)
}

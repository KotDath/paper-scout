package researchmemory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"paper-scout/internal/store"
	"time"
)

// ReportAnnotations is current user context for an immutable report, independent
// of retrieval ranks, run snapshots, or which memory link opened the report.
// Items contains changed episode/claim targets and all current linked user
// annotations, including exclusions. Text and revisions remain those of T19.
type ReportAnnotations struct {
	ReportID string  `json:"report_id"`
	Items    []Entry `json:"items"`
}

func (s *Service) AnnotationsForReport(ctx context.Context, owner, reportID string) (ReportAnnotations, error) {
	out := ReportAnnotations{ReportID: reportID, Items: []Entry{}}
	// Capture checks exact report ownership before doing any projection. This is
	// one bounded, idempotent report write, with no inference or paid replay.
	if _, err := s.Capture(ctx, owner, reportID); err != nil {
		return out, err
	}
	if _, err := s.Refresh(ctx, owner); err != nil {
		return out, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	var exists int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM reports WHERE id=? AND user_id=?`, reportID, owner).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return out, store.ErrNotFound
	}
	if err != nil {
		return out, err
	}
	// Capture admits at most 48 cards plus the episode. A changed historical
	// projection can retain earlier cards; exceed a finite bound explicitly.
	rows, err := tx.QueryContext(ctx, `SELECT m.id FROM research_memories m WHERE m.owner=? AND m.kind IN ('episode','claim') AND ((m.kind='episode' AND m.facet=?) OR EXISTS (SELECT 1 FROM research_memory_origins o JOIN reports r ON r.id=o.report_id WHERE o.memory_id=m.id AND r.id=? AND r.user_id=?)) ORDER BY m.kind,m.id LIMIT 129`, owner, reportID, reportID, owner)
	if err != nil {
		return out, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if rowErr != nil {
		return out, rowErr
	}
	if len(ids) > 128 {
		return out, ErrScope
	}
	req := SearchRequest{Before: time.Now().UTC()}
	emitted := map[string]bool{}
	add := func(entry Entry) {
		if !emitted[entry.MemoryID] {
			emitted[entry.MemoryID] = true
			out.Items = append(out.Items, entry)
		}
	}
	for _, id := range ids {
		entry, e := loadEntry(ctx, tx, owner, id)
		if e != nil {
			return out, e
		}
		oo, e := origins(ctx, tx, owner, id, req, true)
		if e != nil {
			return out, e
		}
		decorate(&entry.Hit, oo)
		if entry.Excluded || entry.State == "disputed" {
			add(entry)
		}
		aa, e := annotations(ctx, tx, owner, entry.Hit, req, true)
		if e != nil {
			return out, e
		}
		for _, h := range aa {
			current, e := loadEntry(ctx, tx, owner, h.MemoryID)
			if e != nil {
				return out, e
			}
			current.Hit = h
			add(current)
		}
	}
	// Never silently omit a correction, nor return a payload the client cannot
	// read. Large reports fail explicitly while their original text stays usable.
	raw, err := json.Marshal(out)
	if err != nil {
		return out, err
	}
	if len(raw) > 2*1024*1024 {
		return out, ErrScope
	}
	return out, tx.Commit()
}

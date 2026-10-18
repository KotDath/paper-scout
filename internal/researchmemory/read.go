package researchmemory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"paper-scout/internal/store"
	"strings"
	"time"
	"unicode/utf8"
)

type querier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

const entryCols = `id,kind,facet,paper_id,target_id,title,text,content_hash,revision,state,excluded`

func loadEntry(ctx context.Context, q querier, owner, id string) (Entry, error) {
	out := Entry{Passages: []Hit{}, Annotations: []Hit{}}
	err := q.QueryRowContext(ctx, `SELECT `+entryCols+` FROM research_memories WHERE id=? AND owner=?`, id, owner).Scan(&out.MemoryID, &out.Kind, &out.Facet, &out.PaperVersionID, &out.TargetID, &out.Title, &out.Text, &out.ContentHash, &out.Revision, &out.State, &out.Excluded)
	if errors.Is(err, sql.ErrNoRows) {
		return out, store.ErrNotFound
	}
	if err != nil {
		return out, err
	}
	if out.Kind == "user_note" {
		var current string
		var pending int
		e := q.QueryRowContext(ctx, `SELECT u.note,j.generation-j.processed FROM user_papers u JOIN research_memory_jobs j ON j.owner=u.user_id AND j.kind='note' AND j.source_id=u.paper_version_id WHERE u.user_id=? AND u.paper_version_id=?`, owner, out.PaperVersionID).Scan(&current, &pending)
		if errors.Is(e, sql.ErrNoRows) {
			return out, store.ErrNotFound
		}
		if e != nil {
			return out, e
		}
		if current != out.Text || pending != 0 {
			return out, ErrPending
		}
	}
	if out.Kind != "claim" {
		out.Facet = ""
	}
	out.Authority = "model"
	if out.Kind == "episode" {
		out.Authority = "research_history"
	}
	if strings.HasPrefix(out.Kind, "user_") {
		out.Authority = "user"
	}
	out.EvidenceIDs = []string{}
	out.Origins = []Origin{}
	return out, nil
}
func origins(ctx context.Context, q querier, owner, id string, req SearchRequest, historical ...bool) ([]Origin, error) {
	extra := ""
	if len(historical) == 0 || !historical[0] {
		extra = ` AND NOT EXISTS(SELECT 1 FROM research_memories ex JOIN research_memory_origins xo ON xo.memory_id=ex.id WHERE ex.owner=p.user_id AND ex.kind='episode' AND ex.excluded=1 AND xo.report_id=p.id)`
	}
	rows, err := q.QueryContext(ctx, `SELECT o.metadata_json FROM research_memory_origins o JOIN reports p ON p.id=o.report_id JOIN runs r ON r.id=p.run_id WHERE o.memory_id=? AND p.user_id=? AND r.status IN ('completed','partial') AND p.created_at<=? AND r.id!=?`+extra+` ORDER BY p.created_at,p.id,o.analysis_id LIMIT 65`, id, owner, req.Before.UTC().Format(time.RFC3339Nano), req.ExcludeRunID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Origin{}
	for rows.Next() {
		var raw string
		var o Origin
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &o); err != nil {
			return nil, err
		}
		out = append(out, o)
		if len(out) > 64 {
			return nil, ErrScope
		}
	}
	rowErr := rows.Err()
	rows.Close()
	if rowErr != nil {
		return nil, rowErr
	}
	valid := []Origin{}
	for _, o := range out {
		ids := []string{}
		for _, id := range o.EvidenceIDs {
			var exists int
			e := q.QueryRowContext(ctx, `SELECT 1 FROM chunks WHERE id=? AND document_id=?`, id, o.DocumentID).Scan(&exists)
			if e == nil {
				ids = append(ids, id)
			} else if !errors.Is(e, sql.ErrNoRows) {
				return nil, e
			}
		}
		if len(ids) != len(o.EvidenceIDs) {
			o.Limitations = append(o.Limitations, "memory_evidence_unavailable")
		}
		o.EvidenceIDs = ids
		if o.AnalysisID != "" && len(ids) == 0 {
			continue
		}
		valid = append(valid, o)
	}
	return valid, nil
}
func decorate(h *Hit, oo []Origin) {
	h.Origins = oo
	h.EvidenceIDs = []string{}
	for _, o := range oo {
		if h.ReportID == "" {
			h.ReportID = o.ReportID
			h.ConversationID = o.ConversationID
		}
		h.EvidenceIDs = append(h.EvidenceIDs, o.EvidenceIDs...)
	}
	h.EvidenceIDs = unique(h.EvidenceIDs)
}

// scope is applied to BOTH SQL branches before their top-k. A conservative
// owner-wide episode guard prevents a disputed/excluded claim or revised note
// returning through a later report paraphrase whose selected papers differ. Original reports remain immutable.
func scope(owner string, req SearchRequest) (string, []any) {
	where := `m.owner=? AND m.excluded=0 AND m.state IN ('active','disputed') AND m.created_at<=? AND (
 (m.kind='user_note' AND EXISTS(SELECT 1 FROM user_papers u JOIN research_memory_jobs j ON j.owner=u.user_id AND j.source_id=u.paper_version_id AND j.kind='note' WHERE u.user_id=m.owner AND u.paper_version_id=m.paper_id AND u.note=m.text AND trim(u.note)!='' AND j.generation=j.processed))
 OR (m.kind IN ('claim','episode') AND EXISTS (
 SELECT 1 FROM research_memory_origins o JOIN reports r ON r.id=o.report_id JOIN runs run ON run.id=r.run_id
 WHERE o.memory_id=m.id AND r.user_id=m.owner AND r.created_at<=? AND run.id!=? AND run.status IN ('completed','partial')
 AND NOT EXISTS (SELECT 1 FROM research_memories ex JOIN research_memory_origins xo ON xo.memory_id=ex.id WHERE ex.owner=m.owner AND ex.kind='episode' AND ex.excluded=1 AND xo.report_id=r.id))))
 AND NOT EXISTS(SELECT 1 FROM research_memories ex JOIN research_memory_origins xo ON xo.memory_id=ex.id JOIN research_memory_origins mo ON mo.report_id=xo.report_id WHERE ex.owner=m.owner AND ex.kind='episode' AND ex.excluded=1 AND mo.memory_id=m.id)
 AND (m.kind!='claim' OR EXISTS(SELECT 1 FROM research_memory_origins vo JOIN json_each(vo.metadata_json,'$.evidence_ids') ev JOIN chunks vc ON vc.id=ev.value AND vc.document_id=vo.document_id WHERE vo.memory_id=m.id))
 AND (m.kind!='episode' OR NOT EXISTS (
 SELECT 1 FROM research_memories c WHERE c.owner=m.owner AND c.id!=m.id AND (c.excluded=1 OR c.state='disputed')))
 AND (m.kind!='episode' OR NOT EXISTS (
 SELECT 1 FROM research_memory_jobs nj WHERE nj.owner=m.owner AND nj.kind='note'))`
	cutoff := req.Before.UTC().Format(time.RFC3339Nano)
	return where, []any{owner, cutoff, cutoff, req.ExcludeRunID}
}

// Get exposes current passages and provenance, with current annotations even
// when the immutable historical report contains an outdated interpretation.
func (s *Service) Get(ctx context.Context, owner, id string) (Entry, error) {
	if _, err := s.Refresh(ctx, owner); err != nil {
		return Entry{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Entry{}, err
	}
	defer tx.Rollback()
	out, err := s.get(ctx, tx, owner, id)
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}
func (s *Service) get(ctx context.Context, tx *sql.Tx, owner, id string) (Entry, error) {
	out, err := loadEntry(ctx, tx, owner, id)
	if err != nil {
		return out, err
	}
	oo, err := origins(ctx, tx, owner, id, SearchRequest{Before: time.Now().UTC()}, true)
	if err != nil {
		return out, err
	}
	if out.Kind == "user_correction" {
		oo, err = origins(ctx, tx, owner, out.TargetID, SearchRequest{Before: time.Now().UTC()}, true)
		if err != nil {
			return out, err
		}
	}
	decorate(&out.Hit, oo)
	rows, err := tx.QueryContext(ctx, `SELECT text,char_start,char_end FROM research_memory_passages WHERE memory_id=? ORDER BY ordinal`, id)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		h := out.Hit
		if err = rows.Scan(&h.Text, &h.CharStart, &h.CharEnd); err != nil {
			break
		}
		out.Passages = append(out.Passages, h)
	}
	re := rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if re != nil {
		return out, re
	}
	out.Annotations, err = annotations(ctx, tx, owner, out.Hit, SearchRequest{Before: time.Now().UTC()}, true)
	return out, err
}

// annotations are fetched by relation, never by top-k. They must fit before a
// base hit is admitted into the context. Corrections inherit target provenance.
func annotations(ctx context.Context, q querier, owner string, h Hit, req SearchRequest, includeExcluded bool) ([]Hit, error) {
	// Discover every related correction, including one scoped to an origin
	// omitted by the context budget. Output still inherits only h's admitted
	// origins; discovery must never enlarge the published provenance group.
	relationOrigins := h.Origins
	if h.Kind == "claim" {
		all, e := origins(ctx, q, owner, h.MemoryID, req)
		if e != nil {
			return nil, e
		}
		relationOrigins = all
	}
	papers := map[string]bool{}
	reports := map[string]bool{}
	if h.PaperVersionID != "" {
		papers[h.PaperVersionID] = true
	}
	for _, o := range relationOrigins {
		papers[o.PaperVersionID] = true
		reports[o.ReportID] = true
	}
	// A pending note may have no projection yet, or an old revision whose
	// passages have already been invalidated. Do not publish related claims
	// without their mandatory current annotation or report a missing resource.
	for paper := range papers {
		var pending int
		e := q.QueryRowContext(ctx, `SELECT count(*) FROM research_memory_jobs WHERE owner=? AND kind='note' AND source_id=? AND generation>processed`, owner, paper).Scan(&pending)
		if e != nil {
			return nil, e
		}
		if pending > 0 {
			return nil, ErrPending
		}
	}
	// Bounded by owner scope cap, enforced by search before this call.
	rows, err := q.QueryContext(ctx, `SELECT id,kind,paper_id,target_id,excluded FROM research_memories WHERE owner=? AND kind IN ('user_note','user_correction') AND state IN ('active','disputed') AND created_at<=? ORDER BY kind,id LIMIT 20001`, owner, req.Before.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	ids := []string{}
	corrections := map[string][]string{}
	scanned := 0
	for rows.Next() {
		scanned++
		if scanned > MaxScopePassages {
			rows.Close()
			return nil, ErrScope
		}
		var id, kind, paper, target string
		var ex bool
		if err = rows.Scan(&id, &kind, &paper, &target, &ex); err != nil {
			break
		}
		if kind == "user_correction" && (!ex || includeExcluded) {
			corrections[target] = append(corrections[target], id)
		}
		if (!ex || includeExcluded) && ((kind == "user_note" && papers[paper]) || (kind == "user_correction" && target == h.MemoryID)) {
			ids = append(ids, id)
		}
	}
	re := rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if re != nil {
		return nil, re
	}
	// An episode-level correction applies to every representation of that report.
	if len(reports) > 0 {
		rr, e := q.QueryContext(ctx, `SELECT c.id,o.report_id FROM research_memories c JOIN research_memories t ON t.id=c.target_id JOIN research_memory_origins o ON o.memory_id=t.id WHERE c.owner=? AND c.kind='user_correction' AND c.state='active' AND t.kind='episode' AND c.created_at<=? AND (c.excluded=0 OR ?) LIMIT 20001`, owner, req.Before.UTC().Format(time.RFC3339Nano), includeExcluded)
		if e != nil {
			return nil, e
		}
		scanned := 0
		for rr.Next() {
			scanned++
			if scanned > MaxScopePassages {
				rr.Close()
				return nil, ErrScope
			}
			var id, report string
			if e = rr.Scan(&id, &report); e != nil {
				break
			}
			if reports[report] {
				ids = append(ids, id)
			}
		}
		re := rr.Err()
		rr.Close()
		if e != nil {
			return nil, e
		}
		if re != nil {
			return nil, re
		}
	}
	// GET episode also exposes corrections on its underlying scientific claims.
	if includeExcluded && h.Kind == "episode" {
		rr, e := q.QueryContext(ctx, `SELECT DISTINCT c.id,o.report_id FROM research_memories c JOIN research_memory_origins o ON o.memory_id=c.target_id WHERE c.owner=? AND c.kind='user_correction' AND c.state='active' LIMIT 20001`, owner)
		if e != nil {
			return nil, e
		}
		scanned := 0
		for rr.Next() {
			scanned++
			if scanned > MaxScopePassages {
				rr.Close()
				return nil, ErrScope
			}
			var id, report string
			if e = rr.Scan(&id, &report); e != nil {
				break
			}
			if reports[report] {
				ids = append(ids, id)
			}
		}
		re := rr.Err()
		rr.Close()
		if e != nil {
			return nil, e
		}
		if re != nil {
			return nil, re
		}
	}
	out := []Hit{}
	ids = unique(ids)
	visited := map[string]bool{h.MemoryID: true}
	for i := 0; i < len(ids); i++ {
		id := ids[i]
		if visited[id] {
			continue
		}
		visited[id] = true
		entry, e := loadEntry(ctx, q, owner, id)
		if e != nil {
			return nil, e
		}
		if entry.Kind == "user_note" {
			if strings.TrimSpace(entry.Text) == "" {
				continue
			}
			// Resolve the closure before Search admits or budgets this entire group.
			// A scientific claim can bring in a note which itself has been corrected.
			if entry.State == "disputed" && len(corrections[id]) == 0 {
				return nil, ErrPending
			}
			ids = append(ids, corrections[id]...)
		}
		decorate(&entry.Hit, h.Origins)
		out = append(out, entry.Hit)
	}
	return out, nil
}

func (s *Service) Patch(ctx context.Context, owner, id string, p Patch) (Entry, error) {
	if owner == "" || p.ExpectedRevision < 1 || (p.Excluded == nil) == (p.Correction == nil) {
		return Entry{}, ErrInvalid
	}
	if p.Correction != nil && (!utf8.ValidString(*p.Correction) || strings.TrimSpace(*p.Correction) == "" || utf8.RuneCountInString(*p.Correction) > 4000) {
		return Entry{}, ErrInvalid
	}
	if _, err := s.Refresh(ctx, owner); err != nil {
		return Entry{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Entry{}, err
	}
	defer tx.Rollback()
	target, err := loadEntry(ctx, tx, owner, id)
	if err != nil {
		return Entry{}, err
	}
	if target.Revision != p.ExpectedRevision {
		return Entry{}, ErrConflict
	}
	if target.Kind == "user_correction" {
		return Entry{}, ErrInvalid
	}
	if p.Excluded != nil {
		_, err = tx.ExecContext(ctx, `UPDATE research_memories SET excluded=?,revision=revision+1,created_at=? WHERE id=? AND owner=? AND revision=?`, *p.Excluded, now(), id, owner, p.ExpectedRevision)
	} else {
		// A stable correction entry has versioned text; previous versions live only
		// in the audit log and immutable run snapshots, never the current index.
		cid := "mem_correction_" + hash(owner+"\x00"+id)
		var rev int
		e := tx.QueryRowContext(ctx, `SELECT revision FROM research_memories WHERE id=? AND owner=?`, cid, owner).Scan(&rev)
		if errors.Is(e, sql.ErrNoRows) {
			rev = 1
			_, err = tx.ExecContext(ctx, `INSERT INTO research_memories(id,owner,kind,facet,paper_id,target_id,title,text,content_hash,created_at) VALUES(?,?,'user_correction',?,?,?,?,?,?,?)`, cid, owner, id, target.PaperVersionID, id, target.Title, *p.Correction, hash(*p.Correction), now())
		} else if e == nil {
			rev++
			_, err = tx.ExecContext(ctx, `UPDATE research_memories SET text=?,content_hash=?,revision=?,excluded=0,created_at=? WHERE id=?`, *p.Correction, hash(*p.Correction), rev, now(), cid)
		} else {
			return Entry{}, e
		}
		if err != nil {
			return Entry{}, err
		}
		if err = saveRevision(ctx, tx, cid); err != nil {
			return Entry{}, err
		}
		// Corrections are deterministic attachments, never independent candidates.
		_, err = tx.ExecContext(ctx, `UPDATE research_memories SET state='disputed',revision=revision+1,created_at=? WHERE id=? AND owner=? AND revision=?`, now(), id, owner, p.ExpectedRevision)
	}
	if err != nil {
		return Entry{}, err
	}
	if err = saveRevision(ctx, tx, id); err != nil {
		return Entry{}, err
	}
	// A changed revision invalidates all old embeddings atomically.
	if err = passages(ctx, tx, id, target.Title, target.Text, target.Revision+1); err != nil {
		return Entry{}, err
	}
	out, err := s.get(ctx, tx, owner, id)
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}

package researchmemory

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"paper-scout/internal/research"
	"paper-scout/internal/store"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

func hash(s string) string              { b := sha256.Sum256([]byte(s)); return hex.EncodeToString(b[:]) }
func now() string                       { return time.Now().UTC().Format(time.RFC3339Nano) }
func marshal(v any) string              { b, _ := json.Marshal(v); return string(b) }
func normalized(s string) string        { return strings.Join(strings.Fields(s), " ") }
func noteID(owner, paper string) string { return "mem_note_" + hash(owner+"\x00"+paper) }

// Refresh processes at most 2 reports and 16 note changes. Projection is separate
// from indexing; Ollama failure cannot roll back an already committed report.
func (s *Service) Refresh(ctx context.Context, owner string) (RefreshResult, error) {
	out := RefreshResult{Reasons: []string{}}
	for _, kind := range []string{"note", "report"} {
		limit := 2
		if kind == "note" {
			limit = 16
		}
		rows, err := s.db.QueryContext(ctx, `SELECT source_id FROM research_memory_jobs WHERE owner=? AND kind=? AND generation>processed ORDER BY source_id LIMIT ?`, owner, kind, limit)
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
		for _, id := range ids {
			var r RefreshResult
			if kind == "report" {
				r, err = s.Capture(ctx, owner, id)
			} else {
				r, err = s.syncNote(ctx, owner, id)
			}
			if err != nil {
				return out, err
			}
			out.Processed += r.Processed
			out.Added += r.Added
			out.Noop += r.Noop
			out.Reasons = append(out.Reasons, r.Reasons...)
		}
	}
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM research_memory_jobs WHERE owner=? AND generation>processed`, owner).Scan(&out.Pending)
	if out.Pending > 0 {
		out.Reasons = append(out.Reasons, "capture_backlog")
	}
	rows, e := s.db.QueryContext(ctx, `SELECT DISTINCT detail FROM research_memory_jobs WHERE owner=? AND detail!=''`, owner)
	if e != nil {
		return out, e
	}
	for rows.Next() {
		var detail string
		if e = rows.Scan(&detail); e != nil {
			break
		}
		out.Reasons = append(out.Reasons, detail)
	}
	re := rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	if re != nil {
		return out, re
	}
	return out, err
}

// Capture is idempotent and requires a persisted completed/partial report.
// It never calls DeepSeek. Rebuild callers can reset job.processed to zero;
// independent entries/revisions preserve all exclusions and corrections.
func (s *Service) Capture(ctx context.Context, owner, reportID string) (RefreshResult, error) {
	out := RefreshResult{Reasons: []string{}}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	var run, conv, title, question, status, created, markdown, sources, coverage, limits string
	err = tx.QueryRowContext(ctx, `SELECT r.id,r.conversation_id,c.title,r.request_message,r.status,p.created_at,p.markdown,p.sources_json,p.coverage_json,p.limitations_json FROM reports p JOIN runs r ON r.id=p.run_id JOIN conversations c ON c.id=r.conversation_id WHERE p.id=? AND p.user_id=? AND r.user_id=? AND r.status IN ('completed','partial')`, reportID, owner, owner).Scan(&run, &conv, &title, &question, &status, &created, &markdown, &sources, &coverage, &limits)
	if errors.Is(err, sql.ErrNoRows) {
		return out, store.ErrNotFound
	}
	if err != nil {
		return out, err
	}
	var gen, processed int
	err = tx.QueryRowContext(ctx, `SELECT generation,processed FROM research_memory_jobs WHERE owner=? AND kind='report' AND source_id=?`, owner, reportID).Scan(&gen, &processed)
	if err != nil {
		return out, err
	}
	if gen == processed {
		out.Noop = 1
		return out, nil
	}
	var limitations []string
	var src []store.ReportSource
	if err = json.Unmarshal([]byte(sources), &src); err != nil {
		return out, err
	}
	if err = json.Unmarshal([]byte(limits), &limitations); err != nil {
		return out, err
	}
	base := Origin{ReportID: reportID, RunID: run, ConversationID: conv, CreatedAt: created, Status: status, Coverage: json.RawMessage(coverage), Limitations: limitations, EvidenceIDs: []string{}}
	episode := Hit{Kind: "episode", Facet: reportID, Title: title, Text: question + "\n\n" + markdown, ContentHash: hash(question + "\n\n" + markdown)}
	// Bounded report passages retain Unicode coordinates and expose truncation.
	if utf8.RuneCountInString(episode.Text) > 64*PassageRunes {
		out.Reasons = append(out.Reasons, "episode_projection_truncated")
		episode.Text = string([]rune(episode.Text)[:64*PassageRunes])
	}
	episode.ContentHash = hash(episode.Text)
	id, added, err := put(ctx, tx, owner, episode, base)
	if err != nil {
		return out, err
	}
	if added {
		out.Added++
	} else {
		out.Noop++
	}
	for _, source := range src {
		o := base
		o.PaperVersionID = source.Paper.ID
		o.AnalysisStatus = source.AnalysisStatus
		o.Coverage = json.RawMessage(marshal(source.Coverage))
		if err = origin(ctx, tx, id, o, "episode:"+o.PaperVersionID); err != nil {
			return out, err
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT a.id,a.document_id,a.result_json,d.paper_version_id,length(d.text),d.extraction_status,d.warnings_json FROM paper_analyses a JOIN documents d ON d.id=a.document_id JOIN run_sources rs ON rs.run_id=a.run_id AND rs.paper_version_id=d.paper_version_id WHERE a.run_id=? ORDER BY a.created_at,a.id LIMIT 33`, run)
	if err != nil {
		return out, err
	}
	type analysisRow struct {
		id, doc, paper       string
		raw                  sql.NullString
		total                int
		extraction, warnings string
	}
	analyses := []analysisRow{}
	for rows.Next() {
		var a analysisRow
		if err = rows.Scan(&a.id, &a.doc, &a.raw, &a.paper, &a.total, &a.extraction, &a.warnings); err != nil {
			break
		}
		analyses = append(analyses, a)
	}
	re := rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if re != nil {
		return out, re
	}
	count, chars := 0, 0
	for ai, a := range analyses {
		if ai == 32 {
			out.Reasons = append(out.Reasons, "claim_projection_truncated")
			break
		}
		analysis := research.Analysis{Status: "partial", TotalRunes: a.total, ExtractionStatus: a.extraction}
		if err = json.Unmarshal([]byte(a.warnings), &analysis.Warnings); err != nil {
			return out, err
		}
		if a.raw.Valid {
			if err = json.Unmarshal([]byte(a.raw.String), &analysis); err != nil {
				return out, err
			}
		}
		observations := []research.Observations{}
		if analysis.Synthesis != nil {
			observations = append(observations, *analysis.Synthesis)
		} else {
			// Only durable successful reading steps; never failed/pending provider data.
			rr, e := tx.QueryContext(ctx, `SELECT analysis_json,char_start,char_end FROM analysis_segments WHERE analysis_id=? AND kind='reading' AND status='succeeded' ORDER BY ordinal LIMIT 32`, a.id)
			if e != nil {
				return out, e
			}
			for rr.Next() {
				var body string
				var obs research.Observations
				var span research.Range
				if e = rr.Scan(&body, &span.Start, &span.End); e != nil {
					break
				}
				if e = json.Unmarshal([]byte(body), &obs); e != nil {
					break
				}
				observations = append(observations, obs)
				if !a.raw.Valid {
					analysis.SuccessfulRanges = append(analysis.SuccessfulRanges, span)
				}
			}
			re := rr.Err()
			rr.Close()
			if e != nil {
				return out, e
			}
			if re != nil {
				return out, re
			}
		}
		if !a.raw.Valid {
			sort.Slice(analysis.SuccessfulRanges, func(i, j int) bool { return analysis.SuccessfulRanges[i].Start < analysis.SuccessfulRanges[j].Start })
			end := 0
			for _, span := range analysis.SuccessfulRanges {
				if span.End > end {
					analysis.CoveredRunes += span.End - max(end, span.Start)
					end = span.End
				}
			}
		}
		for _, obs := range observations {
			for fi, claims := range [][]research.Claim{obs.Methods, obs.Results, obs.Limitations} {
				for _, claim := range claims {
					text := normalized(claim.Text)
					n := utf8.RuneCountInString(text)
					if n == 0 {
						continue
					}
					if count >= 48 || chars+n > 96000 || n > 4000 {
						out.Reasons = append(out.Reasons, "claim_projection_truncated")
						continue
					}
					ids := []string{}
					for _, ev := range claim.EvidenceIDs {
						var exists int
						e := tx.QueryRowContext(ctx, `SELECT 1 FROM chunks WHERE id=? AND document_id=?`, ev, a.doc).Scan(&exists)
						if e == nil {
							ids = append(ids, ev)
						} else if !errors.Is(e, sql.ErrNoRows) {
							return out, e
						}
					}
					if len(ids) == 0 {
						out.Reasons = append(out.Reasons, "claim_evidence_unavailable")
						continue
					}
					o := base
					o.AnalysisID = a.id
					o.DocumentID = a.doc
					o.PaperVersionID = a.paper
					o.EvidenceIDs = ids
					o.AnalysisStatus = analysis.Status
					o.ExtractionStatus = analysis.ExtractionStatus
					if o.AnalysisStatus == "" {
						o.AnalysisStatus = "partial"
					}
					// Typed safe projection: no provider messages or private reasoning.
					o.Coverage = json.RawMessage(marshal(struct {
						Status   string           `json:"analysis_status"`
						Total    int              `json:"total_runes"`
						Covered  int              `json:"covered_runes"`
						Ranges   []research.Range `json:"successful_ranges"`
						Warnings any              `json:"extraction_warnings"`
						Error    string           `json:"error_code,omitempty"`
					}{o.AnalysisStatus, analysis.TotalRunes, analysis.CoveredRunes, analysis.SuccessfulRanges, analysis.Warnings, analysis.ErrorCode}))
					h := Hit{Kind: "claim", Facet: []string{"method", "result", "limitation"}[fi], Title: title, Text: text, PaperVersionID: a.paper, ContentHash: hash(text)}
					_, added, err = put(ctx, tx, owner, h, o)
					if err != nil {
						return out, err
					}
					if added {
						out.Added++
					} else {
						out.Noop++
					}
					count++
					chars += n
				}
			}
		}
	}
	detail := strings.Join(unique(out.Reasons), ",")
	_, err = tx.ExecContext(ctx, `UPDATE research_memory_jobs SET processed=generation,status='captured',detail=? WHERE owner=? AND kind='report' AND source_id=?`, detail, owner, reportID)
	if err != nil {
		return out, err
	}
	err = tx.Commit()
	out.Processed = 1
	return out, err
}
func origin(ctx context.Context, tx *sql.Tx, id string, o Origin, key string) error {
	var previous string
	err := tx.QueryRowContext(ctx, `SELECT metadata_json FROM research_memory_origins WHERE memory_id=? AND report_id=? AND analysis_id=?`, id, o.ReportID, key).Scan(&previous)
	if err == nil {
		var old Origin
		if err = json.Unmarshal([]byte(previous), &old); err != nil {
			return err
		}
		o.EvidenceIDs = unique(append(old.EvidenceIDs, o.EvidenceIDs...))
		o.Limitations = unique(append(old.Limitations, o.Limitations...))
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO research_memory_origins(memory_id,report_id,analysis_id,document_id,metadata_json) VALUES(?,?,?,?,?) ON CONFLICT(memory_id,report_id,analysis_id) DO UPDATE SET metadata_json=excluded.metadata_json`, id, o.ReportID, key, o.DocumentID, marshal(o))
	return err
}
func put(ctx context.Context, tx *sql.Tx, owner string, h Hit, o Origin) (string, bool, error) {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT id FROM research_memories WHERE owner=? AND kind=? AND paper_id=? AND facet=? AND content_hash=?`, owner, h.Kind, h.PaperVersionID, h.Facet, h.ContentHash).Scan(&id)
	added := false
	if errors.Is(err, sql.ErrNoRows) {
		id = "mem_" + rand.Text()
		_, err = tx.ExecContext(ctx, `INSERT INTO research_memories(id,owner,kind,facet,paper_id,title,text,content_hash,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, id, owner, h.Kind, h.Facet, h.PaperVersionID, h.Title, h.Text, h.ContentHash, o.CreatedAt)
		added = true
		if err == nil {
			err = saveRevision(ctx, tx, id)
		}
		if err == nil {
			err = passages(ctx, tx, id, h.Title, h.Text, 1)
		}
	}
	if err != nil {
		return "", false, err
	}
	err = origin(ctx, tx, id, o, o.AnalysisID)
	return id, added, err
}
func saveRevision(ctx context.Context, tx *sql.Tx, id string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO research_memory_revisions SELECT id,revision,json_object('text',text,'content_hash',content_hash,'state',state,'excluded',excluded,'target_id',target_id),? FROM research_memories WHERE id=?`, now(), id)
	return err
}
func passages(ctx context.Context, tx *sql.Tx, id, title, text string, revision int) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM research_memory_passages WHERE memory_id=?`, id); err != nil {
		return err
	}
	runes := []rune(text)
	for start, ordinal := 0, 0; start < len(runes); start, ordinal = start+PassageRunes, ordinal+1 {
		end := min(start+PassageRunes, len(runes))
		part := string(runes[start:end])
		input := embeddingsInput(title, part)
		_, err := tx.ExecContext(ctx, `INSERT INTO research_memory_passages(id,memory_id,ordinal,char_start,char_end,text,input_hash,revision) VALUES(?,?,?,?,?,?,?,?)`, id+"_"+hash(string(rune(ordinal)))[:8], id, ordinal, start, end, part, hash(input), revision)
		if err != nil {
			return err
		}
	}
	return nil
}
func embeddingsInput(title, text string) string {
	return "Research memory v1\nTitle: " + title + "\n\n" + text
}
func unique(in []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, s := range in {
		if !seen[s] {
			out = append(out, s)
			seen[s] = true
		}
	}
	return out
}

func (s *Service) syncNote(ctx context.Context, owner, paper string) (RefreshResult, error) {
	out := RefreshResult{Reasons: []string{}}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	var text, title, changed string
	var gen int
	err = tx.QueryRowContext(ctx, `SELECT u.note,p.title,j.generation,j.updated_at FROM user_papers u JOIN paper_versions p ON p.id=u.paper_version_id JOIN research_memory_jobs j ON j.owner=u.user_id AND j.source_id=u.paper_version_id AND j.kind='note' WHERE u.user_id=? AND u.paper_version_id=? AND j.generation>j.processed`, owner, paper).Scan(&text, &title, &gen, &changed)
	if errors.Is(err, sql.ErrNoRows) {
		out.Noop = 1
		return out, nil
	}
	if err != nil {
		return out, err
	}
	id := noteID(owner, paper)
	var revision int
	err = tx.QueryRowContext(ctx, `SELECT revision FROM research_memories WHERE id=? AND owner=?`, id, owner).Scan(&revision)
	state := "active"
	if strings.TrimSpace(text) == "" {
		state = "superseded"
	}
	if errors.Is(err, sql.ErrNoRows) {
		revision = 1
		_, err = tx.ExecContext(ctx, `INSERT INTO research_memories(id,owner,kind,paper_id,title,text,content_hash,state,created_at) VALUES(?,?,'user_note',?,?,?,?,?,?)`, id, owner, paper, title, text, hash(text), state, changed)
		out.Added++
	} else if err == nil {
		revision++
		_, err = tx.ExecContext(ctx, `UPDATE research_memories SET text=?,title=?,content_hash=?,state=CASE WHEN state='disputed' AND ?='active' THEN 'disputed' ELSE ? END,revision=?,created_at=? WHERE id=? AND owner=?`, text, title, hash(text), state, state, revision, changed, id, owner)
	}
	if err != nil {
		return out, err
	}
	if err = saveRevision(ctx, tx, id); err != nil {
		return out, err
	}
	if state == "active" {
		err = passages(ctx, tx, id, title, text, revision)
	} else {
		_, err = tx.ExecContext(ctx, `DELETE FROM research_memory_passages WHERE memory_id=?`, id)
	}
	if err != nil {
		return out, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE research_memory_jobs SET processed=?,status='captured' WHERE owner=? AND kind='note' AND source_id=? AND generation=?`, gen, owner, paper, gen)
	if err != nil {
		return out, err
	}
	out.Processed = 1
	return out, tx.Commit()
}

// Rebuild requeues derived report projections for bounded Refresh calls. It
// preserves all entry IDs, audit history, annotations, exclusions and note edits.
// It performs no inference and does not clear the independently owned user data.
func (s *Service) Rebuild(ctx context.Context, owner string) error {
	if owner == "" {
		return ErrInvalid
	}
	_, err := s.db.ExecContext(ctx, `UPDATE research_memory_jobs SET processed=0,status='pending' WHERE owner=? AND kind='report'`, owner)
	return err
}

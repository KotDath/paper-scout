package research

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"paper-scout/internal/deepseek"
	"paper-scout/internal/store"
)

// SelectSource is called by the server after import/selection, never directly
// from a model-supplied owner or run ID. It grants this run access to public text.
func (a *Analyzer) SelectSource(ctx context.Context, owner, runID, paperID, reason string) error {
	tx, err := a.db.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status string
	var cancelled bool
	err = tx.QueryRowContext(ctx, "SELECT status,cancel_requested FROM runs WHERE id=? AND user_id=?", runID, owner).Scan(&status, &cancelled)
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrNotFound
	}
	if err != nil {
		return err
	}
	if status != "running" || cancelled {
		return store.ErrRunState
	}
	var count, exists int
	if err = tx.QueryRowContext(ctx, "SELECT count(*),COALESCE(sum(paper_version_id=?),0) FROM run_sources WHERE run_id=?", paperID, runID).Scan(&count, &exists); err != nil {
		return err
	}
	if count >= 3 && exists == 0 {
		return ErrAnalysisBudget
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO run_sources(run_id,paper_version_id,selection_reason) VALUES (?,?,?) ON CONFLICT(run_id,paper_version_id) DO UPDATE SET selection_reason=excluded.selection_reason`, runID, paperID, reason)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (a *Analyzer) available(ctx context.Context, owner, runID, docID string) error {
	var exists int
	err := a.db.DB.QueryRowContext(ctx, `SELECT 1 FROM runs r JOIN run_sources s ON s.run_id=r.id JOIN documents d ON d.paper_version_id=s.paper_version_id WHERE r.id=? AND r.user_id=? AND d.id=?`, runID, owner, docID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrNotFound
	}
	return err
}

func (a *Analyzer) running(ctx context.Context, owner, runID string) error {
	r, err := a.db.Run(ctx, owner, runID)
	if err != nil {
		return err
	}
	if r.CancelRequested {
		return context.Canceled
	}
	if r.Status != "running" {
		return store.ErrRunState
	}
	return ctx.Err()
}

func (a *Analyzer) begin(ctx context.Context, owner string, out Analysis, question string) error {
	tx, err := a.db.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Guard concurrent duplicate analysis via the partial unique index.
	_, err = tx.ExecContext(ctx, `INSERT INTO paper_analyses(id,run_id,document_id,question,created_at)
 SELECT ?,?,?,?,? WHERE EXISTS(SELECT 1 FROM runs WHERE id=? AND user_id=? AND status='running' AND cancel_requested=0)`, out.ID, out.RunID, out.DocumentID, question, time.Now().UTC().Format(time.RFC3339Nano), out.RunID, owner)
	if err != nil {
		return err
	}
	var exists int
	if err = tx.QueryRowContext(ctx, "SELECT 1 FROM paper_analyses WHERE id=?", out.ID).Scan(&exists); err != nil {
		return store.ErrRunState
	}
	_, err = tx.ExecContext(ctx, "UPDATE run_sources SET analysis_status='partial' WHERE run_id=? AND paper_version_id=?", out.RunID, out.PaperVersionID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (a *Analyzer) prepare(ctx context.Context, id string, ordinal int, kind string, packet analysisPacket, request deepseek.Request, ids []string) (string, error) {
	segmentID := "aseg_" + rand.Text()
	input, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	evidence, _ := json.Marshal(ids)
	_, err = a.db.DB.ExecContext(ctx, `INSERT INTO analysis_segments(id,analysis_id,ordinal,kind,char_start,char_end,input_hash,input_json,evidence_ids_json,status,attempted) VALUES (?,?,?,?,?,?,?,?,?,'pending',1)`, segmentID, id, ordinal, kind, packet.Start, packet.End, hashInput(input), string(input), string(evidence))
	return segmentID, err
}

func (a *Analyzer) saveStep(ctx context.Context, id string, response deepseek.Response, obs *Observations, code string) error {
	var usage, body any
	if response.Usage != nil {
		b, _ := json.Marshal(response.Usage)
		usage = string(b)
	}
	if obs != nil {
		b, _ := json.Marshal(obs)
		body = string(b)
	}
	status := "succeeded"
	if code != "" {
		status = "failed"
	}
	_, err := a.db.DB.ExecContext(ctx, `UPDATE analysis_segments SET status=?,attempted=?,analysis_json=?,usage_json=?,error_code=? WHERE id=?`, status, response.Attempted, body, usage, code, id)
	return err
}

func (a *Analyzer) finish(ctx context.Context, owner string, out Analysis) error {
	tx, err := a.db.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := json.Marshal(out)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE paper_analyses SET status=?,result_json=?,error_code=?,finished_at=? WHERE id=? AND run_id IN (SELECT id FROM runs WHERE user_id=?)`, out.Status, string(result), out.ErrorCode, time.Now().UTC().Format(time.RFC3339Nano), out.ID, owner)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE run_sources SET analysis_status=? WHERE run_id=? AND paper_version_id=?`, out.Status, out.RunID, out.PaperVersionID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// Analysis returns durable coverage without performing paid replay. Pending or
// interrupted steps are never counted. It is safe after server restart.
func (a *Analyzer) Analysis(ctx context.Context, owner, id string) (Analysis, error) {
	var runID, docID string
	var result sql.NullString
	err := a.db.DB.QueryRowContext(ctx, `SELECT p.run_id,p.document_id,p.result_json FROM paper_analyses p JOIN runs r ON r.id=p.run_id WHERE p.id=? AND r.user_id=?`, id, owner).Scan(&runID, &docID, &result)
	if errors.Is(err, sql.ErrNoRows) {
		return Analysis{}, store.ErrNotFound
	}
	if err != nil {
		return Analysis{}, err
	}
	if result.Valid {
		var out Analysis
		err = json.Unmarshal([]byte(result.String), &out)
		return out, err
	}
	d, err := a.docs.Get(ctx, docID)
	if err != nil {
		return Analysis{}, err
	}
	out := newAnalysis(id, runID, d)
	rows, err := a.db.DB.QueryContext(ctx, `SELECT char_start,char_end,kind,analysis_json FROM analysis_segments WHERE analysis_id=? AND status='succeeded' ORDER BY ordinal`, id)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var start, end int
		var kind, body string
		if err = rows.Scan(&start, &end, &kind, &body); err != nil {
			break
		}
		var obs Observations
		if err = json.Unmarshal([]byte(body), &obs); err != nil {
			break
		}
		if kind == "reading" {
			out.SuccessfulRanges = append(out.SuccessfulRanges, Range{start, end})
			out.Observations = append(out.Observations, obs)
		}
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if rowErr != nil {
		return out, rowErr
	}
	out.Status = "partial"
	out.ErrorCode = "analysis_in_progress"
	r, err := a.db.Run(ctx, owner, runID)
	if err != nil {
		return out, err
	}
	if r.CancelRequested || r.Status == "cancelled" {
		out.ErrorCode = "cancelled"
	} else if r.Status != "running" {
		out.ErrorCode = "run_interrupted"
	}
	finalizeCoverage(&out, d, false)
	return out, nil
}

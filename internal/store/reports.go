package store

import (
	"context"
	"encoding/json"
)

type CoverageRange struct {
	Start int `json:"char_start"`
	End   int `json:"char_end"`
}

type Coverage struct {
	ID               string          `json:"id"`
	RunID            string          `json:"run_id"`
	DocumentID       string          `json:"document_id"`
	Status           string          `json:"analysis_status"`
	TotalRunes       int             `json:"total_runes"`
	CoveredRunes     int             `json:"covered_runes"`
	SuccessfulRanges []CoverageRange `json:"successful_ranges"`
	ErrorCode        string          `json:"error_code,omitempty"`
}
type ReportSource struct {
	Paper           Paper      `json:"paper"`
	SelectionReason string     `json:"selection_reason"`
	AnalysisStatus  string     `json:"analysis_status"`
	Coverage        []Coverage `json:"coverage"`
}

// Source returns only this conversation's latest durable analysis for a paper.
// Import/index/retrieval never promote its analysis_status to complete.
func (s *Store) Source(ctx context.Context, owner, run, paper string) (ReportSource, error) {
	r, err := s.Run(ctx, owner, run)
	if err != nil {
		return ReportSource{}, err
	}
	p, err := s.Paper(ctx, owner, paper)
	if err != nil {
		return ReportSource{}, err
	}
	out := ReportSource{Paper: p, AnalysisStatus: "none", Coverage: []Coverage{}}
	var reason string
	err = s.DB.QueryRowContext(ctx, `SELECT rs.selection_reason FROM run_sources rs JOIN runs r ON r.id=rs.run_id WHERE rs.paper_version_id=? AND r.user_id=? AND r.conversation_id=? AND (r.id=? OR (r.status='completed' AND r.created_at<=?)) ORDER BY (r.id=?) DESC,r.created_at DESC LIMIT 1`, paper, owner, r.ConversationID, run, r.CreatedAt, run).Scan(&reason)
	if err != nil && notFound(err) != ErrNotFound {
		return out, err
	}
	out.SelectionReason = reason
	var raw *string
	var id, analysisRun, doc, status string
	var total int
	err = s.DB.QueryRowContext(ctx, `SELECT a.id,a.run_id,a.document_id,a.status,a.result_json,length(d.text) FROM paper_analyses a JOIN runs r ON r.id=a.run_id JOIN documents d ON d.id=a.document_id WHERE d.paper_version_id=? AND r.user_id=? AND r.conversation_id=? AND (r.id=? OR (r.status='completed' AND r.created_at<=?)) ORDER BY (r.id=?) DESC,a.created_at DESC,a.id DESC LIMIT 1`, paper, owner, r.ConversationID, run, r.CreatedAt, run).Scan(&id, &analysisRun, &doc, &status, &raw, &total)
	if notFound(err) == ErrNotFound {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	c := Coverage{ID: id, RunID: analysisRun, DocumentID: doc, Status: status, TotalRunes: total, SuccessfulRanges: []CoverageRange{}}
	if raw != nil {
		if err = json.Unmarshal([]byte(*raw), &c); err != nil {
			return out, err
		}
	} else {
		c.Status = "partial"
		c.ErrorCode = "analysis_incomplete"
		if r.CancelRequested || r.Status == "cancelled" {
			c.ErrorCode = "cancelled"
		} else if r.Status != "running" {
			c.ErrorCode = "run_interrupted"
		}
		rows, err := s.DB.QueryContext(ctx, `SELECT char_start,char_end FROM analysis_segments WHERE analysis_id=? AND kind='reading' AND status='succeeded' ORDER BY char_start,char_end`, id)
		if err != nil {
			return out, err
		}
		end := 0
		for rows.Next() {
			var span CoverageRange
			if err = rows.Scan(&span.Start, &span.End); err != nil {
				rows.Close()
				return out, err
			}
			c.SuccessfulRanges = append(c.SuccessfulRanges, span)
			if span.End > end {
				c.CoveredRunes += span.End - max(end, span.Start)
				end = span.End
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return out, err
		}
	}
	out.AnalysisStatus = c.Status
	out.Coverage = append(out.Coverage, c)
	return out, nil
}
func (s *Store) RunSources(ctx context.Context, owner, run string) ([]ReportSource, error) {
	if _, err := s.Run(ctx, owner, run); err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT paper_version_id FROM run_sources WHERE run_id=? ORDER BY paper_version_id`, run)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []ReportSource{}
	for _, id := range ids {
		source, err := s.Source(ctx, owner, run, id)
		if err != nil {
			return nil, err
		}
		out = append(out, source)
	}
	return out, nil
}

type Report struct {
	HistoryReplay    *ReportReplay   `json:"-"`
	Grounded         bool            `json:"-"`
	GroundingRepairs int             `json:"-"`
	ResearchMemory   *ResearchMemory `json:"research_memory"`
	ID               string          `json:"id"`
	RunID            string          `json:"run_id"`
	Markdown         string          `json:"markdown"`
	Citations        json.RawMessage `json:"citations"`
	Sources          []ReportSource  `json:"sources"`
	Coverage         json.RawMessage `json:"coverage"`
	Limitations      []string        `json:"limitations"`
	CreatedAt        string          `json:"created_at"`
}

// ReportReplay separates the accepted answer prefix from the appended state
// notice and records all reports whose current context was delivered. It is
// private provenance, not a new report, snapshot or citation manifest.
type ReportReplay struct {
	ArchiveBytes  int      `json:"archive_bytes"`
	ArchiveSHA256 string   `json:"archive_sha256"`
	Reports       []string `json:"reports"`
	Complete      bool     `json:"complete"`
}

func (s *Store) Report(ctx context.Context, owner, id string) (Report, error) {
	var out Report
	var citations, sources, coverage, limits string
	var memory, replay *string
	err := s.DB.QueryRowContext(ctx, `SELECT id,run_id,markdown,citations_json,sources_json,coverage_json,limitations_json,created_at,research_memory_json,history_replay_json FROM reports WHERE id=? AND user_id=?`, id, owner).Scan(&out.ID, &out.RunID, &out.Markdown, &citations, &sources, &coverage, &limits, &out.CreatedAt, &memory, &replay)
	if err != nil {
		return out, notFound(err)
	}
	if memory != nil {
		if err = json.Unmarshal([]byte(*memory), &out.ResearchMemory); err != nil {
			return out, err
		}
	}
	if replay != nil {
		if err = json.Unmarshal([]byte(*replay), &out.HistoryReplay); err != nil {
			return out, err
		}
	}
	out.Citations = json.RawMessage(citations)
	out.Coverage = json.RawMessage(coverage)
	if err = json.Unmarshal([]byte(sources), &out.Sources); err != nil {
		return out, err
	}
	err = json.Unmarshal([]byte(limits), &out.Limitations)
	return out, err
}

// CompleteReport is one commit: readers cannot observe completed without its
// report, or report_saved without the terminal run. Cancellation wins if already
// committed; a cancelled run gets no report. Only the server builds this input.
func (s *Store) CompleteReport(ctx context.Context, owner, run string, report Report, usage json.RawMessage) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := scanRun(tx.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runs WHERE id=? AND user_id=?`, run, owner))
	if err != nil {
		return err
	}
	if r.Status != "running" {
		return ErrRunState
	}
	status := "completed"
	var reportID *string
	if r.CancelRequested {
		status = "cancelled"
	} else {
		report.ID = newID("report_")
		report.RunID = run
		report.CreatedAt = timestamp()
		reportID = &report.ID
		sources, err := json.Marshal(report.Sources)
		if err != nil {
			return err
		}
		limits, err := json.Marshal(report.Limitations)
		if err != nil {
			return err
		}
		memory, err := json.Marshal(report.ResearchMemory)
		if err != nil {
			return err
		}
		replay, err := json.Marshal(report.HistoryReplay)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO reports(id,user_id,run_id,markdown,citations_json,sources_json,coverage_json,limitations_json,created_at,research_memory_json,history_replay_json) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, report.ID, owner, run, report.Markdown, string(report.Citations), string(sources), string(report.Coverage), string(limits), report.CreatedAt, string(memory), string(replay))
		if err != nil {
			return err
		}
		if report.Grounded {
			if _, err = appendMessage(ctx, tx, r, Message{Role: "assistant", Content: &report.Markdown}); err != nil {
				return err
			}
			payload, _ := json.Marshal(map[string]any{"contract": "grounded_v1", "repairs": report.GroundingRepairs})
			if err = appendEvent(ctx, tx, run, "answer_accepted", payload, nil); err != nil {
				return err
			}
		}
		payload, _ := json.Marshal(map[string]string{"report_id": report.ID})
		if err = appendEvent(ctx, tx, run, "report_saved", payload, nil); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE runs SET status=?,report_id=?,usage_json=?,finished_at=?,error_code='' WHERE id=? AND user_id=?`, status, reportID, string(usage), timestamp(), run, owner)
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]any{"status": status, "report_id": reportID, "cancel_requested": r.CancelRequested})
	if err = appendEvent(ctx, tx, run, "run_finished", payload, nil); err != nil {
		return err
	}
	return tx.Commit()
}

package store

import (
	"context"
	"encoding/json"
)

type MemoryOrigin struct {
	ReportID         string          `json:"report_id"`
	RunID            string          `json:"run_id"`
	ConversationID   string          `json:"conversation_id"`
	AnalysisID       string          `json:"analysis_id,omitempty"`
	DocumentID       string          `json:"document_id,omitempty"`
	PaperVersionID   string          `json:"paper_version_id,omitempty"`
	CreatedAt        string          `json:"created_at"`
	Status           string          `json:"status"`
	AnalysisStatus   string          `json:"analysis_status,omitempty"`
	ExtractionStatus string          `json:"extraction_status,omitempty"`
	Coverage         json.RawMessage `json:"coverage"`
	Limitations      []string        `json:"limitations"`
	EvidenceIDs      []string        `json:"evidence_ids"`
}
type MemoryHit struct {
	MemoryID       string         `json:"memory_id"`
	Kind           string         `json:"kind"`
	Facet          string         `json:"facet,omitempty"`
	Authority      string         `json:"authority"`
	State          string         `json:"state"`
	Revision       int            `json:"revision"`
	ReportID       string         `json:"report_id,omitempty"`
	ConversationID string         `json:"conversation_id,omitempty"`
	PaperVersionID string         `json:"paper_version_id,omitempty"`
	TargetID       string         `json:"target_id,omitempty"`
	Title          string         `json:"title"`
	Text           string         `json:"text"`
	ContentHash    string         `json:"content_hash"`
	EvidenceIDs    []string       `json:"evidence_ids"`
	Origins        []MemoryOrigin `json:"origins"`
	CharStart      int            `json:"char_start"`
	CharEnd        int            `json:"char_end"`
	LexicalRank    int            `json:"lexical_rank,omitempty"`
	DenseRank      int            `json:"dense_rank,omitempty"`
	RRFScore       float64        `json:"rrf_score,omitempty"`
	BM25           float64        `json:"bm25,omitempty"`
	Cosine         float64        `json:"cosine,omitempty"`
}

// MemorySnapshot is immutable data reserved for a model request in this run.
type MemorySnapshot struct {
	Point         string      `json:"point"`
	Query         string      `json:"query"`
	RetrievalMode string      `json:"retrieval_mode"`
	Reasons       []string    `json:"reasons"`
	Hits          []MemoryHit `json:"hits"`
}

// ResearchMemory distinguishes delivery from explicitly validated references.
type ResearchMemory struct {
	RetrievalMode string      `json:"retrieval_mode"`
	Reasons       []string    `json:"reasons"`
	Recalled      []MemoryHit `json:"recalled"`
	Referenced    []MemoryHit `json:"referenced"`
}

func (s *Store) MemorySnapshots(ctx context.Context, owner, run string) ([]MemorySnapshot, error) {
	if _, err := s.Run(ctx, owner, run); err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT snapshot_json FROM run_memory_snapshots WHERE run_id=? ORDER BY ordinal`, run)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MemorySnapshot{}
	for rows.Next() {
		var raw string
		var snap MemorySnapshot
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &snap); err != nil {
			return nil, err
		}
		out = append(out, snap)
	}
	return out, rows.Err()
}

// Snapshot and public event commit together before the next model request.
func (s *Store) SaveMemorySnapshot(ctx context.Context, owner, run string, snap MemorySnapshot) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status string
	var cancelled bool
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT status,cancel_requested FROM runs WHERE id=? AND user_id=?`, run, owner).Scan(&status, &cancelled); err != nil {
		return notFound(err)
	}
	if status != "running" || cancelled {
		return ErrRunState
	}
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM run_memory_snapshots WHERE run_id=?`, run).Scan(&count); err != nil {
		return err
	}
	if count >= 2 || (count == 0 && snap.Point != "question") || (count == 1 && snap.Point != "content") {
		return ErrRunState
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO run_memory_snapshots(run_id,ordinal,snapshot_json) VALUES(?,?,?)`, run, count, string(raw)); err != nil {
		return err
	}
	if err = appendEvent(ctx, tx, run, "memory_recalled", raw, nil); err != nil {
		return err
	}
	return tx.Commit()
}

// Capture diagnostics may follow terminal commit; they never change its result.
func (s *Store) MemoryCaptureEvent(ctx context.Context, owner, run string, payload json.RawMessage) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id string
	if err = tx.QueryRowContext(ctx, `SELECT id FROM runs WHERE id=? AND user_id=? AND report_id IS NOT NULL`, run, owner).Scan(&id); err != nil {
		return notFound(err)
	}
	if err = appendEvent(ctx, tx, run, "memory_captured", payload, nil); err != nil {
		return err
	}
	return tx.Commit()
}

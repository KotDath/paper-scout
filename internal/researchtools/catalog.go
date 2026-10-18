package researchtools

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"paper-scout/internal/agent"
	"paper-scout/internal/arxiv"
	"paper-scout/internal/documents"
	"paper-scout/internal/retrieval"
)

type libraryState struct {
	PreviouslyKnown bool `json:"previously_known"`
	Saved           bool `json:"saved"`
	Read            bool `json:"read"`
}

func (t *tools) known(ctx context.Context, owner, paper string) (libraryState, error) {
	var out libraryState
	err := t.db.DB.QueryRowContext(ctx, `SELECT 1,saved,read FROM user_papers WHERE user_id=? AND paper_version_id=?`, owner, paper).Scan(&out.PreviouslyKnown, &out.Saved, &out.Read)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	return out, err
}
func (t *tools) searchPapers(ctx context.Context, s agent.Scope, raw json.RawMessage) (json.RawMessage, error) {
	var args arxiv.SearchRequest
	_ = agent.DecodeArgs(raw, &args)
	if err := t.event(ctx, s, "search_started", args, nil); err != nil {
		return nil, err
	}
	result, err := t.catalog.Search(ctx, args)
	if err != nil {
		return nil, err
	}
	type candidate struct {
		arxiv.Paper
		libraryState
	}
	papers := make([]candidate, 0, len(result.Papers))
	for _, p := range result.Papers {
		state, err := t.known(ctx, s.Owner, p.ID)
		if err != nil {
			return nil, err
		}
		papers = append(papers, candidate{p, state})
	}
	out := struct {
		Papers   []candidate `json:"papers"`
		CacheHit bool        `json:"cache_hit"`
		Attempts int         `json:"attempts"`
	}{papers, result.CacheHit, result.Attempts}
	if err = t.event(ctx, s, "papers_found", map[string]any{"count": len(papers), "cache_hit": result.CacheHit, "attempts": result.Attempts}, nil); err != nil {
		return nil, err
	}
	return json.Marshal(out)
}

type imported struct {
	Paper arxiv.Paper `json:"paper"`
	libraryState
	DocumentID       string                `json:"document_id"`
	ExtractionStatus string                `json:"extraction_status"`
	Warnings         []documents.Warning   `json:"extraction_warnings"`
	Cached           bool                  `json:"document_cache_hit"`
	MetadataCacheHit bool                  `json:"metadata_cache_hit"`
	MetadataAttempts int                   `json:"metadata_attempts"`
	Index            retrieval.IndexStatus `json:"index"`
	AnalysisStatus   string                `json:"analysis_status"`
}

func (t *tools) importPaper(ctx context.Context, s agent.Scope, raw json.RawMessage) (json.RawMessage, error) {
	var args importArgs
	_ = agent.DecodeArgs(raw, &args)
	result, err := t.catalog.Get(ctx, args.ArxivID)
	if err != nil {
		return nil, err
	}
	p := result.Papers[0]
	state, err := t.known(ctx, s.Owner, p.ID)
	if err != nil {
		return nil, err
	}
	reason := args.Reason
	if reason == "" {
		reason = "Selected for the current research question"
	}
	// Select before membership/download: a fourth source cannot grant library access.
	if err = t.analyzer.SelectSource(ctx, s.Owner, s.RunID, p.ID, reason); err != nil {
		return nil, err
	}
	if err = t.retrieval.AddPaper(ctx, s.Owner, p.ID); err != nil {
		return nil, err
	}
	d, err := t.docs.Import(ctx, documents.Source{PaperVersionID: p.ID, ArxivID: fmt.Sprintf("%sv%d", p.ArxivID, p.Version), Abstract: p.Abstract})
	if err != nil {
		return nil, err
	}
	if err = t.event(ctx, s, "document_loaded", map[string]any{"paper_version_id": p.ID, "document_id": d.ID, "cache_hit": d.Cached}, nil); err != nil {
		return nil, err
	}
	if err = t.event(ctx, s, "document_extracted", map[string]any{"document_id": d.ID, "extraction_status": d.ExtractionStatus, "warnings": d.Warnings, "chunks": len(d.Chunks)}, nil); err != nil {
		return nil, err
	}
	index, indexErr := t.retrieval.Index(ctx, s.Owner, d.ID)
	// Keep a useful imported source on an embedding failure. The persisted status
	// and explicit error distinguish it from a ready hybrid index.
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if indexErr != nil && index.LastError == "" {
		return nil, indexErr
	}
	if err = t.event(ctx, s, "index_progress", index, nil); err != nil {
		return nil, err
	}
	var status string
	if err = t.db.DB.QueryRowContext(ctx, `SELECT analysis_status FROM run_sources WHERE run_id=? AND paper_version_id=?`, s.RunID, p.ID).Scan(&status); err != nil {
		return nil, err
	}
	return json.Marshal(imported{p, state, d.ID, d.ExtractionStatus, d.Warnings, d.Cached, result.CacheHit, result.Attempts, index, status})
}

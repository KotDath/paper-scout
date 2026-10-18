package researchtools

import (
	"context"
	"encoding/json"
	"errors"
	"unicode/utf8"

	"paper-scout/internal/agent"
	"paper-scout/internal/deepseek"
	"paper-scout/internal/documents"
	"paper-scout/internal/research"
	"paper-scout/internal/retrieval"
)

func (t *tools) analyzePaper(ctx context.Context, s agent.Scope, raw json.RawMessage) (json.RawMessage, error) {
	var args analyzeArgs
	_ = agent.DecodeArgs(raw, &args)
	if s.Complete == nil {
		return nil, &agent.ToolError{Code: "missing_run_gateway"}
	}
	var docID string
	err := t.db.DB.QueryRowContext(ctx, `SELECT d.id FROM documents d JOIN run_sources rs ON rs.paper_version_id=d.paper_version_id WHERE rs.run_id=? AND rs.paper_version_id=? AND d.extractor_version=? AND d.chunking_version=? ORDER BY (d.extraction_status='full_text') DESC,d.created_at DESC,d.id LIMIT 1`, s.RunID, args.PaperID, documents.ExtractorVersion, documents.ChunkingVersion).Scan(&docID)
	if err != nil {
		return nil, err
	}
	out, analysisErr := t.analyzer.Analyze(ctx, s.Owner, s.RunID, docID, args.Question, func(callCtx context.Context, req deepseek.Request) (deepseek.Response, error) {
		response, err := s.Complete(callCtx, req)
		var tool *agent.ToolError
		if errors.As(err, &tool) {
			switch tool.Code {
			case "budget_exceeded":
				err = research.ErrAnalysisBudget
			case "context_limit":
				err = research.ErrAnalysisContext
			}
		}
		return response, err
	})
	if out.ID == "" || (analysisErr != nil && out.ErrorCode == "") {
		return nil, analysisErr
	}
	// Durable full analysis stays in Analyzer. The model gets bounded observations
	// and cited evidence IDs; it can open original text with read_passages. Avoid
	// injecting every source chunk into the outer conversation a second time.
	payload := map[string]any{"analysis_id": out.ID, "paper_version_id": out.PaperVersionID, "document_id": out.DocumentID, "analysis_status": out.Status, "extraction_status": out.ExtractionStatus, "extraction_warnings": out.Warnings, "covered_runes": out.CoveredRunes, "total_runes": out.TotalRunes, "successful_ranges": out.SuccessfulRanges, "error_code": out.ErrorCode}
	if out.Synthesis != nil {
		payload["observations"] = out.Synthesis
	} else {
		payload["observations"] = out.Observations
	}
	// Errors with a persisted result are returned as explicit partial data. The
	// shared gateway still latches exhausted budgets; cancellation remains fatal.
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return json.Marshal(payload)
}

func (t *tools) searchLibrary(ctx context.Context, s agent.Scope, raw json.RawMessage) (json.RawMessage, error) {
	var args retrieval.SearchRequest
	_ = agent.DecodeArgs(raw, &args)
	out, err := t.retrieval.Search(ctx, s.Owner, args)
	if err != nil {
		return nil, err
	}
	// Full branch diagnostics are durable trace, not unbounded model context.
	if err = t.event(ctx, s, "retrieval_completed", map[string]any{"retrieval_mode": out.RetrievalMode, "reasons": out.Reasons, "index_coverage": out.IndexCoverage, "lexical_candidates": len(out.LexicalCandidates), "dense_candidates": len(out.DenseCandidates), "hits": len(out.Hits)}, out); err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Query        string          `json:"query"`
		LexicalTerms string          `json:"lexical_terms"`
		Mode         string          `json:"retrieval_mode"`
		Reasons      []string        `json:"reasons"`
		Coverage     float64         `json:"index_coverage"`
		Hits         []retrieval.Hit `json:"hits"`
	}{out.Query, out.LexicalTerms, out.RetrievalMode, out.Reasons, out.IndexCoverage, out.Hits})
}
func (t *tools) readPassages(ctx context.Context, s agent.Scope, raw json.RawMessage) (json.RawMessage, error) {
	var args readArgs
	_ = agent.DecodeArgs(raw, &args)
	hits := make([]retrieval.Hit, 0, len(args.IDs))
	total := 0
	for _, id := range args.IDs {
		h, err := t.retrieval.Evidence(ctx, s.Owner, id)
		if err != nil {
			return nil, err
		}
		total += utf8.RuneCountInString(h.Text)
		if total > retrieval.ContextRunes {
			return nil, &agent.ToolError{Code: "passage_limit"}
		}
		hits = append(hits, h)
	}
	return json.Marshal(map[string]any{"passages": hits})
}

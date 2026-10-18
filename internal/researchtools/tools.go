// Package researchtools composes the existing research modules for AgentRunner.
// It owns no agent loop, provider client, report entity or public HTTP route.
package researchtools

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"paper-scout/internal/agent"
	"paper-scout/internal/arxiv"
	"paper-scout/internal/deepseek"
	"paper-scout/internal/documents"
	"paper-scout/internal/research"
	"paper-scout/internal/retrieval"
	"paper-scout/internal/store"
)

type tools struct {
	db        *store.Store
	catalog   *arxiv.Client
	docs      *documents.Service
	retrieval *retrieval.Service
	analyzer  *research.Analyzer
}

// New registers the five application tools. The caller supplies shared services;
// in particular, analysis has no direct DeepSeek client and requires Scope.Complete.
func New(db *store.Store, catalog *arxiv.Client, docs *documents.Service, search *retrieval.Service, analyzer *research.Analyzer) (*agent.Registry, error) {
	if db == nil || catalog == nil || docs == nil || search == nil || analyzer == nil {
		return nil, errors.New("missing research tool dependency")
	}
	t := &tools{db, catalog, docs, search, analyzer}
	return agent.NewRegistry(
		t.tool("search_papers", "Search arXiv metadata in English. No date restriction unless requested. Does not import or read papers. Returns known/saved/read flags for your account.", `{"type":"object","properties":{"query":{"type":"string","maxLength":2000},"from":{"type":"string"},"to":{"type":"string"},"limit":{"type":"integer","minimum":1,"maximum":10}},"required":["query"],"additionalProperties":false}`, validateSearch, t.searchPapers),
		t.tool("import_paper", "Resolve an arXiv ID/version, select at most three sources per run, import HTML/PDF or explicit abstract fallback, and index. Returns IDs, extraction warnings and indexing status; this is not analysis. Existing membership is marked previously_known.", `{"type":"object","properties":{"arxiv_id":{"type":"string"},"selection_reason":{"type":"string","maxLength":1000}},"required":["arxiv_id"],"additionalProperties":false}`, validateImport, t.importPaper),
		t.tool("analyze_paper", "Read ALL available text of a paper selected by import_paper in this run. Returns complete/partial/abstract_only/none coverage, supported observations and evidence IDs. Nested requests share the run budget. Do not infer full reading from retrieval.", `{"type":"object","properties":{"paper_version_id":{"type":"string"},"question":{"type":"string","maxLength":8000}},"required":["paper_version_id","question"],"additionalProperties":false}`, validateAnalyze, t.analyzePaper),
		t.tool("search_library", "Search only your imported library with BM25, Qwen3 embeddings and RRF. Returns bounded passages and explicit retrieval_mode/degradation. Optional lexical_terms are English keywords; query remains the original question. Retrieval does not change analysis coverage.", `{"type":"object","properties":{"query":{"type":"string","maxLength":4000},"lexical_terms":{"type":"string","maxLength":4000},"paper_version_ids":{"type":"array","items":{"type":"string"},"maxItems":100},"limit":{"type":"integer","minimum":1,"maximum":8}},"required":["query"],"additionalProperties":false}`, validateLibrary, t.searchLibrary),
		t.tool("read_passages", "Read original evidence passages owned by your account, with source coordinates and URL. Up to eight unique evidence IDs and 16000 characters total; narrow the request on passage_limit. Reading passages is not complete article analysis.", `{"type":"object","properties":{"evidence_ids":{"type":"array","items":{"type":"string"},"minItems":1,"maxItems":8}},"required":["evidence_ids"],"additionalProperties":false}`, validateRead, t.readPassages),
	)
}

func (t *tools) tool(name, description, schema string, validate func(json.RawMessage) error, execute func(context.Context, agent.Scope, json.RawMessage) (json.RawMessage, error)) agent.Tool {
	return agent.Tool{Definition: deepseek.Tool{Type: "function", Function: deepseek.Function{Name: name, Description: description, Parameters: json.RawMessage(schema)}}, Validate: validate, Execute: func(ctx context.Context, s agent.Scope, raw json.RawMessage) (json.RawMessage, error) {
		if validate(raw) != nil {
			return nil, &agent.ToolError{Code: "invalid_arguments"}
		}
		r, err := t.db.Run(ctx, s.Owner, s.RunID)
		if err == nil && r.ConversationID != s.ConversationID {
			err = store.ErrNotFound
		}
		if err == nil && r.CancelRequested {
			err = context.Canceled
		}
		if err == nil && r.Status != "running" {
			err = store.ErrRunState
		}
		if err != nil {
			return nil, toolError(err)
		}
		out, err := execute(ctx, s, raw)
		return out, toolError(err)
	}}
}

func toolError(err error) error {
	if err == nil {
		return nil
	}
	var tool *agent.ToolError
	if errors.As(err, &tool) {
		return tool
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	code := "research_error"
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, retrieval.ErrNotFound), errors.Is(err, arxiv.ErrNotFound), errors.Is(err, sql.ErrNoRows):
		code = "not_found"
	case errors.Is(err, store.ErrRunState):
		code = "run_not_running"
	case errors.Is(err, research.ErrAnalysisBudget):
		code = "budget_exceeded"
	case errors.Is(err, research.ErrAnalysisContext):
		code = "context_limit"
	case errors.Is(err, retrieval.ErrInvalid):
		code = "invalid_arguments"
	case errors.Is(err, retrieval.ErrScopeLimit):
		code = "scope_limit"
	default:
		var source *arxiv.Error
		if errors.As(err, &source) {
			code = source.Code
		}
	}
	return &agent.ToolError{Code: code}
}

func validText(s string, max int) bool {
	return utf8.ValidString(s) && strings.TrimSpace(s) != "" && utf8.RuneCountInString(s) <= max
}
func validID(s string) bool {
	return validText(s, 200) && strings.IndexFunc(s, unicode.IsSpace) < 0 && strings.IndexFunc(s, unicode.IsControl) < 0
}
func validateSearch(raw json.RawMessage) error {
	var a arxiv.SearchRequest
	if agent.DecodeArgs(raw, &a) != nil || !validText(a.Query, 2000) || len(a.Query) > 2000 || strings.IndexFunc(a.Query, unicode.IsControl) >= 0 || a.Limit < 0 || a.Limit > 10 {
		return errors.New("invalid search")
	}
	for _, date := range []string{a.From, a.To} {
		if date != "" {
			if _, err := time.Parse("2006-01-02", date); err != nil {
				return err
			}
		}
	}
	if a.From != "" && a.To != "" && a.From > a.To {
		return errors.New("reversed dates")
	}
	return nil
}

type importArgs struct {
	ArxivID string `json:"arxiv_id"`
	Reason  string `json:"selection_reason,omitempty"`
}

func validateImport(raw json.RawMessage) error {
	var a importArgs
	if agent.DecodeArgs(raw, &a) != nil || len(a.ArxivID) > 200 || utf8.RuneCountInString(a.Reason) > 1000 || !utf8.ValidString(a.Reason) {
		return errors.New("invalid import")
	}
	_, err := arxiv.ParseID(a.ArxivID)
	return err
}

type analyzeArgs struct {
	PaperID  string `json:"paper_version_id"`
	Question string `json:"question"`
}

func validateAnalyze(raw json.RawMessage) error {
	var a analyzeArgs
	if agent.DecodeArgs(raw, &a) != nil || !validID(a.PaperID) || !validText(a.Question, 8000) {
		return errors.New("invalid analysis")
	}
	return nil
}
func validateLibrary(raw json.RawMessage) error {
	var a retrieval.SearchRequest
	if agent.DecodeArgs(raw, &a) != nil || !validText(a.Query, 4000) || !utf8.ValidString(a.LexicalTerms) || utf8.RuneCountInString(a.LexicalTerms) > 4000 || a.Limit < 0 || a.Limit > 8 || len(a.PaperVersionIDs) > 100 {
		return errors.New("invalid retrieval")
	}
	for _, id := range a.PaperVersionIDs {
		if !validID(id) {
			return errors.New("invalid paper ID")
		}
	}
	return nil
}

type readArgs struct {
	IDs []string `json:"evidence_ids"`
}

func validateRead(raw json.RawMessage) error {
	var a readArgs
	if agent.DecodeArgs(raw, &a) != nil || len(a.IDs) < 1 || len(a.IDs) > 8 {
		return errors.New("invalid passages")
	}
	seen := map[string]bool{}
	for _, id := range a.IDs {
		if !validID(id) || seen[id] {
			return errors.New("invalid evidence ID")
		}
		seen[id] = true
	}
	return nil
}
func (t *tools) event(ctx context.Context, s agent.Scope, kind string, public, private any) error {
	pub, err := json.Marshal(public)
	if err != nil {
		return err
	}
	var priv []byte
	if private != nil {
		priv, err = json.Marshal(private)
		if err != nil {
			return err
		}
	}
	return t.db.AppendEvent(ctx, s.Owner, s.RunID, kind, pub, priv)
}

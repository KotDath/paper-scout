package research

import (
	"context"
	"errors"

	"paper-scout/internal/deepseek"
	"paper-scout/internal/documents"
	"paper-scout/internal/store"
)

// BudgetedComplete must use the SAME run request/token/deadline accounting as
// AgentRunner, including attempted failures, retries and unknown usage. There
// is deliberately no fallback direct client: T11 supplies its run-bound gateway.
// A rejected reservation returns ErrAnalysisBudget (or ErrAnalysisContext).
// Response must be preserved even when a post-call budget check returns error.
type BudgetedComplete func(context.Context, deepseek.Request) (deepseek.Response, error)

var (
	ErrAnalysisBudget  = errors.New("analysis: budget_exceeded")
	ErrAnalysisContext = errors.New("analysis: context_limit")
)

type AnalyzerConfig struct {
	// MaxSegmentRunes bounds article text per request; the gateway must also
	// estimate the complete prompt, including evidence metadata and output.
	MaxSegmentRunes   int
	MaxSynthesisRunes int
	MaxRequests       int // secondary local cap; never replaces the shared run budget
	OutputTokens      int
}

type Analyzer struct {
	db   *store.Store
	docs *documents.Service
	cfg  AnalyzerConfig
}

func NewAnalyzer(db *store.Store, docs *documents.Service, cfg AnalyzerConfig) (*Analyzer, error) {
	if db == nil || docs == nil {
		return nil, errors.New("analysis: missing store or documents")
	}
	if cfg.MaxSegmentRunes == 0 {
		cfg.MaxSegmentRunes = 36000
	}
	if cfg.MaxSynthesisRunes == 0 {
		cfg.MaxSynthesisRunes = 48000
	}
	if cfg.MaxRequests == 0 {
		cfg.MaxRequests = 12
	}
	if cfg.OutputTokens == 0 {
		cfg.OutputTokens = 2048
	}
	if cfg.MaxSegmentRunes < 256 || cfg.MaxSegmentRunes > 100000 || cfg.MaxSynthesisRunes < 256 || cfg.MaxSynthesisRunes > 100000 || cfg.MaxRequests < 1 || cfg.MaxRequests > 32 || cfg.OutputTokens < 1 || cfg.OutputTokens > 4096 {
		return nil, errors.New("analysis: invalid limits")
	}
	return &Analyzer{db: db, docs: docs, cfg: cfg}, nil
}

type Claim struct {
	Text        string   `json:"text"`
	EvidenceIDs []string `json:"evidence_ids"`
}

type Observations struct {
	Methods     []Claim `json:"methods"`
	Results     []Claim `json:"results"`
	Limitations []Claim `json:"limitations"`
}

type Range struct {
	Start int `json:"char_start"`
	End   int `json:"char_end"`
}

type Evidence struct {
	ID         string `json:"evidence_id"`
	DocumentID string `json:"document_id"`
	Text       string `json:"text"`
	Start      int    `json:"char_start"`
	End        int    `json:"char_end"`
	PageStart  int    `json:"page_start,omitempty"`
	PageEnd    int    `json:"page_end,omitempty"`
	Section    string `json:"section,omitempty"`
	SourceURL  string `json:"source_url"`
}

type Analysis struct {
	ID               string              `json:"id"`
	RunID            string              `json:"run_id"`
	DocumentID       string              `json:"document_id"`
	PaperVersionID   string              `json:"paper_version_id"`
	Status           string              `json:"analysis_status"`
	ExtractionStatus string              `json:"extraction_status"`
	Warnings         []documents.Warning `json:"extraction_warnings"`
	TotalRunes       int                 `json:"total_runes"`
	CoveredRunes     int                 `json:"covered_runes"`
	SuccessfulRanges []Range             `json:"successful_ranges"`
	Observations     []Observations      `json:"observations"`
	Synthesis        *Observations       `json:"synthesis,omitempty"`
	Evidence         []Evidence          `json:"evidence"`
	ErrorCode        string              `json:"error_code,omitempty"`
}

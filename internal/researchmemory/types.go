// Package researchmemory projects durable research and explicit user annotations.
// It performs no generation, paid replay, or scientific truth adjudication.
package researchmemory

import (
	"database/sql"
	"encoding/json"
	"errors"
	"paper-scout/internal/embeddings"
	"time"
)

const ContextRunes = 12000
const PassageRunes = 1200
const MaxScopePassages = 20000

var ErrInvalid = errors.New("invalid memory input")
var ErrConflict = errors.New("memory revision conflict")
var ErrScope = errors.New("memory scope limit")

type Service struct {
	db    *sql.DB
	embed *embeddings.EmbeddingClient
}

func New(db *sql.DB, embed *embeddings.EmbeddingClient) *Service {
	return &Service{db: db, embed: embed}
}

type SearchRequest struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
	// Server-only bounds; HTTP cannot choose an owner, run or historical cutoff.
	ExcludeRunID string    `json:"-"`
	Before       time.Time `json:"-"`
}
type Origin struct {
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
type Hit struct {
	MemoryID       string   `json:"memory_id"`
	Kind           string   `json:"kind"`
	Facet          string   `json:"facet,omitempty"`
	Authority      string   `json:"authority"`
	State          string   `json:"state"`
	Revision       int      `json:"revision"`
	ReportID       string   `json:"report_id,omitempty"`
	ConversationID string   `json:"conversation_id,omitempty"`
	PaperVersionID string   `json:"paper_version_id,omitempty"`
	TargetID       string   `json:"target_id,omitempty"`
	Title          string   `json:"title"`
	Text           string   `json:"text"`
	ContentHash    string   `json:"content_hash"`
	EvidenceIDs    []string `json:"evidence_ids"`
	Origins        []Origin `json:"origins"`
	CharStart      int      `json:"char_start"`
	CharEnd        int      `json:"char_end"`
	LexicalRank    int      `json:"lexical_rank,omitempty"`
	DenseRank      int      `json:"dense_rank,omitempty"`
	RRFScore       float64  `json:"rrf_score,omitempty"`
	BM25           float64  `json:"bm25,omitempty"`
	Cosine         float64  `json:"cosine,omitempty"`
}
type SearchResult struct {
	Query             string        `json:"query"`
	RetrievalMode     string        `json:"retrieval_mode"`
	Reasons           []string      `json:"reasons"`
	Hits              []Hit         `json:"hits"`
	LexicalCandidates []Hit         `json:"lexical_candidates"`
	DenseCandidates   []Hit         `json:"dense_candidates"`
	IndexCoverage     float64       `json:"index_coverage"`
	Refresh           RefreshResult `json:"refresh"`
}
type Patch struct {
	ExpectedRevision int     `json:"expected_revision"`
	Excluded         *bool   `json:"excluded,omitempty"`
	Correction       *string `json:"correction,omitempty"`
}
type Entry struct {
	Hit
	Excluded    bool  `json:"excluded"`
	Passages    []Hit `json:"passages"`
	Annotations []Hit `json:"annotations"`
}
type RefreshResult struct {
	Processed int      `json:"processed"`
	Added     int      `json:"added"`
	Noop      int      `json:"noop"`
	Pending   int      `json:"pending"`
	Reasons   []string `json:"reasons"`
}

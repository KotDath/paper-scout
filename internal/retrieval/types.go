// Package retrieval indexes public chunks and searches an owner's library.
// Retrieval and index readiness never imply model analysis or full reading.
package retrieval

import (
	"database/sql"
	"errors"

	"paper-scout/internal/embeddings"
)

const MaxScopeChunks = 20000
const ContextRunes = 16000

var ErrInvalid = errors.New("invalid retrieval input")
var ErrNotFound = errors.New("retrieval resource not found")
var ErrScopeLimit = errors.New("retrieval scope exceeds 20000 chunks; narrow paper_version_ids")

type Service struct {
	db        *sql.DB
	embed     *embeddings.EmbeddingClient
	indexLock chan struct{}
}

// A nil client provides explicit lexical_only mode while Ollama is unavailable.
func New(db *sql.DB, embed *embeddings.EmbeddingClient) *Service {
	return &Service{db: db, embed: embed, indexLock: make(chan struct{}, 1)}
}

type SearchRequest struct {
	Query           string   `json:"query"`
	LexicalTerms    string   `json:"lexical_terms,omitempty"` // optional agent-generated English terms
	PaperVersionIDs []string `json:"paper_version_ids,omitempty"`
	Limit           int      `json:"limit,omitempty"`
}

type Hit struct {
	EvidenceID       string  `json:"evidence_id"`
	DocumentID       string  `json:"document_id"`
	PaperVersionID   string  `json:"paper_version_id"`
	Title            string  `json:"title"`
	Text             string  `json:"text"`
	CharStart        int     `json:"char_start"`
	CharEnd          int     `json:"char_end"`
	PageStart        int     `json:"page_start,omitempty"`
	PageEnd          int     `json:"page_end,omitempty"`
	Section          string  `json:"section,omitempty"`
	SourceURL        string  `json:"source_url"`
	ExtractionStatus string  `json:"extraction_status"`
	LexicalRank      int     `json:"lexical_rank,omitempty"`
	DenseRank        int     `json:"dense_rank,omitempty"`
	BM25             float64 `json:"bm25,omitempty"`
	Cosine           float64 `json:"cosine,omitempty"`
	RRFScore         float64 `json:"rrf_score,omitempty"`
}

type SearchResult struct {
	Query             string   `json:"query"`
	LexicalTerms      string   `json:"lexical_terms"`
	RetrievalMode     string   `json:"retrieval_mode"`
	Reasons           []string `json:"reasons"`
	IndexCoverage     float64  `json:"index_coverage"`
	TotalChunks       int      `json:"total_chunks"`
	IndexedChunks     int      `json:"indexed_chunks"`
	LexicalCandidates []Hit    `json:"lexical_candidates"`
	DenseCandidates   []Hit    `json:"dense_candidates"`
	Hits              []Hit    `json:"hits"`
}

type IndexStatus struct {
	DocumentID    string `json:"document_id"`
	Status        string `json:"status"`
	IndexedChunks int    `json:"indexed_chunks"`
	TotalChunks   int    `json:"total_chunks"`
	LastError     string `json:"last_error,omitempty"`
	CacheHits     int    `json:"cache_hits"`
}

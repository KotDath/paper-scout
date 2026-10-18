// Package researchmemory projects durable research and explicit user annotations.
// It performs no generation, paid replay, or scientific truth adjudication.
package researchmemory

import (
	"database/sql"
	"errors"
	"paper-scout/internal/embeddings"
	"paper-scout/internal/store"
	"time"
)

const ContextRunes = 12000
const PassageRunes = 1200
const MaxScopePassages = 20000

var ErrInvalid = errors.New("invalid memory input")
var ErrConflict = errors.New("memory revision conflict")

// ErrPending means a mandatory current note or correction is not ready.
var ErrPending = errors.New("memory annotation projection pending")
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

// Aliases preserve the T19 caller contract while Report uses the same wire types.
type Origin = store.MemoryOrigin
type Hit = store.MemoryHit

type SearchResult struct {
	// Groups retain each base's full annotation closure for further server-side
	// prompt budgeting. Hits stay deduplicated on the existing HTTP contract.
	Groups            [][]Hit       `json:"-"`
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

// Package documents obtains and extracts public arXiv article text without OCR.
// Extraction says nothing about model analysis coverage or index readiness.
package documents

import (
	"context"
	"database/sql"
	"net/http"
	"time"
)

const ExtractorVersion = "t05-poppler-html-v1"
const ChunkingVersion = "runes-2000-overlap-250-v1"

// Source must identify an exact public paper version, registered by the catalog.
// No caller-supplied URLs or filesystem paths are accepted.
type Source struct {
	PaperVersionID string
	ArxivID        string
	Abstract       string
}

type Config struct {
	CacheDir         string
	HTTPClient       *http.Client
	DownloadTimeout  time.Duration
	ExtractTimeout   time.Duration
	MaxDownloadBytes int64
}

type Service struct {
	db     *sql.DB
	cfg    Config
	client *http.Client
}

type Warning struct {
	Code string `json:"code"`
	Page int    `json:"page,omitempty"`
}

// All offsets are zero-based Unicode code points in Text, end exclusive.
// Segments partition Text; PDF pages with no text still have an empty segment.
type Segment struct {
	CharStart int    `json:"char_start"`
	CharEnd   int    `json:"char_end"`
	Page      int    `json:"page,omitempty"`
	Section   string `json:"section,omitempty"`
}

type Chunk struct {
	ID         string `json:"id"`
	DocumentID string `json:"document_id"`
	Ordinal    int    `json:"ordinal"`
	Text       string `json:"text"`
	CharStart  int    `json:"char_start"`
	CharEnd    int    `json:"char_end"`
	PageStart  int    `json:"page_start,omitempty"`
	PageEnd    int    `json:"page_end,omitempty"`
	Section    string `json:"section,omitempty"`
}

type Document struct {
	ID               string    `json:"id"`
	PaperVersionID   string    `json:"paper_version_id"`
	Format           string    `json:"format"`
	ContentHash      string    `json:"content_hash"`
	ExtractorVersion string    `json:"extractor_version"`
	ChunkingVersion  string    `json:"chunking_version"`
	ExtractionStatus string    `json:"extraction_status"`
	SourceURL        string    `json:"source_url"`
	LocalPath        string    `json:"-"`
	Text             string    `json:"text"`
	Segments         []Segment `json:"segments"`
	Warnings         []Warning `json:"warnings"`
	Chunks           []Chunk   `json:"chunks"`
	Cached           bool      `json:"cached"`
}

// Get returns a technical public cache entry. Callers MUST check membership/run
// ownership before exposing this document or any chunk through API/tools.
func (s *Service) Get(ctx context.Context, id string) (Document, error) {
	return s.load(ctx, "id", id)
}

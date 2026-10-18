// Package embeddings encodes retrieval queries and document inputs using the
// existing local Ollama service. It owns no index, history or retry loop.
package embeddings

import (
	"fmt"
	"time"
)

const (
	Model            = "qwen3-embedding:0.6b"
	Dimensions       = 1024
	EncodingVersion  = "qwen3-scientific-v1"
	QueryInstruction = "Instruct: Retrieve scientific passages relevant to the research question.\nQuery: "
	MaxBatchSize     = 32
	MaxInputBytes    = 256 << 10
	MaxBatchBytes    = 1 << 20
)

type Mode string

const (
	Query    Mode = "query"
	Document Mode = "document"
)

// Identity must match before stored and query vectors can be compared.
// Chunking version belongs to the index, independently of this encoding version.
type Identity struct {
	Model           string
	Digest          string // lower-case SHA-256 hex, without the sha256: prefix
	Dimensions      int
	EncodingVersion string
}

type Result struct {
	Identity     Identity
	Mode         Mode
	InputHashes  []string    // SHA-256 of exact encoded UTF-8 input, in input order
	Vectors      [][]float32 // finite, nonzero, L2-normalized; all or nothing
	PromptTokens int
}

// Error never includes input text, URLs, response bodies or transport details.
// A context cancellation/deadline is available through errors.Is.
type Error struct {
	Code       string
	HTTPStatus int
	Retryable  bool
	RetryAfter time.Duration
	cause      error
}

func (e *Error) Error() string { return fmt.Sprintf("embeddings: %s (HTTP %d)", e.Code, e.HTTPStatus) }
func (e *Error) Unwrap() error { return e.cause }

// DocumentText includes public source metadata, never a user's private notes.
// Document mode sends this exact string, without a query instruction.
func DocumentText(title, section, text string) string {
	return "Title: " + title + "\nSection: " + section + "\n\n" + text
}

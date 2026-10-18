package researchmemory

import (
	"context"
	"log/slog"
	"time"

	"paper-scout/internal/embeddings"
)

// Separate memory embedding accounting from paid generation and read/write
// projection. Input text, vectors and credentials never enter application logs.
func (s *Service) encode(ctx context.Context, owner string, mode embeddings.Mode, inputs []string) (embeddings.Result, error) {
	start := time.Now()
	result, err := s.embed.Embed(ctx, mode, inputs)
	code := ""
	if err != nil {
		code = embedCode(err)
	}
	slog.Info("research memory embedding", "owner_id", owner, "mode", mode, "inputs", len(inputs), "prompt_tokens", result.PromptTokens, "error_code", code, "duration_ms", time.Since(start).Milliseconds())
	return result, err
}

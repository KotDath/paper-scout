package embeddings

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const maxResponseBytes = 8 << 20

type EmbeddingClient struct {
	config        Config
	identity      Identity
	contextLength int
	http          *http.Client
}

// New verifies installed model metadata and pins its digest. No pull, model
// creation, generation request or retry occurs. Recreate after intentional
// model changes and reindex; a running client refuses a changed tag.
func New(ctx context.Context, cfg Config) (*EmbeddingClient, error) {
	cfg, err := cfg.validated()
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil // local text must never pass through an environment proxy
	c := &EmbeddingClient{config: cfg, http: &http.Client{Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	digest, err := c.installedDigest(ctx)
	if err != nil {
		return nil, err
	}
	if cfg.ExpectedDigest != "" && cfg.ExpectedDigest != digest {
		return nil, &Error{Code: "embedding_incompatible"}
	}
	var show struct {
		Capabilities []string `json:"capabilities"`
		RemoteHost   string   `json:"remote_host"`
		RemoteModel  string   `json:"remote_model"`
		ModelInfo    struct {
			Architecture  string `json:"general.architecture"`
			Dimensions    int    `json:"qwen3.embedding_length"`
			ContextLength int    `json:"qwen3.context_length"`
		} `json:"model_info"`
	}
	if err := c.request(ctx, http.MethodPost, "/api/show", map[string]string{"model": cfg.Model}, &show); err != nil {
		return nil, err
	}
	if !slices.Contains(show.Capabilities, "embedding") || show.RemoteHost != "" || show.RemoteModel != "" || show.ModelInfo.Architecture != "qwen3" || show.ModelInfo.Dimensions != Dimensions || show.ModelInfo.ContextLength < 1 {
		return nil, &Error{Code: "embedding_incompatible"}
	}
	c.identity = Identity{Model: cfg.Model, Digest: digest, Dimensions: Dimensions, EncodingVersion: EncodingVersion}
	c.contextLength = show.ModelInfo.ContextLength
	if err := c.checkDigest(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *EmbeddingClient) Identity() Identity { return c.identity }

// Embed sends exactly one batch, with no hidden retries or splitting. Callers
// own bounded retries and splitting on context_limit. Errors return no vectors.
// Query texts are raw questions: do not pre-apply QueryInstruction.
func (c *EmbeddingClient) Embed(ctx context.Context, mode Mode, texts []string) (Result, error) {
	var empty Result
	if (mode != Query && mode != Document) || len(texts) < 1 || len(texts) > MaxBatchSize {
		return empty, &Error{Code: "invalid_request"}
	}
	inputs := make([]string, len(texts))
	hashes := make([]string, len(texts))
	total := 0
	for i, text := range texts {
		if !utf8.ValidString(text) || strings.TrimSpace(text) == "" {
			return empty, &Error{Code: "invalid_request"}
		}
		if mode == Query {
			text = QueryInstruction + text
		}
		total += len(text)
		if len(text) > MaxInputBytes || total > MaxBatchBytes {
			return empty, &Error{Code: "input_limit"}
		}
		inputs[i] = text
		sum := sha256.Sum256([]byte(text))
		hashes[i] = hex.EncodeToString(sum[:])
	}
	ctx, cancel := context.WithTimeout(ctx, c.config.Timeout)
	defer cancel()
	if err := c.checkDigest(ctx); err != nil {
		return empty, err
	}
	wire := struct {
		Model    string         `json:"model"`
		Input    []string       `json:"input"`
		Truncate bool           `json:"truncate"`
		Options  map[string]int `json:"options"`
	}{Model: c.config.Model, Input: inputs, Truncate: false, Options: map[string]int{"num_ctx": c.contextLength}}
	var response struct {
		Model        string       `json:"model"`
		Embeddings   [][]*float64 `json:"embeddings"`
		PromptTokens *int         `json:"prompt_eval_count"`
	}
	if err := c.request(ctx, http.MethodPost, "/api/embed", wire, &response); err != nil {
		return empty, err
	}
	if response.Model != c.identity.Model {
		return empty, &Error{Code: "embedding_incompatible"}
	}
	if len(response.Embeddings) != len(inputs) || response.PromptTokens == nil || *response.PromptTokens <= 0 {
		return empty, &Error{Code: "invalid_response"}
	}
	vectors := make([][]float32, len(inputs))
	for i, values := range response.Embeddings {
		if len(values) != Dimensions {
			return empty, &Error{Code: "embedding_incompatible"}
		}
		norm := 0.0
		for _, value := range values {
			if value == nil {
				return empty, &Error{Code: "invalid_vector"}
			}
			v := *value
			if math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) > math.MaxFloat32 {
				return empty, &Error{Code: "invalid_vector"}
			}
			norm += v * v
		}
		if norm == 0 || math.IsInf(norm, 0) {
			return empty, &Error{Code: "invalid_vector"}
		}
		norm = math.Sqrt(norm)
		vectors[i] = make([]float32, Dimensions)
		for j, v := range values {
			vectors[i][j] = float32(*v / norm)
		}
	}
	if err := c.checkDigest(ctx); err != nil {
		return empty, err
	}
	return Result{Identity: c.identity, Mode: mode, InputHashes: hashes, Vectors: vectors, PromptTokens: *response.PromptTokens}, nil
}

func (c *EmbeddingClient) installedDigest(ctx context.Context) (string, error) {
	var tags struct {
		Models []struct {
			Name   string `json:"name"`
			Digest string `json:"digest"`
		} `json:"models"`
	}
	if err := c.request(ctx, http.MethodGet, "/api/tags", nil, &tags); err != nil {
		return "", err
	}
	for _, m := range tags.Models {
		if m.Name == c.config.Model {
			if digest, ok := validDigest(m.Digest); ok {
				return digest, nil
			}
			return "", &Error{Code: "embedding_incompatible"}
		}
	}
	return "", &Error{Code: "model_unavailable"}
}

func (c *EmbeddingClient) checkDigest(ctx context.Context) error {
	digest, err := c.installedDigest(ctx)
	if err != nil {
		return err
	}
	if digest != c.identity.Digest {
		return &Error{Code: "embedding_incompatible"}
	}
	return nil
}

func (c *EmbeddingClient) request(ctx context.Context, method, path string, body, output any) error {
	if err := ctx.Err(); err != nil {
		return contextError(err)
	}
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			return &Error{Code: "invalid_request"}
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.config.BaseURL+path, bytes.NewReader(data))
	if err != nil {
		return &Error{Code: "invalid_request"}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return contextError(ctx.Err())
		}
		return &Error{Code: "embedding_unavailable", Retryable: true}
	}
	defer resp.Body.Close()
	data, err = io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return contextError(ctx.Err())
		}
		return &Error{Code: "embedding_unavailable", Retryable: true}
	}
	if len(data) > maxResponseBytes {
		return &Error{Code: "response_too_large", HTTPStatus: resp.StatusCode}
	}
	if resp.StatusCode != http.StatusOK {
		e := &Error{Code: "embedding_error", HTTPStatus: resp.StatusCode}
		switch resp.StatusCode {
		case 400, 413, 422:
			e.Code = "invalid_request"
			// Inspect only to classify; never retain or expose provider text.
			if path == "/api/embed" && bytes.Contains(bytes.ToLower(data), []byte("context length")) {
				e.Code = "context_limit"
			}
		case 404:
			e.Code = "model_unavailable"
		case 429:
			e.Code, e.Retryable = "embedding_rate_limited", true
		case 500, 502, 503, 504:
			e.Code, e.Retryable = "embedding_unavailable", true
		}
		if seconds, err := strconv.ParseInt(resp.Header.Get("Retry-After"), 10, 32); err == nil && seconds >= 0 {
			e.RetryAfter = time.Duration(seconds) * time.Second
		} else if deadline, err := http.ParseTime(resp.Header.Get("Retry-After")); err == nil && deadline.After(time.Now()) {
			e.RetryAfter = time.Until(deadline)
		}
		return e
	}
	if json.Unmarshal(data, output) != nil {
		return &Error{Code: "invalid_response"}
	}
	return nil
}

func contextError(err error) *Error {
	code := "cancelled"
	if errors.Is(err, context.DeadlineExceeded) {
		code = "embedding_timeout"
	}
	return &Error{Code: code, cause: err}
}

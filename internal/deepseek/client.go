package deepseek

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const maxResponseBytes = 8 << 20

type DeepSeekClient struct {
	endpoint string
	model    string
	apiKey   string
	thinking string
	timeout  time.Duration
	http     *http.Client
}

func New(cfg Config) (*DeepSeekClient, error) {
	cfg, endpoint, err := cfg.validated()
	if err != nil {
		return nil, err
	}
	return &DeepSeekClient{
		endpoint: endpoint, model: cfg.Model, apiKey: cfg.APIKey,
		thinking: cfg.Thinking, timeout: cfg.Timeout,
		http: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
	}, nil
}

// Complete makes exactly one HTTP attempt. The harness owns bounded retries,
// RetryAfter delays and the global budget, including uncertain failed calls.
// It must not execute tools when err != nil, including finish_reason=length.
func (c *DeepSeekClient) Complete(ctx context.Context, input Request) (Response, error) {
	var result Response
	if err := validateRequest(input); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, contextError(err, false)
	}
	wire := struct {
		Model          string          `json:"model"`
		Messages       []Message       `json:"messages"`
		Tools          []Tool          `json:"tools,omitempty"`
		ToolChoice     string          `json:"tool_choice,omitempty"`
		MaxTokens      int             `json:"max_tokens"`
		Stream         bool            `json:"stream"`
		ResponseFormat json.RawMessage `json:"response_format,omitempty"`
		Temperature    *float64        `json:"temperature,omitempty"`
		Thinking       struct {
			Type string `json:"type"`
		} `json:"thinking"`
	}{Model: c.model, Messages: input.Messages, Tools: input.Tools, MaxTokens: input.MaxTokens}
	wire.Thinking.Type = c.thinking
	if input.JSONOutput {
		wire.ResponseFormat = json.RawMessage(`{"type":"json_object"}`)
		zero := float64(0)
		wire.Temperature = &zero
	}
	if len(input.Tools) > 0 {
		wire.ToolChoice = "auto"
	}
	body, err := json.Marshal(wire)
	if err != nil {
		return result, &Error{Code: "invalid_request"}
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return result, &Error{Code: "invalid_request"}
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	result.Attempted = true
	response, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return result, contextError(ctx.Err(), true)
		}
		return result, &Error{Code: "provider_unavailable", Retryable: true, UsageUnknown: true}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return result, statusError(response.StatusCode, response.Header.Get("Retry-After"))
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return result, contextError(ctx.Err(), true)
		}
		return result, &Error{Code: "provider_unavailable", Retryable: true, UsageUnknown: true}
	}
	if len(data) > maxResponseBytes {
		return result, &Error{Code: "response_too_large", UsageUnknown: true}
	}
	var decoded struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Message      Message `json:"message"`
			FinishReason string  `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens          *int `json:"prompt_tokens"`
			CompletionTokens      *int `json:"completion_tokens"`
			TotalTokens           *int `json:"total_tokens"`
			PromptCacheHitTokens  int  `json:"prompt_cache_hit_tokens"`
			PromptCacheMissTokens int  `json:"prompt_cache_miss_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(data, &decoded) != nil {
		return result, &Error{Code: "invalid_response", UsageUnknown: true}
	}
	result.ID, result.Model = decoded.ID, decoded.Model
	u := decoded.Usage
	if u != nil && u.PromptTokens != nil && u.CompletionTokens != nil && u.TotalTokens != nil &&
		*u.PromptTokens >= 0 && *u.CompletionTokens >= 0 && *u.TotalTokens >= 0 &&
		*u.TotalTokens == *u.PromptTokens+*u.CompletionTokens && u.PromptCacheHitTokens >= 0 && u.PromptCacheMissTokens >= 0 {
		result.Usage = &Usage{PromptTokens: *u.PromptTokens, CompletionTokens: *u.CompletionTokens,
			TotalTokens: *u.TotalTokens, PromptCacheHitTokens: u.PromptCacheHitTokens, PromptCacheMissTokens: u.PromptCacheMissTokens}
	}
	if len(decoded.Choices) != 1 {
		return result, &Error{Code: "invalid_response", UsageUnknown: result.Usage == nil}
	}
	result.Message, result.FinishReason = decoded.Choices[0].Message, decoded.Choices[0].FinishReason
	code := ""
	switch result.FinishReason {
	case "length":
		code = "output_limit"
	case "content_filter":
		code = "content_filtered"
	case "stop":
		if len(result.Message.ToolCalls) > 0 || result.Message.Content == nil || strings.TrimSpace(*result.Message.Content) == "" {
			code = "invalid_response"
		}
	case "tool_calls":
		if len(result.Message.ToolCalls) == 0 {
			code = "invalid_response"
		}
		seen := make(map[string]bool)
		for _, call := range result.Message.ToolCalls {
			if call.ID == "" || seen[call.ID] || call.Type != "function" || call.Function.Name == "" {
				code = "invalid_response"
			}
			seen[call.ID] = true
		}
	default:
		code = "invalid_response"
	}
	if result.Message.Role != "assistant" || result.ID == "" || result.Model == "" || result.Usage == nil {
		code = "invalid_response"
	}
	if code != "" {
		return result, &Error{Code: code, UsageUnknown: result.Usage == nil}
	}
	return result, nil
}

func validateRequest(r Request) error {
	if len(r.Messages) == 0 || r.MaxTokens < 1 || r.MaxTokens > 4096 {
		return &Error{Code: "invalid_request"}
	}
	for _, m := range r.Messages {
		switch m.Role {
		case "system", "user", "tool":
			if m.Content == nil || (m.Role == "tool" && m.ToolCallID == "") {
				return &Error{Code: "invalid_request"}
			}
		case "assistant":
			if m.Content == nil && len(m.ToolCalls) == 0 {
				return &Error{Code: "invalid_request"}
			}
		default:
			return &Error{Code: "invalid_request"}
		}
	}
	for _, tool := range r.Tools {
		if tool.Type != "function" || tool.Function.Name == "" || !json.Valid(tool.Function.Parameters) {
			return &Error{Code: "invalid_request"}
		}
	}
	return nil
}

func contextError(err error, attempted bool) *Error {
	code := "cancelled"
	if errors.Is(err, context.DeadlineExceeded) {
		code = "provider_timeout"
	}
	return &Error{Code: code, UsageUnknown: attempted, cause: err}
}

func statusError(status int, retry string) *Error {
	e := &Error{Code: "provider_error", HTTPStatus: status, UsageUnknown: true}
	switch status {
	case 400, 422:
		e.Code = "provider_invalid_request"
	case 401, 403:
		e.Code = "provider_authentication"
	case 402:
		e.Code = "provider_balance"
	case 429:
		e.Code, e.Retryable = "provider_rate_limited", true
	case 500, 502, 503, 504:
		e.Code, e.Retryable = "provider_unavailable", true
	}
	if seconds, err := strconv.ParseInt(retry, 10, 32); err == nil && seconds >= 0 {
		e.RetryAfter = time.Duration(seconds) * time.Second
	} else if deadline, err := http.ParseTime(retry); err == nil && deadline.After(time.Now()) {
		e.RetryAfter = time.Until(deadline)
	}
	return e
}

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"paper-scout/internal/deepseek"
	"paper-scout/internal/store"
)

type Limits struct {
	ToolCalls       int           `json:"tool_calls"`
	Requests        int           `json:"deepseek_requests"`
	Tokens          int           `json:"tokens"`
	OutputTokens    int           `json:"output_tokens"`
	DurationSeconds int           `json:"duration_seconds"`
	ContextBytes    int           `json:"-"`
	ToolTimeout     time.Duration `json:"-"`
	Retries         int           `json:"-"`
}

func DefaultLimits() Limits {
	return Limits{ToolCalls: 24, Requests: 32, Tokens: 250000, OutputTokens: 4096, DurationSeconds: 900, ContextBytes: 384000, ToolTimeout: 120 * time.Second, Retries: 1}
}

// Configuration may lower the durable run defaults, never silently raise them.
func (l Limits) validate() error {
	d := DefaultLimits()
	if l.ToolCalls < 1 || l.ToolCalls > d.ToolCalls || l.Requests < 1 || l.Requests > d.Requests || l.Tokens < 1 || l.Tokens > d.Tokens || l.OutputTokens < 1 || l.OutputTokens > d.OutputTokens || l.DurationSeconds < 1 || l.DurationSeconds > d.DurationSeconds || l.ContextBytes < 1 || l.ContextBytes > d.ContextBytes || l.ToolTimeout <= 0 || l.ToolTimeout > d.ToolTimeout || l.Retries < 0 || l.Retries > 2 {
		return errors.New("invalid agent limits")
	}
	return nil
}

type Usage struct {
	Requests            int `json:"deepseek_requests"`
	ToolCalls           int `json:"tool_calls"`
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	TotalTokens         int `json:"total_tokens"`
	UnknownRequests     int `json:"unknown_usage_requests"`
	UnknownTokenReserve int `json:"unknown_token_reserve"`
}

type runBudget struct {
	mu             sync.Mutex
	db             *store.Store
	client         *deepseek.DeepSeekClient
	owner, run     string
	ctx            context.Context
	limits         Limits
	usage          Usage
	limitExceeded  bool
	deferAssistant bool // production gate admits canonical messages after validation
}

// Once admission or actual usage reaches a global limit, a tool cannot clear
// that condition by handling its nested Complete error and returning success.
func (b *runBudget) exhausted() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.limitExceeded || b.usage.TotalTokens+b.usage.UnknownTokenReserve >= b.limits.Tokens
}

func (b *runBudget) save(kind string, public, private any, message *store.Message) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	usage, _ := json.Marshal(b.usage)
	pub, _ := json.Marshal(public)
	priv, _ := json.Marshal(private)
	return b.db.SaveAgentStep(ctx, b.owner, b.run, kind, usage, pub, priv, message)
}

// complete serializes all outer/nested requests. A reservation with unknown
// usage is committed before network I/O, so a crash never hides a paid attempt.
func (b *runBudget) complete(ctx context.Context, req deepseek.Request, assistant bool) (deepseek.Response, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if req.MaxTokens < 1 || req.MaxTokens > b.limits.OutputTokens {
		req.MaxTokens = b.limits.OutputTokens
	}
	wire, marshalErr := json.Marshal(struct {
		Messages []deepseek.Message
		Tools    []deepseek.Tool
	}{req.Messages, req.Tools})
	if marshalErr != nil {
		return deepseek.Response{}, &ToolError{Code: "invalid_request"}
	}
	if len(wire) > b.limits.ContextBytes {
		return deepseek.Response{}, &ToolError{Code: "context_limit"}
	}
	// Byte count is a deliberately conservative input-token estimate, not billing.
	if b.limitExceeded || b.usage.Requests >= b.limits.Requests || b.usage.TotalTokens+b.usage.UnknownTokenReserve+len(wire)+req.MaxTokens > b.limits.Tokens {
		b.limitExceeded = true
		return deepseek.Response{}, &ToolError{Code: "budget_exceeded"}
	}
	if err := b.ctx.Err(); err != nil {
		return deepseek.Response{}, err
	}
	callCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(b.ctx, cancel)
	defer stop()
	defer cancel()
	if err := callCtx.Err(); err != nil {
		return deepseek.Response{}, err
	}
	b.usage.Requests++
	b.usage.UnknownRequests++
	estimate := len(wire) + req.MaxTokens
	b.usage.UnknownTokenReserve += estimate
	if err := b.save("model_request_started", map[string]any{"request": b.usage.Requests}, nil, nil); err != nil {
		b.usage.Requests--
		b.usage.UnknownRequests--
		b.usage.UnknownTokenReserve -= estimate
		return deepseek.Response{}, err
	}
	response, err := b.client.Complete(callCtx, req)
	if !response.Attempted {
		b.usage.Requests--
		b.usage.UnknownRequests--
		b.usage.UnknownTokenReserve -= estimate
	} else if response.Usage != nil {
		b.usage.UnknownRequests--
		b.usage.UnknownTokenReserve -= estimate
		b.usage.PromptTokens += response.Usage.PromptTokens
		b.usage.CompletionTokens += response.Usage.CompletionTokens
		b.usage.TotalTokens += response.Usage.TotalTokens
	}
	var message *store.Message
	if assistant && !b.deferAssistant && response.Message.Role == "assistant" {
		calls, _ := json.Marshal(response.Message.ToolCalls)
		provider, _ := json.Marshal(map[string]any{"id": response.ID, "model": response.Model, "finish_reason": response.FinishReason, "usage": response.Usage, "accepted": err == nil})
		message = &store.Message{Role: "assistant", Content: response.Message.Content, ReasoningContent: response.Message.ReasoningContent, ToolCalls: calls, ProviderJSON: provider}
	}
	code := failureCode(err)
	if saveErr := b.save("model_request_finished", map[string]any{"request": b.usage.Requests, "error_code": code}, map[string]any{"response": response}, message); saveErr != nil {
		return response, saveErr
	}
	if b.usage.TotalTokens+b.usage.UnknownTokenReserve >= b.limits.Tokens {
		b.limitExceeded = true
		if err == nil {
			err = &ToolError{Code: "budget_exceeded"}
		}
	}
	return response, err
}

func failureCode(err error) string {
	if err == nil {
		return ""
	}
	var tool *ToolError
	if errors.As(err, &tool) && toolName.MatchString(tool.Code) {
		return tool.Code
	}
	var provider *deepseek.Error
	if errors.As(err, &provider) {
		return provider.Code
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "budget_exceeded"
	}
	return "agent_error"
}

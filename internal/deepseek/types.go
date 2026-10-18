// Package deepseek is the application's single Chat Completions HTTP client.
// Messages and responses are private server data: never serialize them into a
// public API response or log them (they may contain reasoning_content).
package deepseek

import (
	"encoding/json"
	"fmt"
	"time"
)

type Message struct {
	Role             string     `json:"role"`
	Content          *string    `json:"content"`
	ReasoningContent *string    `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
}

func Text(role, content string) Message { return Message{Role: role, Content: &content} }

type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type Tool struct {
	Type     string   `json:"type"`
	Function Function `json:"function"`
}

type Function struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

// Request contains no provider credentials or model override. Complete always
// disables streaming and uses tool_choice=auto when Tools is nonempty.
type Request struct {
	Messages  []Message
	Tools     []Tool
	MaxTokens int // required, 1..4096; includes reasoning tokens if thinking is enabled
	// JSONOutput requests provider JSON-object grammar and temperature=0 for
	// bounded structured analysis/repair. Validation still belongs to the caller.
	JSONOutput bool
}

type Usage struct {
	PromptTokens          int `json:"prompt_tokens"`
	CompletionTokens      int `json:"completion_tokens"`
	TotalTokens           int `json:"total_tokens"`
	PromptCacheHitTokens  int `json:"prompt_cache_hit_tokens,omitempty"`
	PromptCacheMissTokens int `json:"prompt_cache_miss_tokens,omitempty"`
}

// Response is also returned on error. Count Attempted against the run request
// budget; persist any Usage even if a finish reason prevents tool execution.
// A nil Usage means unknown usage, never zero billed tokens.
type Response struct {
	ID           string
	Model        string
	Message      Message
	FinishReason string
	Usage        *Usage
	Attempted    bool
}

// Error deliberately excludes provider bodies, headers, URLs and transport
// diagnostics, all of which may contain secrets or private article text.
type Error struct {
	Code         string
	HTTPStatus   int
	Retryable    bool
	RetryAfter   time.Duration
	UsageUnknown bool
	cause        error
}

func (e *Error) Error() string {
	return fmt.Sprintf("deepseek: %s (HTTP %d)", e.Code, e.HTTPStatus)
}

func (e *Error) Unwrap() error { return e.cause }

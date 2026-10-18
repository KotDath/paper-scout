// Executable integration checks against HTTP provider boundaries; no unit tests.
// The local fixture is never evidence of live DeepSeek availability.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"paper-scout/internal/config"
	"paper-scout/internal/deepseek"
)

func main() {
	live := flag.Bool("live", false, "explicit paid preflight: three calls, at most 416 output tokens")
	envFile := flag.String("env-file", "", "optional dotenv path, loaded only in live mode")
	flag.Parse()
	if *live {
		if err := liveCheck(*envFile); err != nil {
			fmt.Fprintln(os.Stderr, "FAIL:", err)
			os.Exit(1)
		}
		return
	}
	if *envFile != "" {
		fmt.Fprintln(os.Stderr, "FAIL: -env-file requires -live")
		os.Exit(1)
	}
	if err := localChecks(); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
}

var multiply = deepseek.Tool{Type: "function", Function: deepseek.Function{
	Name: "multiply", Description: "Multiply two small integers using the local calculator.",
	Parameters: json.RawMessage(`{"type":"object","properties":{"a":{"type":"integer"},"b":{"type":"integer"}},"required":["a","b"],"additionalProperties":false}`),
}}

func liveCheck(envFile string) error {
	if _, err := config.Load(envFile); err != nil {
		return err
	}
	cfg := deepseek.ConfigFromEnv()
	// Prevent a fixture or accidental proxy setting from being called "live".
	if cfg.BaseURL != "" && strings.TrimRight(cfg.BaseURL, "/") != "https://api.deepseek.com" && strings.TrimRight(cfg.BaseURL, "/") != "https://api.deepseek.com/v1" {
		return errors.New("live preflight requires the official DeepSeek HTTPS endpoint")
	}
	if cfg.Thinking != "" && cfg.Thinking != "disabled" {
		return errors.New("small live preflight requires DEEPSEEK_THINKING=disabled")
	}
	client, err := deepseek.New(cfg)
	if err != nil {
		return err
	}
	fmt.Println("PASS: live credentials/configuration present; values withheld")
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	first, err := client.Complete(ctx, deepseek.Request{
		Messages: []deepseek.Message{deepseek.Text("user", "Reply with exactly T03_OK.")}, MaxTokens: 96,
	})
	if err != nil {
		return err
	}
	if first.Message.Content == nil || strings.TrimSpace(*first.Message.Content) != "T03_OK" {
		return errors.New("live plain completion did not return the requested marker")
	}
	printUsage("plain completion", first)
	messages := []deepseek.Message{deepseek.Text("user", "Use the multiply tool to calculate 6 times 7. Do not calculate it yourself. After receiving the tool result, reply with only the number.")}
	called, err := client.Complete(ctx, deepseek.Request{Messages: messages, Tools: []deepseek.Tool{multiply}, MaxTokens: 256})
	if err != nil {
		return err
	}
	if len(called.Message.ToolCalls) != 1 {
		return errors.New("live model did not request exactly one calculator call")
	}
	call := called.Message.ToolCalls[0]
	var args struct {
		A int `json:"a"`
		B int `json:"b"`
	}
	if call.Function.Name != "multiply" || json.Unmarshal([]byte(call.Function.Arguments), &args) != nil || args.A != 6 || args.B != 7 {
		return errors.New("live calculator arguments failed validation")
	}
	printUsage("tool request", called)
	messages = append(messages, called.Message)
	toolResult := deepseek.Text("tool", fmt.Sprint(args.A*args.B))
	toolResult.ToolCallID = call.ID
	messages = append(messages, toolResult)
	final, err := client.Complete(ctx, deepseek.Request{Messages: messages, Tools: []deepseek.Tool{multiply}, MaxTokens: 64})
	if err != nil {
		return err
	}
	if final.FinishReason != "stop" || final.Message.Content == nil || strings.TrimSpace(*final.Message.Content) != "42" {
		return errors.New("live tool result was not consumed correctly")
	}
	printUsage("tool result completion", final)
	fmt.Println("PASS: real DeepSeek plain completion and coherent calculator tool exchange; three requests, no retries")
	return nil
}

func printUsage(label string, r deepseek.Response) {
	fmt.Printf("PASS: %s; usage prompt=%d completion=%d total=%d\n", label, r.Usage.PromptTokens, r.Usage.CompletionTokens, r.Usage.TotalTokens)
}

func localChecks() error {
	var requests atomic.Int32
	var fixtureFailure atomic.Bool
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected.Add(1)
		w.WriteHeader(500)
	}))
	defer target.Close()
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer integration-only" || r.Header.Get("Content-Type") != "application/json" {
			fixtureFailure.Store(true)
			w.WriteHeader(400)
			return
		}
		var body struct {
			Model      string             `json:"model"`
			Messages   []deepseek.Message `json:"messages"`
			Tools      []deepseek.Tool    `json:"tools"`
			ToolChoice string             `json:"tool_choice"`
			Stream     *bool              `json:"stream"`
			MaxTokens  int                `json:"max_tokens"`
			Thinking   struct {
				Type string `json:"type"`
			} `json:"thinking"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || len(body.Messages) == 0 || body.Model != "integration-model" || body.Stream == nil || *body.Stream || body.MaxTokens != 64 || body.Thinking.Type != "enabled" {
			fixtureFailure.Store(true)
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		scenario := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/"), "/chat/completions")
		if scenario == "status" {
			status := 500
			fmt.Sscan(*body.Messages[0].Content, &status)
			w.Header().Set("Retry-After", "3")
			w.WriteHeader(status)
			fmt.Fprint(w, `{"error":{"message":"integration-only private-error-marker"}}`)
			return
		}
		switch scenario {
		case "redirect":
			w.Header().Set("Location", target.URL)
			w.WriteHeader(307)
			return
		case "wait":
			<-r.Context().Done()
			return
		case "malformed":
			fmt.Fprint(w, `{"private-error-marker":`)
			return
		case "oversized":
			fmt.Fprint(w, strings.Repeat("x", (8<<20)+1))
			return
		}
		message := deepseek.Text("assistant", "42")
		finish := "stop"
		usage := any(deepseek.Usage{PromptTokens: 17, CompletionTokens: 4, TotalTokens: 21, PromptCacheHitTokens: 3, PromptCacheMissTokens: 14})
		if scenario == "tools" || scenario == "length" {
			if len(body.Tools) != 1 || body.ToolChoice != "auto" || body.Tools[0].Function.Name != "multiply" {
				fixtureFailure.Store(true)
			}
			if len(body.Messages) == 1 {
				reasoning := "private-reasoning-marker"
				message = deepseek.Message{Role: "assistant", ReasoningContent: &reasoning, ToolCalls: []deepseek.ToolCall{{ID: "call_one", Type: "function", Function: deepseek.FunctionCall{Name: "multiply", Arguments: `{"a":6,"b":7}`}}}}
				finish = "tool_calls"
			} else if len(body.Messages) != 3 || body.Messages[1].ReasoningContent == nil || *body.Messages[1].ReasoningContent != "private-reasoning-marker" || body.Messages[1].Content != nil || len(body.Messages[1].ToolCalls) != 1 || body.Messages[2].Role != "tool" || body.Messages[2].ToolCallID != "call_one" || body.Messages[2].Content == nil || *body.Messages[2].Content != "42" {
				fixtureFailure.Store(true)
			}
			if scenario == "length" {
				finish = "length"
			}
		} else if len(body.Tools) != 0 || body.ToolChoice != "" {
			fixtureFailure.Store(true)
		}
		switch scenario {
		case "no-usage":
			usage = nil
		case "empty-usage":
			usage = map[string]int{}
		case "bad-usage":
			usage = deepseek.Usage{PromptTokens: 2, CompletionTokens: 3, TotalTokens: 0}
		case "filter":
			finish = "content_filter"
		case "bad-finish":
			finish = "unknown"
		}
		json.NewEncoder(w).Encode(map[string]any{"id": "fixture", "model": "integration-model", "usage": usage,
			"choices": []any{map[string]any{"message": message, "finish_reason": finish}}})
	}))
	defer fixture.Close()
	newClient := func(scenario string, timeout time.Duration) *deepseek.DeepSeekClient {
		c, err := deepseek.New(deepseek.Config{BaseURL: fixture.URL + "/" + scenario, Model: "integration-model", APIKey: "integration-only", Thinking: "enabled", Timeout: timeout})
		if err != nil {
			panic("integration client configuration failed")
		}
		return c
	}
	input := deepseek.Request{Messages: []deepseek.Message{deepseek.Text("user", "calculate")}, MaxTokens: 64}
	ctx := context.Background()
	plain, err := newClient("v1", 0).Complete(ctx, input)
	if err != nil || !plain.Attempted || plain.Usage == nil || plain.Usage.TotalTokens != 21 || plain.Usage.PromptCacheHitTokens != 3 {
		return errors.New("HTTP plain completion or usage failed")
	}
	toolInput := input
	toolInput.Tools = []deepseek.Tool{multiply}
	client := newClient("tools", 0)
	called, err := client.Complete(ctx, toolInput)
	if err != nil || called.Message.Content != nil || len(called.Message.ToolCalls) != 1 || called.Message.ReasoningContent == nil {
		return errors.New("HTTP tool completion fields failed")
	}
	toolInput.Messages = append(toolInput.Messages, called.Message)
	toolResult := deepseek.Text("tool", "42")
	toolResult.ToolCallID = called.Message.ToolCalls[0].ID
	toolInput.Messages = append(toolInput.Messages, toolResult)
	final, err := client.Complete(ctx, toolInput)
	if err != nil || final.FinishReason != "stop" || final.Message.Content == nil || *final.Message.Content != "42" || fixtureFailure.Load() {
		return errors.New("HTTP tool history/reasoning replay failed")
	}
	fmt.Println("PASS: HTTP POST /v1/chat/completions, auth, explicit stream/thinking/token settings, plain/tool round trip, nullable content, private reasoning replay, usage/cache fields")
	for status, code := range map[int]string{400: "provider_invalid_request", 401: "provider_authentication", 402: "provider_balance", 422: "provider_invalid_request", 429: "provider_rate_limited", 500: "provider_unavailable", 503: "provider_unavailable"} {
		r := input
		r.Messages = []deepseek.Message{deepseek.Text("user", fmt.Sprint(status))}
		before := requests.Load()
		response, err := newClient("status", 0).Complete(ctx, r)
		var failure *deepseek.Error
		if !errors.As(err, &failure) || failure.Code != code || failure.HTTPStatus != status || failure.RetryAfter != 3*time.Second || failure.Retryable != (status == 429 || status >= 500) || !failure.UsageUnknown || response.Usage != nil || !response.Attempted || requests.Load() != before+1 || strings.Contains(err.Error(), "integration-only") || strings.Contains(err.Error(), "private-error-marker") {
			return errors.New("HTTP error classification/sanitization/attempt count failed")
		}
	}
	fmt.Println("PASS: HTTP 400/401/402/422/429/500/503, Retry-After, sanitized errors, unknown usage, exactly one attempt without hidden retries")
	for scenario, code := range map[string]string{"redirect": "provider_error", "malformed": "invalid_response", "oversized": "response_too_large", "no-usage": "invalid_response", "empty-usage": "invalid_response", "bad-usage": "invalid_response", "filter": "content_filtered", "bad-finish": "invalid_response", "length": "output_limit"} {
		r := input
		if scenario == "length" {
			r.Tools = []deepseek.Tool{multiply}
		}
		response, err := newClient(scenario, 0).Complete(ctx, r)
		var failure *deepseek.Error
		if !errors.As(err, &failure) || failure.Code != code || !response.Attempted {
			return fmt.Errorf("HTTP %s rejection failed", scenario)
		}
		if scenario == "length" && (response.Usage == nil || response.Usage.TotalTokens != 21 || len(response.Message.ToolCalls) != 1) {
			return errors.New("truncated response must preserve usage and private message with error")
		}
	}
	if redirected.Load() != 0 {
		return errors.New("redirect unexpectedly followed")
	}
	fmt.Println("PASS: redirects refused, invalid/oversized bodies, absent/inconsistent usage, unknown finish/content filtering, length prevents successful tool execution while preserving usage")
	for _, cancellation := range []bool{false, true} {
		callCtx, cancel := context.WithCancel(ctx)
		if cancellation {
			time.AfterFunc(30*time.Millisecond, cancel)
		}
		response, err := newClient("wait", 80*time.Millisecond).Complete(callCtx, input)
		cancel()
		want := context.DeadlineExceeded
		if cancellation {
			want = context.Canceled
		}
		var failure *deepseek.Error
		if !errors.Is(err, want) || !errors.As(err, &failure) || !failure.UsageUnknown || !response.Attempted {
			return errors.New("in-flight cancellation/deadline failed")
		}
	}
	before := requests.Load()
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	response, err := client.Complete(cancelled, input)
	if !errors.Is(err, context.Canceled) || response.Attempted || requests.Load() != before {
		return errors.New("pre-cancelled request was sent")
	}
	invalid := input
	invalid.MaxTokens = 4097
	response, err = client.Complete(ctx, invalid)
	if err == nil || response.Attempted || requests.Load() != before {
		return errors.New("oversized output budget was sent")
	}
	closed := httptest.NewServer(http.NotFoundHandler())
	closedURL := closed.URL
	closed.Close()
	unavailable, _ := deepseek.New(deepseek.Config{BaseURL: closedURL, Model: "integration-model", APIKey: "integration-only"})
	response, err = unavailable.Complete(ctx, input)
	var failure *deepseek.Error
	if !errors.As(err, &failure) || failure.Code != "provider_unavailable" || !failure.UsageUnknown || !response.Attempted || strings.Contains(err.Error(), closedURL) {
		return errors.New("transport failure sanitization/usage failed")
	}
	if fixtureFailure.Load() {
		return errors.New("fixture observed a protocol contract violation")
	}
	fmt.Println("PASS: real in-flight HTTP cancellation and deadline, pre-send cancellation, output budget validation, refused connection sanitization")
	return nil
}

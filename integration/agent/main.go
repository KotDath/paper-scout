// Executable integration checks: real SQLite + lifecycle + registry + HTTP client.
// No unit tests or mocked application components.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"paper-scout/internal/agent"
	"paper-scout/internal/config"
	"paper-scout/internal/deepseek"
	"paper-scout/internal/research"
	"paper-scout/internal/store"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}
func check(ok bool, label string) {
	if !ok {
		panic(label)
	}
}

var ctx = context.Background()

func main() {
	runtime := flag.String("runtime", "", "temporary runtime directory inside this worktree")
	live := flag.Bool("live", false, "bounded official DeepSeek exchange")
	envFile := flag.String("env-file", "", "dotenv loaded by process only with -live")
	flag.Parse()
	check(*runtime != "", "runtime required")
	if !*live {
		check(*envFile == "", "env-file requires live mode")
	}
	db, err := store.Open(ctx, filepath.Join(*runtime, "agent.sqlite"))
	must(err)
	defer db.Close()
	must(db.SeedAccounts(ctx, []store.SeedAccount{
		{User: store.User{ID: "a", Login: "agent-a", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}, PasswordHash: "integration-only"},
		{User: store.User{ID: "b", Login: "agent-b", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}, PasswordHash: "integration-only"},
	}))
	_, err = db.UpdateProfile(ctx, "a", "T09_OK", "ru", time.Now().UTC().Format(time.RFC3339Nano))
	must(err)
	_, err = db.UpdateProfile(ctx, "b", "OTHER_ACCOUNT_PRIVATE_MARKER", "ru", time.Now().UTC().Format(time.RFC3339Nano))
	must(err)
	service := research.New(ctx, db)
	defer service.Close()
	var executions atomic.Int32
	var cancelID string
	definition := deepseek.Tool{Type: "function", Function: deepseek.Function{Name: "inspect_profile", Description: "Read the current user's verification marker from their profile. No arguments.", Parameters: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)}}
	tool := agent.Tool{Definition: definition, Validate: func(raw json.RawMessage) error { return agent.DecodeArgs(raw, &struct{}{}) }, Execute: func(callCtx context.Context, s agent.Scope, raw json.RawMessage) (json.RawMessage, error) {
		executions.Add(1)
		check(s.Owner == "a" && s.ConversationID != "" && s.RunID != "", "scope missing authenticated IDs")
		p, err := db.Profile(callCtx, s.Owner)
		if err != nil {
			return nil, err
		}
		if cancelID == s.RunID {
			_, err = service.Cancel(ctx, s.Owner, s.RunID)
			must(err)
		}
		return json.Marshal(map[string]string{"marker": p.Interests})
	}}
	registry, err := agent.NewRegistry(tool)
	must(err)
	_, err = agent.NewRegistry(tool, tool)
	check(err != nil, "duplicate registry accepted")
	create := func(conversation, message string) store.Run {
		r, _, err := service.Create(ctx, "a", conversation, fmt.Sprint(time.Now().UnixNano()), message)
		must(err)
		return r
	}
	newConversation := func() string { c, err := db.CreateConversation(ctx, "a", "Agent integration"); must(err); return c.ID }
	runWith := func(client *deepseek.DeepSeekClient, reg *agent.Registry, l agent.Limits, conversation, message string) (agent.Result, error) {
		runner, err := agent.New(db, service, client, reg, l)
		must(err)
		run := create(conversation, message)
		return runner.Run(ctx, "a", conversation, run.ID)
	}
	limits := agent.DefaultLimits()
	limits.OutputTokens = 192
	if *live {
		_, err = config.Load(*envFile)
		must(err)
		cfg := deepseek.ConfigFromEnv()
		check(cfg.BaseURL == "" || strings.TrimRight(cfg.BaseURL, "/") == "https://api.deepseek.com" || strings.TrimRight(cfg.BaseURL, "/") == "https://api.deepseek.com/v1", "official live endpoint required")
		check(cfg.Thinking == "" || cfg.Thinking == "disabled", "bounded live check requires thinking disabled")
		client, err := deepseek.New(cfg)
		must(err)
		limits.Requests = 3
		limits.Retries = 0
		limits.ToolCalls = 1
		result, err := runWith(client, registry, limits, newConversation(), "Call inspect_profile exactly once with an empty JSON object. After receiving its result reply with exactly its marker, T09_OK. Do not answer before calling the tool.")
		must(err)
		check(result.Run.Status == "completed" && strings.TrimSpace(result.Answer) == "T09_OK" && executions.Load() == 1 && result.Usage.Requests == 2, "live loop did not complete one tool exchange")
		fmt.Printf("PASS: live official DeepSeek -> owner-scoped SQLite tool -> DeepSeek; model=%s requests=%d prompt=%d completion=%d total=%d unknown=%d; no retries; secrets/reasoning withheld\n", cfg.Model, result.Usage.Requests, result.Usage.PromptTokens, result.Usage.CompletionTokens, result.Usage.TotalTokens, result.Usage.UnknownRequests)
		return
	}
	var requests atomic.Int32
	var fixtureBad atomic.Bool
	var waitStarted = make(chan struct{}, 1)
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		var input struct {
			Messages  []deepseek.Message `json:"messages"`
			Tools     []deepseek.Tool    `json:"tools"`
			MaxTokens int                `json:"max_tokens"`
		}
		if json.NewDecoder(r.Body).Decode(&input) != nil {
			fixtureBad.Store(true)
			w.WriteHeader(400)
			return
		}
		scenario := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/"), "/chat/completions")
		var latest string
		var toolResults int
		for _, m := range input.Messages {
			if m.Content != nil && strings.Contains(*m.Content, "OTHER_ACCOUNT_PRIVATE_MARKER") {
				fixtureBad.Store(true)
			}
			if m.Role == "user" && m.Content != nil {
				latest = *m.Content
				toolResults = 0
			}
			if m.Role == "tool" {
				toolResults++
			}
		}
		if scenario == "wait" {
			select {
			case waitStarted <- struct{}{}:
			default:
			}
			<-r.Context().Done()
			return
		}
		if scenario == "unavailable" || (scenario == "retry" && n%2 == 1) {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(503)
			return
		}
		message := deepseek.Text("assistant", "T09_OK")
		finish := "stop"
		var calls []deepseek.ToolCall
		mk := func(id, name, args string) deepseek.ToolCall {
			return deepseek.ToolCall{ID: id, Type: "function", Function: deepseek.FunctionCall{Name: name, Arguments: args}}
		}
		switch latest {
		case "exchange", "cancel", "truncated", "tokens", "nested", "tool_timeout":
			if toolResults == 0 {
				calls = []deepseek.ToolCall{mk("first", "inspect_profile", `{}`)}
			}
			if latest == "cancel" && toolResults == 0 {
				calls = append(calls, mk("second", "inspect_profile", `{}`))
			}
		case "nested-token-limit":
			if toolResults == 0 {
				calls = []deepseek.ToolCall{mk("analyze", "analyze_fixture", `{}`), mk("after-analysis", "inspect_profile", `{}`), mk("last", "inspect_profile", `{}`)}
			}
		case "invalid":
			if toolResults == 0 {
				calls = []deepseek.ToolCall{mk("unknown", "no_such_tool", `{}`), mk("json", "inspect_profile", `{`), mk("fields", "inspect_profile", `{"owner":"b"}`), mk("null", "inspect_profile", `null`)}
			}
		case "repeat":
			calls = []deepseek.ToolCall{mk(fmt.Sprint(n), "inspect_profile", `{}`)}
		}
		if len(calls) > 0 {
			reasoning := "private-reasoning-fixture"
			message = deepseek.Message{Role: "assistant", ReasoningContent: &reasoning, ToolCalls: calls}
			finish = "tool_calls"
		}
		if latest == "truncated" {
			finish = "length"
		}
		// Validate actual replay of reasoning and original tool IDs at HTTP boundary.
		if toolResults > 0 {
			pending := map[string]bool{}
			for _, m := range input.Messages {
				if m.Role == "assistant" && len(m.ToolCalls) > 0 {
					if m.ReasoningContent == nil || *m.ReasoningContent != "private-reasoning-fixture" {
						fixtureBad.Store(true)
					}
					for _, call := range m.ToolCalls {
						pending[call.ID] = true
					}
				}
				if m.Role == "tool" {
					if !pending[m.ToolCallID] {
						fixtureBad.Store(true)
					}
					delete(pending, m.ToolCallID)
				}
			}
			if len(pending) > 0 {
				fixtureBad.Store(true)
			}
		}
		usage := deepseek.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}
		if latest == "tokens" {
			usage = deepseek.Usage{PromptTokens: 249990, CompletionTokens: 10, TotalTokens: 250000}
		}
		if latest == "nested-tokens" {
			usage = deepseek.Usage{PromptTokens: 249975, CompletionTokens: 10, TotalTokens: 249985}
		}
		json.NewEncoder(w).Encode(map[string]any{"id": fmt.Sprint(n), "model": "integration-model", "choices": []any{map[string]any{"message": message, "finish_reason": finish}}, "usage": usage})
	}))
	defer fixture.Close()
	clientFor := func(scenario string) *deepseek.DeepSeekClient {
		c, err := deepseek.New(deepseek.Config{BaseURL: fixture.URL + "/" + scenario, Model: "integration-model", APIKey: "integration-only", Thinking: "enabled"})
		must(err)
		return c
	}
	client := clientFor("normal")
	conversation := newConversation()
	result, err := runWith(client, registry, limits, conversation, "exchange")
	must(err)
	check(result.Answer == "T09_OK" && result.Usage.Requests == 2 && result.Usage.ToolCalls == 1 && result.Usage.TotalTokens == 30, "loop/usage failed")
	check(result.Usage.UnknownRequests == 0, "successful usage unknown")
	stored, err := db.Messages(ctx, "a", conversation)
	must(err)
	check(len(stored) == 4 && stored[1].ReasoningContent != nil && stored[2].ToolCallID == "first", "private history not durable")
	previous := result.Run.ID
	_, err = runWith(client, registry, limits, conversation, "follow-up")
	must(err)
	history, err := (agent.AgentSession{Store: db}).Context(ctx, "a", conversation, previous)
	must(err)
	check(len(history) >= 8, "completed history not replayed")
	_, err = db.AgentMessages(ctx, "b", conversation, previous)
	check(errors.Is(err, store.ErrNotFound), "foreign history readable")
	runner, err := agent.New(db, service, client, registry, limits)
	must(err)
	_, err = runner.Run(ctx, "b", conversation, previous)
	check(errors.Is(err, store.ErrNotFound), "foreign run admitted")
	_, err = runner.Run(ctx, "a", newConversation(), previous)
	check(errors.Is(err, store.ErrNotFound), "wrong conversation admitted")
	_, err = runner.Run(ctx, "a", conversation, previous)
	check(errors.Is(err, store.ErrRunState), "completed run replayed")
	fmt.Println("PASS: actual Chat Completions/tool loop, original IDs and private reasoning replay, SQLite profile ownership, completed follow-up, no paid replay")
	before := executions.Load()
	result, err = runWith(client, registry, limits, newConversation(), "invalid")
	must(err)
	check(executions.Load() == before && result.Usage.ToolCalls == 4, "invalid arguments/unknown tool executed")
	events, err := db.RunEvents(ctx, "a", result.Run.ID, 0, 100)
	must(err)
	encoded, _ := json.Marshal(events)
	check(!strings.Contains(string(encoded), "private-reasoning-fixture") && strings.Contains(string(encoded), "invalid_arguments"), "public events leaked private data or lost errors")
	fmt.Println("PASS: unknown tools, malformed/null JSON and owner injection yield structured tool errors without execution; public events exclude private reasoning")
	result, err = runWith(client, registry, limits, newConversation(), "truncated")
	check(err != nil && result.Run.ErrorCode == "output_limit" && executions.Load() == before && result.Usage.TotalTokens == 15, "truncated tools executed/usage lost")
	failedConversation := result.Run.ConversationID
	next := create(failedConversation, "after failure")
	history, err = (agent.AgentSession{Store: db}).Context(ctx, "a", failedConversation, next.ID)
	must(err)
	check(len(history) == 3, "failed turn replayed")
	_, err = service.Cancel(ctx, "a", next.ID)
	must(err)
	failedMessages, err := db.Messages(ctx, "a", failedConversation)
	must(err)
	check(failedMessages[2].ToolCallID == "first" && strings.Contains(*failedMessages[2].Content, "output_limit"), "truncated tool lacked non-execution result")
	for _, scenario := range []string{"requests", "tools", "context", "tokens", "repeat"} {
		l := limits
		message := "exchange"
		switch scenario {
		case "requests":
			l.Requests = 1
		case "tools":
			l.ToolCalls = 1
			message = "invalid"
		case "context":
			l.ContextBytes = 1
		case "tokens":
			message = "tokens"
		case "repeat":
			l.ToolCalls = 4
			message = "repeat"
		}
		start := executions.Load()
		result, err = runWith(client, registry, l, newConversation(), message)
		check(err != nil, "limit unexpectedly completed: "+scenario)
		expectedStatus := "failed"
		if scenario == "requests" || scenario == "repeat" {
			expectedStatus = "partial"
		}
		check(result.Run.Status == expectedStatus, "limit misclassified useful work: "+scenario)
		if scenario == "tools" {
			check(executions.Load() == start && result.Usage.ToolCalls == 1, "rejected call accounting changed")
			events, err := db.RunEvents(ctx, "a", result.Run.ID, 0, 100)
			must(err)
			for _, event := range events {
				check(event.Type != "tool_started", "rejected call started execution")
			}
		}
		if scenario == "context" {
			check(result.Run.ErrorCode == "context_limit" && result.Usage.Requests == 0, "context limit sent provider request")
		} else {
			check(result.Run.ErrorCode == "budget_exceeded", "wrong budget terminal code: "+scenario)
		}
		if scenario == "tokens" {
			check(executions.Load() == start && result.Usage.TotalTokens == 250000, "post-usage limit executed tool")
		}
		if scenario == "repeat" {
			check(executions.Load() == start+2, "identical tool executed more than twice")
		}
	}
	fmt.Println("PASS: output truncation prevents tools, failed turns excluded; request/tool/context/token limits and canonical identical-call cap")
	// Cancellation inside the first of two tool calls must journal the second as
	// cancelled without executing it, and cancellation wins terminal state.
	before = executions.Load()
	c := newConversation()
	r := create(c, "cancel")
	cancelID = r.ID
	result, err = runner.Run(ctx, "a", c, r.ID)
	check(err != nil && result.Run.Status == "cancelled" && executions.Load() == before+1, "multi-call cancellation failed")
	messages, err := db.Messages(ctx, "a", c)
	must(err)
	check(len(messages) == 4 && messages[3].ToolCallID == "second" && strings.Contains(*messages[3].Content, "cancelled"), "unstarted tool not accounted cancelled")
	cancelID = ""
	// Cancel an actual in-flight provider HTTP request via the real service.
	waiting, err := agent.New(db, service, clientFor("wait"), registry, limits)
	must(err)
	c = newConversation()
	r = create(c, "wait")
	done := make(chan agent.Result, 1)
	go func() { res, _ := waiting.Run(ctx, "a", c, r.ID); done <- res }()
	<-waitStarted
	_, err = service.Cancel(ctx, "a", r.ID)
	must(err)
	result = <-done
	check(result.Run.Status == "cancelled" && result.Usage.UnknownRequests == 1, "HTTP cancellation lost uncertain usage")
	// Bounded retry at the actual HTTP provider boundary.
	requests.Store(0)
	result, err = runWith(clientFor("retry"), registry, limits, newConversation(), "retry")
	must(err)
	check(result.Usage.Requests == 2 && result.Usage.UnknownRequests == 1, "retry budget/uncertainty failed")
	requests.Store(0)
	result, err = runWith(clientFor("unavailable"), registry, limits, newConversation(), "retry-exhausted")
	check(err != nil && result.Usage.Requests == 2 && result.Usage.UnknownRequests == 2 && result.Usage.UnknownTokenReserve > 0 && result.Run.ErrorCode == "provider_unavailable", "unbounded retries or missing uncertainty reserve")
	lDuration := limits
	lDuration.DurationSeconds = 1
	result, err = runWith(clientFor("wait"), registry, lDuration, newConversation(), "deadline")
	check(err != nil && result.Run.ErrorCode == "budget_exceeded" && result.Usage.UnknownRequests == 1, "overall duration limit failed")
	fmt.Println("PASS: cancellation stops queued tool and in-flight provider HTTP; terminal cancellation wins; bounded 503 retry preserves unknown usage")
	// Nested analysis uses the same client and budget; its output is tool data.
	nested := tool
	nested.Execute = func(callCtx context.Context, s agent.Scope, raw json.RawMessage) (json.RawMessage, error) {
		_, err := s.Complete(callCtx, deepseek.Request{Messages: []deepseek.Message{deepseek.Text("user", "nested-analysis")}, MaxTokens: 64})
		if err != nil {
			return nil, err
		}
		return json.RawMessage(`{"analysis":"done"}`), nil
	}
	nestedRegistry, err := agent.NewRegistry(nested)
	must(err)
	l := limits
	l.Requests = 2
	result, err = runWith(client, nestedRegistry, l, newConversation(), "nested")
	check(err != nil && result.Usage.Requests == 2 && result.Usage.TotalTokens == 30, "nested call bypassed shared budget")
	// A nested response exhausts the global token allowance while further calls
	// are still pending in the same assistant turn. Even a callback that handles
	// the nested error must not permit another tool or model request.
	for _, preservePartial := range []bool{false, true} {
		analyzing := tool
		analyzing.Definition.Function.Name = "analyze_fixture"
		var analyses int
		analyzing.Execute = func(callCtx context.Context, s agent.Scope, raw json.RawMessage) (json.RawMessage, error) {
			analyses++
			response, err := s.Complete(callCtx, deepseek.Request{Messages: []deepseek.Message{deepseek.Text("user", "nested-tokens")}, MaxTokens: 64})
			check(err != nil, "nested token exhaustion was not reported")
			if preservePartial {
				return json.Marshal(map[string]any{"observations": response.Message.Content})
			}
			return nil, err
		}
		reg, err := agent.NewRegistry(analyzing, tool)
		must(err)
		start := executions.Load()
		result, err = runWith(client, reg, limits, newConversation(), "nested-token-limit")
		check(err != nil && result.Run.ErrorCode == "budget_exceeded", "nested exhaustion did not stop run")
		check(analyses == 1 && executions.Load() == start, "tool executed after nested token exhaustion")
		check(result.Usage.Requests == 2 && result.Usage.ToolCalls == 1 && result.Usage.TotalTokens == 250000, "nested exhaustion accounting incorrect")
		expected := "failed"
		if preservePartial {
			expected = "partial"
		}
		check(result.Run.Status == expected, "nested exhaustion misclassified useful work")
		messages, err := db.Messages(ctx, "a", result.Run.ConversationID)
		must(err)
		check(len(messages) == 5, "nested exhaustion lost matching tool results")
		for i, id := range []string{"after-analysis", "last"} {
			check(messages[i+3].ToolCallID == id && strings.Contains(*messages[i+3].Content, "budget_exceeded"), "pending tool lacks non-execution result")
		}
		events, err := db.RunEvents(ctx, "a", result.Run.ID, 0, 100)
		must(err)
		started := 0
		for _, event := range events {
			if event.Type == "tool_started" {
				started++
			}
		}
		check(started == 1, "pending tool started after nested token exhaustion")
	}
	fmt.Println("PASS: nested token exhaustion stops remaining calls, preserves original skipped IDs and accounting, and distinguishes successful partial work")
	// Context deadlines are propagated to tool code, then recorded as failures.
	timeoutTool := tool
	timeoutTool.Execute = func(callCtx context.Context, s agent.Scope, raw json.RawMessage) (json.RawMessage, error) {
		<-callCtx.Done()
		return nil, callCtx.Err()
	}
	timeoutRegistry, err := agent.NewRegistry(timeoutTool)
	must(err)
	l = limits
	l.ToolTimeout = 20 * time.Millisecond
	result, err = runWith(client, timeoutRegistry, l, newConversation(), "tool_timeout")
	must(err)
	check(result.Usage.ToolCalls == 1, "tool deadline not handled")
	timeoutMessages, err := db.Messages(ctx, "a", result.Run.ConversationID)
	must(err)
	check(strings.Contains(*timeoutMessages[2].Content, "tool_timeout"), "tool timeout reported as run budget")
	check(!fixtureBad.Load(), "provider boundary observed incoherent history or foreign profile")
	c = newConversation()
	r = create(c, "finalizer")
	result, err = runner.RunWithFinalizer(ctx, "a", c, r.ID, func(persistCtx context.Context, res agent.Result) error {
		check(res.Answer == "T09_OK" && res.Usage.Requests == 1, "finalizer did not receive answer/usage")
		usage, _ := json.Marshal(res.Usage)
		_, err := db.FinishRun(persistCtx, "a", r.ID, "completed", "", usage)
		return err
	})
	must(err)
	check(result.Run.Status == "completed", "successful finalizer failed")
	c = newConversation()
	r = create(c, "bad finalizer")
	result, err = runner.RunWithFinalizer(ctx, "a", c, r.ID, func(context.Context, agent.Result) error { return nil })
	check(err != nil && result.Run.ErrorCode == "finalization_error", "uncommitted finalizer accepted")
	var integrity string
	must(db.DB.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity))
	check(integrity == "ok", "SQLite corrupt")
	// Reopen actual DB and recover an in-progress reservation; no provider replay.
	c = newConversation()
	r = create(c, "interrupted")
	_, _, err = service.Start(ctx, "a", r.ID)
	must(err)
	must(db.SaveAgentStep(ctx, "a", r.ID, "model_request_started", json.RawMessage(`{"deepseek_requests":1,"unknown_usage_requests":1}`), nil, nil, nil))
	must(db.Close())
	db, err = store.Open(ctx, filepath.Join(*runtime, "agent.sqlite"))
	must(err)
	defer db.Close()
	must(db.RecoverRuns(ctx))
	saved, err := db.Run(ctx, "a", r.ID)
	must(err)
	check(saved.Status == "interrupted" && strings.Contains(string(saved.Usage), "unknown_usage_requests"), "restart lost in-flight reservation")
	saved, err = db.Run(ctx, "a", previous)
	must(err)
	check(saved.Status == "completed", "restart lost complete turn")
	fmt.Println("PASS: nested model requests share run budget, tool deadline propagation, SQLite integrity, reopen retains completed history and interrupted request reservation")
}

package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"paper-scout/internal/deepseek"
	"paper-scout/internal/research"
	"paper-scout/internal/researchmemory"
	"paper-scout/internal/store"
)

type AgentRunner struct {
	grounded bool
	memory   *researchmemory.Service
	db       *store.Store
	runs     *research.Service
	client   *deepseek.DeepSeekClient
	registry *Registry
	limits   Limits
}

type Result struct {
	Run              store.Run
	Answer           string
	Usage            Usage
	Grounded         bool
	EvidenceIDs      []string
	MemoryIDs        []string
	GroundingRepairs int
}

func New(db *store.Store, runs *research.Service, client *deepseek.DeepSeekClient, registry *Registry, limits Limits) (*AgentRunner, error) {
	if db == nil || runs == nil || client == nil || registry == nil {
		return nil, errors.New("missing agent dependency")
	}
	if err := limits.validate(); err != nil {
		return nil, err
	}
	return &AgentRunner{db: db, runs: runs, client: client, registry: registry, limits: limits}, nil
}

// Run starts a queued run exactly once. ctx is used for admission only; research
// execution belongs to the server lifetime and explicit run cancellation. The
// final assistant text is durable; report materialization is the T12 boundary.
func (a *AgentRunner) Run(ctx context.Context, owner, conversation, id string) (Result, error) {
	return a.RunWithFinalizer(ctx, owner, conversation, id, nil)
}

// Finalizer is the research application's successful-result transaction. It
// must commit the report, usage and terminal run/event together, respecting a
// concurrent cancellation. The runner then releases lifecycle resources through
// Service.Finish (which is idempotent for a terminal run). No provider I/O here.
type Finalizer func(context.Context, Result) error

func (a *AgentRunner) RunWithFinalizer(ctx context.Context, owner, conversation, id string, finalize Finalizer) (result Result, err error) {
	queued, err := a.db.Run(ctx, owner, id)
	if err != nil {
		return result, err
	}
	if queued.ConversationID != conversation {
		return result, store.ErrNotFound
	}
	runCtx, started, err := a.runs.Start(ctx, owner, id)
	if err != nil {
		return result, err
	}
	result.Run = started
	limits := a.limits
	var durable Limits
	if json.Unmarshal(started.Budget, &durable) != nil {
		return a.finish(result, "failed", "invalid_budget")
	}
	limits.ToolCalls = min(limits.ToolCalls, durable.ToolCalls)
	limits.Requests = min(limits.Requests, durable.Requests)
	limits.Tokens = min(limits.Tokens, durable.Tokens)
	limits.OutputTokens = min(limits.OutputTokens, durable.OutputTokens)
	limits.DurationSeconds = min(limits.DurationSeconds, durable.DurationSeconds)
	if limits.validate() != nil {
		return a.finish(result, "failed", "invalid_budget")
	}
	runCtx, cancel := context.WithTimeout(runCtx, time.Duration(limits.DurationSeconds)*time.Second)
	defer cancel()
	effective, _ := json.Marshal(limits)
	result.Run.Budget = effective
	if err := a.db.SaveAgentLimits(runCtx, owner, id, effective); err != nil {
		return a.finish(result, "failed", "persistence_error")
	}
	budget := &runBudget{db: a.db, client: a.client, owner: owner, run: id, ctx: runCtx, limits: limits, deferAssistant: a.grounded}
	repairs := 0
	repairHint := ""
	status, code := "failed", ""
	hasUsefulResult := false
	scope := Scope{Owner: owner, ConversationID: conversation, RunID: id}
	repeats := map[[32]byte]int{}
	if a.memory != nil {
		if err := a.recall(runCtx, started, "question", started.RequestMessage); err != nil {
			return a.finish(result, "failed", failureCode(err))
		}
	}
	contentQuery := ""
	contentRecalled := false
	for {
		if runCtx.Err() != nil {
			code = failureCode(runCtx.Err())
			break
		}
		if a.memory != nil && !contentRecalled && contentQuery != "" {
			if err := a.recall(runCtx, started, "content", contentQuery); err != nil {
				code = failureCode(err)
				break
			}
			contentRecalled = true
		}
		messages, buildErr := (AgentSession{Store: a.db, CanonicalHistory: a.grounded}).Context(runCtx, owner, conversation, id)
		if buildErr != nil {
			code = "incoherent_history"
			break
		}
		var ground *grounding
		if a.grounded {
			ground, buildErr = a.grounding(runCtx, started)
			if buildErr != nil {
				code = "grounding_unavailable"
				break
			}
			manifest := ground.context()
			snaps, e := a.db.MemorySnapshots(runCtx, owner, id)
			wire, _ := json.Marshal(snaps)
			if e != nil || (len(snaps) > 0 && utf8.RuneCount(wire)+ground.memoryWireSize() > memoryContextRunes) {
				code = "memory_context_limit"
				break
			}
			messages = append(messages, deepseek.Text("system", finalContract), deepseek.Text("user", manifest))
			if repairHint != "" {
				messages = append(messages, deepseek.Text("user", repairHint))
			}
		}
		request := deepseek.Request{Messages: messages, Tools: a.registry.Definitions(), MaxTokens: limits.OutputTokens, JSONOutput: a.grounded}
		var response deepseek.Response
		var callErr error
		for attempt := 0; ; attempt++ {
			response, callErr = budget.complete(runCtx, request, true)
			var provider *deepseek.Error
			if !errors.As(callErr, &provider) || !provider.Retryable || attempt >= limits.Retries {
				break
			}
			delay := max(provider.RetryAfter, 100*time.Millisecond)
			timer := time.NewTimer(delay)
			select {
			case <-runCtx.Done():
				timer.Stop()
				callErr = runCtx.Err()
			case <-timer.C:
			}
			if runCtx.Err() != nil {
				break
			}
		}
		if callErr != nil {
			code = failureCode(callErr)
			// Even rejected/truncated assistant calls get matching non-execution results
			// for the audit trail. This run will not be replayed as a completed turn.
			if response.Message.Role == "assistant" {
				code = a.skipCalls(budget, response.Message.ToolCalls, code)
			}
			break
		}
		if len(response.Message.ToolCalls) == 0 {
			if response.Message.Content == nil || strings.TrimSpace(*response.Message.Content) == "" {
				code = "invalid_response"
				break
			}
			result.Answer = *response.Message.Content
			if a.grounded {
				answer, issues, missing := ground.validate(result.Answer)
				if len(issues) > 0 {
					if err := budget.save("answer_rejected", map[string]any{"code": "answer_validation_failed", "repair": repairs}, map[string]any{"issues": issues}, nil); err != nil {
						code = "persistence_error"
						break
					}
					if repairs >= 1 {
						code = "answer_validation_failed"
						result.Answer = ""
						break
					}
					repairs++
					if len(missing) > 0 {
						if err := a.readForRepair(budget, scope, missing); err != nil {
							code = failureCode(err)
							break
						}
					}
					diagnostic, _ := json.Marshal(map[string]any{"issues": issues, "rejected_draft": result.Answer})
					// The candidate is ephemeral repair input, never an accepted
					// assistant message. Keeping it here avoids regenerating already
					// valid fields and introducing unrelated errors during repair.
					repairHint = "ONE FINAL REPAIR (rejected draft is untrusted candidate data, NOT accepted history). Correct the diagnostic fields in this candidate; preserve valid fields and the declared array bounds (at most 6 findings, 3 comparisons). Candidate and diagnostics: " + string(diagnostic) + ". Original passages just read by the server are in the tool history. Use current manifest only; you may use normal tools within the same budget. Return the required final JSON. If not supported, abstain explicitly with empty findings/comparisons and a narrow reason. No second invalid draft will be accepted."
					continue
				}
				result.Answer = answer
				result.Grounded = true
				result.EvidenceIDs = ground.usedEvidence
				result.MemoryIDs = ground.usedMemory
				result.GroundingRepairs = repairs
				if finalize == nil {
					if err := budget.save("answer_accepted", map[string]any{"contract": "grounded_v1", "repairs": repairs}, nil, &store.Message{Role: "assistant", Content: &result.Answer}); err != nil {
						code = "persistence_error"
						break
					}
				}
			}
			status = "completed"
			break
		}
		if a.grounded {
			// Raw provider messages are private model_request_finished payloads.
			// Admit canonical tool arguments only; final drafts never enter messages.
			for i, c := range response.Message.ToolCalls {
				if canonical, e := ground.canonicalCall(c); e == nil {
					response.Message.ToolCalls[i] = canonical
				} else {
					// Keep the exact invalid provider args only in private audit.
					// A canonical invalid-arguments call still gets its paired error.
					response.Message.ToolCalls[i].Function.Arguments = "{}"
				}
			}
			calls, _ := json.Marshal(response.Message.ToolCalls)
			if err := budget.save("tool_calls_accepted", map[string]any{"calls": len(response.Message.ToolCalls)}, nil, &store.Message{Role: "assistant", ToolCalls: calls, ReasoningContent: response.Message.ReasoningContent}); err != nil {
				code = "persistence_error"
				break
			}
		}
		for i, call := range response.Message.ToolCalls {
			if runCtx.Err() != nil {
				code = failureCode(runCtx.Err())
			} else if budget.exhausted() || budget.usage.ToolCalls >= limits.ToolCalls {
				code = "budget_exceeded"
			}
			if code != "" {
				code = a.skipCalls(budget, response.Message.ToolCalls[i:], code)
				break
			}
			budget.usage.ToolCalls++
			raw := json.RawMessage(call.Function.Arguments)
			var args map[string]any
			valid := len(raw) <= 32768 && json.Unmarshal(raw, &args) == nil && args != nil
			canonical, _ := json.Marshal(args)
			key := sha256.Sum256(append([]byte(call.Function.Name+":"), canonical...))
			repeats[key]++
			tool, exists := a.registry.tools[call.Function.Name]
			toolCode := ""
			var output json.RawMessage
			switch {
			case !exists:
				toolCode = "unknown_tool"
			case !valid:
				toolCode = "invalid_arguments"
			case repeats[key] > 2:
				toolCode = "repeated_tool_call"
			case tool.Validate(raw) != nil:
				toolCode = "invalid_arguments"
			default:
				// Journal before execution: a crash exposes an unfinished tool, never an
				// automatically replayable action. Registered callbacks obey their context.
				if err := budget.save("tool_started", map[string]any{"tool": call.Function.Name, "call_id": call.ID}, nil, nil); err != nil {
					code = "persistence_error"
					break
				}
				toolCtx, stop := context.WithTimeout(runCtx, limits.ToolTimeout)
				var execErr error
				toolScope := scope
				toolScope.Complete = func(ctx context.Context, request deepseek.Request) (deepseek.Response, error) {
					nestedCtx, cancel := context.WithCancel(ctx)
					stopNested := context.AfterFunc(toolCtx, cancel)
					defer stopNested()
					defer cancel()
					if toolCtx.Err() != nil {
						return deepseek.Response{}, toolCtx.Err()
					}
					return budget.complete(nestedCtx, request, false)
				}
				output, execErr = tool.Execute(toolCtx, toolScope, raw)
				if toolCtx.Err() != nil {
					execErr = toolCtx.Err()
				}
				stop()
				if execErr != nil {
					toolCode = failureCode(execErr)
					if errors.Is(execErr, context.DeadlineExceeded) && runCtx.Err() == nil {
						toolCode = "tool_timeout"
					}
				} else if !json.Valid(output) || len(output) > 128000 {
					toolCode = "invalid_tool_result"
				}
			}
			if code != "" {
				code = a.skipCalls(budget, response.Message.ToolCalls[i:], code)
				break
			}
			if toolCode != "" {
				output = errorResult(toolCode)
			}
			if err := a.toolMessage(budget, call, output, toolCode); err != nil {
				code = "persistence_error"
				break
			}
			if toolCode == "" {
				hasUsefulResult = true
				if a.memory != nil && !contentRecalled && contentQuery == "" && call.Function.Name == "analyze_paper" {
					contentQuery = a.contentQuery(runCtx, owner, output)
				}
			}
		}
		if code == "" && budget.exhausted() {
			code = "budget_exceeded"
		}
		if code != "" {
			break
		}
	}
	if runCtx.Err() != nil {
		if errors.Is(runCtx.Err(), context.Canceled) {
			status = "cancelled"
			code = "cancelled"
		} else {
			status = "failed"
			code = "budget_exceeded"
		}
	}
	if code == "budget_exceeded" && hasUsefulResult {
		status = "partial"
	}
	result.Usage = budget.usage
	if status == "completed" && finalize != nil {
		persistCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		finalErr := finalize(persistCtx, result)
		if finalErr == nil {
			saved, readErr := a.db.Run(persistCtx, owner, id)
			if readErr != nil || saved.Status == "running" || saved.Status == "queued" {
				finalErr = errors.New("finalizer did not commit terminal state")
			}
		}
		stop()
		if finalErr != nil {
			status, code = "failed", "finalization_error"
		}
	}
	return a.finish(result, status, code)
}

func (a *AgentRunner) toolMessage(b *runBudget, call deepseek.ToolCall, output json.RawMessage, code string) error {
	text := string(output)
	kind := "tool_finished"
	if code != "" {
		kind = "tool_failed"
	}
	return b.save(kind, map[string]any{"tool": call.Function.Name, "call_id": call.ID, "error_code": code}, nil, &store.Message{Role: "tool", Content: &text, ToolCallID: call.ID})
}

// skipCalls retains the stop code unless journaling a skipped call fails.
func (a *AgentRunner) skipCalls(b *runBudget, calls []deepseek.ToolCall, code string) string {
	for _, call := range calls {
		if err := a.toolMessage(b, call, errorResult(code), code); err != nil {
			return "persistence_error"
		}
	}
	return code
}
func (a *AgentRunner) finish(result Result, status, code string) (Result, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	usage, _ := json.Marshal(result.Usage)
	r, err := a.runs.Finish(ctx, result.Run.UserID, result.Run.ID, status, code, usage)
	if err != nil {
		return result, err
	}
	result.Run = r
	if r.Status != "completed" {
		code := r.ErrorCode
		if code == "" {
			code = r.Status
		}
		return result, &ToolError{Code: code}
	}
	return result, nil
}

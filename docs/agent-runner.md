# AgentRunner — T09

`internal/agent` is a handwritten Chat Completions loop, using the existing
`DeepSeekClient`, SQLite history and `research.Service`. It adds no dependencies
or agent libraries. Research tool implementations belong to T11; analysis and
reports belong to T10/T12.

## Wiring

Create one immutable `Registry` using `NewRegistry(Tool{...}, ...)`, then
`agent.New(db, sharedResearchService, deepseekClient, registry, limits)`.
`agent.DefaultLimits()` matches the durable T07 budgets; settings may lower
those limits. The effective budget is saved on the run before execution.

A tool includes its Chat Completions definition, a required validator and an
execution callback. Use `DecodeArgs` for strict object decoding, then check
required values, types, lengths, ranges and domain restrictions in the validator.
Registration validates the schema envelope, not arbitrary JSON Schema semantics.
Unknown tools, malformed arguments and validation failures become structured
`{"error":{"code":"..."}}` tool results; they never reach execution.

The server supplies `Scope.Owner`, `ConversationID` and `RunID`. Model arguments
must not supply authority. A tool must perform owner-scoped reads/writes and
check source membership; the registry cannot infer ownership from arbitrary
arguments. T11 must enforce its three-selected-paper limit in the research
adapters as well as the prompt. This node deliberately registers no shell,
arbitrary HTTP, or code-execution tools.

Tool execution is sequential, in provider order. Callbacks must honor their
context, complete nested calls before returning, and never retain the scope for
background work. A callback returns JSON, with a 128000-byte result ceiling.
Use a stable `ToolError.Code` for expected failures; other errors are sanitized.
The per-tool timeout is separate from the overall run deadline.

`Scope.Complete(ctx, deepseek.Request{...})` is the only nested-analysis model
entry point. It uses the same client, serializes requests, intersects caller,
tool and run cancellation, clamps output size and charges the same request/token
budget. Pass this function to the analyzer adapter; do not construct another
DeepSeek client or call an unbudgeted client from a tool. Shared request/token
budget exhaustion remains terminal even if a tool handles its nested-call error:
pending calls receive non-execution results and no further actions start.
Nested responses remain private events, not assistant turns in the outer
conversation.

## Execution and persistence

`Run(admissionContext, owner, conversationID, queuedRunID)` starts exactly once
through `research.Service.Start`. Only admission depends on the calling HTTP
context; execution follows the server lifetime and `Service.Cancel`. Schedule
only newly created runs. Repeated/terminal/foreign starts do not issue requests.

`AgentSession.Context` loads the owner's profile, completed prior turns and the
current turn in sequence. It verifies whole assistant/tool-call groups, retains
`reasoning_content` privately, and refuses incoherent or oversized context.
Failed, partial, cancelled and interrupted turns stay in storage but are not
replayed as successfully completed conversation. There is no compaction or
silent history truncation. The context limit is serialized message/tool bytes
(default 384000); input token estimation conservatively uses that byte count.

Before each request, an unknown-usage reservation is committed. Afterward,
usage, a private response, the optional outer assistant message and a progress
event are saved atomically. Actual tokens remain distinct from
`unknown_usage_requests` and `unknown_token_reserve`; the latter conservatively
charges input bytes plus output allowance for attempts without usage. All
requests, including bounded retries and nested analysis, share the run limits.
An interrupted process leaves a durable uncertain reservation and is recovered
as `interrupted` at startup, without paid replay.

Only retryable provider errors are retried (default one retry, at most two when
configured); Retry-After and all waits are cancellable within the run deadline.
Truncated/rejected assistant calls never execute. Original tool call IDs are
saved, including structured non-execution results for pending calls stopped by
cancellation or limits. Canonical identical arguments execute at most twice.
Malformed/unknown/blocked calls also consume the finite tool-call allowance.

Cancellation persistence uses fresh five-second contexts. Cancellation wins the
terminal state race. A limit produces `partial` only after a successful tool
result has been durably saved. Charged calls rejected by validation, failed
execution or invalid output do not count as useful work and leave `failed`.
Tools with usable partial observations should return them as successful JSON;
the registry cannot infer scientific usefulness from arbitrary tool data.
Successful final text is durable in messages.
Public events contain codes/identifiers, not provider bodies, reasoning, errors
or article text. The existing API projection continues to hide private fields.

`RunWithFinalizer(..., finalize)` is the T12 transaction boundary. The successful
callback receives the answer and usage and must atomically commit report,
citations/coverage, terminal state and event while honoring cancellation. A
callback that leaves the run active is rejected as `finalization_error`. After
that commit, the runner calls idempotent `Service.Finish` to release resources.
The default `Run` finalizes only the run and stored assistant text. It does not
claim that a research report, citations, coverage or addressability validation
has been implemented; T12 supplies those checks and the report transaction.

## Checks

```sh
python3 integration/agent.py
DEEPSEEK_MODEL=deepseek-flash DEEPSEEK_THINKING=disabled \
  python3 integration/agent.py --live \
  --env-file /home/kotdath/omp/personal/ai/paper-scout/.env
```

The executable integrates actual SQLite, lifecycle, session, registry and HTTP
client with provider-boundary fixtures. Live mode separately performs at most
three official requests, 192 output tokens each, no retries and one real local
profile tool execution. It is a harness preflight, not research/RAG/UI evidence.
All generated files/build caches stay under this worktree's `.bb/runtime/t09`.
No unit/widget tests are created.

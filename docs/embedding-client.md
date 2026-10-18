# EmbeddingClient (T06)

`internal/embeddings` uses the existing local Ollama; it never pulls or creates
models. No dependency or agent library was added. Indexing, vector persistence,
ownership and retrieval remain T08 responsibilities.

## Server use

After optional `config.Load(envFile)`, call `embeddings.ConfigFromEnv()` and
`embeddings.New(ctx, cfg)`. Construction performs GET `/api/tags`, POST
`/api/show`, GET `/api/tags`: the installed `qwen3-embedding:0.6b` must have
embedding capability, local execution, Qwen3 architecture, 1024 dimensions and
a positive context length. The SHA-256 manifest digest is pinned for the
client's lifetime. `Identity()` returns a value copy and the client can be
shared between goroutines.

Configuration (values never logged by the client):

| Variable | Default / constraint |
| --- | --- |
| `OLLAMA_BASE_URL` | `http://127.0.0.1:11434`; loopback HTTP origin only; `localhost` accepted |
| `EMBEDDING_MODEL` | `qwen3-embedding:0.6b`; other models rejected in this version |
| `EMBEDDING_DIMENSIONS` | `1024`; other sizes rejected |
| `EMBEDDING_MODEL_DIGEST` | Optional exact SHA-256 pin, hex with optional `sha256:` prefix |

`Config.Timeout` defaults to 120 seconds and can be shortened; 120 seconds is
the maximum. One deadline covers the whole construction or Embed operation,
including metadata checks and response reads. Earlier caller deadlines and
cancellation propagate through `errors.Is`. Requests bypass environment HTTP
proxies and refuse redirects. The client never logs input, provider bodies,
URLs or configuration values.

Call `Embed(ctx, embeddings.Document, texts)` with public document inputs built
using `DocumentText(title, section, chunkText)`. It produces exactly
`Title: <title>\nSection: <section>\n\n<chunkText>`, with actual newline bytes.
Document mode sends the input unchanged; do not put private notes into a shared
public document embedding.

Call `Embed(ctx, embeddings.Query, rawQuestions)` with unformatted questions.
The client prefixes each question exactly once with the following instruction
(one actual newline):

```text
Instruct: Retrieve scientific passages relevant to the research question.
Query: <raw question>
```

The instruction is applied by this client. Ollama 0.30.6's
[`EmbedHandler`](https://github.com/ollama/ollama/blob/v0.30.6/server/routes.go#L781-L911)
tokenizes and embeds request input directly, without executing the model's
chat template. Thus its installed chat template does not add the instruction
again. The document/query distinction follows the
[Qwen authors' example](https://github.com/QwenLM/Qwen3-Embedding#embedding-model).
Changing encoding behavior requires bumping `EncodingVersion` and reindexing.

## Batch and compatibility contract

Each Embed performs a digest check, one POST `/api/embed`, then another digest
check before returning vectors. It sends `truncate=false`, the model's full
context as `options.num_ctx`, and no dimensionality reduction. This follows
the [Ollama embedding API](https://docs.ollama.com/api/embed); excess context
must produce an error. There are no client retries, auto-splitting, shortened
inputs or lexical fallback. Maximums: 32 inputs, 256 KiB per encoded input,
1 MiB total encoded UTF-8 input, 8 MiB response. Empty/invalid UTF-8 inputs
are rejected before HTTP. Byte limits are resource limits, not token estimates.

Successful results contain ordered normalized float32 vectors, mode, exact
encoded-input SHA-256 hashes, prompt token count and an `Identity` containing
model, digest, dimensions and `qwen3-scientific-v1` encoding version. Every
vector must have 1024 finite numeric values and nonzero norm. Wrong response
counts, null/non-numeric/nonfinite values, zero norm, overflow, wrong model or
dimension reject the entire batch. No partial result escapes on error.

T08 must persist that identity and exact input hash, together with its own
chunking version. Compare all identity fields before using a stored vector in
search. Cache keys also include mode and input hash. A changed tag fails with
`embedding_incompatible`; intentional upgrades require reconstructing the
client and reindexing. Tags do not provide an atomic digest-bound inference
transaction: avoid replacing the Ollama model while indexing. The checks catch
observed changes around inference, but cannot detect a tag changed and restored
entirely between metadata checks.

Errors are `*embeddings.Error`: sanitized `Code`, `HTTPStatus`, `Retryable` and
`RetryAfter`. `context_limit` requires smaller inputs; `input_limit` means the
local byte cap. `embedding_incompatible` means recheck/reindex, never compare
vectors anyway. `model_unavailable`, `embedding_unavailable`,
`embedding_rate_limited` and `embedding_timeout` distinguish dependency failure.
Only transient network/429/selected 5xx failures are marked retryable; callers
own bounded retry counts and run budgets. No error implies successful hybrid
retrieval.

## Integration commands

```sh
python3 integration/embeddings.py
python3 integration/embeddings.py --live
# If shared configuration is needed, only the Go process loads its path:
python3 integration/embeddings.py --live --env-file /absolute/path/to/.env
```

The default command runs the real client against ephemeral HTTP fixture
servers for protocol/failure evidence. `--live` uses the installed local model
for two public document inputs, English/Russian questions and one oversized
input. It prints sanitized identity, token counts and cosine smoke results.
It performs no paid calls or model downloads. All caches/build outputs live
under this worktree's ignored `.bb/runtime/t06/`. Neither command creates unit
tests. Actual results and limitations are recorded in [T06 verification](verification/T06.md).

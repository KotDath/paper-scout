# Hybrid retrieval (T08)

`internal/retrieval` uses the existing `documents`, `embeddings` and SQLite
entities. Migration `T08_retrieval.sql` owns `user_papers`, `chunks_fts`,
`chunk_embeddings` and `document_indexes`; downstream nodes should reuse these
tables. No run, report, analysis or agent entity is introduced here.

## Import and ownership

Construct `retrieval.New(db.DB, embeddingClient)`. The trusted importer calls
`AddPaper(ctx, authenticatedUserID, paperVersionID)` after resolving public
metadata, then `Index(ctx, authenticatedUserID, documentID)`. Membership is
idempotent and does not change `saved`, `read` or `note`. Search arguments and
public document-cache lookups never grant membership. As specified in the data
contract, importing a source into a run creates membership; this lets the run's
owner subsequently resolve its evidence without adding a parallel ownership
entity. T11 must perform that grant from its server-owned user ID.

`Index` and `Evidence` check membership. Both search branches use the same
owner/paper scope in SQL before top-k. A supplied foreign paper ID narrows to
an empty scope, rather than granting access. Unknown and foreign evidence
produce the same 404. Notes, saved/read flags and other private data are absent
from FTS and document encoding.

## Indexing and cache

FTS is populated for existing chunks during migration and maintained with
insert/update/delete triggers, including public title updates. Changes invalidate
stored readiness; the final ready write checks exact source inputs again in the
same short transaction, so concurrent source changes cannot publish a stale index. Thus lexical
retrieval remains available when embedding inference fails.

Indexing encodes `DocumentText(title, section, text)` in batches of at most 32,
using the T06 client with `truncate=false`. It stores normalized little-endian
float32 vectors and exact input SHA-256, model ID/digest, dimensions, encoding
and chunking versions. This table is exclusively a **document-mode** cache;
query-mode vectors are not stored. Exact public inputs can reuse validated
vectors across evidence IDs with the same identity and chunking version.
There are no automatic retries, downloads of models or hidden input truncation.
Context/input errors remain explicit indexing failures; changing extraction or
chunking to resolve such an error creates a new document version.

Network calls run outside SQLite transactions. Successful batches commit
before the next batch. `document_indexes` records pending/ready/partial/failed,
counts and a sanitized error code; cancellation attempts a bounded status write
without further inference. A subsequent Index resumes from valid cached inputs.
Index calls on one service serialize with context-aware acquisition. An empty
text is `failed`, never ready. Stored vector bytes are validated again before
cache reuse and search. Model/digest, dimensions, encoding, chunking and exact
input hash must all match; equal vector length alone is insufficient.

Readiness here concerns the index. No analysis status or read coverage is
created or promoted by indexing, retrieval, or opening evidence.

## Search and HTTP

`Search(ctx, authenticatedUserID, SearchRequest)` accepts a raw question, optional
`lexical_terms` from the agent, up to 100 paper version IDs and limit 1..8
(default 8). Question and lexical terms each have a 4000 Unicode-character
limit. It returns both original strings for the harness to record in its run
trace. T08 does not create a separate conversation/event store.

The lexical branch tokenizes into at most 64 distinct quoted Unicode
letter/number terms joined by OR and binds MATCH as a SQL parameter. The dense
branch embeds the raw question with the T06 query instruction exactly once and
compares only compatible owner-scoped vectors using exact dot products. A
scope above 20000 chunks is rejected with `scope_limit`, before query inference;
it is never silently sampled. Each branch returns top-20, with ties broken by
evidence ID. RRF uses `sum(1/(60+rank))`, rank starting at one. Fusion sorts by
score then evidence ID, removes identical text within one paper version and
source ranges overlapping by at least half the shorter range within one document,
and fits at most eight whole passages in 16000 Unicode characters.

Results include `lexical_candidates`, `dense_candidates`, final `hits`, BM25,
cosine, branch ranks, RRF score, stable evidence ID, source URL, section/pages,
character offsets and extraction status. Diagnostic candidate lists are for
trace/display; the bounded LLM context is `hits`. Nearest-neighbor search has
no calibrated relevance threshold: even an unrelated query can have dense
hits. Existence or rank of an evidence ID does not prove a scientific claim.

Authenticated endpoints:

- `POST /api/v1/library/search`: the SearchRequest/Result above.
- `GET /api/v1/evidence/{id}`: a full source chunk with coordinates and URL.

`httpapi.New(db, service)` wires the routes; `New(db)` retains compatibility
with earlier callers and explicitly provides lexical-only retrieval. Composed
servers can call `RegisterRetrieval` once instead. No indexing HTTP endpoint is
exposed; the importer/tool layer owns it.

`retrieval_mode=hybrid` means query inference succeeded and every scoped chunk
has a compatible valid vector; `hybrid_partial` reports missing/incompatible
vectors with `index_incomplete_or_incompatible`. Query/backend failure produces
`lexical_only` with the sanitized embedding reason. `index_coverage` is the
fraction of chunks with compatible valid document vectors, independently of
query inference success. Without a model identity (nil client), coverage is
unknown and reported conservatively as zero. Empty scopes return empty lists
and coverage zero. Degraded output is never labeled full hybrid.

The server constructs its client in a bounded startup preflight. If it fails,
search starts in lexical-only mode and needs a server restart to retry client
construction. With a constructed client, a transient query outage is retried
naturally by the next explicit search. Intentional model changes require a new
client and reindexing. Search responses have up to a 120-second embedding
operation; the server write timeout is 130 seconds.

## Reproduce the live integration check

```sh
python3 integration/retrieval.py --artifact "$PWD/.bb/runtime/t08/retrieval-evaluation.json"
# Optional shared configuration is loaded by the Go process only:
python3 integration/retrieval.py --env-file /absolute/path/to/.env
```

This command requires the existing real Qwen3 model in local Ollama. There is
no offline success mode or generated-vector substitute. It uses ephemeral HTTP
ports, disposable SQLite and safe generated accounts. Its labeled HTML corpus
and catalog rows are synthetic fixtures; production extraction, auth, indexing,
embedding inference, SQL, fusion and HTTP handlers run unchanged. A loopback
proxy forwards healthy requests to real Ollama, and injects 503 only for the
outage cases without stopping the shared service. Build/cache/temp files stay
in this worktree under ignored `.bb/runtime/t08`.

The JSON artifact contains questions and branch/fusion ranks with evidence IDs,
not credentials or model reasoning. Actual results and limitations are recorded
in [verification/T08.md](verification/T08.md). Live arXiv acquisition, DeepSeek
answer citations and device UI belong to the later DAG nodes.

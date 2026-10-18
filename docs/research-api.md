# Research API T12

Production entrypoint `cmd/paper-scout` composes the existing shared arXiv,
Documents, EmbeddingClient/Retrieval, Analyzer, DeepSeekClient, registry and
AgentRunner. `researchapp` attaches their worker to `research.Service` before
HTTP begins listening. There is one provider client and one agent loop.

## Admission and polling

All routes use the existing bearer authentication and owner derived from the
session. POST a conversation `{ "title": "Memory research" }`, then POST
`/api/v1/conversations/{id}/runs` with `message` and `client_request_id`.
The response remains `{run_id,status,progress_url}` with HTTP 202. Run, user
message and queue event commit before a worker starts. Only a newly created
run starts a worker: retries return the same ID, including after restart.
The worker uses server lifetime, not the request context. SQLite retains the
one-active-run-per-conversation and two-active-runs-per-server limits.

`GET /api/v1/runs/{id}` returns the existing Run fields, including usage,
budget, error_code and nullable report_id, plus `sources`. Each source has:

- `paper`: owner-scoped public metadata, canonical URLs and `documents`;
- `selection_reason`, `analysis_status`;
- `coverage`: analysis ID, originating run ID, document ID, analysis_status,
  covered_runes, total_runes, successful_ranges (Unicode offsets) and error_code.

A document has extraction_status, warnings, index_status, indexed/total chunks
and index_error. These axes remain independent. Pending/interrupted analysis
exposes successful persisted reading intervals without claiming complete.
No available analysis in this conversation yields `none` and an empty coverage array.

`GET /api/v1/runs/{id}/events?after_seq=0&limit=50` exposes existing increasing
public events, next_after_seq and has_more. Progress comes from real tool,
index and Analyzer events. No model progress percentage is invented.
Cancellation uses the existing empty-body POST `/api/v1/runs/{id}/cancel`.
A terminal cancellation request preserves the completed report.

## Reports

`GET /api/v1/reports/{id}` returns:

```json
{
  "id": "report_example",
  "run_id": "run_example",
  "markdown": "Russian final answer with evidence IDs",
  "citations": [],
  "sources": [],
  "coverage": {},
  "limitations": ["no_addressable_citations"],
  "created_at": "2026-10-18T00:00:00Z"
}
```

IDs and time above illustrate the shape. Citations are existing Retrieval Hit
objects: evidence_id, original text, paper/document ID, coordinates, section,
source_url and extraction_status. The server resolves IDs appearing in the
final answer through the owner's evidence API. Missing/foreign citations are
excluded and recorded as `unresolved_citation: ID`; an answer without any
resolvable citation has `no_addressable_citations`. Resolution checks access
and addressability, not scientific entailment or independent replication.

Sources include this run's selections and papers cited from the user's
library. For follow-up, prior completed runs in the same conversation may
supply analysis coverage; its originating run_id remains explicit. A citation
from another conversation has no implied reading coverage here. Coverage is
also keyed by paper ID at the top level. Extraction warnings, incomplete
analysis, unready indexes and degraded retrieval are persisted in limitations.

`T12_reports.sql` creates the sole reports table. Report markdown, citation and
source/coverage snapshots, usage, report_id, report_saved and run_finished
commit in one SQLite transaction. A committed cancel request prevents report
creation. A report insert failure rolls back the link/events and the runner
records failed/finalization_error. Failed/cancelled/interrupted runs, and budget
stops without a final answer, have no report; persisted source coverage and
usage remain available through polling. Partial or abstract-only source
analysis may be part of a completed answer, with explicit limitations.

Snapshots are immutable; subsequent saved/read/note changes are read through
the library/paper routes. Provider messages, private events, reasoning and local
cache paths are absent from these public projections. Startup recovers active
rows as interrupted, without paid replay. Shutdown cancels and joins attached
workers before closing SQLite. Missing provider configuration keeps persisted
reads available and new admitted runs fail with provider_unavailable.

## Library

- `GET /api/v1/library?cursor=...&limit=50` returns `{items,next_cursor}`,
  sorted by opaque paper_version_id. Limit is 1–100; empty cursor ends paging.
  Membership includes imported papers; `saved` is the user's explicit bookmark.
- `GET /api/v1/papers/{paper_version_id}` returns public metadata, current
  document extraction/indexing details and this owner's saved/read/note.
- `PATCH /api/v1/library/{paper_version_id}` accepts any nonempty subset of
  `{saved: bool, read: bool, note: string}`. Note is at most 10000 Unicode
  characters; empty string clears it. Omitted values remain unchanged.
  This route updates existing membership and cannot grant access by guessed ID.
- Existing `POST /api/v1/library/search` and `GET /api/v1/evidence/{id}` retain
  their hybrid retrieval and owner checks.

Unknown body/query fields are rejected. Foreign report/paper/evidence/run IDs
return the same 404 as missing IDs. Personal notes never enter the public
source text or embedding cache.

## Reproducible checks

```sh
GOPROXY=off python3 integration/researchapi.py \
  --artifact "$PWD/.bb/runtime/t12/fixture-result.json"
DEEPSEEK_MODEL=deepseek-flash DEEPSEEK_THINKING=disabled GOPROXY=off \
  python3 integration/researchapi.py --live \
  --env-file /home/kotdath/omp/personal/ai/paper-scout/.env \
  --artifact "$PWD/.bb/runtime/t12/live-result.json"
```

The launcher shares T11's HTTP source/provider fixtures instead of duplicating
its modules. Default uses real Ollama, real SQLite, real HTTP auth and all
production research modules; external arXiv/document/DeepSeek boundaries are
fixtures. It also launches the real production executable and restarts its OS
process against the same DB, with a provider fixture that explicitly does no
source research. Live uses official arXiv/DeepSeek and real Qwen3. Both primary
and follow-up are admitted/polled/read through HTTP. Strict live requires
complete analysis; no allow-partial escape is supported in API mode.
Each live run is capped at 12 requests, 180000 tokens and 2048 output tokens per
request; Analyzer additionally caps its work at 6 requests. Caches/builds,
listener ports, databases and downloaded files stay in this worktree's ignored
runtime area; temporary DB/source files are removed after each run. Dotenv is
loaded only by the Go process. Actual evidence: [verification/T12.md](verification/T12.md).

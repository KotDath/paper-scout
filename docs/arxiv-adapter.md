# arXiv adapter (T04)

`internal/arxiv.New(db.DB, "")` uses the application's migrated SQLite store
and `https://export.arxiv.org/api/query`. Instantiate once in the server when
T11 wires research tools. T04 does not add a public search route, user library
membership, document downloads, extraction or analysis coverage.

```go
client, err := arxiv.New(db.DB, "")
// Handle err before calling the client.
result, err := client.Search(ctx, arxiv.SearchRequest{
    Query: `all:memory AND all:agents`, Limit: 3,
})
// Resolve optional version through arXiv; result.Papers contains one version.
resolved, err := client.Get(ctx, "arXiv:2310.08560")
```

Search accepts an arXiv query expression, at most 10 results, and optional
inclusive UTC dates (`From`/`To`, `YYYY-MM-DD`). There is no implicit period.
Parameters are URL encoded. Get accepts bare IDs, an `arXiv:` prefix, or official
abs/html/pdf URLs; it never requests a caller-supplied URL. Modern and legacy
IDs retain an explicit version in persisted metadata. Versionless requests
resolve to the actual version in the Atom ID or matching versioned links;
missing/conflicting versions fail rather than silently assuming v1.
Legacy `math`, `cs`, `nlin` and `q-bio` identifiers drop redundant subject-class
suffixes before lookup or metadata comparison: `math.DG/0211159v1` becomes
`math/0211159v1`. Bare IDs, prefixes and URLs therefore share the same canonical
request cache key and persisted source identity. Other archives do not accept
subject-class suffixes. This follows the
[interacting-services identifier contract](https://info.arxiv.org/help/arxiv_identifier_for_services.html).

`paper_versions` is the shared public metadata table, owned by migration
`T04_arxiv.sql`. Its unique `(arxiv_id, version)` and deterministic `pv_` ID
allow later documents and user membership tables to reference the same source.
Repeat discovery upserts metadata without changing the ID. Different versions
remain distinct. Original links are canonical HTTPS abs/html/pdf links derived
from the checked ID. HTMLURL is a candidate location, not proof HTML exists.

The query cache and metadata upserts commit together. Cache keys include the
endpoint and canonical encoded parameters. Successful results (including empty
results) persist for 24 hours; failures do not. Expired entries refresh and
upsert existing sources. Get returns `ErrNotFound` for an empty result. Search
and Get use separate request cache keys, so an exact Get after discovery may
make another network call without creating another source row.

`Result.CacheHit`, `FetchedAt` and `Attempts` expose provenance and actual
network attempts. Cache hits have zero attempts; callers must account for all
attempts, including errors. `Error` carries sanitized provider status, code and
retryability; context errors remain recognizable through `errors.Is`.

Every client in one Go process shares a sequential gate. A response body is
consumed/closed before release, with at least three seconds after completion
before the next call. Concurrent identical misses recheck the persistent cache
under that gate. The limiter covers retries too. There are at most three
attempts on transport failures, 429 and 5xx; `Retry-After` applies to the shared
cooldown. A delay over 30 seconds returns the provider error immediately and
keeps the cooldown for subsequent uncached calls. Each HTTP call times out in
20 seconds, the entire adapter operation in 90 seconds, both bounded further
by caller cancellation. Response bodies are limited to 2 MiB. Redirects are
rejected before following them.

The limiter is process-local, as required by the single-process server design.
Do not run parallel production server instances against arXiv. The independent
T05 downloader must apply the same source restrictions for its own requests;
the metadata adapter does not provide arbitrary HTTP transport access.

The optional endpoint argument allows official HTTPS API endpoints and literal
loopback HTTP fixtures only. It is server configuration, never tool input.
Fixtures use ephemeral ports and synthetic Atom, and do not establish live
availability.

For checks, keep all Go caches and build output inside this worktree:

```sh
mkdir -p .bb/runtime/t04/{gocache,gomod,gopath,tmp}
export GOCACHE="$PWD/.bb/runtime/t04/gocache"
export GOMODCACHE="$PWD/.bb/runtime/t04/gomod"
export GOPATH="$PWD/.bb/runtime/t04/gopath"
export TMPDIR="$PWD/.bb/runtime/t04/tmp"
go build -o .bb/runtime/t04/arxiv-check ./integration/arxiv
.bb/runtime/t04/arxiv-check
# Explicit live mode, no credentials or paid calls:
.bb/runtime/t04/arxiv-check -live -db .bb/runtime/t04/live.sqlite
# A second process must reuse persisted results:
.bb/runtime/t04/arxiv-check -live -db .bb/runtime/t04/live.sqlite -expect-cache
```

The live command searches `ti:MemGPT`, resolves a discovered exact version,
repeats through its canonical URL and verifies one SQLite source row. Use a
fresh temporary DB for a new network proof. Public Atom metadata and local
runtime evidence remain outside Git; the sanitized outcome is recorded in
`docs/verification/T04.md`.

Official contracts checked for this implementation:
[Atom API manual](https://info.arxiv.org/help/api/user-manual.html),
[identifier/version rules](https://info.arxiv.org/help/arxiv_identifier.html),
[API rate limits](https://info.arxiv.org/help/api/tou.html).

-- Run-scoped selection is the access grant used by analysis and later tools.
CREATE TABLE run_sources (
    run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    paper_version_id TEXT NOT NULL REFERENCES paper_versions(id),
    selection_reason TEXT NOT NULL,
    analysis_status TEXT NOT NULL DEFAULT 'none'
        CHECK(analysis_status IN ('none','partial','complete','abstract_only')),
    PRIMARY KEY(run_id, paper_version_id)
);
CREATE TABLE paper_analyses (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    document_id TEXT NOT NULL REFERENCES documents(id),
    question TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'none'
        CHECK(status IN ('none','partial','complete','abstract_only')),
    result_json TEXT CHECK(result_json IS NULL OR json_valid(result_json)),
    error_code TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    finished_at TEXT
);
CREATE INDEX paper_analyses_run ON paper_analyses(run_id, document_id);
CREATE UNIQUE INDEX paper_analyses_active ON paper_analyses(run_id, document_id)
    WHERE finished_at IS NULL;
-- Every provider invocation, including synthesis and failures, is durable.
-- NULL usage means unknown, never zero cost. Ranges use Unicode code points.
CREATE TABLE analysis_segments (
    id TEXT PRIMARY KEY,
    analysis_id TEXT NOT NULL REFERENCES paper_analyses(id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL,
    kind TEXT NOT NULL CHECK(kind IN ('reading','synthesis')),
    char_start INTEGER NOT NULL CHECK(char_start >= 0),
    char_end INTEGER NOT NULL CHECK(char_end >= char_start),
    input_hash TEXT NOT NULL,
    input_json TEXT NOT NULL CHECK(json_valid(input_json)),
    evidence_ids_json TEXT NOT NULL CHECK(json_valid(evidence_ids_json)),
    status TEXT NOT NULL CHECK(status IN ('pending','succeeded','failed')),
    attempted INTEGER NOT NULL DEFAULT 0 CHECK(attempted IN (0,1)),
    analysis_json TEXT CHECK(analysis_json IS NULL OR json_valid(analysis_json)),
    usage_json TEXT CHECK(usage_json IS NULL OR json_valid(usage_json)),
    error_code TEXT NOT NULL DEFAULT '',
    UNIQUE(analysis_id, ordinal)
);

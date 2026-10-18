-- paper_versions belongs to T04. SQLite permits this reference before T04 is
-- merged; ingestion needs a registered paper version, never a duplicate entity.
CREATE TABLE documents (
    id TEXT PRIMARY KEY,
    paper_version_id TEXT NOT NULL REFERENCES paper_versions(id),
    format TEXT NOT NULL CHECK (format IN ('html', 'pdf', 'abstract')),
    content_hash TEXT NOT NULL,
    extractor_version TEXT NOT NULL,
    chunking_version TEXT NOT NULL,
    extraction_status TEXT NOT NULL CHECK (extraction_status IN ('full_text', 'abstract_only', 'failed')),
    source_url TEXT NOT NULL,
    local_path TEXT NOT NULL,
    text TEXT NOT NULL,
    segments_json TEXT NOT NULL CHECK (json_valid(segments_json)),
    warnings_json TEXT NOT NULL CHECK (json_valid(warnings_json)),
    created_at TEXT NOT NULL,
    UNIQUE (paper_version_id, format, content_hash, extractor_version, chunking_version),
    CHECK (extraction_status != 'full_text' OR length(trim(text)) > 0)
);
CREATE INDEX documents_paper ON documents(paper_version_id, created_at);
CREATE TABLE chunks (
    id TEXT PRIMARY KEY,
    document_id TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    text TEXT NOT NULL CHECK (length(text) > 0),
    char_start INTEGER NOT NULL CHECK (char_start >= 0),
    char_end INTEGER NOT NULL CHECK (char_end > char_start),
    page_start INTEGER,
    page_end INTEGER,
    section TEXT NOT NULL DEFAULT '',
    UNIQUE (document_id, ordinal)
);

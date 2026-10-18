-- Public versioned metadata only; user membership is owned by later nodes.
CREATE TABLE paper_versions (
    id TEXT PRIMARY KEY,
    arxiv_id TEXT NOT NULL,
    version INTEGER NOT NULL CHECK (version > 0),
    title TEXT NOT NULL,
    authors_json TEXT NOT NULL,
    abstract TEXT NOT NULL,
    abstract_url TEXT NOT NULL,
    html_url TEXT NOT NULL,
    pdf_url TEXT NOT NULL,
    published_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    fetched_at TEXT NOT NULL,
    UNIQUE (arxiv_id, version)
);

CREATE TABLE arxiv_query_cache (
    cache_key TEXT PRIMARY KEY,
    result_json TEXT NOT NULL,
    fetched_at TEXT NOT NULL,
    expires_at INTEGER NOT NULL
);

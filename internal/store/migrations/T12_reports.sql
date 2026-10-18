CREATE UNIQUE INDEX t12_runs_owner ON runs(id,user_id);
CREATE TABLE reports (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    run_id TEXT NOT NULL UNIQUE,
    markdown TEXT NOT NULL CHECK(length(trim(markdown)) > 0),
    citations_json TEXT NOT NULL CHECK(json_valid(citations_json)),
    sources_json TEXT NOT NULL CHECK(json_valid(sources_json)),
    coverage_json TEXT NOT NULL CHECK(json_valid(coverage_json)),
    limitations_json TEXT NOT NULL CHECK(json_valid(limitations_json)),
    created_at TEXT NOT NULL,
    FOREIGN KEY(run_id,user_id) REFERENCES runs(id,user_id) ON DELETE CASCADE
);
CREATE INDEX t12_reports_owner ON reports(user_id,id);

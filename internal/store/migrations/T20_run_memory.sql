CREATE TABLE run_memory_snapshots (
 run_id TEXT NOT NULL REFERENCES runs(id),
 ordinal INTEGER NOT NULL CHECK(ordinal IN (0,1)),
 snapshot_json TEXT NOT NULL CHECK(json_valid(snapshot_json)),
 PRIMARY KEY(run_id,ordinal)
);
ALTER TABLE reports ADD COLUMN research_memory_json TEXT CHECK(research_memory_json IS NULL OR json_valid(research_memory_json));

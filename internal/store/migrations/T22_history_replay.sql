-- Private provenance for accepted historical context; immutable Markdown stays intact.
ALTER TABLE reports ADD COLUMN history_replay_json TEXT;

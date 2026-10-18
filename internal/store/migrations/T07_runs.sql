CREATE TABLE conversations (
    id TEXT PRIMARY KEY NOT NULL,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE(id, user_id)
);
CREATE INDEX conversations_owner ON conversations(user_id, id);

CREATE TABLE runs (
    id TEXT PRIMARY KEY NOT NULL,
    user_id TEXT NOT NULL,
    conversation_id TEXT NOT NULL,
    client_request_id TEXT NOT NULL,
    request_message TEXT NOT NULL,
    status TEXT NOT NULL CHECK(status IN ('queued','running','completed','partial','failed','cancelled','interrupted')),
    cancel_requested INTEGER NOT NULL DEFAULT 0 CHECK(cancel_requested IN (0,1)),
    created_at TEXT NOT NULL,
    started_at TEXT,
    finished_at TEXT,
    error_code TEXT NOT NULL DEFAULT '',
    budget_json TEXT NOT NULL CHECK(json_valid(budget_json)),
    usage_json TEXT NOT NULL DEFAULT '{}' CHECK(json_valid(usage_json)),
    report_id TEXT,
    FOREIGN KEY(conversation_id, user_id) REFERENCES conversations(id, user_id) ON DELETE CASCADE,
    UNIQUE(user_id, conversation_id, client_request_id),
    UNIQUE(id, conversation_id, user_id)
);
CREATE UNIQUE INDEX runs_one_active ON runs(conversation_id)
    WHERE status IN ('queued','running');
CREATE INDEX runs_owner ON runs(user_id, conversation_id, created_at);

CREATE TABLE messages (
    id TEXT PRIMARY KEY NOT NULL,
    user_id TEXT NOT NULL,
    conversation_id TEXT NOT NULL,
    run_id TEXT NOT NULL,
    seq INTEGER NOT NULL CHECK(seq > 0),
    role TEXT NOT NULL CHECK(role IN ('system','user','assistant','tool')),
    content TEXT,
    reasoning_content TEXT,
    tool_calls_json TEXT NOT NULL DEFAULT '[]' CHECK(json_valid(tool_calls_json)),
    tool_call_id TEXT NOT NULL DEFAULT '',
    provider_json TEXT NOT NULL DEFAULT '{}' CHECK(json_valid(provider_json)),
    created_at TEXT NOT NULL,
    FOREIGN KEY(run_id, conversation_id, user_id) REFERENCES runs(id, conversation_id, user_id) ON DELETE CASCADE,
    UNIQUE(conversation_id, seq)
);

CREATE TABLE run_events (
    run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    seq INTEGER NOT NULL CHECK(seq > 0),
    event_type TEXT NOT NULL,
    public_payload_json TEXT NOT NULL CHECK(json_valid(public_payload_json)),
    private_payload_json TEXT NOT NULL DEFAULT '{}' CHECK(json_valid(private_payload_json)),
    created_at TEXT NOT NULL,
    PRIMARY KEY(run_id, seq)
);

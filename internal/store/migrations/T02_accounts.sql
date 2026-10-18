CREATE TABLE users (
    id TEXT PRIMARY KEY NOT NULL,
    login TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE auth_sessions (
    token_hash TEXT PRIMARY KEY NOT NULL CHECK (length(token_hash) = 64),
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at TEXT NOT NULL,
    revoked_at TEXT
);
CREATE INDEX auth_sessions_user ON auth_sessions(user_id);

CREATE TABLE profiles (
    user_id TEXT PRIMARY KEY NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    interests TEXT NOT NULL DEFAULT '',
    preferred_language TEXT NOT NULL DEFAULT 'ru',
    updated_at TEXT NOT NULL
);

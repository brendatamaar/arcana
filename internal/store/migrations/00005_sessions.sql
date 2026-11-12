-- +goose Up
CREATE TABLE sessions (
 id TEXT PRIMARY KEY, key_id TEXT NOT NULL REFERENCES api_keys(id),
 token_hash BLOB NOT NULL UNIQUE, created_at TIMESTAMP NOT NULL,
 expires_at TIMESTAMP NOT NULL, last_seen_at TIMESTAMP
);
CREATE INDEX sessions_key ON sessions(key_id, created_at);
CREATE INDEX sessions_expiry ON sessions(expires_at);
-- Exec history is listed newest first, per sandbox.
CREATE INDEX execs_sandbox_started ON execs(sandbox_id, started_at DESC, id DESC);
CREATE INDEX sandboxes_ended ON sandboxes(ended_at DESC) WHERE ended_at IS NOT NULL;

-- +goose Down
DROP INDEX sandboxes_ended;
DROP INDEX execs_sandbox_started;
DROP TABLE sessions;

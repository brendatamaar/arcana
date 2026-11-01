-- +goose Up
CREATE TABLE api_keys (
 id TEXT PRIMARY KEY NOT NULL, name TEXT NOT NULL, prefix TEXT NOT NULL,
 hash BLOB NOT NULL UNIQUE, is_admin INTEGER NOT NULL DEFAULT 0 CHECK (is_admin IN (0,1)),
 created_at TIMESTAMP NOT NULL, last_used_at TIMESTAMP, revoked_at TIMESTAMP
);
CREATE TABLE sandboxes (
 id TEXT PRIMARY KEY NOT NULL, owner_key_id TEXT NOT NULL REFERENCES api_keys(id),
 template TEXT NOT NULL, runtime TEXT NOT NULL, state TEXT NOT NULL,
 container_id TEXT, source TEXT NOT NULL,
 cpus REAL, memory_mb INTEGER, disk_mb INTEGER, pids INTEGER,
 network_mode TEXT NOT NULL, network_allow TEXT,
 env_keys TEXT, labels TEXT, from_snapshot_id TEXT,
 created_at TIMESTAMP NOT NULL, expires_at TIMESTAMP NOT NULL,
 idle_timeout_seconds INTEGER NOT NULL, last_activity_at TIMESTAMP NOT NULL,
 ended_at TIMESTAMP, end_reason TEXT
);
CREATE INDEX sandboxes_owner_state ON sandboxes(owner_key_id,state,id);
CREATE TABLE execs (
 id TEXT PRIMARY KEY NOT NULL, sandbox_id TEXT NOT NULL REFERENCES sandboxes(id),
 command TEXT NOT NULL, is_shell INTEGER NOT NULL, cwd TEXT, tty INTEGER NOT NULL DEFAULT 0,
 started_at TIMESTAMP NOT NULL, ended_at TIMESTAMP,
 exit_code INTEGER, timed_out INTEGER, cancelled INTEGER, truncated INTEGER,
 stdout_bytes INTEGER, stderr_bytes INTEGER
);
CREATE INDEX execs_sandbox_id ON execs(sandbox_id,id);
CREATE TABLE meta (key TEXT PRIMARY KEY NOT NULL, value TEXT NOT NULL);

-- +goose Down
DROP TABLE meta;
DROP TABLE execs;
DROP TABLE sandboxes;
DROP TABLE api_keys;

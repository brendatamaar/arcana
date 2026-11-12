-- +goose Up
CREATE TABLE snapshots (
 id TEXT PRIMARY KEY, owner_key_id TEXT NOT NULL REFERENCES api_keys(id),
 source_sandbox_id TEXT NOT NULL, template TEXT NOT NULL,
 spec TEXT NOT NULL,                 -- JSON: resources, env keys (never values), network
 path TEXT NOT NULL,                 -- file name under data_dir/snapshots
 size_bytes INTEGER NOT NULL, sha256 TEXT NOT NULL, created_at TIMESTAMP NOT NULL
);
CREATE INDEX snapshots_owner ON snapshots(owner_key_id, id);

-- +goose Down
DROP TABLE snapshots;

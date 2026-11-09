-- +goose Up
CREATE TABLE audit_events (
 id TEXT PRIMARY KEY, ts TIMESTAMP NOT NULL,
 key_id TEXT NOT NULL DEFAULT '', sandbox_id TEXT NOT NULL DEFAULT '',
 action TEXT NOT NULL, details TEXT NOT NULL
);
CREATE INDEX audit_events_time ON audit_events(ts,id);
CREATE INDEX audit_events_sandbox ON audit_events(sandbox_id,ts,id);
CREATE INDEX audit_events_key ON audit_events(key_id,ts,id);
CREATE INDEX audit_events_action ON audit_events(action,ts,id);

-- +goose Down
DROP TABLE audit_events;

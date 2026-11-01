-- +goose Up
CREATE TABLE sandbox_transitions (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT,
 sandbox_id TEXT NOT NULL REFERENCES sandboxes(id),
 state TEXT NOT NULL, at TIMESTAMP NOT NULL, reason TEXT
);
CREATE INDEX sandbox_transitions_sandbox ON sandbox_transitions(sandbox_id,sequence);
INSERT INTO sandbox_transitions(sandbox_id,state,at,reason)
SELECT id,state,COALESCE(ended_at,created_at),end_reason FROM sandboxes;

-- +goose Down
DROP TABLE sandbox_transitions;

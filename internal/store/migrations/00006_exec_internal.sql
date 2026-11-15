-- +goose Up
-- Workspace scans and file transfers run through the exec path and stay recorded (FR-EX-8), but they
-- are not commands a user ran. The flag keeps them out of the user-facing exec history.
ALTER TABLE execs ADD COLUMN internal INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE execs DROP COLUMN internal;

# Audit log

Arcana records metadata for key creation/revocation, sandbox lifecycle, exec
start/end, file writes/deletes, archive uploads and allowlist proxy decisions.
Snapshot recording is added with the snapshot feature in Phase 3. File and
archive bodies, exec output, environment values and API credentials are never
included. API-key-shaped strings are redacted even in command metadata. Command argv or the caller's explicit shell string is recorded, as
required for command auditing; callers should pass credentials through env
rather than command arguments.

Events enter a nonblocking 256-entry queue. A single joined worker persists
them to SQLite; overflow, invalid/oversized metadata and storage errors are
counted for metrics. Events are not guaranteed durable before enqueue returns;
a crash can lose queued events. The default retention is 30 days, configured
by audit.retention_days (1..3650), with pruning at startup and daily. Graceful shutdown drains
within the shutdown budget and counts records dropped after cancellation.

GET /v1/audit requires an admin API key. Filters: sandbox_id, key_id, action,
since and until (RFC3339 timestamps). Pages are ordered by timestamp and ID;
limit is 1..1000 and next_cursor is opaque. Non-admin keys receive 403, and
unauthenticated requests receive 401. The Go client exposes ListAudit.

File writes and archives report transferred bytes. Deletes report the
sandbox-local entry size observed just before removal; symlinks are measured
without following them, and directory size is the entry size, not recursive
content size. Sandbox code can race metadata observations within its own
filesystem. Host workspace files are never opened.

key_id identifies the initiating API key when available. Internal lifecycle
and egress events use the sandbox owner; local key CLI/bootstrap events have
no API actor. Retained sandbox records preserve egress owner attribution.

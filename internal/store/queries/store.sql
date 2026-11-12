-- name: GetMeta :one
SELECT value FROM meta WHERE key = ?;

-- name: SetMetaIfAbsent :exec
INSERT INTO meta (key,value) VALUES (?,?) ON CONFLICT(key) DO NOTHING;

-- name: CreateKey :one
INSERT INTO api_keys (id,name,prefix,hash,is_admin,created_at) VALUES (?,?,?,?,?,?) RETURNING *;

-- name: GetKey :one
SELECT * FROM api_keys WHERE id = ?;

-- name: GetKeyByHash :one
SELECT * FROM api_keys WHERE hash = ?;

-- name: ListKeys :many
SELECT * FROM api_keys WHERE id > ? ORDER BY id LIMIT MIN(MAX(CAST(sqlc.arg(page_limit) AS INTEGER), 1), 1000);

-- name: RevokeKey :execrows
UPDATE api_keys SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL;

-- name: TouchKey :exec
UPDATE api_keys SET last_used_at = ? WHERE id = ? AND revoked_at IS NULL;

-- name: CountKeys :one
SELECT count(*) FROM api_keys;

-- name: CreateSandbox :one
INSERT INTO sandboxes (id,owner_key_id,template,runtime,state,container_id,source,cpus,memory_mb,disk_mb,pids,network_mode,network_allow,env_keys,labels,from_snapshot_id,created_at,expires_at,idle_timeout_seconds,last_activity_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) RETURNING *;

-- name: GetSandbox :one
SELECT * FROM sandboxes WHERE id = ?;

-- name: ListSandboxes :many
SELECT * FROM sandboxes WHERE id > ? AND (sqlc.arg(owner) = '' OR owner_key_id = sqlc.arg(owner)) AND (sqlc.arg(state) = '' OR state = sqlc.arg(state))
AND (sqlc.arg(label_key) = '' OR labels ->> sqlc.arg(label_key) = sqlc.arg(label_value))
ORDER BY id LIMIT MIN(MAX(CAST(sqlc.arg(page_limit) AS INTEGER), 1), 1000);

-- name: RecordSandboxTransition :exec
INSERT INTO sandbox_transitions(sandbox_id,state,at,reason) VALUES (?,?,?,?);

-- name: CountActiveSandboxes :one
SELECT count(*) FROM sandboxes WHERE state IN ('creating','running','destroying') AND (sqlc.arg(owner) = '' OR owner_key_id = sqlc.arg(owner));

-- name: UpdateSandboxState :execrows
UPDATE sandboxes SET state = ?, container_id = ?, ended_at = ?, end_reason = ? WHERE id = ? AND state = sqlc.arg(previous_state);

-- name: TouchSandbox :execrows
UPDATE sandboxes SET last_activity_at = MAX(last_activity_at,sqlc.arg(last_activity_at)) WHERE id = sqlc.arg(id) AND state = 'running';

-- name: CreateExec :one
INSERT INTO execs (id,sandbox_id,command,is_shell,cwd,tty,started_at) VALUES (?,?,?,?,?,?,?) RETURNING *;

-- name: GetExec :one
SELECT * FROM execs WHERE id = ?;

-- name: FinishExec :execrows
UPDATE execs SET ended_at = ?,exit_code = ?,timed_out = ?,cancelled = ?,truncated = ?,stdout_bytes = ?,stderr_bytes = ? WHERE id = ? AND ended_at IS NULL;

-- name: ListExecs :many
SELECT * FROM execs WHERE sandbox_id = ? AND id > ? ORDER BY id LIMIT MIN(MAX(CAST(sqlc.arg(page_limit) AS INTEGER), 1), 1000);

-- name: ListActiveSandboxes :many
SELECT * FROM sandboxes WHERE id > ? AND state IN ('creating','running','destroying') ORDER BY id LIMIT 1000;

-- name: HasUnfinishedExec :one
SELECT EXISTS(SELECT 1 FROM execs WHERE sandbox_id = ? AND ended_at IS NULL);

-- name: FailUnfinishedExecs :exec
UPDATE execs SET ended_at = ?, cancelled = 1 WHERE sandbox_id = ? AND ended_at IS NULL;

-- name: CreateSnapshot :one
INSERT INTO snapshots (id,owner_key_id,source_sandbox_id,template,spec,path,size_bytes,sha256,created_at) VALUES (?,?,?,?,?,?,?,?,?) RETURNING *;

-- name: GetSnapshot :one
SELECT * FROM snapshots WHERE id = ?;

-- name: ListSnapshots :many
SELECT * FROM snapshots WHERE id > ? AND (sqlc.arg(owner) = '' OR owner_key_id = sqlc.arg(owner))
ORDER BY id LIMIT MIN(MAX(CAST(sqlc.arg(page_limit) AS INTEGER), 1), 1000);

-- name: DeleteSnapshot :execrows
DELETE FROM snapshots WHERE id = ?;

-- name: CountSnapshots :one
SELECT count(*) FROM snapshots WHERE sqlc.arg(owner) = '' OR owner_key_id = sqlc.arg(owner);

-- name: SumSnapshotBytes :one
SELECT CAST(COALESCE(SUM(size_bytes),0) AS INTEGER) FROM snapshots;

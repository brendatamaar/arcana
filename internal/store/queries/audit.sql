-- name: AppendAuditEvent :exec
INSERT INTO audit_events(id,ts,key_id,sandbox_id,action,details) VALUES(?,?,?,?,?,?);

-- name: ListAuditEvents :many
SELECT * FROM audit_events
WHERE (sqlc.arg(sandbox_filter) = '' OR sandbox_id = sqlc.arg(sandbox_filter))
 AND (sqlc.arg(key_filter) = '' OR key_id = sqlc.arg(key_filter))
 AND (sqlc.arg(action_filter) = '' OR action = sqlc.arg(action_filter))
 AND ts >= sqlc.arg(since_ts) AND ts <= sqlc.arg(until_ts)
 AND (ts > sqlc.arg(after_ts) OR (ts = sqlc.arg(after_ts) AND id > sqlc.arg(after_id)))
ORDER BY ts,id LIMIT MIN(MAX(CAST(sqlc.arg(page_limit) AS INTEGER),1),1000);

-- name: PruneAuditEvents :execrows
DELETE FROM audit_events WHERE ts < ?;

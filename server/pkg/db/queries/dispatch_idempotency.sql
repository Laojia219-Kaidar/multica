-- name: InsertDispatchIdempotency :one
INSERT INTO dispatch_idempotency (workspace_id, idempotency_key, request_digest, task_ids)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetDispatchIdempotency :one
SELECT * FROM dispatch_idempotency
WHERE workspace_id = $1 AND idempotency_key = $2;

-- name: PruneDispatchIdempotency :exec
DELETE FROM dispatch_idempotency
WHERE workspace_id = $1 AND created_at < $2;

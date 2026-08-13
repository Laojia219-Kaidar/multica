-- name: GetProjectLifecycleReceipt :one
SELECT id, workspace_id, project_id, action, idempotency_key, payload_digest,
       before_status, after_status, task_id, issue_id, blockers, applied, replayed, created_at
FROM project_lifecycle_receipt
WHERE workspace_id = $1 AND idempotency_key = $2;

-- name: InsertProjectLifecycleReceipt :one
INSERT INTO project_lifecycle_receipt (
    workspace_id, project_id, action, idempotency_key, payload_digest,
    before_status, after_status, task_id, issue_id, blockers, applied, replayed
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
RETURNING id, workspace_id, project_id, action, idempotency_key, payload_digest,
          before_status, after_status, task_id, issue_id, blockers, applied, replayed, created_at;

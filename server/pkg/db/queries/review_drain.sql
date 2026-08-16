-- Review-drain progress ledger for the legacy in_review queue (Lane B / P2).
-- The drain job advances at most batch_size rows per tick and records each
-- classification + terminal disposition here so a crash resumes without
-- re-fanning-out the whole queue.

-- name: UpsertReviewDrainProgress :one
-- Idempotent classification write. A re-classification updates the row in
-- place; review_task_id is never cleared once set (COALESCE keeps the first
-- review task the drain created).
INSERT INTO review_drain_progress (
    issue_id, workspace_id, classification, status, reason, review_task_id, processed_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (issue_id) DO UPDATE SET
    classification = EXCLUDED.classification,
    status = EXCLUDED.status,
    reason = EXCLUDED.reason,
    review_task_id = COALESCE(EXCLUDED.review_task_id, review_drain_progress.review_task_id),
    processed_at = EXCLUDED.processed_at,
    updated_at = now()
RETURNING *;

-- name: ListPendingDrainProgress :many
-- The next unprocessed classification rows for a workspace, oldest first.
SELECT * FROM review_drain_progress
WHERE workspace_id = $1 AND status = 'pending'
ORDER BY created_at ASC, issue_id ASC
LIMIT $2;

-- name: GetDrainProgressForIssue :one
SELECT * FROM review_drain_progress WHERE issue_id = $1;

-- name: GetProjectStatusForIssue :one
-- Read-only lifecycle projection used by the drain fail-closed gate. A NULL
-- project status means this legacy issue is not linked to a project and keeps
-- the historical review behavior.
SELECT COALESCE(p.status, '') AS project_status
FROM issue i
LEFT JOIN project p ON p.id = i.project_id
WHERE i.id = $1;

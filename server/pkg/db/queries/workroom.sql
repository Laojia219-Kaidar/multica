-- name: CreateWorkroom :one
INSERT INTO workroom (workspace_id, name, project_id, issue_id, work_order_id, created_by)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, workspace_id, name, project_id, issue_id, work_order_id, created_by, created_at, updated_at;

-- name: ListWorkrooms :many
SELECT id, workspace_id, name, project_id, issue_id, work_order_id, created_by, created_at, updated_at
FROM workroom
WHERE workspace_id = $1
ORDER BY created_at DESC;

-- name: GetWorkroom :one
SELECT id, workspace_id, name, project_id, issue_id, work_order_id, created_by, created_at, updated_at
FROM workroom
WHERE id = $1;

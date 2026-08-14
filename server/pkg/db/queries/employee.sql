-- name: CreateEmployee :one
INSERT INTO employee (workspace_id, name, position, department, agent_id, status)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, workspace_id, name, position, department, agent_id, status, created_at, updated_at;

-- name: ListEmployees :many
SELECT id, workspace_id, name, position, department, agent_id, status, created_at, updated_at
FROM employee WHERE workspace_id = $1 ORDER BY created_at DESC;

-- name: UpdateEmployeeBinding :one
UPDATE employee SET agent_id = $2, status = $3, updated_at = now()
WHERE id = $1
RETURNING id, workspace_id, name, position, department, agent_id, status, created_at, updated_at;

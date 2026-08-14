-- OperatingProgram is a HiveCrew workflow organization projection. It stores
-- no Project lifecycle fields and all reads/writes carry workspace scope.

-- name: GetWorkflowOperatingProgramByIdempotency :one
SELECT * FROM workflow_operating_program
WHERE workspace_id = $1 AND idempotency_key = $2;

-- name: InsertWorkflowOperatingProgram :one
INSERT INTO workflow_operating_program
    (id, workspace_id, name, description, idempotency_key)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (workspace_id, idempotency_key)
DO UPDATE SET idempotency_key = EXCLUDED.idempotency_key
RETURNING *;

-- name: ListWorkflowOperatingPrograms :many
SELECT * FROM workflow_operating_program
WHERE workspace_id = $1
ORDER BY created_at ASC, id ASC;

-- name: GetWorkflowOperatingProgramInWorkspace :one
SELECT * FROM workflow_operating_program
WHERE workspace_id = $1 AND id = $2;

-- name: UpdateWorkflowOperatingProgram :one
UPDATE workflow_operating_program
SET name = $3, description = $4, updated_at = now()
WHERE workspace_id = $1 AND id = $2
RETURNING *;

-- name: DeleteWorkflowOperatingProgramProjects :exec
DELETE FROM workflow_operating_program_project
WHERE workspace_id = $1 AND program_id = $2;

-- name: DeleteWorkflowOperatingProgram :exec
DELETE FROM workflow_operating_program
WHERE workspace_id = $1 AND id = $2;

-- name: ListWorkflowOperatingProgramProjectIDs :many
SELECT project_id FROM workflow_operating_program_project
WHERE workspace_id = $1 AND program_id = $2
ORDER BY project_id ASC;

-- name: GetWorkflowOperatingProgramProject :one
SELECT program_id, workspace_id, project_id
FROM workflow_operating_program_project
WHERE workspace_id = $1 AND project_id = $2;

-- name: InsertWorkflowOperatingProgramProject :one
INSERT INTO workflow_operating_program_project (program_id, workspace_id, project_id)
VALUES ($1, $2, $3)
ON CONFLICT (program_id, project_id)
DO UPDATE SET project_id = EXCLUDED.project_id
RETURNING program_id, workspace_id, project_id;

-- name: DeleteWorkflowOperatingProgramProject :exec
DELETE FROM workflow_operating_program_project
WHERE workspace_id = $1 AND program_id = $2 AND project_id = $3;

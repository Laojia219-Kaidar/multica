-- name: CreateDataset :one
INSERT INTO dataset (workspace_id, name, domain, version, authorized_agent_ids)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, workspace_id, name, domain, version, authorized_agent_ids, created_at, updated_at;

-- name: ListDatasets :many
SELECT id, workspace_id, name, domain, version, authorized_agent_ids, created_at, updated_at
FROM dataset WHERE workspace_id = $1 ORDER BY created_at DESC;

-- name: UpdateDatasetAuthorization :one
UPDATE dataset SET authorized_agent_ids = $2, updated_at = now()
WHERE id = $1
RETURNING id, workspace_id, name, domain, version, authorized_agent_ids, created_at, updated_at;

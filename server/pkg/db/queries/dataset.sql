-- name: CreateDataset :one
INSERT INTO dataset (workspace_id, name, domain, version, product_type, authorized_agent_ids)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, workspace_id, name, domain, version, product_type, authorized_agent_ids, created_at, updated_at;

-- name: ListDatasets :many
SELECT id, workspace_id, name, domain, version, product_type, authorized_agent_ids, created_at, updated_at
FROM dataset WHERE workspace_id = $1 ORDER BY created_at DESC;

-- name: GetDataset :one
SELECT id, workspace_id, name, domain, version, product_type, authorized_agent_ids, created_at, updated_at
FROM dataset WHERE id = $1 AND workspace_id = $2;

-- name: UpdateDataset :one
-- Partial update. NULL params preserve the stored value; a provided
-- authorized_agent_ids (including an empty slice) replaces the set. The
-- workspace_id guard is defense-in-depth for the tenant boundary, matching
-- DeleteSkill.
UPDATE dataset SET
    name = COALESCE(sqlc.narg('name'), name),
    domain = COALESCE(sqlc.narg('domain'), domain),
    product_type = COALESCE(sqlc.narg('product_type'), product_type),
    version = COALESCE(sqlc.narg('version'), version),
    authorized_agent_ids = COALESCE(sqlc.narg('authorized_agent_ids'), authorized_agent_ids),
    updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING id, workspace_id, name, domain, version, product_type, authorized_agent_ids, created_at, updated_at;

-- name: DeleteDataset :execrows
DELETE FROM dataset WHERE id = $1 AND workspace_id = $2;

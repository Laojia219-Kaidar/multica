CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS workflow_definition_version_definition_version_uidx
    ON workflow_definition_version (workspace_id, definition_id, version);

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS workflow_definition_version_workspace_idempotency_uidx
    ON workflow_definition_version (workspace_id, idempotency_key);

CREATE INDEX CONCURRENTLY IF NOT EXISTS workflow_definition_version_workspace_created_idx
    ON workflow_definition_version (workspace_id, created_at DESC, definition_id, version DESC);

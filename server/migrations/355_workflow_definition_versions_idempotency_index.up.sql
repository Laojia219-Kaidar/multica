CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS workflow_definition_version_workspace_idempotency_uidx
    ON workflow_definition_version (workspace_id, idempotency_key);

CREATE INDEX CONCURRENTLY IF NOT EXISTS workflow_definition_version_workspace_created_idx
    ON workflow_definition_version (workspace_id, created_at DESC, definition_id, version DESC);

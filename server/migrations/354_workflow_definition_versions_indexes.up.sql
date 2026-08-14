CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS workflow_definition_version_definition_version_uidx
    ON workflow_definition_version (workspace_id, definition_id, version);

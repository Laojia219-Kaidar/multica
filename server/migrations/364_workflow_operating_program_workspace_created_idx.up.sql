CREATE INDEX CONCURRENTLY IF NOT EXISTS workflow_operating_program_workspace_created_idx
    ON workflow_operating_program (workspace_id, created_at DESC, id DESC);

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS workflow_operating_program_workspace_idempotency_idx
    ON workflow_operating_program (workspace_id, idempotency_key);

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS workflow_operating_program_project_project_idx
    ON workflow_operating_program_project (workspace_id, project_id);

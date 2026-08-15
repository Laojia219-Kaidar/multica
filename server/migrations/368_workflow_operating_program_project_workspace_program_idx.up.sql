CREATE INDEX CONCURRENTLY IF NOT EXISTS workflow_operating_program_project_workspace_program_idx
    ON workflow_operating_program_project (workspace_id, program_id, project_id);

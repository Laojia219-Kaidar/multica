CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS workflow_operating_program_project_program_idx
    ON workflow_operating_program_project (program_id, project_id);

-- 351_workflow_workspace_scope_indexes.up.sql
-- This file intentionally contains only concurrent index statements.
CREATE INDEX CONCURRENTLY IF NOT EXISTS workflow_definition_workspace_id_idx
    ON workflow_definition (workspace_id, id);

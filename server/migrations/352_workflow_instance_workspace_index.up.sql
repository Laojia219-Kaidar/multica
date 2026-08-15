-- 352_workflow_instance_workspace_index.up.sql
CREATE INDEX CONCURRENTLY IF NOT EXISTS workflow_instance_workspace_created_idx
    ON workflow_instance (workspace_id, created_at DESC, id);

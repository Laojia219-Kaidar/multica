-- 350_workflow_workspace_scope.down.sql
ALTER TABLE workflow_instance DROP COLUMN IF EXISTS workspace_id;
ALTER TABLE workflow_definition DROP COLUMN IF EXISTS workspace_id;

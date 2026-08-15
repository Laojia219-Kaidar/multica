-- 350_workflow_workspace_scope.up.sql
-- Candidate-only forward migration for workflow workspace isolation.
-- Existing 342 rows remain readable through legacy queries until explicitly
-- backfilled by an Owner-authorized migration; new HTTP writes must provide a
-- workspace_id and use the scoped queries.

ALTER TABLE workflow_definition
    ADD COLUMN IF NOT EXISTS workspace_id UUID;

ALTER TABLE workflow_instance
    ADD COLUMN IF NOT EXISTS workspace_id UUID;

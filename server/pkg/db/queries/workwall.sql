-- name: GetWorkspaceIssuePrefix :one
-- Work Wall execution-chain projection (HIV-797): the card composes a human
-- issue identifier from the workspace's stored prefix, and needs nothing
-- else from the workspace row — never settings, context or repos.
SELECT issue_prefix
FROM workspace
WHERE id = $1;

-- name: GetRuntimeProfileForWorkWall :one
-- Workspace-scoped profile evidence for the Work Wall card: who the profile
-- is (id + display name), never what it runs (command, fixed_args, protocol).
SELECT id, workspace_id, display_name
FROM runtime_profile
WHERE id = $1 AND workspace_id = $2;

-- name: GetExecutionReceiptForWorkWall :one
-- Receipt lineage + closed terminal status only, for the Work Wall card.
-- task_id is the receipt primary key, so this is exactly the receipt of the
-- task being projected. Runtime/result snapshots, digests, terminal errors
-- and other payload columns are deliberately not selected.
SELECT task_id, workspace_id, issue_id, terminal_status
FROM execution_receipt
WHERE task_id = $1;

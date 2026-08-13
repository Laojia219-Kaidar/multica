-- Project lifecycle closure read-model queries.
-- These are READ-ONLY projections over the existing project / issue /
-- agent_task_queue truth. They do NOT create a second source of truth.

-- name: ListProjectActiveTasks :many
-- Nonterminal tasks joined back to their issue's project, workspace-scoped.
-- 'deferred' is included: it is a scheduled but not-yet-live nonterminal state,
-- so a project with only deferred work is still "active" per the HIV-553
-- contract (frontier = nonterminal Task/Run set), never silently stalled.
SELECT atq.id AS task_id,
       atq.status AS task_status,
       atq.started_at,
       atq.agent_id,
       atq.issue_id,
       i.project_id AS project_id,
       i.number AS issue_number,
       i.title AS issue_title
FROM agent_task_queue atq
JOIN issue i ON i.id = atq.issue_id
WHERE i.workspace_id = $1
  AND i.project_id IS NOT NULL
  AND atq.status IN ('queued', 'dispatched', 'running', 'waiting_local_directory', 'deferred')
ORDER BY atq.created_at;

-- name: ListProjectSuccessProgress :many
-- Per-project most recent successful (completed) task time and completed count.
-- Failed and cancelled tasks are activity, never progress.
SELECT i.project_id AS project_id,
       MAX(atq.completed_at)::timestamptz AS last_success_at,
       COUNT(*) AS completed_count
FROM agent_task_queue atq
JOIN issue i ON i.id = atq.issue_id
WHERE i.workspace_id = $1
  AND i.project_id IS NOT NULL
  AND atq.status = 'completed'
GROUP BY i.project_id;

-- pipeline.sql — read-only project pipeline projection for HIV-367 (P0-E).
--
-- Contract (issue HIV-367 §1, §7): no second pipeline-status table; this is a
-- read-only composition of issue + agent_task_queue (the canonical Task/Run
-- row) + comment. Every query is workspace-exact (issue.workspace_id = $1)
-- AND project-exact (issue.project_id = $2); agent_task_queue carries no
-- workspace_id column, so its scoping rides on the parent issue.
--
-- Two queries back GET /api/projects/{id}/pipeline:
--   1. ListProjectPipelineRows — one row per non-terminal issue in the
--      project, joined to its latest agent_task_queue row (by created_at) and
--      its latest task-linked comment (by created_at). The service layer
--      computes per-status-column aggregations from this set.
--   2. CountProjectPipelineTasksByStatus — one row per (issue_status,
--      task_status) with a count. Used as a cheap cross-check / fallback when
--      the row set is large; the service prefers the row set for accuracy.
--
-- "Latest task" = the most recently created agent_task_queue row for that
-- issue (created_at DESC, id DESC tiebreaker). This matches ListTasksByIssue's
-- ORDER BY and the issue-detail execution-log "past runs" ordering.

-- name: ListProjectPipelineRows :many
-- One row per non-terminal issue (status NOT IN done/cancelled) in the
-- project, LEFT JOINed to its latest task row and its latest task-linked
-- comment (comment.source_task_id IS NOT NULL). The LEFT JOINs keep issues
-- with no task (the "no_task / 停滞" state) and issues with no task-linked
-- comment (the "terminal_no_writeback" signal) visible — they are the states
-- HIV-367 §4 requires us to mark explicitly. Tasks are scoped to the issue
-- via the same agent_task_queue.issue_id join used by ListTasksByIssue; no
-- new writer, no schema change.
SELECT
    i.id            AS issue_id,
    i.status        AS issue_status,
    i.priority      AS issue_priority,
    i.title         AS issue_title,
    i.assignee_type AS issue_assignee_type,
    i.assignee_id   AS issue_assignee_id,
    i.updated_at    AS issue_updated_at,
    i.created_at    AS issue_created_at,
    t.id            AS task_id,
    COALESCE(t.status, '') AS task_status,
    COALESCE(t.priority, 0) AS task_priority,
    t.dispatched_at AS task_dispatched_at,
    t.started_at    AS task_started_at,
    t.completed_at  AS task_completed_at,
    t.failure_reason AS task_failure_reason,
    t.wait_reason   AS task_wait_reason,
    t.trigger_comment_id AS task_trigger_comment_id,
    t.delivered_comment_ids AS task_delivered_comment_ids,
    t.created_at    AS task_created_at,
    c.id            AS latest_receipt_comment_id,
    c.created_at    AS latest_receipt_comment_at,
    COALESCE(c.content, '') AS latest_receipt_comment_content
FROM issue i
LEFT JOIN LATERAL (
    SELECT q.*
    FROM agent_task_queue q
    WHERE q.issue_id = i.id
    ORDER BY q.created_at DESC, q.id DESC
    LIMIT 1
) t ON true
LEFT JOIN LATERAL (
    SELECT cm.*
    FROM comment cm
    WHERE cm.issue_id = i.id
      AND cm.source_task_id IS NOT NULL
    ORDER BY cm.created_at DESC, cm.id DESC
    LIMIT 1
) c ON true
WHERE i.workspace_id = $1
  AND i.project_id   = $2
  AND i.status NOT IN ('done', 'cancelled')
ORDER BY i.status ASC, i.priority ASC, i.position ASC, i.created_at DESC;

-- name: CountProjectPipelineTasksByStatus :many
-- Aggregated (issue_status, task_status) -> count over the same non-terminal
-- issue set. Used to size column headers cheaply when the row set would be
-- large, and as a strict total cross-check. The service layer prefers the
-- row set for issue-level fields and only falls back to this for header
-- totals when the row set is paginated.
SELECT
    i.status                AS issue_status,
    COALESCE(t.status, 'no_task')::text AS task_status,
    COUNT(*)::bigint        AS cnt
FROM issue i
LEFT JOIN agent_task_queue t ON t.issue_id = i.id
WHERE i.workspace_id = $1
  AND i.project_id   = $2
  AND i.status NOT IN ('done', 'cancelled')
GROUP BY i.status, COALESCE(t.status, 'no_task')::text
ORDER BY i.status, task_status;

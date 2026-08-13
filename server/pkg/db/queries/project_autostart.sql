-- project_autostart.sql — dependency-ready wave for HIV-405 (Owner control).
--
-- ListProjectReadyIssues returns non-terminal issues in a project that are
-- "ready" for dispatch: either they have no parent, or all their siblings
-- under the same parent are done/cancelled (the parent gate is satisfied).
-- The issue must have an assignee (agent or squad) and must not itself be
-- in a terminal state. This is the bounded "wave" the Owner control panel
-- previews before dispatching.
--
-- No schema change — reuses issue.parent_issue_id and existing statuses.

-- name: ListProjectReadyIssues :many
SELECT
    i.id            AS issue_id,
    i.status        AS issue_status,
    i.priority      AS issue_priority,
    i.title         AS issue_title,
    i.assignee_type AS issue_assignee_type,
    i.assignee_id   AS issue_assignee_id,
    i.updated_at    AS issue_updated_at,
    i.parent_issue_id,
    t.id            AS task_id,
    COALESCE(t.status, '') AS task_status,
    t.created_at    AS task_created_at
FROM issue i
LEFT JOIN LATERAL (
    SELECT q.*
    FROM agent_task_queue q
    WHERE q.issue_id = i.id
    ORDER BY q.created_at DESC, q.id DESC
    LIMIT 1
) t ON true
WHERE i.workspace_id = $1
  AND i.project_id   = $2
  AND i.status NOT IN ('done', 'cancelled')
  AND i.assignee_type IS NOT NULL
  AND i.assignee_id IS NOT NULL
  AND (
    i.parent_issue_id IS NULL
    OR NOT EXISTS (
      SELECT 1 FROM issue sibling
      WHERE sibling.parent_issue_id = i.parent_issue_id
        AND sibling.id != i.id
        AND sibling.status NOT IN ('done', 'cancelled')
    )
  )
ORDER BY i.priority ASC, i.position ASC, i.created_at DESC;

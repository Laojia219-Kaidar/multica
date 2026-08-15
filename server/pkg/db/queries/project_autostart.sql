-- project_autostart.sql — dependency-ready wave for HIV-405 (Owner control).
--
-- ListProjectReadyIssues returns non-terminal issues in a project for the
-- Owner control wave. The parent prerequisite gate is evaluated as an
-- explicit prereq_met column instead of a WHERE filter: rows whose parent
-- gate is NOT satisfied must stay visible so Preview can surface them as
-- blocked with a concrete reason (missing_prerequisite). They never vanish
-- from the SQL result set into silence (HIV-465 item 4).
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
    (
      i.parent_issue_id IS NULL
      OR NOT EXISTS (
        SELECT 1 FROM issue sibling
        WHERE sibling.parent_issue_id = i.parent_issue_id
          AND sibling.id != i.id
          AND sibling.status NOT IN ('done', 'cancelled')
      )
    ) AS prereq_met,
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
ORDER BY i.priority ASC, i.position ASC, i.created_at DESC;
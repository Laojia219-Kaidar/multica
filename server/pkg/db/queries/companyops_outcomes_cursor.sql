-- HiveCrew outcome-center cursor read model (Lane E, P4 scalable history).
-- Keyset pagination over assignment_dispatch_receipt ordered by
-- (created_at DESC, command_id DESC). The cursor predicate is optional:
-- when cursor_created_at is NULL the query returns the newest page, which
-- makes it also usable as a plain newest-first window.

-- name: ListCompanyOpsOutcomeRowsCursor :many
SELECT
    adr.command_id,
    adr.workspace_id,
    adr.issue_id,
    adr.local_agent_id,
    adr.initial_task_id,
    adr.work_order_ref,
    adr.work_order_revision,
    adr.work_order_digest,
    adr.input_digest,
    adr.employee_ref,
    adr.employee_revision,
    adr.employee_digest,
    adr.binding_ref,
    adr.binding_revision,
    adr.binding_digest,
    adr.agent_ref,
    adr.agent_revision,
    adr.agent_digest,
    adr.created_at AS assignment_created_at,
    i.title AS issue_title,
    i.number AS issue_number,
    i.status AS issue_status,
    i.project_id AS issue_project_id,
    w.issue_prefix AS workspace_issue_prefix,
    a.name AS agent_display_name,
    a.model AS agent_model,
    a.status AS agent_runtime_status,
    lc.id AS artifact_candidate_id,
    COALESCE(lc.revision, 0)::int AS artifact_candidate_revision,
    COALESCE(lc.durable_object_ref, '')::text AS artifact_durable_object_ref,
    COALESCE(lc.digest, '')::text AS artifact_digest,
    COALESCE(lc.content_type, '')::text AS artifact_content_type,
    COALESCE(le.event_type, '')::text AS artifact_lifecycle_status,
    le.formal_artifact_ref AS artifact_formal_ref,
    vc.version_count,
    t.status AS initial_task_status,
    er.terminal_status AS execution_terminal_status,
    ct.id AS current_task_id,
    ct.status AS current_task_status,
    cer.terminal_status AS current_execution_terminal_status,
    latest_event_at.max_event_at AS latest_event_at,
    rtc.rework_task_count AS rework_task_count
FROM assignment_dispatch_receipt adr
LEFT JOIN issue i ON i.id = adr.issue_id AND i.workspace_id = adr.workspace_id
LEFT JOIN workspace w ON w.id = adr.workspace_id
LEFT JOIN agent a ON a.id = adr.local_agent_id AND a.workspace_id = adr.workspace_id
LEFT JOIN agent_task_queue t ON t.id = adr.initial_task_id
    AND t.issue_id = adr.issue_id AND i.id = t.issue_id
LEFT JOIN execution_receipt er ON er.task_id = adr.initial_task_id
    AND er.workspace_id = adr.workspace_id
    AND er.issue_id = adr.issue_id
    AND er.assignment_command_id = adr.command_id
LEFT JOIN LATERAL (
    SELECT ac.id, ac.revision, ac.durable_object_ref, ac.digest, ac.content_type
    FROM artifact_candidate ac
    WHERE ac.workspace_id = adr.workspace_id AND ac.lineage_id = adr.command_id
    ORDER BY ac.revision DESC
    LIMIT 1
) lc ON true
LEFT JOIN LATERAL (
    SELECT ae.event_type, ae.formal_artifact_ref
    FROM artifact_event ae
    WHERE ae.workspace_id = adr.workspace_id AND ae.lineage_id = adr.command_id
      AND ae.candidate_id = lc.id
    ORDER BY ae.sequence DESC
    LIMIT 1
) le ON true
LEFT JOIN LATERAL (
    SELECT COUNT(*)::int AS version_count
    FROM artifact_candidate ac
    WHERE ac.workspace_id = adr.workspace_id AND ac.lineage_id = adr.command_id
) vc ON true
LEFT JOIN LATERAL (
    SELECT MAX(ae3.created_at) AS max_event_at
    FROM artifact_event ae3
    WHERE ae3.workspace_id = adr.workspace_id AND ae3.lineage_id = adr.command_id
) latest_event_at ON true
LEFT JOIN LATERAL (
    SELECT COUNT(*)::int AS rework_task_count
    FROM agent_task_queue atq
    WHERE atq.issue_id = adr.issue_id AND i.id = atq.issue_id
      AND atq.trigger_evidence_kind = 'artifact_revision'
      AND atq.trigger_evidence_ref_id = (
          SELECT ae2.id FROM artifact_event ae2
          WHERE ae2.workspace_id = adr.workspace_id AND ae2.lineage_id = adr.command_id
            AND ae2.candidate_id = lc.id
            AND ae2.event_type = 'changes_requested'
          ORDER BY ae2.sequence DESC LIMIT 1
      )
) rtc ON true
LEFT JOIN agent_task_queue ct ON ct.id = COALESCE(
    (SELECT atq.id FROM agent_task_queue atq
     WHERE atq.issue_id = adr.issue_id AND i.id = atq.issue_id
       AND atq.trigger_evidence_kind = 'artifact_revision'
       AND atq.trigger_evidence_ref_id = (
           SELECT ae2.id FROM artifact_event ae2
           WHERE ae2.workspace_id = adr.workspace_id AND ae2.lineage_id = adr.command_id
             AND ae2.candidate_id = lc.id
             AND ae2.event_type = 'changes_requested'
           ORDER BY ae2.sequence DESC LIMIT 1
       )
     ORDER BY atq.created_at ASC, atq.id ASC LIMIT 1),
    lc.id,
    adr.initial_task_id
)
    AND ct.issue_id = adr.issue_id AND i.id = ct.issue_id
LEFT JOIN execution_receipt cer ON cer.task_id = ct.id
    AND cer.workspace_id = adr.workspace_id
    AND cer.issue_id = adr.issue_id
    AND cer.assignment_command_id = adr.command_id
WHERE adr.workspace_id = @workspace_id
    AND (sqlc.narg('q_text')::text IS NULL OR
         i.title ILIKE '%' || sqlc.narg('q_text') || '%' OR
         (w.issue_prefix || '-' || i.number::text) ILIKE '%' || sqlc.narg('q_text') || '%' OR
         adr.work_order_ref ILIKE '%' || sqlc.narg('q_text') || '%' OR
         adr.employee_ref ILIKE '%' || sqlc.narg('q_text') || '%' OR
         a.name ILIKE '%' || sqlc.narg('q_text') || '%' OR
         le.formal_artifact_ref::text ILIKE '%' || sqlc.narg('q_text') || '%')
    AND (sqlc.narg('agent_filter')::uuid IS NULL OR adr.local_agent_id = sqlc.narg('agent_filter'))
    AND (sqlc.narg('project_filter')::uuid IS NULL OR i.project_id = sqlc.narg('project_filter'))
    AND (sqlc.narg('employee_filter')::text IS NULL OR
         adr.employee_ref = 'hivecosm://employees/' || sqlc.narg('employee_filter'))
    AND (sqlc.narg('type_filter')::text IS NULL OR lc.content_type = sqlc.narg('type_filter'))
    AND (
        sqlc.narg('status_filter')::text IS NULL OR
        le.event_type = sqlc.narg('status_filter') OR
        (le.event_type IS NULL AND
            COALESCE(cer.terminal_status,
                CASE
                    WHEN ct.status IN ('queued','dispatched','waiting_local_directory','deferred') THEN 'awaiting_claim'
                    WHEN ct.status = 'running' THEN 'running'
                    WHEN ct.status IS NOT NULL THEN ct.status
                END
            ) = sqlc.narg('status_filter'))
    )
    AND (
        sqlc.narg('formal_visible_filter')::bool IS NULL OR
        (sqlc.narg('formal_visible_filter')::bool = true AND
         le.event_type = 'authority_readback_confirmed' AND
         le.formal_artifact_ref IS NOT NULL AND
         btrim(le.formal_artifact_ref) <> '' AND
         lc.id IS NOT NULL) OR
        (sqlc.narg('formal_visible_filter')::bool = false AND
         (le.event_type IS NULL OR
          le.event_type <> 'authority_readback_confirmed' OR
          le.formal_artifact_ref IS NULL OR
          btrim(le.formal_artifact_ref) = '' OR
          lc.id IS NULL))
    )
    AND (
        sqlc.narg('cursor_created_at')::timestamptz IS NULL
        OR adr.created_at < sqlc.narg('cursor_created_at')
        OR (adr.created_at = sqlc.narg('cursor_created_at') AND adr.command_id < sqlc.narg('cursor_command_id'))
    )
ORDER BY adr.created_at DESC, adr.command_id DESC
LIMIT @limit_rows;

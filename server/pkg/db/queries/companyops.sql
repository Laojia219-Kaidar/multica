-- name: LockCompanyOpsAssignmentCommand :exec
SELECT pg_advisory_xact_lock(hashtextextended(
	concat(
		CAST(sqlc.arg(workspace_id) AS uuid),
		':',
		CAST(sqlc.arg(command_id) AS uuid)
	),
    0
));

-- name: InsertExternalWorkOrderLink :one
INSERT INTO external_work_order_link (
    workspace_id, work_order_ref, linked_revision, linked_digest,
    source_observed_at, freshness_at_link, issue_id
) VALUES (
    @workspace_id, @work_order_ref, @linked_revision, @linked_digest,
    @source_observed_at, @freshness_at_link, @issue_id
)
ON CONFLICT (workspace_id, work_order_ref) DO NOTHING
RETURNING *;

-- name: GetExternalWorkOrderLink :one
SELECT * FROM external_work_order_link
WHERE workspace_id = @workspace_id AND work_order_ref = @work_order_ref;

-- name: InsertAssignmentDispatchReceipt :one
INSERT INTO assignment_dispatch_receipt (
    command_id, workspace_id, issue_id, local_agent_id, initial_task_id,
    work_order_ref, work_order_revision, work_order_digest, input_digest,
    employee_ref, employee_revision, employee_digest,
    binding_ref, binding_revision, binding_digest,
    agent_ref, agent_revision, agent_digest
) VALUES (
    @command_id, @workspace_id, @issue_id, @local_agent_id, @initial_task_id,
    @work_order_ref, @work_order_revision, @work_order_digest, @input_digest,
    @employee_ref, @employee_revision, @employee_digest,
    @binding_ref, @binding_revision, @binding_digest,
    @agent_ref, @agent_revision, @agent_digest
)
ON CONFLICT (command_id) DO NOTHING
RETURNING *;

-- name: GetAssignmentDispatchReceiptByCommand :one
SELECT * FROM assignment_dispatch_receipt
WHERE command_id = @command_id;

-- name: GetAssignmentDispatchReceipt :one
SELECT * FROM assignment_dispatch_receipt
WHERE workspace_id = @workspace_id AND command_id = @command_id;

-- name: GetLatestAssignmentDispatchReceiptByIssue :one
SELECT * FROM assignment_dispatch_receipt
WHERE workspace_id = @workspace_id AND issue_id = @issue_id
ORDER BY created_at DESC, command_id DESC
LIMIT 1;

-- name: GetCompanyOpsTaskByTriggerEvidence :one
SELECT * FROM agent_task_queue
WHERE issue_id = @issue_id
  AND trigger_evidence_kind = @trigger_evidence_kind
  AND trigger_evidence_ref_id = @trigger_evidence_ref_id
ORDER BY created_at ASC, id ASC
LIMIT 1;

-- name: InsertExecutionReceiptClaim :one
INSERT INTO execution_receipt (
    task_id, workspace_id, issue_id, assignment_command_id,
    work_order_ref, work_order_revision, work_order_digest, input_digest,
    employee_ref, employee_revision, employee_digest,
    binding_ref, binding_revision, binding_digest,
    agent_ref, agent_revision, agent_digest,
    runtime_snapshot, runtime_digest, claimed_at
) VALUES (
    @task_id, @workspace_id, @issue_id, @assignment_command_id,
    @work_order_ref, @work_order_revision, @work_order_digest, @input_digest,
    @employee_ref, @employee_revision, @employee_digest,
    @binding_ref, @binding_revision, @binding_digest,
    @agent_ref, @agent_revision, @agent_digest,
    @runtime_snapshot, @runtime_digest, @claimed_at
)
ON CONFLICT (task_id) DO NOTHING
RETURNING *;

-- name: GetExecutionReceipt :one
SELECT * FROM execution_receipt
WHERE task_id = @task_id;

-- name: FinalizeExecutionReceipt :one
UPDATE execution_receipt
SET terminal_status = @terminal_status,
    completed_at = @completed_at,
    output_digest = sqlc.narg('output_digest'),
    result_snapshot = sqlc.narg('result_snapshot'),
    terminal_error = sqlc.narg('terminal_error'),
    finalized_at = now()
WHERE task_id = @task_id
  AND terminal_status IS NULL
RETURNING *;

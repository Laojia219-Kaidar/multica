-- name: InsertContinuousDispatchReceipt :one
INSERT INTO continuous_dispatch_receipt (
    workspace_id, issue_id, stage, candidate_revision, generation,
    task_id, employee_ref, local_agent_id, runtime_id, model, account_ref,
    request_digest
) VALUES (
    @workspace_id, @issue_id, @stage, @candidate_revision, @generation,
    @task_id, @employee_ref, @local_agent_id, @runtime_id, @model, @account_ref,
    @request_digest
)
ON CONFLICT (workspace_id, issue_id, stage, candidate_revision, generation) DO NOTHING
RETURNING *;

-- name: GetContinuousDispatchReceipt :one
SELECT * FROM continuous_dispatch_receipt
WHERE workspace_id = @workspace_id
  AND issue_id = @issue_id
  AND stage = @stage
  AND candidate_revision = @candidate_revision
  AND generation = @generation;

-- name: StampContinuousDispatchTaskIdentity :one
UPDATE agent_task_queue AS task
SET context = COALESCE(task.context, '{}'::jsonb) || jsonb_build_object(
    'continuous_dispatch',
    jsonb_build_object(
        'workspace_id', CAST(@workspace_id AS uuid),
        'issue_id', CAST(@issue_id AS uuid),
        'stage', @stage::text,
        'candidate_revision', @candidate_revision::text,
        'generation', @generation::text
    )
)
FROM issue
WHERE task.id = @task_id
  AND task.issue_id = @issue_id
  AND issue.id = task.issue_id
  AND issue.workspace_id = @workspace_id
  AND NOT (COALESCE(task.context, '{}'::jsonb) ? 'continuous_dispatch')
RETURNING task.*;

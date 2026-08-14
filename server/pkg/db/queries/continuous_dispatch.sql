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

-- name: LockContinuousDispatchIdentity :exec
SELECT pg_advisory_xact_lock(
    hashtextextended(
        concat_ws(
            E'\x1f',
            CAST(@workspace_id AS uuid)::text,
            CAST(@issue_id AS uuid)::text,
            CAST(@stage AS text),
            CAST(@candidate_revision AS text),
            CAST(@generation AS text)
        ),
        0
    )
);

-- name: LockContinuousDispatchIssue :one
-- The dispatch transaction keeps the exact Issue row stable through Task and
-- receipt creation. A review command must not survive a concurrent lifecycle
-- or candidate-generation change after its server-side preview.
SELECT * FROM issue
WHERE id = @issue_id AND workspace_id = @workspace_id
FOR SHARE;

-- name: LockReviewSourceCommentForContinuousDispatch :one
-- A review source is a specific immutable agent Comment, not merely a Task.
-- FOR SHARE prevents an update or deletion from racing past final lineage
-- validation before the review Task+receipt transaction commits.
SELECT id, issue_id, author_type, author_id, workspace_id, source_task_id
FROM comment
WHERE id = @source_comment_id
  AND issue_id = @issue_id
  AND workspace_id = @workspace_id
FOR SHARE;

-- name: LockReviewSourceTaskForContinuousDispatch :one
-- Lock the completed implementation Task together with the source Comment so
-- its completion state and stamped generation cannot change mid-dispatch.
SELECT id, agent_id, issue_id, status, context, handoff_note
FROM agent_task_queue
WHERE id = @source_task_id
FOR SHARE;

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
) || CASE
    WHEN sqlc.narg('review_source_task_id')::uuid IS NULL THEN '{}'::jsonb
    ELSE jsonb_build_object(
        'review_dispatch',
        jsonb_build_object(
            'source_ref', sqlc.narg('review_source_ref')::text,
            'source_issue_id', sqlc.narg('review_source_issue_id')::uuid,
            'source_task_id', sqlc.narg('review_source_task_id')::uuid,
            'initiator_source', sqlc.narg('review_initiator_source')::text
        )
    )
END
FROM issue
WHERE task.id = @task_id
  AND task.issue_id = @issue_id
  AND issue.id = task.issue_id
  AND issue.workspace_id = @workspace_id
  AND NOT (COALESCE(task.context, '{}'::jsonb) ? 'continuous_dispatch')
RETURNING task.*;

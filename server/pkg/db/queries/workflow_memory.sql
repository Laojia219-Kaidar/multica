-- Workflow + employee memory candidate persistence queries (Slice-W2).
-- These tables are HiveCrew-owned orchestration/candidate state; they never
-- become a second source of company knowledge / playbook / skill truth.

-- name: InsertWorkflowDefinition :exec
INSERT INTO workflow_definition (id, version, risk, stages)
VALUES ($1, $2, $3, $4)
ON CONFLICT (id) DO UPDATE SET version = $2, risk = $3, stages = $4, updated_at = now();

-- name: GetWorkflowDefinition :one
SELECT * FROM workflow_definition WHERE id = $1;

-- name: GetWorkflowDefinitionVersionByIdempotency :one
SELECT * FROM workflow_definition_version
WHERE workspace_id = $1 AND idempotency_key = $2;

-- name: GetWorkflowDefinitionVersion :one
SELECT * FROM workflow_definition_version
WHERE workspace_id = $1 AND definition_id = $2 AND version = $3;

-- name: GetLatestWorkflowDefinitionVersion :one
SELECT * FROM workflow_definition_version
WHERE workspace_id = $1 AND definition_id = $2
ORDER BY version DESC
LIMIT 1;

-- name: ListWorkflowDefinitionVersions :many
SELECT * FROM workflow_definition_version
WHERE workspace_id = $1
ORDER BY created_at DESC, definition_id ASC, version DESC;

-- name: ListLatestWorkflowDefinitionVersions :many
SELECT DISTINCT ON (definition_id) *
FROM workflow_definition_version
WHERE workspace_id = $1
ORDER BY definition_id ASC, version DESC;

-- name: InsertWorkflowDefinitionVersion :one
INSERT INTO workflow_definition_version
    (definition_id, workspace_id, version, risk, stages, graph, digest, idempotency_key)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (workspace_id, idempotency_key)
DO UPDATE SET idempotency_key = EXCLUDED.idempotency_key
RETURNING *;

-- name: InsertWorkflowInstance :exec
INSERT INTO workflow_instance (id, definition_id, definition_version, context, stage_index, status)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (id) DO UPDATE SET stage_index = $5, status = $6, updated_at = now();

-- name: GetWorkflowInstance :one
SELECT * FROM workflow_instance WHERE id = $1;

-- name: ListWorkflowInstances :many
SELECT * FROM workflow_instance
WHERE workspace_id = $1
ORDER BY created_at DESC, id DESC;

-- name: GetWorkflowInstanceInWorkspace :one
SELECT * FROM workflow_instance
WHERE workspace_id = $1 AND id = $2;

-- name: InsertWorkflowInstanceInWorkspace :exec
INSERT INTO workflow_instance (workspace_id, id, definition_id, definition_version, context, stage_index, status)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (id) DO UPDATE SET
  stage_index = $6,
  status = $7,
  updated_at = now()
WHERE workflow_instance.workspace_id = $1;

-- name: UpdateWorkflowInstanceInWorkspace :exec
UPDATE workflow_instance
SET stage_index = $3, status = $4, updated_at = now()
WHERE workspace_id = $1 AND id = $2;

-- name: UpdateWorkflowInstance :exec
UPDATE workflow_instance SET stage_index = $2, status = $3, updated_at = now() WHERE id = $1;

-- name: InsertWorkflowEvent :exec
INSERT INTO workflow_event (instance_id, kind, source_ref, actor, occurred_at, observed_at, idempotency_key)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (instance_id, idempotency_key) DO NOTHING;

-- name: ListWorkflowEvents :many
SELECT * FROM workflow_event WHERE instance_id = $1 ORDER BY id ASC;

-- name: ListWorkflowEventsInWorkspace :many
SELECT e.*
FROM workflow_event e
JOIN workflow_instance i ON i.id = e.instance_id
WHERE i.workspace_id = $1 AND e.instance_id = $2
ORDER BY e.id ASC;

-- name: InsertMemoryCandidate :exec
INSERT INTO memory_candidate (id, employee_id, position_id, kind, content, evidence, source_refs, author_id, status)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (id) DO UPDATE SET content = $5, evidence = $6, source_refs = $7, status = $9, updated_at = now();

-- name: GetMemoryCandidate :one
SELECT * FROM memory_candidate WHERE id = $1;

-- name: ListMemoryCandidatesByEmployee :many
SELECT * FROM memory_candidate WHERE employee_id = $1 ORDER BY created_at DESC;

-- name: ListMemoryCandidatesByPosition :many
SELECT * FROM memory_candidate WHERE position_id = $1 ORDER BY created_at DESC;

-- name: UpdateMemoryCandidateStatus :exec
UPDATE memory_candidate SET status = $2, updated_at = now() WHERE id = $1;

-- name: InsertMemoryPromotion :exec
INSERT INTO memory_promotion (candidate_id, target, reviewer_id, approved, reason)
VALUES ($1, $2, $3, $4, $5);

-- name: ListMemoryPromotions :many
SELECT * FROM memory_promotion WHERE candidate_id = $1 ORDER BY promoted_at DESC;

-- name: InsertMemoryRevocation :exec
INSERT INTO memory_revocation (candidate_id, reviewer_id, reason)
VALUES ($1, $2, $3);

-- name: ListMemoryRevocations :many
SELECT * FROM memory_revocation WHERE candidate_id = $1 ORDER BY revoked_at DESC;

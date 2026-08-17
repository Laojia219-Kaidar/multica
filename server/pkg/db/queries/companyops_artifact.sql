-- name: InsertArtifactCandidate :one
INSERT INTO artifact_candidate (
    id, workspace_id, lineage_id, revision, supersedes_id,
    storage_key, durable_object_ref, digest,
    filename, content_type, size_bytes,
    source_attachment_id, source_comment_id, idempotency_key
) VALUES (
    @id, @workspace_id, @lineage_id, @revision, sqlc.narg('supersedes_id'),
    @storage_key, @durable_object_ref, @digest,
    @filename, @content_type, @size_bytes,
    sqlc.narg('source_attachment_id'), sqlc.narg('source_comment_id'), @idempotency_key
)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: GetArtifactCandidate :one
SELECT * FROM artifact_candidate
WHERE workspace_id = @workspace_id AND id = @id;

-- name: GetArtifactCandidateByIdempotency :one
SELECT * FROM artifact_candidate
WHERE workspace_id = @workspace_id AND idempotency_key = @idempotency_key;

-- name: ListArtifactCandidatesByLineage :many
SELECT * FROM artifact_candidate
WHERE workspace_id = @workspace_id AND lineage_id = @lineage_id
ORDER BY revision ASC;

-- name: LockArtifactLineage :exec
SELECT pg_advisory_xact_lock(hashtextextended(@lock_key::text, 0));

-- name: NextArtifactEventSequence :one
SELECT (COALESCE(MAX(sequence), 0) + 1)::int AS next_sequence
FROM artifact_event
WHERE workspace_id = @workspace_id AND lineage_id = @lineage_id;

-- name: InsertArtifactEvent :one
INSERT INTO artifact_event (
    id, workspace_id, lineage_id, sequence, event_type,
    candidate_id, candidate_revision, candidate_digest,
    candidate_object_ref, formal_artifact_ref, idempotency_key, actor_user_id
) VALUES (
    @id, @workspace_id, @lineage_id, @sequence, @event_type,
    @candidate_id, @candidate_revision, @candidate_digest,
    @candidate_object_ref, sqlc.narg('formal_artifact_ref'), @idempotency_key,
    sqlc.narg('actor_user_id')
)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: GetArtifactEventByIdempotency :one
SELECT * FROM artifact_event
WHERE workspace_id = @workspace_id AND idempotency_key = @idempotency_key;

-- name: GetArtifactEvent :one
SELECT * FROM artifact_event
WHERE workspace_id = @workspace_id AND id = @id;

-- name: LockArtifactEventForPromotion :one
SELECT * FROM artifact_event
WHERE workspace_id = @workspace_id AND id = @id
FOR SHARE;

-- name: ListArtifactEventsByLineage :many
SELECT * FROM artifact_event
WHERE workspace_id = @workspace_id AND lineage_id = @lineage_id
ORDER BY sequence ASC;

-- name: InsertArtifactMaterializationIntent :one
INSERT INTO artifact_materialization_intent (
    workspace_id, candidate_id, lineage_id,
    storage_key, durable_object_ref, digest,
    filename, content_type, size_bytes,
    source_attachment_id, source_comment_id, idempotency_key
) VALUES (
    @workspace_id, @candidate_id, @lineage_id,
    @storage_key, @durable_object_ref, @digest,
    @filename, @content_type, @size_bytes,
    sqlc.narg('source_attachment_id'), sqlc.narg('source_comment_id'), @idempotency_key
)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: GetArtifactMaterializationIntent :one
SELECT * FROM artifact_materialization_intent
WHERE workspace_id = @workspace_id AND storage_key = @storage_key;

-- name: MarkArtifactMaterializationIntentCleanupPending :one
UPDATE artifact_materialization_intent
SET state = 'cleanup_pending',
    last_error = @last_error,
    updated_at = now()
WHERE workspace_id = @workspace_id
  AND storage_key = @storage_key
  AND candidate_id = @candidate_id
  AND lineage_id = @lineage_id
  AND durable_object_ref = @durable_object_ref
  AND digest = @digest
  AND state = 'pending'
RETURNING *;

-- name: TombstoneArtifactMaterializationIntent :one
UPDATE artifact_materialization_intent
SET state = 'tombstoned', updated_at = now()
WHERE workspace_id = @workspace_id
  AND storage_key = @storage_key
  AND state = 'cleanup_pending'
RETURNING *;

-- name: DeleteCommittedArtifactMaterializationIntent :execrows
DELETE FROM artifact_materialization_intent
WHERE workspace_id = @workspace_id
  AND storage_key = @storage_key
  AND candidate_id = @candidate_id
  AND lineage_id = @lineage_id
  AND durable_object_ref = @durable_object_ref
  AND digest = @digest
  AND state = 'pending';

-- name: IsArtifactMaterializationExactlyReferenced :one
SELECT EXISTS (
    SELECT 1
    FROM artifact_candidate
    WHERE workspace_id = @workspace_id
      AND id = @candidate_id
      AND lineage_id = @lineage_id
      AND storage_key = @storage_key
      AND durable_object_ref = @durable_object_ref
      AND digest = @digest
) AS exactly_referenced;

-- name: ClaimArtifactPromotion :one
INSERT INTO artifact_promotion_claim (
    workspace_id, promotion_id, candidate_id, lineage_id, payload_digest,
    source_task_id, writer_lease_target_digest, completion_receipt_digest
) VALUES (
    @workspace_id, @promotion_id, @candidate_id, @lineage_id, @payload_digest,
    sqlc.narg('source_task_id'), sqlc.narg('writer_lease_target_digest'),
    sqlc.narg('completion_receipt_digest')
)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: GetArtifactPromotionClaim :one
SELECT * FROM artifact_promotion_claim
WHERE workspace_id = @workspace_id AND promotion_id = @promotion_id;

-- name: InsertArtifactPromotionDelivery :one
INSERT INTO artifact_promotion_delivery (
    workspace_id, promotion_id, candidate_id, lineage_id,
    source_task_id, writer_lease_target_digest, completion_receipt_digest,
    payload_digest, request_payload
) VALUES (
    @workspace_id, @promotion_id, @candidate_id, @lineage_id,
    sqlc.narg('source_task_id'), sqlc.narg('writer_lease_target_digest'),
    sqlc.narg('completion_receipt_digest'), @payload_digest, @request_payload
)
ON CONFLICT (workspace_id, promotion_id) DO NOTHING
RETURNING *;

-- name: GetArtifactPromotionDelivery :one
SELECT * FROM artifact_promotion_delivery
WHERE workspace_id = @workspace_id AND promotion_id = @promotion_id;

-- name: ClaimArtifactPromotionDelivery :one
UPDATE artifact_promotion_delivery
SET state = 'dispatching', attempt = attempt + 1,
    dispatch_token = gen_random_uuid(), lease_until = now() + interval '5 minutes',
    claimed_at = now(), updated_at = now(), last_error = NULL
WHERE workspace_id = @workspace_id
  AND promotion_id = @promotion_id
  AND payload_digest = @payload_digest
  AND state IN ('pending', 'failed')
RETURNING *;

-- name: MarkArtifactPromotionDeliverySucceeded :one
UPDATE artifact_promotion_delivery
SET state = 'succeeded', response_receipt = @response_receipt,
    dispatch_token = NULL, lease_until = NULL,
    completed_at = now(), updated_at = now(), last_error = NULL
WHERE workspace_id = @workspace_id
  AND promotion_id = @promotion_id
  AND payload_digest = @payload_digest
  AND dispatch_token = @dispatch_token
  AND state = 'dispatching'
RETURNING *;

-- name: MarkArtifactPromotionDeliveryReadbackConfirmed :one
UPDATE artifact_promotion_delivery
SET state = 'readback_confirmed', readback_receipt = @readback_receipt,
    completed_at = now(), updated_at = now(), last_error = NULL
WHERE workspace_id = @workspace_id
  AND promotion_id = @promotion_id
  AND payload_digest = @payload_digest
  AND state = 'succeeded'
RETURNING *;

-- name: RecoverArtifactPromotionDeliveryFromReadback :one
UPDATE artifact_promotion_delivery
SET state = 'readback_confirmed', response_receipt = @response_receipt,
    readback_receipt = @readback_receipt, dispatch_token = NULL,
    lease_until = NULL, completed_at = now(), updated_at = now(), last_error = NULL
WHERE workspace_id = @workspace_id
  AND promotion_id = @promotion_id
  AND payload_digest = @payload_digest
  AND dispatch_token = @dispatch_token
  AND state = 'dispatching'
RETURNING *;

-- name: MarkArtifactPromotionDeliveryFailed :one
UPDATE artifact_promotion_delivery
SET state = 'failed', last_error = @last_error,
    dispatch_token = NULL, lease_until = NULL, updated_at = now()
WHERE workspace_id = @workspace_id
  AND promotion_id = @promotion_id
  AND payload_digest = @payload_digest
  AND dispatch_token = @dispatch_token
  AND state = 'dispatching'
RETURNING *;

-- name: MarkArtifactPromotionDeliveryDefiniteAbsent :one
UPDATE artifact_promotion_delivery
SET state = 'failed', last_error = @last_error, dispatch_token = NULL,
    lease_until = NULL, updated_at = now()
WHERE workspace_id = @workspace_id AND promotion_id = @promotion_id
  AND payload_digest = @payload_digest AND dispatch_token = @dispatch_token
  AND state = 'dispatching' AND lease_until < now()
RETURNING *;

-- Storage-location rows are a replica placement ledger, not Artifact lifecycle
-- events. Every read is explicitly workspace-scoped and can be addressed by
-- the formal Outcome identity (outcome_id) or a candidate revision.
-- name: InsertArtifactReplicaLocation :one
INSERT INTO artifact_replica_location (
    id, workspace_id, outcome_id, candidate_id, candidate_revision,
    location_class, location_id, storage_id, object_ref, state,
    digest, metadata_digest, size_bytes, retention_hint, metadata
) VALUES (
    @id, @workspace_id, @outcome_id, @candidate_id, @candidate_revision,
    @location_class, @location_id, @storage_id, @object_ref, @state,
    @digest, @metadata_digest, @size_bytes, @retention_hint, @metadata
)
ON CONFLICT (workspace_id, outcome_id, candidate_id, location_class, location_id)
DO NOTHING
RETURNING *;

-- name: GetArtifactReplicaLocation :one
SELECT * FROM artifact_replica_location
WHERE workspace_id = @workspace_id
  AND id = @id;

-- name: GetArtifactReplicaLocationByIdentity :one
SELECT * FROM artifact_replica_location
WHERE workspace_id = @workspace_id
  AND outcome_id = @outcome_id
  AND candidate_id = @candidate_id
  AND location_class = @location_class
  AND location_id = @location_id;

-- name: UpdateArtifactReplicaLocationState :one
UPDATE artifact_replica_location
SET state = @state,
    digest = @digest,
    metadata_digest = @metadata_digest,
    size_bytes = @size_bytes,
    updated_at = now()
WHERE workspace_id = @workspace_id
  AND id = @id
RETURNING *;

-- name: ListArtifactReplicaLocationsByOutcome :many
SELECT * FROM artifact_replica_location
WHERE workspace_id = @workspace_id
  AND outcome_id = @outcome_id
ORDER BY created_at ASC, id ASC;

-- name: ListArtifactReplicaLocationsByCandidate :many
SELECT * FROM artifact_replica_location
WHERE workspace_id = @workspace_id
  AND candidate_id = @candidate_id
ORDER BY created_at ASC, id ASC;

-- name: ListArchivePendingArtifactCandidates :many
-- Candidates that have an approved-or-later lifecycle event (the archive
-- mirrors accepted work only) and no verified nas-primary replica row yet.
-- The NOT EXISTS anti-join keeps candidates without any ledger row in scope;
-- LIMIT bounds the reconciler's byte traffic per cycle.
SELECT c.*
FROM artifact_candidate c
WHERE c.workspace_id = @workspace_id
  AND EXISTS (
    SELECT 1
    FROM artifact_event e
    WHERE e.workspace_id = c.workspace_id
      AND e.candidate_id = c.id
      AND e.event_type IN ('approved', 'promotion_requested', 'promotion_succeeded', 'authority_readback_confirmed')
  )
  AND NOT EXISTS (
    SELECT 1
    FROM artifact_replica_location l
    WHERE l.workspace_id = c.workspace_id
      AND l.candidate_id = c.id
      AND l.location_class = 'nas-primary'
      AND l.state = 'verified'
      AND l.digest = c.digest
  )
ORDER BY c.created_at ASC
LIMIT @limit_rows;

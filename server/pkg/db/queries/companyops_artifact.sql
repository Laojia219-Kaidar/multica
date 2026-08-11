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
    candidate_object_ref, formal_artifact_ref, idempotency_key
) VALUES (
    @id, @workspace_id, @lineage_id, @sequence, @event_type,
    @candidate_id, @candidate_revision, @candidate_digest,
    @candidate_object_ref, sqlc.narg('formal_artifact_ref'), @idempotency_key
)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: GetArtifactEventByIdempotency :one
SELECT * FROM artifact_event
WHERE workspace_id = @workspace_id AND idempotency_key = @idempotency_key;

-- name: GetArtifactEvent :one
SELECT * FROM artifact_event
WHERE workspace_id = @workspace_id AND id = @id;

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

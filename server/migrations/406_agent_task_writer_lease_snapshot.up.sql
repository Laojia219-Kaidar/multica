-- Immutable task-level writer-lease authority captured at claim time.
-- migration 262 remains the lease-row/fencing authority; these columns only
-- preserve the target set and rollout mode that authorized a task claim.
ALTER TABLE agent_task_queue
    ADD COLUMN writer_lease_claim_mode TEXT,
    ADD COLUMN writer_lease_target_snapshot JSONB,
    ADD COLUMN writer_lease_target_digest TEXT;

ALTER TABLE agent_task_queue
    ADD CONSTRAINT agent_task_queue_writer_lease_claim_mode_check
        CHECK (writer_lease_claim_mode IS NULL OR writer_lease_claim_mode IN ('off', 'shadow', 'enforce')),
    ADD CONSTRAINT agent_task_queue_writer_lease_snapshot_array_check
        CHECK (writer_lease_target_snapshot IS NULL OR jsonb_typeof(writer_lease_target_snapshot) = 'array'),
    ADD CONSTRAINT agent_task_queue_writer_lease_snapshot_all_or_none_check
        CHECK (
            (writer_lease_claim_mode IS NULL AND writer_lease_target_snapshot IS NULL AND writer_lease_target_digest IS NULL)
            OR
            (writer_lease_claim_mode IS NOT NULL AND writer_lease_target_snapshot IS NOT NULL AND writer_lease_target_digest IS NOT NULL)
        ),
    ADD CONSTRAINT agent_task_queue_writer_lease_target_digest_check
        CHECK (writer_lease_target_digest IS NULL OR writer_lease_target_digest ~ '^[0-9a-f]{64}$');

ALTER TABLE agent_task_queue
    DROP CONSTRAINT IF EXISTS agent_task_queue_writer_lease_target_digest_check,
    DROP CONSTRAINT IF EXISTS agent_task_queue_writer_lease_snapshot_all_or_none_check,
    DROP CONSTRAINT IF EXISTS agent_task_queue_writer_lease_snapshot_array_check,
    DROP CONSTRAINT IF EXISTS agent_task_queue_writer_lease_claim_mode_check;

ALTER TABLE agent_task_queue
    DROP COLUMN IF EXISTS writer_lease_target_digest,
    DROP COLUMN IF EXISTS writer_lease_target_snapshot,
    DROP COLUMN IF EXISTS writer_lease_claim_mode;

DROP TABLE IF EXISTS artifact_promotion_delivery;
DROP FUNCTION IF EXISTS reject_artifact_promotion_delivery_delete();
DROP FUNCTION IF EXISTS reject_artifact_promotion_delivery_mutation();
ALTER TABLE artifact_promotion_claim
    DROP CONSTRAINT IF EXISTS artifact_promotion_claim_writer_target_digest_chk,
    DROP CONSTRAINT IF EXISTS artifact_promotion_claim_completion_receipt_digest_chk,
    DROP CONSTRAINT IF EXISTS artifact_promotion_claim_binding_all_or_none_chk,
    DROP COLUMN IF EXISTS source_task_id,
    DROP COLUMN IF EXISTS writer_lease_target_digest,
    DROP COLUMN IF EXISTS completion_receipt_digest;

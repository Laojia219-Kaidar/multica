-- C3b2: bind promotion claims to the completed task and durable lease receipt.
ALTER TABLE artifact_promotion_claim
    ADD COLUMN IF NOT EXISTS source_task_id UUID,
    ADD COLUMN IF NOT EXISTS writer_lease_target_digest TEXT,
    ADD COLUMN IF NOT EXISTS completion_receipt_digest TEXT;

ALTER TABLE artifact_promotion_claim
    ADD CONSTRAINT artifact_promotion_claim_writer_target_digest_chk
        CHECK (writer_lease_target_digest IS NULL OR writer_lease_target_digest ~ '^[0-9a-f]{64}$'),
    ADD CONSTRAINT artifact_promotion_claim_completion_receipt_digest_chk
        CHECK (completion_receipt_digest IS NULL OR completion_receipt_digest ~ '^sha256:[0-9a-f]{64}$'),
    ADD CONSTRAINT artifact_promotion_claim_binding_all_or_none_chk
        CHECK ((source_task_id IS NULL AND writer_lease_target_digest IS NULL AND completion_receipt_digest IS NULL)
            OR (source_task_id IS NOT NULL AND writer_lease_target_digest IS NOT NULL AND completion_receipt_digest IS NOT NULL));

-- No foreign keys: claims bind evidence by digest and remain independently auditable.
CREATE TABLE artifact_promotion_delivery (
    id                         UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id               UUID NOT NULL,
    promotion_id               TEXT NOT NULL,
    candidate_id               UUID NOT NULL,
    lineage_id                 UUID NOT NULL,
    source_task_id             UUID,
    writer_lease_target_digest TEXT,
    completion_receipt_digest  TEXT,
    payload_digest             TEXT NOT NULL,
    state                      TEXT NOT NULL DEFAULT 'pending',
    request_payload            JSONB NOT NULL,
    response_receipt           JSONB,
    readback_receipt           JSONB,
    attempt                    INTEGER NOT NULL DEFAULT 0,
    dispatch_token             UUID,
    lease_until                TIMESTAMPTZ,
    last_error                 TEXT,
    claimed_at                 TIMESTAMPTZ,
    completed_at               TIMESTAMPTZ,
    created_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT artifact_promotion_delivery_state_chk
        CHECK (state IN ('pending', 'dispatching', 'succeeded', 'readback_confirmed', 'failed')),
    CONSTRAINT artifact_promotion_delivery_promotion_id_chk
        CHECK (promotion_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'),
    CONSTRAINT artifact_promotion_delivery_payload_digest_chk
        CHECK (payload_digest ~ '^sha256:[0-9a-f]{64}$'),
    CONSTRAINT artifact_promotion_delivery_target_digest_chk
        CHECK (writer_lease_target_digest IS NULL OR writer_lease_target_digest ~ '^[0-9a-f]{64}$'),
    CONSTRAINT artifact_promotion_delivery_receipt_digest_chk
        CHECK (completion_receipt_digest IS NULL OR completion_receipt_digest ~ '^sha256:[0-9a-f]{64}$'),
    CONSTRAINT artifact_promotion_delivery_binding_all_or_none_chk
        CHECK ((source_task_id IS NULL AND writer_lease_target_digest IS NULL AND completion_receipt_digest IS NULL)
            OR (source_task_id IS NOT NULL AND writer_lease_target_digest IS NOT NULL AND completion_receipt_digest IS NOT NULL)),
    CONSTRAINT artifact_promotion_delivery_payload_object_chk
        CHECK (jsonb_typeof(request_payload) = 'object'),
    CONSTRAINT artifact_promotion_delivery_claim_token_chk
        CHECK ((state = 'dispatching' AND dispatch_token IS NOT NULL AND lease_until IS NOT NULL)
            OR (state <> 'dispatching' AND dispatch_token IS NULL AND lease_until IS NULL)),
    CONSTRAINT artifact_promotion_delivery_succeeded_receipt_chk
        CHECK ((state IN ('succeeded', 'readback_confirmed')) = (response_receipt IS NOT NULL)),
    CONSTRAINT artifact_promotion_delivery_readback_receipt_chk
        CHECK ((state = 'readback_confirmed') = (readback_receipt IS NOT NULL)),
    CONSTRAINT artifact_promotion_delivery_response_object_chk
        CHECK (response_receipt IS NULL OR jsonb_typeof(response_receipt) = 'object'),
    CONSTRAINT artifact_promotion_delivery_readback_object_chk
        CHECK (readback_receipt IS NULL OR jsonb_typeof(readback_receipt) = 'object'),
    CONSTRAINT artifact_promotion_delivery_state_receipt_chk
        CHECK (
            (state = 'pending' AND dispatch_token IS NULL AND lease_until IS NULL AND response_receipt IS NULL AND readback_receipt IS NULL)
            OR (state = 'dispatching' AND dispatch_token IS NOT NULL AND lease_until IS NOT NULL AND response_receipt IS NULL AND readback_receipt IS NULL)
            OR (state = 'failed' AND dispatch_token IS NULL AND lease_until IS NULL AND response_receipt IS NULL AND readback_receipt IS NULL)
            OR (state = 'succeeded' AND dispatch_token IS NULL AND lease_until IS NULL AND response_receipt IS NOT NULL AND readback_receipt IS NULL)
            OR (state = 'readback_confirmed' AND dispatch_token IS NULL AND lease_until IS NULL AND response_receipt IS NOT NULL AND readback_receipt IS NOT NULL)
        )
);

CREATE OR REPLACE FUNCTION reject_artifact_promotion_delivery_delete() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'artifact_promotion_delivery is durable and cannot be deleted';
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION reject_artifact_promotion_delivery_mutation() RETURNS trigger AS $$
BEGIN
    IF NEW.workspace_id IS DISTINCT FROM OLD.workspace_id
       OR NEW.promotion_id IS DISTINCT FROM OLD.promotion_id
       OR NEW.candidate_id IS DISTINCT FROM OLD.candidate_id
       OR NEW.lineage_id IS DISTINCT FROM OLD.lineage_id
       OR NEW.source_task_id IS DISTINCT FROM OLD.source_task_id
       OR NEW.writer_lease_target_digest IS DISTINCT FROM OLD.writer_lease_target_digest
       OR NEW.completion_receipt_digest IS DISTINCT FROM OLD.completion_receipt_digest
       OR NEW.payload_digest IS DISTINCT FROM OLD.payload_digest
       OR NEW.request_payload IS DISTINCT FROM OLD.request_payload
       OR (OLD.response_receipt IS NOT NULL AND NEW.response_receipt IS DISTINCT FROM OLD.response_receipt)
       OR (OLD.readback_receipt IS NOT NULL AND NEW.readback_receipt IS DISTINCT FROM OLD.readback_receipt)
    THEN
        RAISE EXCEPTION 'artifact_promotion_delivery immutable evidence cannot be rewritten';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER artifact_promotion_delivery_reject_delete
    BEFORE DELETE ON artifact_promotion_delivery
    FOR EACH ROW EXECUTE FUNCTION reject_artifact_promotion_delivery_delete();

CREATE TRIGGER artifact_promotion_delivery_reject_mutation
    BEFORE UPDATE ON artifact_promotion_delivery
    FOR EACH ROW EXECUTE FUNCTION reject_artifact_promotion_delivery_mutation();

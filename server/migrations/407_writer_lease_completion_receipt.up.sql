-- C3b2: immutable, digest-bound evidence for one completed writer-lease task.
-- No foreign keys: this receipt is an append-only operational evidence ledger.
CREATE TABLE writer_lease_completion_receipt (
    id                         UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id               UUID NOT NULL,
    task_id                    UUID NOT NULL,
    target_digest              TEXT NOT NULL,
    proof_snapshot              JSONB NOT NULL,
    proof_digest                TEXT NOT NULL,
    receipt_digest              TEXT NOT NULL,
    created_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT writer_lease_completion_receipt_target_digest_chk
        CHECK (target_digest ~ '^[0-9a-f]{64}$'),
    CONSTRAINT writer_lease_completion_receipt_snapshot_array_chk
        CHECK (jsonb_typeof(proof_snapshot) = 'array'),
    CONSTRAINT writer_lease_completion_receipt_proof_digest_chk
        CHECK (proof_digest ~ '^sha256:[0-9a-f]{64}$'),
    CONSTRAINT writer_lease_completion_receipt_digest_chk
        CHECK (receipt_digest ~ '^sha256:[0-9a-f]{64}$')
);

CREATE OR REPLACE FUNCTION reject_writer_lease_completion_receipt_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'writer_lease_completion_receipt is append-only';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER writer_lease_completion_receipt_reject_mutation
    BEFORE UPDATE OR DELETE ON writer_lease_completion_receipt
    FOR EACH ROW EXECUTE FUNCTION reject_writer_lease_completion_receipt_mutation();

-- Universal Work Registration receipt (HIVECREW-UNIVERSAL-DEVELOPMENT-ENTRY
-- OS V1, Phase-1 join >=400). One row per (workspace_id, dedupe_key): the full
-- idempotency anchor + actor/intent snapshot so exact replay returns the
-- original receipt including actor_identity (contract §4.3), which
-- project_lifecycle_receipt cannot hold. Append-only; never a second project
-- truth (Project/Issue/Task remain the execution projection).

CREATE TABLE work_registration_receipt (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id   uuid NOT NULL,
    work_ref       text NOT NULL,
    dedupe_key     text NOT NULL,
    payload_digest text NOT NULL,
    project_id     uuid,
    issue_id       uuid,
    task_id        uuid,
    decision       text NOT NULL,
    actor          jsonb NOT NULL DEFAULT '{}'::jsonb,
    intent         jsonb NOT NULL DEFAULT '{}'::jsonb,
    applied        boolean NOT NULL DEFAULT false,
    replayed       boolean NOT NULL DEFAULT false,
    created_at     timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT work_registration_receipt_idem_uidx UNIQUE (workspace_id, dedupe_key)
);

CREATE INDEX work_registration_receipt_work_ref_idx
    ON work_registration_receipt (workspace_id, work_ref);

-- Immutable ledger: no UPDATE/DELETE.
CREATE OR REPLACE FUNCTION reject_work_registration_receipt_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'work_registration_receipt is append-only';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER work_registration_receipt_reject_mutation
    BEFORE UPDATE OR DELETE ON work_registration_receipt
    FOR EACH ROW EXECUTE FUNCTION reject_work_registration_receipt_mutation();

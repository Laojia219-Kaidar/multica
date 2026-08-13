-- Append-only project lifecycle operation receipt (HIV-553 contract: receipts
-- are idempotency + audit evidence, never a second project truth).
--
-- One row per (workspace_id, idempotency_key): replaying the same key + same
-- digest returns the stored receipt; a different digest under the same key is
-- a conflict (409). Applied/replayed/blockers capture what actually happened.

CREATE TABLE project_lifecycle_receipt (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id     uuid NOT NULL,
    project_id       uuid NOT NULL,
    action           text NOT NULL,
    idempotency_key  text NOT NULL,
    payload_digest   text NOT NULL,
    before_status    text NOT NULL,
    after_status     text NOT NULL,
    task_id          uuid,
    issue_id         uuid,
    blockers         jsonb NOT NULL DEFAULT '[]'::jsonb,
    applied          boolean NOT NULL DEFAULT false,
    replayed         boolean NOT NULL DEFAULT false,
    created_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT project_lifecycle_receipt_idem_uidx UNIQUE (workspace_id, idempotency_key)
);

-- Immutable ledger: no UPDATE/DELETE.
CREATE OR REPLACE FUNCTION reject_project_lifecycle_receipt_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'project_lifecycle_receipt is append-only';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER project_lifecycle_receipt_reject_mutation
    BEFORE UPDATE OR DELETE ON project_lifecycle_receipt
    FOR EACH ROW EXECUTE FUNCTION reject_project_lifecycle_receipt_mutation();

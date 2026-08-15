-- Universal Work Registration event ledger (append-only). One row per
-- (workspace_id, work_ref, idempotency_key): same key + same payload replays
-- the stored event; a different payload under the same key is a conflict (409).
CREATE TABLE work_event (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    uuid NOT NULL,
    work_ref        text NOT NULL,
    session_id      text,
    run_id          text,
    event_type      text NOT NULL,
    event_payload   jsonb NOT NULL DEFAULT '{}'::jsonb,
    blocker_reason  text,
    receiver        text,
    idempotency_key text NOT NULL,
    occurred_at     timestamptz,
    observed_at     timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT work_event_idem_uidx UNIQUE (workspace_id, work_ref, idempotency_key)
);
CREATE INDEX work_event_work_ref_idx ON work_event (workspace_id, work_ref);

CREATE OR REPLACE FUNCTION reject_work_event_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'work_event is append-only';
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER work_event_reject_mutation
    BEFORE UPDATE OR DELETE ON work_event
    FOR EACH ROW EXECUTE FUNCTION reject_work_event_mutation();

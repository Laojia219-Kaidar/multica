-- This is an additive audit event. Application rollback must never update or
-- delete immutable approval_revoked rows. Schema rollback is rehearsal-only
-- and is safe only before any approval_revoked data exists; otherwise fail
-- without changing the ledger.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM artifact_event WHERE event_type = 'approval_revoked') THEN
        RAISE EXCEPTION 'cannot roll back approval_revoked schema while immutable audit rows exist';
    END IF;
END
$$;

ALTER TABLE artifact_event
    DROP CONSTRAINT IF EXISTS artifact_event_event_type_check;

ALTER TABLE artifact_event
    ADD CONSTRAINT artifact_event_event_type_check CHECK (event_type IN (
        'submitted',
        'changes_requested',
        'approved',
        'rejected',
        'promotion_requested',
        'promotion_succeeded',
        'promotion_failed',
        'authority_readback_confirmed'
    ));

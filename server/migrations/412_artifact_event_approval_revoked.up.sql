ALTER TABLE artifact_event
    DROP CONSTRAINT IF EXISTS artifact_event_event_type_check;

ALTER TABLE artifact_event
    ADD CONSTRAINT artifact_event_event_type_check CHECK (event_type IN (
        'submitted',
        'changes_requested',
        'approved',
        'rejected',
        'approval_revoked',
        'promotion_requested',
        'promotion_succeeded',
        'promotion_failed',
        'authority_readback_confirmed'
    ));

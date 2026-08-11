-- HiveCrew owns temporary artifact candidates and their review ledger. Formal
-- artifacts remain external authority references carried by lifecycle events.
-- Source attachment/comment ids are provenance snapshots only: deliberately no
-- foreign keys or cascades.
CREATE TABLE artifact_candidate (
    id                   UUID NOT NULL,
    workspace_id         UUID NOT NULL,
    lineage_id           UUID NOT NULL,
    revision             INT NOT NULL CHECK (revision > 0),
    supersedes_id        UUID,
    storage_key          TEXT NOT NULL,
    durable_object_ref   TEXT NOT NULL,
    digest               TEXT NOT NULL,
    filename             TEXT NOT NULL DEFAULT '',
    content_type         TEXT NOT NULL DEFAULT 'application/octet-stream',
    size_bytes           BIGINT NOT NULL DEFAULT 0 CHECK (size_bytes >= 0),
    source_attachment_id UUID,
    source_comment_id    UUID,
    idempotency_key      TEXT NOT NULL,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (
        (revision = 1 AND supersedes_id IS NULL)
        OR (revision > 1 AND supersedes_id IS NOT NULL)
    )
);

CREATE TABLE artifact_event (
    id                   UUID NOT NULL,
    workspace_id         UUID NOT NULL,
    lineage_id           UUID NOT NULL,
    sequence             INT NOT NULL CHECK (sequence > 0),
    event_type           TEXT NOT NULL CHECK (event_type IN (
        'submitted',
        'changes_requested',
        'approved',
        'rejected',
        'promotion_requested',
        'promotion_succeeded',
        'promotion_failed',
        'authority_readback_confirmed'
    )),
    candidate_id         UUID NOT NULL,
    candidate_revision   INT NOT NULL CHECK (candidate_revision > 0),
    candidate_digest     TEXT NOT NULL,
    candidate_object_ref TEXT NOT NULL,
    formal_artifact_ref  TEXT,
    idempotency_key      TEXT NOT NULL,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- This operational ledger fences object-store side effects; it is not an
-- artifact value or a second lifecycle truth.
CREATE TABLE artifact_materialization_intent (
    workspace_id         UUID NOT NULL,
    candidate_id         UUID NOT NULL,
    lineage_id           UUID NOT NULL,
    storage_key          TEXT NOT NULL,
    durable_object_ref   TEXT NOT NULL,
    digest               TEXT NOT NULL,
    filename             TEXT NOT NULL DEFAULT '',
    content_type         TEXT NOT NULL DEFAULT 'application/octet-stream',
    size_bytes           BIGINT NOT NULL DEFAULT 0 CHECK (size_bytes >= 0),
    source_attachment_id UUID,
    source_comment_id    UUID,
    idempotency_key      TEXT NOT NULL,
    state                TEXT NOT NULL DEFAULT 'pending'
        CHECK (state IN ('pending', 'cleanup_pending', 'tombstoned')),
    last_error           TEXT,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE FUNCTION reject_companyops_artifact_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION '% is immutable and append-only', TG_TABLE_NAME
        USING ERRCODE = '55000';
END;
$$;

CREATE TRIGGER artifact_candidate_reject_mutation
BEFORE UPDATE OR DELETE ON artifact_candidate
FOR EACH ROW EXECUTE FUNCTION reject_companyops_artifact_mutation();

CREATE TRIGGER artifact_event_reject_mutation
BEFORE UPDATE OR DELETE ON artifact_event
FOR EACH ROW EXECUTE FUNCTION reject_companyops_artifact_mutation();

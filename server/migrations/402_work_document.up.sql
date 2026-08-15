-- Universal Work Registration handoff/completion candidate documents
-- (append-only). kind = handoff | completion.
CREATE TABLE work_document (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id      uuid NOT NULL,
    work_ref          text NOT NULL,
    kind              text NOT NULL,
    package           jsonb NOT NULL DEFAULT '{}'::jsonb,
    routed_to_review  boolean NOT NULL DEFAULT false,
    created_at        timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT work_document_kind_check CHECK (kind IN ('handoff','completion')),
    CONSTRAINT work_document_uidx UNIQUE (workspace_id, work_ref, kind)
);

CREATE OR REPLACE FUNCTION reject_work_document_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'work_document is append-only';
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER work_document_reject_mutation
    BEFORE UPDATE OR DELETE ON work_document
    FOR EACH ROW EXECUTE FUNCTION reject_work_document_mutation();

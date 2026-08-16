-- Independent closure-package review record (HIV-553 contract gate 5: the
-- package must be independently reviewed before close). Append-only.
CREATE TABLE closure_package_review (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id     uuid NOT NULL,
    project_id       uuid NOT NULL,
    reviewer_user_id uuid NOT NULL,
    decision         text NOT NULL CHECK (decision IN ('approve', 'reject')),
    reviewed_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT closure_package_review_uidx UNIQUE (workspace_id, project_id, reviewer_user_id)
);

CREATE OR REPLACE FUNCTION reject_closure_package_review_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'closure_package_review is append-only';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER closure_package_review_reject_mutation
    BEFORE UPDATE OR DELETE ON closure_package_review
    FOR EACH ROW EXECUTE FUNCTION reject_closure_package_review_mutation();

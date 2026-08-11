-- A promotion claim durably binds a stable promotion_id to exactly one
-- (workspace, candidate, lineage) triple BEFORE the HiveCosm Formal Artifact
-- authority POST. The unique constraints enforce the binding:
--   (workspace_id, promotion_id)  — same id cannot claim two objects
--   (workspace_id, candidate_id)  — same object cannot carry two ids
-- No foreign keys: like the artifact ledger, this is an operational fence that
-- references canonical receipts without inheriting their lifecycle.
CREATE TABLE artifact_promotion_claim (
    workspace_id  UUID NOT NULL,
    promotion_id  TEXT NOT NULL,
    candidate_id  UUID NOT NULL,
    lineage_id    UUID NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX artifact_promotion_claim_promotion_uidx
    ON artifact_promotion_claim (workspace_id, promotion_id);

CREATE UNIQUE INDEX artifact_promotion_claim_candidate_uidx
    ON artifact_promotion_claim (workspace_id, candidate_id);

CREATE TRIGGER artifact_promotion_claim_reject_mutation
BEFORE UPDATE OR DELETE ON artifact_promotion_claim
FOR EACH ROW EXECUTE FUNCTION reject_companyops_artifact_mutation();

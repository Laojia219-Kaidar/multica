-- Legacy rows remain readable for fail-closed handling; all new claims must use
-- the lowercase canonical UUID form accepted by the repository boundary.
ALTER TABLE artifact_promotion_claim
    ADD CONSTRAINT artifact_promotion_claim_promotion_id_chk
    CHECK (promotion_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$') NOT VALID;

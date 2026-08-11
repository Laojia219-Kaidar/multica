-- NOT VALID avoids inventing a digest for legacy rows while enforcing a
-- canonical, non-NULL SHA-256 digest for every new or changed row immediately.
ALTER TABLE artifact_promotion_claim
    ADD CONSTRAINT artifact_promotion_claim_payload_digest_chk
    CHECK (payload_digest IS NOT NULL AND payload_digest ~ '^sha256:[0-9a-f]{64}$') NOT VALID;

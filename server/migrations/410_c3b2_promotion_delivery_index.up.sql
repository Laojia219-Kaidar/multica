CREATE UNIQUE INDEX CONCURRENTLY artifact_promotion_delivery_promotion_uidx
    ON artifact_promotion_delivery (workspace_id, promotion_id);

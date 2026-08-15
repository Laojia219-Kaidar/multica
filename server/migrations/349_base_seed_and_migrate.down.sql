UPDATE agent SET home_base_id = NULL, fallback_base_id = NULL;
DELETE FROM base WHERE code IN ('BASE-01','BASE-02','BASE-03','BASE-04','BASE-05');

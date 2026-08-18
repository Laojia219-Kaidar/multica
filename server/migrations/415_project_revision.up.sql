ALTER TABLE project
    ADD COLUMN revision BIGINT NOT NULL DEFAULT 1
        CHECK (revision > 0 AND revision <= 9007199254740991);

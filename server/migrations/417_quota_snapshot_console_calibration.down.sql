ALTER TABLE provider_quota_snapshot
    DROP CONSTRAINT IF EXISTS provider_quota_snapshot_source_check;

ALTER TABLE provider_quota_snapshot
    ADD CONSTRAINT provider_quota_snapshot_source_check
        CHECK (source IN ('task_usage', 'live_vendor', 'manual_cap', 'hivecosm'));

ALTER TABLE provider_quota_snapshot
    DROP CONSTRAINT IF EXISTS provider_quota_snapshot_window_kind_check;

ALTER TABLE provider_quota_snapshot
    ADD CONSTRAINT provider_quota_snapshot_window_kind_check
        CHECK (window_kind IN ('5h', '7d', 'monthly', 'daily'));

ALTER TABLE provider_quota_snapshot
    DROP COLUMN IF EXISTS percentage,
    DROP COLUMN IF EXISTS unit;

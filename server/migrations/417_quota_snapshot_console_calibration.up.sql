-- Console-calibrated quota snapshots: percent-only bars, credits/CNY units,
-- provider-specific window kinds (30d, mcp_monthly, credits, etc.).

ALTER TABLE provider_quota_snapshot
    ADD COLUMN IF NOT EXISTS percentage DOUBLE PRECISION
        CHECK (percentage IS NULL OR (percentage >= 0 AND percentage <= 100)),
    ADD COLUMN IF NOT EXISTS unit TEXT NOT NULL DEFAULT 'tokens'
        CHECK (unit IN ('tokens', 'percent', 'credits', 'cny'));

ALTER TABLE provider_quota_snapshot
    DROP CONSTRAINT IF EXISTS provider_quota_snapshot_window_kind_check;

ALTER TABLE provider_quota_snapshot
    ADD CONSTRAINT provider_quota_snapshot_window_kind_check
        CHECK (window_kind IN (
            '5h', '7d', '30d', 'monthly', 'daily', 'mcp_monthly',
            'credits', 'cny_balance', 'package', 'code_5h', 'code_7d',
            'session', 'unlimited', '30d_cost'
        ));

ALTER TABLE provider_quota_snapshot
    DROP CONSTRAINT IF EXISTS provider_quota_snapshot_source_check;

ALTER TABLE provider_quota_snapshot
    ADD CONSTRAINT provider_quota_snapshot_source_check
        CHECK (source IN ('task_usage', 'live_vendor', 'manual_cap', 'hivecosm', 'console'));

-- Lane D quota snapshots: live vendor observations, HiveCosm ingest, and
-- task_usage-derived window state. Manual caps remain in provider_usage_quota;
-- this table holds the latest observation per (plan, window_kind).

-- Extend manual-cap cycles to match operator windows (5h / 7d / monthly).
ALTER TABLE provider_usage_quota
    DROP CONSTRAINT IF EXISTS provider_usage_quota_cycle_check;

ALTER TABLE provider_usage_quota
    ADD CONSTRAINT provider_usage_quota_cycle_check
        CHECK (cycle IN ('5h', '7d', 'daily', 'weekly', 'monthly', 'never'));

-- One manual cap row per (workspace, provider, plan, account, cycle).
ALTER TABLE provider_usage_quota
    DROP CONSTRAINT IF EXISTS provider_usage_quota_workspace_id_provider_plan_account_label_key;

ALTER TABLE provider_usage_quota
    ADD CONSTRAINT provider_usage_quota_workspace_plan_account_cycle_key
        UNIQUE (workspace_id, provider, plan, account_label, cycle);

CREATE TABLE provider_quota_snapshot (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    provider TEXT NOT NULL,
    plan TEXT NOT NULL,
    account_label TEXT NOT NULL DEFAULT '',
    api_key_label TEXT NOT NULL DEFAULT '',
    window_kind TEXT NOT NULL
        CHECK (window_kind IN ('5h', '7d', 'monthly', 'daily')),
    limit_tokens BIGINT CHECK (limit_tokens IS NULL OR limit_tokens >= 0),
    used_tokens BIGINT NOT NULL DEFAULT 0 CHECK (used_tokens >= 0),
    remaining_tokens BIGINT CHECK (remaining_tokens IS NULL OR remaining_tokens >= 0),
    reset_at TIMESTAMPTZ,
    observed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    source TEXT NOT NULL
        CHECK (source IN ('task_usage', 'live_vendor', 'manual_cap', 'hivecosm')),
    source_ref TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, provider, plan, account_label, window_kind)
);

CREATE INDEX idx_provider_quota_snapshot_workspace ON provider_quota_snapshot (workspace_id);

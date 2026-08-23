DROP TABLE IF EXISTS provider_quota_snapshot;

ALTER TABLE provider_usage_quota
    DROP CONSTRAINT IF EXISTS provider_usage_quota_workspace_plan_account_cycle_key;

ALTER TABLE provider_usage_quota
    ADD CONSTRAINT provider_usage_quota_workspace_id_provider_plan_account_label_key
        UNIQUE (workspace_id, provider, plan, account_label);

ALTER TABLE provider_usage_quota
    DROP CONSTRAINT IF EXISTS provider_usage_quota_cycle_check;

ALTER TABLE provider_usage_quota
    ADD CONSTRAINT provider_usage_quota_cycle_check
        CHECK (cycle IN ('daily', 'weekly', 'monthly', 'never'));

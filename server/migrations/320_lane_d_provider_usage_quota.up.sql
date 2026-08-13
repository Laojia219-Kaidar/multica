-- Lane D (P3 usage aggregation + capacity routing): provider/plan quota table.
--
-- WHY A TABLE:
--   The usage page must render 周期/总额/已用/剩余/百分比/重置时间 per
--   provider/plan/billing account. "已用" (used) is derived from live
--   task_usage; "总额" (total), the cycle, and the reset anchor are operator
--   facts that no existing table stores. This table is the single home for
--   those facts. It is seeded EMPTY: an unmetered/unknown plan is rendered as
--   "配额未配置" rather than inventing a number.
--
-- WHY THE UNIQUE KEY IS (workspace, provider, plan, account) AND NOT THE
-- API-KEY LABEL:
--   Usage rows only know the billing account (derived from the runtime name),
--   never which concrete API key produced them. Matching a quota to usage is
--   therefore possible only on (provider, plan, account); api_key_label is a
--   display-only alias the operator sets, and it is not part of the key.
--
-- WHY API-KEY LABEL IS A LABEL ONLY:
--   Secrets are never persisted. api_key_label holds a human-facing identifier
--   (e.g. the alias the operator uses for the key in their own inventory). It
--   must never contain the key material. The page renders it verbatim as an
--   identifier and nothing reads it as a credential.
--
-- WHY LOCAL MODEL IS A FLAG:
--   Local models (e.g. a DGX checkpoint) still burn capacity slots but have no
--   provider billing plan. The flag lets the usage page bucket them without
--   pretending they have a paid token quota.
CREATE TABLE provider_usage_quota (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    provider TEXT NOT NULL,
    plan TEXT NOT NULL,
    account_label TEXT NOT NULL DEFAULT '',
    api_key_label TEXT NOT NULL DEFAULT '',
    cycle TEXT NOT NULL DEFAULT 'monthly'
        CHECK (cycle IN ('daily', 'weekly', 'monthly', 'never')),
    -- 总额 in tokens. 0 means "unmetered" (no hard limit).
    total_tokens BIGINT NOT NULL DEFAULT 0
        CHECK (total_tokens >= 0),
    -- Anchor for the reset boundary. daily/weekly ignore it; monthly uses
    -- 1..28 as the day-of-month boundary; 29/30/31 are deliberately rejected
    -- so the anchor never lands on a missing month day.
    reset_day INTEGER CHECK (reset_day BETWEEN 1 AND 28),
    local_model BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, provider, plan, account_label)
);

CREATE INDEX idx_provider_usage_quota_workspace ON provider_usage_quota (workspace_id);

# Provider usage quota seed (operator one-shot)

HiveCrew stores **manual package caps** in `provider_usage_quota` and **live observations**
in `provider_quota_snapshot`. Neither table contains secrets. Operator API keys stay on
the Mac Keychain (`hivecosm-model-*`); cloud/Linux hosts ingest observations via env poller
or `POST /api/company-ops/usage/quota-observation`.

## One-shot SQL seed

Replace `$WORKSPACE_ID` with the target workspace UUID, then run against the workspace DB:

```sql
-- Qwen Coding Plan (manual cap; no public remaining API)
INSERT INTO provider_usage_quota (workspace_id, provider, plan, account_label, api_key_label, cycle, total_tokens)
VALUES
  ('$WORKSPACE_ID', '阿里云 · Qwen', 'Qwen Coding Plan', 'qwen-coding', 'qwen-coding-1', '5h', 0),
  ('$WORKSPACE_ID', '阿里云 · Qwen', 'Qwen Coding Plan', 'qwen-coding', 'qwen-coding-1', '7d', 0),
  ('$WORKSPACE_ID', '阿里云 · Qwen', 'Qwen Coding Plan', 'qwen-coding', 'qwen-coding-1', 'monthly', 0)
ON CONFLICT (workspace_id, provider, plan, account_label, cycle) DO UPDATE SET
  api_key_label = EXCLUDED.api_key_label,
  total_tokens = EXCLUDED.total_tokens,
  updated_at = now();

-- GLM Coding / Zhipu (live vendor optional via QUOTA_POLL_ZHIPU_API_KEY)
INSERT INTO provider_usage_quota (workspace_id, provider, plan, account_label, api_key_label, cycle, total_tokens)
VALUES
  ('$WORKSPACE_ID', '智谱 · GLM', 'GLM API', 'secure zhipu', 'glm-coding-1', '5h', 0),
  ('$WORKSPACE_ID', '智谱 · GLM', 'GLM API', 'secure zhipu', 'glm-coding-1', '7d', 0),
  ('$WORKSPACE_ID', '智谱 · GLM', 'GLM API', 'secure zhipu', 'glm-coding-1', 'monthly', 0)
ON CONFLICT (workspace_id, provider, plan, account_label, cycle) DO UPDATE SET
  api_key_label = EXCLUDED.api_key_label,
  total_tokens = EXCLUDED.total_tokens,
  updated_at = now();

-- Doubao / Volcengine Agent
INSERT INTO provider_usage_quota (workspace_id, provider, plan, account_label, api_key_label, cycle, total_tokens)
VALUES
  ('$WORKSPACE_ID', '火山引擎 · Doubao', 'Volcengine Agent Plan', 'volcengine-agent', 'doubao-agent-1', '5h', 0),
  ('$WORKSPACE_ID', '火山引擎 · Doubao', 'Volcengine Agent Plan', 'volcengine-agent', 'doubao-agent-1', '7d', 0),
  ('$WORKSPACE_ID', '火山引擎 · Doubao', 'Volcengine Agent Plan', 'volcengine-agent', 'doubao-agent-1', 'monthly', 0)
ON CONFLICT (workspace_id, provider, plan, account_label, cycle) DO UPDATE SET
  api_key_label = EXCLUDED.api_key_label,
  total_tokens = EXCLUDED.total_tokens,
  updated_at = now();

-- MiniMax
INSERT INTO provider_usage_quota (workspace_id, provider, plan, account_label, api_key_label, cycle, total_tokens)
VALUES
  ('$WORKSPACE_ID', 'MiniMax', 'MiniMax API', 'secure minimax', 'minimax-1', '5h', 0),
  ('$WORKSPACE_ID', 'MiniMax', 'MiniMax API', 'secure minimax', 'minimax-1', '7d', 0)
ON CONFLICT (workspace_id, provider, plan, account_label, cycle) DO UPDATE SET
  api_key_label = EXCLUDED.api_key_label,
  total_tokens = EXCLUDED.total_tokens,
  updated_at = now();

-- Xiaomi MiMo (no public remaining API)
INSERT INTO provider_usage_quota (workspace_id, provider, plan, account_label, api_key_label, cycle, total_tokens)
VALUES
  ('$WORKSPACE_ID', '小米 · MiMo', 'MiMo API', 'secure mimo', 'mimo-1', '7d', 0),
  ('$WORKSPACE_ID', '小米 · MiMo', 'MiMo API', 'secure mimo', 'mimo-1', 'monthly', 0)
ON CONFLICT (workspace_id, provider, plan, account_label, cycle) DO UPDATE SET
  api_key_label = EXCLUDED.api_key_label,
  total_tokens = EXCLUDED.total_tokens,
  updated_at = now();

-- DeepSeek (cash balance only; monthly row is display-only)
INSERT INTO provider_usage_quota (workspace_id, provider, plan, account_label, api_key_label, cycle, total_tokens)
VALUES
  ('$WORKSPACE_ID', 'DeepSeek', 'DeepSeek API', 'secure deepseek', 'deepseek-1', 'monthly', 0)
ON CONFLICT (workspace_id, provider, plan, account_label, cycle) DO UPDATE SET
  api_key_label = EXCLUDED.api_key_label,
  total_tokens = EXCLUDED.total_tokens,
  updated_at = now();
```

Set `total_tokens` to the operator-known caps when available. `0` means unmetered until a
live observation or manual value is supplied.

## Live vendor poller (env, not Keychain)

Configure on the server host that may reach vendor APIs (typically the operator Mac wrapper
exporting env to the HiveCrew process):

| Env | Vendor |
|-----|--------|
| `QUOTA_POLL_MINIMAX_API_KEY` | MiniMax subscription key |
| `QUOTA_POLL_ZHIPU_API_KEY` | 智谱 API key |
| `QUOTA_POLL_VOLCENGINE_AK` / `QUOTA_POLL_VOLCENGINE_SK` | 火山 Ark AK/SK |
| `QUOTA_POLL_DEEPSEEK_API_KEY` | DeepSeek API key |

When set, `GET /api/company-ops/usage` refreshes snapshots before rendering. Keys are never
logged (`metrics.MaskSecret`).

## Mac-side ingest (POST)

A HiveCosm wrapper on the operator Mac can POST observations without storing secrets in git:

```http
POST /api/company-ops/usage/quota-observation
X-Workspace-Slug: <slug>
Content-Type: application/json

{
  "observations": [{
    "provider": "MiniMax",
    "plan": "MiniMax API",
    "account": "secure minimax",
    "api_key_label": "minimax-1",
    "window_kind": "5h",
    "limit_tokens": 1000000,
    "used_tokens": 200000,
    "remaining_tokens": 800000,
    "source": "live_vendor",
    "source_ref": "minimax:token_plan/remains",
    "observed_at": "2026-08-22T12:00:00Z"
  }]
}
```

## Digital employees

`GET /api/work/quota` returns the same aggregator as the usage page (5h / 7d / monthly windows,
sources, observed_at). Workspace member auth applies; **no** `RequireHumanActor`, so daemons
and PAT may call it beside `/api/work/mcp/*`.

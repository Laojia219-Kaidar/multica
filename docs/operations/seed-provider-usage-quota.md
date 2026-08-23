# Provider usage quota — console calibration ingest

HiveCrew treats **vendor console observations** as ground truth for remaining quota.
Local `task_usage` is only a partial slice (round 1: Zhipu 7d local ~103.7M vs console
337.2M ≈ 31%; Kimi/MiniMax/MiMo 7d local 0). Until coverage is high, **`source: console`
snapshots override local math** on `/{slug}/usage` and `GET /api/work/quota`.

No second usage page. Codex/Claude are out of v1 quota bars.

## POST console snapshots

```http
POST /api/company-ops/usage/quota-observation
X-Workspace-Slug: <slug>
Content-Type: application/json
```

```json
{
  "observations": [
    {
      "provider": "智谱 · GLM",
      "plan": "GLM Coding Max V1",
      "account": "secure zhipu",
      "window_kind": "5h",
      "percentage": 42.5,
      "unit": "percent",
      "source": "console",
      "source_ref": "zhipu:console:5h",
      "observed_at": "2026-08-23T04:00:00+08:00"
    },
    {
      "provider": "智谱 · GLM",
      "plan": "GLM Coding Max V1",
      "account": "secure zhipu",
      "window_kind": "7d",
      "limit_tokens": 465000000,
      "used_tokens": 337200000,
      "remaining_tokens": 127800000,
      "unit": "tokens",
      "source": "console",
      "source_ref": "zhipu:console:7d-tokens",
      "observed_at": "2026-08-23T04:00:00+08:00"
    },
    {
      "provider": "智谱 · GLM",
      "plan": "GLM Coding Max V1",
      "account": "secure zhipu",
      "window_kind": "mcp_monthly",
      "percentage": 18.0,
      "unit": "percent",
      "source": "console",
      "source_ref": "zhipu:console:mcp-monthly"
    }
  ]
}
```

**Zhipu Coding Max V1**: 5h % + MCP monthly %; **no weekly cap**; 7d absolute tokens on usage page.

### 阿里云百炼 Coding Pro

```json
{"provider":"阿里云百炼","plan":"Coding Pro","account":"qwen-coding","window_kind":"5h","percentage":12.0,"unit":"percent","source":"console","source_ref":"bailian:coding-pro:5h"}
{"provider":"阿里云百炼","plan":"Coding Pro","account":"qwen-coding","window_kind":"7d","percentage":55.0,"unit":"percent","source":"console","source_ref":"bailian:coding-pro:7d"}
{"provider":"阿里云百炼","plan":"Coding Pro","account":"qwen-coding","window_kind":"30d","percentage":71.0,"unit":"percent","source":"console","source_ref":"bailian:coding-pro:30d"}
```

### Token Plan Personal (7d % only)

```json
{"provider":"阿里云百炼","plan":"Token Plan Personal","account":"bailian-token-plan-personal","window_kind":"7d","percentage":100.0,"unit":"percent","source":"console","source_ref":"bailian:token-plan:7d"}
```

### 小米 MiMo Pro annual (Credits absolute)

```json
{"provider":"小米 · MiMo","plan":"MiMo Pro annual","account":"secure mimo","window_kind":"credits","limit_tokens":456000000000,"used_tokens":70400000000,"remaining_tokens":385600000000,"unit":"credits","source":"console","source_ref":"mimo:console:credits"}
```

### MiniMax TokenPlanPlus

```json
{"provider":"MiniMax","plan":"TokenPlanPlus","account":"secure minimax","window_kind":"5h","percentage":33.0,"unit":"percent","source":"console","source_ref":"minimax:console:5h"}
{"provider":"MiniMax","plan":"TokenPlanPlus","account":"secure minimax","window_kind":"unlimited","source":"console","source_ref":"minimax:console:weekly-unlimited"}
{"provider":"MiniMax","plan":"TokenPlanPlus","account":"secure minimax","window_kind":"credits","limit_tokens":1200000,"remaining_tokens":850000,"unit":"credits","source":"console","source_ref":"minimax:console:credits-balance"}
```

### 火山 Ark — **two sibling plans**

**Agent Plan** (`.../subscription/agent-plan`) — AFP tokens:

```json
{"provider":"火山引擎 · Doubao","plan":"Ark Agent Plan","account":"volcengine-agent","window_kind":"5h","limit_tokens":1000000,"used_tokens":200000,"remaining_tokens":800000,"unit":"tokens","source":"console","source_ref":"ark:agent:afp-5h"}
```

**Coding Plan** (`.../coding-plan`) — session/7d/30d percents:

```json
{"provider":"火山引擎 · Doubao","plan":"Ark Coding Plan","account":"volcengine-coding","window_kind":"session","percentage":15.0,"unit":"percent","source":"console","source_ref":"ark:coding:session"}
{"provider":"火山引擎 · Doubao","plan":"Ark Coding Plan","account":"volcengine-coding","window_kind":"7d","percentage":40.0,"unit":"percent","source":"console","source_ref":"ark:coding:7d"}
{"provider":"火山引擎 · Doubao","plan":"Ark Coding Plan","account":"volcengine-coding","window_kind":"30d","percentage":62.0,"unit":"percent","source":"console","source_ref":"ark:coding:30d"}
```

### DeepSeek (CNY + 30d stats, no package bars)

```json
{"provider":"DeepSeek","plan":"DeepSeek API","account":"secure deepseek","window_kind":"cny_balance","remaining_tokens":12800,"unit":"cny","source":"console","source_ref":"deepseek:console:cny"}
{"provider":"DeepSeek","plan":"DeepSeek API","account":"secure deepseek","window_kind":"30d_cost","used_tokens":10480000000,"unit":"tokens","source":"console","source_ref":"deepseek:console:30d-tokens"}
```

CNY values are stored in **cents** (`remaining_tokens` field reused as integer cents).

### Kimi — three surfaces

**(a) Membership Allegro** — package source of truth (`kimi.com/membership/...`):

```json
{"provider":"月之暗面 · Kimi","plan":"Kimi Membership Allegro","account":"membership","window_kind":"package","percentage":68.0,"unit":"percent","source":"console","source_ref":"kimi:membership:package"}
{"provider":"月之暗面 · Kimi","plan":"Kimi Membership Allegro","account":"membership","window_kind":"code_5h","percentage":22.0,"unit":"percent","source":"console","source_ref":"kimi:membership:code-5h"}
{"provider":"月之暗面 · Kimi","plan":"Kimi Membership Allegro","account":"membership","window_kind":"code_7d","percentage":41.0,"unit":"percent","source":"console","source_ref":"kimi:membership:code-7d"}
```

**(b) Code console only** (`kimi.com/code/console`):

```json
{"provider":"月之暗面 · Kimi","plan":"Kimi Code Console","account":"code-console","window_kind":"code_5h","percentage":22.0,"unit":"percent","source":"console","source_ref":"kimi:code-console:5h"}
```

**(c) Prepaid CNY** (`platform.kimi.com/console/account`):

```json
{"provider":"月之暗面 · Kimi","plan":"Kimi Prepaid CNY","account":"prepaid","window_kind":"cny_balance","remaining_tokens":50000,"unit":"cny","source":"console","source_ref":"kimi:prepaid:cny"}
```

## Digital employees

`GET /api/work/quota` returns the same window shapes (labels, units, sources,
`observed_at`). Workspace member auth; no `RequireHumanActor`.

## Optional env poller

Still supported but **not required** when the operator POSTs browser-console snapshots:

| Env | Vendor |
|-----|--------|
| `QUOTA_POLL_MINIMAX_API_KEY` | MiniMax |
| `QUOTA_POLL_ZHIPU_API_KEY` | 智谱 |
| `QUOTA_POLL_VOLCENGINE_AK` / `SK` | 火山 Ark |
| `QUOTA_POLL_DEEPSEEK_API_KEY` | DeepSeek balance |

Keys never go in git. Browser-session ingest via POST is the primary path.

## Manual cap fallback

`PUT /api/company-ops/usage/quota` still upserts operator caps when console
observations are absent. Console/live snapshots always win when fresh.

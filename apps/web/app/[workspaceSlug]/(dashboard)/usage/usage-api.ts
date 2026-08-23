import { getApi } from "@multica/core/api";

export interface QuotaWindowView {
  kind: string;
  label?: string;
  unit?: string;
  total_tokens?: number;
  used_tokens: number;
  remaining_tokens?: number;
  percentage?: number;
  reset_at?: string;
  source: string;
  observed_at?: string;
  unlimited?: boolean;
  local_only?: boolean;
}

export interface QuotaState {
  cycle: string;
  total_tokens?: number;
  used_tokens: number;
  remaining_tokens?: number;
  percentage?: number;
  reset_at?: string;
  reset_day?: number;
  local_model: boolean;
  windows?: QuotaWindowView[];
  source?: string;
  observed_at?: string;
}

export interface TaskUsage {
  task_id: string;
  issue_id?: string;
  model: string;
  used_tokens: number;
  cost_usd_ticks: number;
}

export interface ModelUsage {
  model: string;
  used_tokens: number;
  employee_count: number;
  task_count: number;
}

export interface EmployeeUsage {
  agent_id: string;
  name: string;
  used_tokens: number;
  models: ModelUsage[];
  tasks: TaskUsage[];
}

export interface PlanUsage {
  plan: string;
  account: string;
  api_key_label?: string;
  local_model: boolean;
  used_tokens: number;
  quota?: QuotaState | null;
  models: ModelUsage[];
  employees: EmployeeUsage[];
}

export interface ProviderUsage {
  provider: string;
  local_model: boolean;
  used_tokens: number;
  plans: PlanUsage[];
}

export interface UsageTotals {
  used_tokens: number;
  task_count: number;
  employee_count: number;
  plan_count: number;
  local_model_count: number;
}

export interface UsageHierarchy {
  workspace_id: string;
  since: string;
  generated_at: string;
  data_gaps: string[];
  totals: UsageTotals;
  providers: ProviderUsage[];
}

export interface ProviderUsageQuotaInput {
  provider: string;
  plan: string;
  account: string;
  api_key_label?: string;
  cycle: string;
  total_tokens: number;
  reset_day?: number;
  local_model?: boolean;
}

export async function fetchUsageHierarchy(
  slug: string,
  days: number,
): Promise<UsageHierarchy> {
  const api = getApi();
  const base = api.getBaseUrl();
  const res = await fetch(`${base}/api/company-ops/usage?days=${days}`, {
    headers: { "X-Workspace-Slug": slug },
    credentials: "include",
  });
  if (!res.ok) {
    let message = `API error: ${res.status} ${res.statusText}`;
    try {
      const body = (await res.json()) as { error?: string };
      if (body?.error) message = body.error;
    } catch {
      // keep the status message
    }
    throw new Error(message);
  }
  return (await res.json()) as UsageHierarchy;
}

export async function upsertProviderUsageQuota(
  slug: string,
  input: ProviderUsageQuotaInput,
): Promise<void> {
  const api = getApi();
  const base = api.getBaseUrl();
  const res = await fetch(`${base}/api/company-ops/usage/quota`, {
    method: "PUT",
    headers: {
      "Content-Type": "application/json",
      "X-Workspace-Slug": slug,
    },
    credentials: "include",
    body: JSON.stringify(input),
  });
  if (!res.ok) {
    let message = `API error: ${res.status} ${res.statusText}`;
    try {
      const body = (await res.json()) as { error?: string };
      if (body?.error) message = body.error;
    } catch {
      // keep the status message
    }
    throw new Error(message);
  }
}

export function quotaSourceLabel(source?: string): string {
  switch (source) {
    case "console":
      return "控制台";
    case "live_vendor":
      return "厂商 API";
    case "manual_cap":
      return "手动上限";
    case "hivecosm":
      return "HiveCosm";
    case "task_usage":
      return "本地 task_usage";
    default:
      return source ?? "—";
  }
}

export function windowKindLabel(kind: string, label?: string): string {
  if (label) return label;
  switch (kind) {
    case "5h":
      return "5 小时";
    case "7d":
      return "7 天";
    case "30d":
      return "30 天";
    case "monthly":
      return "每月";
    case "mcp_monthly":
      return "MCP 每月";
    case "credits":
      return "Credits";
    case "cny_balance":
      return "CNY 余额";
    case "package":
      return "套餐总量";
    case "code_5h":
      return "Code 5 小时";
    case "code_7d":
      return "Code 7 天";
    case "session":
      return "会话";
    case "unlimited":
      return "每周（不限）";
    case "30d_cost":
      return "30 天用量";
    default:
      return kind;
  }
}

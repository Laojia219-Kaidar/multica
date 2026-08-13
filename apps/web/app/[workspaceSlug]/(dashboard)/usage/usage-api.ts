import { getApi } from "@multica/core/api";

export interface QuotaState {
  cycle: string;
  total_tokens?: number;
  used_tokens: number;
  remaining_tokens?: number;
  percentage?: number;
  reset_at?: string;
  reset_day?: number;
  local_model: boolean;
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

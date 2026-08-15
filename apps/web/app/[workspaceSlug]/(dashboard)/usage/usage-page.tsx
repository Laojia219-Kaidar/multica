"use client";

import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, BarChart3, Building2, Cpu, Gauge, Users } from "lucide-react";
import { useWorkspaceId } from "@multica/core/hooks";
import { useRequiredWorkspaceSlug } from "@multica/core/paths";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@multica/ui/components/ui/card";
import { Progress } from "@multica/ui/components/ui/progress";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import {
  fetchUsageHierarchy,
  type EmployeeUsage,
  type ModelUsage,
  type PlanUsage,
  type ProviderUsage,
  type QuotaState,
} from "./usage-api";

const PERIODS = [
  { days: 7, label: "7d" },
  { days: 30, label: "30d" },
  { days: 90, label: "90d" },
] as const;

function formatTokens(value: number): string {
  if (value >= 1_000_000_000) return `${(value / 1_000_000_000).toFixed(2)}B`;
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(2)}M`;
  if (value >= 1_000) return `${(value / 1_000).toFixed(1)}K`;
  return String(value);
}

function formatResetAt(resetAt?: string): string {
  if (!resetAt) return "—";
  const date = new Date(resetAt);
  if (Number.isNaN(date.getTime())) return "—";
  return date.toLocaleString(undefined, {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  });
}

function cycleLabel(cycle: string): string {
  switch (cycle) {
    case "daily":
      return "每日";
    case "weekly":
      return "每周";
    case "monthly":
      return "每月";
    case "never":
      return "不限";
    default:
      return cycle;
  }
}

function QuotaCell({ quota }: { quota?: QuotaState | null }) {
  if (!quota) {
    return (
      <span className="text-xs text-muted-foreground" data-testid="quota-unconfigured">
        配额未配置
      </span>
    );
  }
  const hasLimit = typeof quota.total_tokens === "number" && quota.total_tokens > 0;
  const percentage = hasLimit ? Math.round((quota.percentage ?? 0) * 10) / 10 : null;
  return (
    <div className="space-y-1 text-xs">
      <div className="flex items-center gap-2">
        <span className="text-muted-foreground">{cycleLabel(quota.cycle)}</span>
        <span className="font-medium">{hasLimit ? formatTokens(quota.total_tokens!) : "不限量"}</span>
        <span className="text-muted-foreground">·</span>
        <span>已用 {formatTokens(quota.used_tokens)}</span>
        {hasLimit && <span>· 剩余 {formatTokens(quota.remaining_tokens ?? 0)}</span>}
      </div>
      {hasLimit ? (
        <div className="flex items-center gap-2">
          <Progress value={Math.min(quota.percentage ?? 0, 100)} className="h-1.5 flex-1" />
          <span className="w-14 text-right tabular-nums">{percentage}%</span>
        </div>
      ) : (
        <span className="text-muted-foreground">无硬性额度</span>
      )}
      <div className="text-muted-foreground">重置 {formatResetAt(quota.reset_at)}</div>
    </div>
  );
}

function ModelTable({ models }: { models: ModelUsage[] }) {
  if (models.length === 0) return null;
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-xs">
        <thead>
          <tr className="border-b text-left text-muted-foreground">
            <th className="py-1 pr-3 font-medium">模型</th>
            <th className="py-1 pr-3 text-right font-medium">Tokens</th>
            <th className="py-1 pr-3 text-right font-medium">员工</th>
            <th className="py-1 text-right font-medium">Task</th>
          </tr>
        </thead>
        <tbody>
          {models.map((m) => (
            <tr key={m.model} className="border-b last:border-0">
              <td className="py-1 pr-3 font-mono">{m.model || "未标记模型"}</td>
              <td className="py-1 pr-3 text-right tabular-nums">{formatTokens(m.used_tokens)}</td>
              <td className="py-1 pr-3 text-right tabular-nums">{m.employee_count}</td>
              <td className="py-1 text-right tabular-nums">{m.task_count}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function EmployeeSection({ employees }: { employees: EmployeeUsage[] }) {
  const [open, setOpen] = useState<string | null>(null);
  if (employees.length === 0) return null;
  return (
    <div className="space-y-1">
      {employees.map((e) => (
        <div key={e.agent_id} className="rounded border">
          <button
            type="button"
            className="flex w-full items-center justify-between px-3 py-1.5 text-left text-xs hover:bg-muted/50"
            onClick={() => setOpen(open === e.agent_id ? null : e.agent_id)}
          >
            <span className="font-medium">{e.name || e.agent_id}</span>
            <span className="tabular-nums text-muted-foreground">
              {formatTokens(e.used_tokens)} · {e.tasks.length} task
            </span>
          </button>
          {open === e.agent_id && (
            <div className="border-t px-3 py-2 space-y-1">
              {e.tasks.map((task) => (
                <div key={task.task_id} className="flex items-center gap-2 text-xs">
                  <span className="font-mono text-muted-foreground">{task.task_id.slice(0, 8)}</span>
                  <span className="font-mono">{task.model}</span>
                  <span className="ml-auto tabular-nums">{formatTokens(task.used_tokens)}</span>
                </div>
              ))}
            </div>
          )}
        </div>
      ))}
    </div>
  );
}

function PlanRow({ plan }: { plan: PlanUsage }) {
  return (
    <div className="border-b last:border-0 py-2" data-testid="plan-row">
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-medium">{plan.plan || "未命名套餐"}</span>
        {plan.local_model && (
          <Badge variant="outline">
            <Cpu className="h-3 w-3" /> 本地模型
          </Badge>
        )}
        <span className="text-xs text-muted-foreground">{plan.account}</span>
        {plan.api_key_label && (
          <span className="text-xs text-muted-foreground">API key · {plan.api_key_label}</span>
        )}
        <span className="ml-auto tabular-nums font-medium">{formatTokens(plan.used_tokens)}</span>
      </div>
      <div className="mt-1 pl-1">
        <QuotaCell quota={plan.quota} />
      </div>
      <div className="mt-2 space-y-2 pl-1">
        <ModelTable models={plan.models} />
        <EmployeeSection employees={plan.employees} />
      </div>
    </div>
  );
}

function ProviderCard({ provider }: { provider: ProviderUsage }) {
  const [open, setOpen] = useState(true);
  return (
    <Card data-testid="provider-card">
      <CardHeader className="py-3">
        <button
          type="button"
          className="flex w-full items-center gap-2 text-left"
          onClick={() => setOpen((v) => !v)}
        >
          <Building2 className="h-4 w-4 text-muted-foreground" />
          <CardTitle className="text-sm">{provider.provider}</CardTitle>
          {provider.local_model && (
            <Badge variant="outline">
              <Cpu className="h-3 w-3" /> 本地模型
            </Badge>
          )}
          <span className="ml-auto text-sm font-medium tabular-nums">
            {formatTokens(provider.used_tokens)}
          </span>
        </button>
      </CardHeader>
      {open && (
        <CardContent className="space-y-2 pt-0">
          {provider.plans.map((plan) => (
            <PlanRow key={`${plan.plan}:${plan.account}`} plan={plan} />
          ))}
        </CardContent>
      )}
    </Card>
  );
}

function DataGapBanner({ gaps }: { gaps: string[] }) {
  if (gaps.length === 0) return null;
  const messages: Record<string, string> = {
    usage_no_rows: "该周期内没有真实 token 用量记录（数据缺口，未伪造用量）。",
    quota_unconfigured: "部分或全部套餐尚未配置配额；未配置项不显示总额/剩余/百分比。",
  };
  return (
    <div
      className="flex items-start gap-2 rounded border border-amber-300 bg-amber-50 px-3 py-2 text-xs text-amber-900"
      data-testid="data-gap-banner"
    >
      <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" />
      <div>
        {gaps.map((g) => (
          <div key={g}>{messages[g] ?? g}</div>
        ))}
      </div>
    </div>
  );
}

export function UsagePage() {
  const wsId = useWorkspaceId();
  const slug = useRequiredWorkspaceSlug();
  const [days, setDays] = useState<number>(30);

  const query = useQuery({
    queryKey: ["lane-d", "usage", wsId, slug, days],
    queryFn: () => fetchUsageHierarchy(slug, days),
    enabled: !!wsId && !!slug,
    staleTime: 30_000,
  });

  const data = useMemo(() => query.data ?? null, [query.data]);

  return (
    <div className="space-y-4">
      <div className="flex items-center gap-2 border-b pb-3">
        <BarChart3 className="h-4 w-4 text-muted-foreground" />
        <h1 className="text-sm font-semibold">用量</h1>
        <div className="ml-auto flex items-center gap-1">
          {PERIODS.map((p) => (
            <Button
              key={p.days}
              variant={days === p.days ? "secondary" : "ghost"}
              size="sm"
              onClick={() => setDays(p.days)}
            >
              {p.label}
            </Button>
          ))}
        </div>
      </div>

      <DataGapBanner gaps={data?.data_gaps ?? []} />

      <div className="grid grid-cols-2 gap-3 md:grid-cols-5">
        {[
          { label: "Tokens 已用", value: data?.totals.used_tokens, icon: Gauge },
          { label: "Task", value: data?.totals.task_count, icon: BarChart3 },
          { label: "员工", value: data?.totals.employee_count, icon: Users },
          { label: "套餐", value: data?.totals.plan_count, icon: Building2 },
          { label: "本地模型", value: data?.totals.local_model_count, icon: Cpu },
        ].map((kpi) => (
          <Card key={kpi.label}>
            <CardContent className="flex items-center gap-2 py-3">
              <kpi.icon className="h-4 w-4 text-muted-foreground" />
              <div>
                <div className="text-xs text-muted-foreground">{kpi.label}</div>
                <div className="text-lg font-semibold tabular-nums">
                  {query.isPending ? (
                    <Skeleton className="h-5 w-16" />
                  ) : typeof kpi.value === "number" ? (
                    kpi.label.startsWith("Tokens") ? formatTokens(kpi.value) : kpi.value
                  ) : (
                    "—"
                  )}
                </div>
              </div>
            </CardContent>
          </Card>
        ))}
      </div>

      {query.isError && (
        <div
          className="rounded border border-red-300 bg-red-50 px-3 py-2 text-xs text-red-900"
          data-testid="usage-error"
        >
          用量加载失败：{query.error instanceof Error ? query.error.message : String(query.error)}
        </div>
      )}

      {query.isPending && <Skeleton className="h-40 w-full" />}

      {data && data.providers.length === 0 && !query.isPending && (
        <div className="rounded border px-4 py-8 text-center text-sm text-muted-foreground">
          该周期内暂无用量数据。
        </div>
      )}

      <div className="space-y-3">
        {data?.providers.map((provider) => (
          <ProviderCard key={provider.provider} provider={provider} />
        ))}
      </div>
    </div>
  );
}

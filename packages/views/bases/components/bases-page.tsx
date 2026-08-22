/* eslint-disable i18next/no-literal-string -- bounded Owner workbench surface: governance copy (registry banner, cockpit projection, unregistered notice) stays verbatim; locale keys are out of this work order's write scope */
"use client";

import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronRight, Network, Server } from "lucide-react";
import { toast } from "sonner";
import { useWorkspaceId } from "@multica/core/hooks";
import { api } from "@multica/core/api";
import type { CockpitProjection } from "@multica/core/api";
import { runtimeListOptions } from "@multica/core/runtimes/queries";
import { agentListOptions } from "@multica/core/workspace/queries";
import type { Agent } from "@multica/core/types";
import {
  buildRuntimeMachines,
  splitRuntimeName,
  type RuntimeMachine,
} from "../../runtimes/components/runtime-machines";
import { CollectionPageHeader } from "../../layout/collection-page";
import { useT } from "../../i18n";

type CompanyBase = Awaited<ReturnType<typeof api.getCompanyBases>>[number];

/** 驾驶舱投影是 DGX 专属能力：注册表编码 BASE-06（迁移 371/374），不用显示名判断。 */
const BASE_CODE_DGX = "BASE-06";

const SECURE_PREFIX = "HiveCosm Secure ";

/**
 * 观测 Runtime 机器映射到基地的唯一权威：正式基地注册表的
 * `machine_title`（GET /api/bases/company）。只接受两种形态——
 * 与 `machine_title` 完全相等，或既有的中点后缀形态
 * `machine_title · device detail`。括号后缀、任意前缀、子串与
 * 关键词推断一律不作为权威；多条注册项同时命中时取最长
 * `machine_title`，保证最具体的注册项胜出。
 */
function matchRegisteredBase(
  machineTitle: string,
  registry: readonly CompanyBase[],
): CompanyBase | null {
  let match: CompanyBase | null = null;
  for (const base of registry) {
    const title = base.machine_title;
    if (!title) continue;
    if (machineTitle !== title && !machineTitle.startsWith(`${title} · `)) continue;
    if (!match || title.length > match.machine_title.length) match = base;
  }
  return match;
}

/** 从 runtime 名提取 Secure 配置档（如 deepseek / qwen-coding / zhipu）。 */
function secureProfile(name: string): string | null {
  const { base } = splitRuntimeName(name);
  if (!base.startsWith(SECURE_PREFIX)) return null;
  return base.slice(SECURE_PREFIX.length);
}

/** 驾驶舱投影行：一段 1421 只读快照的要点摘要。 */
function CockpitSectionRow({
  label,
  section,
  picks,
}: {
  label: string;
  section: CockpitProjection["sections"]["health_surface"] | undefined;
  picks: string[];
}) {
  const healthy = section?.ok;
  return (
    <div className="flex items-start gap-2 text-xs">
      <span className={"mt-0.5 inline-block size-1.5 shrink-0 rounded-full " + (healthy ? "bg-emerald-500" : "bg-red-400")} />
      <div className="min-w-0 flex-1">
        <span className="text-muted-foreground">{label}</span>
        {section?.summary ? (
          <span className="ml-1 font-medium">
            {picks
              .filter((k) => section.summary?.[k] !== undefined && section.summary?.[k] !== null)
              .map((k) => `${k === "total_agents" ? "agents" : k.replace(/_/g, " ")}: ${String(section.summary?.[k])}`)
              .join(" · ")}
          </span>
        ) : section?.error ? (
          <span className="ml-1 text-red-400" title={section.error}>不可达</span>
        ) : null}
      </div>
    </div>
  );
}

/** 底座基地卡片专属：1421 驾驶舱只读投影区块。 */
function CockpitProjectionBlock({ cockpit }: { cockpit?: CockpitProjection }) {
  const s = cockpit?.sections;
  const universe = s?.agent_universe?.summary as Record<string, unknown> | undefined;
  return (
    <div className="mt-2 rounded-md border bg-surface-muted/50 p-2.5">
      <div className="mb-1.5 flex items-center justify-between">
        <span className="text-xs font-semibold">1421 驾驶舱投影（只读）</span>
        <span className="text-[10px] text-muted-foreground">
          {cockpit?.fetched_at ? new Date(cockpit.fetched_at).toLocaleTimeString("zh-CN", { hour12: false }) : "—"}
        </span>
      </div>
      <div className="space-y-1">
        <CockpitSectionRow label="健康面" section={s?.health_surface} picks={[]} />
        <CockpitSectionRow label="运行拓扑" section={s?.runtime_topology} picks={["services", "exposed_routes", "session_stores"]} />
        <CockpitSectionRow
          label="员工宇宙"
          section={s?.agent_universe}
          picks={["total_agents", "dispatch_enabled", "hermes_port_ready"]}
        />
        <CockpitSectionRow label="世界入口" section={s?.world_entry_snapshot} picks={["current_truth_nodes", "runtime_routes", "issues"]} />
      </div>
      {universe && (universe.virtual_worker_ready !== undefined || universe.holdout !== undefined) ? (
        <p className="mt-1.5 text-[10px] text-muted-foreground">
          virtual worker ready: {String(universe.virtual_worker_ready ?? 0)} · holdout: {String(universe.holdout ?? 0)}
        </p>
      ) : null}
    </div>
  );
}

export function BasesPage() {
  const wsId = useWorkspaceId();
  const { t } = useT("bases");
  const qc = useQueryClient();
  const { data: runtimes = [], isLoading } = useQuery(runtimeListOptions(wsId));
  const { data: agents = [] } = useQuery(agentListOptions(wsId));
  const [expanded, setExpanded] = useState<string | null>(null);

  // 驾驶舱联邦（只读投影）：底座基地卡片展开时拉取 DGX 1421 owner cockpit
  // 聚合快照；后端 30s 缓存，这里 60s 轮询 + 窗口聚焦刷新已足够。
  const { data: cockpit } = useQuery<CockpitProjection>({
    queryKey: ["bases", "cockpit-projection"],
    queryFn: () => api.getCockpitProjection(),
    refetchInterval: 60_000,
    retry: 1,
  });

  const migrateMutation = useMutation({
    mutationFn: ({ agentId, runtimeId }: { agentId: string; runtimeId: string }) =>
      api.updateAgent(agentId, { runtime_id: runtimeId }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: agentListOptions(wsId).queryKey });
      void qc.invalidateQueries({ queryKey: runtimeListOptions(wsId).queryKey });
      toast.success(t(($) => $.migrateSuccess));
    },
    onError: () => toast.error(t(($) => $.migrateFailed)),
  });

  const { data: baseList = [] } = useQuery({
    queryKey: ["bases", wsId],
    queryFn: () => api.listBases(),
  });
  const { data: companyBases = [] } = useQuery({
    queryKey: ["bases-company", wsId],
    queryFn: () => api.getCompanyBases(),
  });
  const drainedMap = useMemo(
    () => new Map(baseList.map((b) => [b.machine_title, b.drained])),
    [baseList],
  );

  const drainMutation = useMutation({
    mutationFn: ({ machineTitle, mode }: { machineTitle: string; mode: "resting" | "active" }) =>
      api.setBaseOperationalMode(machineTitle, mode),
    onSuccess: (data) => {
      void qc.invalidateQueries({ queryKey: ["bases", wsId] });
      void qc.invalidateQueries({ queryKey: agentListOptions(wsId).queryKey });
      toast.success(data.mode === "resting" ? t(($) => $.drainSuccess) : t(($) => $.resumeSuccess));
    },
    onError: () => toast.error(t(($) => $.drainFailed)),
  });

  const bases = useMemo(() => {
    const machines = buildRuntimeMachines(runtimes, { now: Date.now() });
    const runtimeToMachine = new Map<string, RuntimeMachine>();
    machines.forEach((m) => m.runtimes.forEach((r) => runtimeToMachine.set(r.id, m)));
    const agentsByMachine = new Map<string, Agent[]>();
    for (const a of agents) {
      const m = runtimeToMachine.get(a.runtime_id);
      if (!m) continue;
      const list = agentsByMachine.get(m.id) ?? [];
      list.push(a);
      agentsByMachine.set(m.id, list);
    }
    return machines.map((m) => ({
      machine: m,
      // 唯一映射权威是正式注册表 machine_title；不做任何模糊推断。
      registeredBase: matchRegisteredBase(m.title, companyBases),
      employees: agentsByMachine.get(m.id) ?? [],
      runtimeCount: m.runtimes.length,
    }));
  }, [runtimes, agents, companyBases]);

  function migrate(agent: Agent, targetMachine: RuntimeMachine) {
    const current = runtimes.find((r) => r.id === agent.runtime_id);
    const profile = current ? secureProfile(current.name) : null;
    if (!profile) {
      toast.error(t(($) => $.noTargetRuntime));
      return;
    }
    const targetRuntime = targetMachine.runtimes.find(
      (r) => splitRuntimeName(r.name).base === `${SECURE_PREFIX}${profile}`,
    );
    if (!targetRuntime) {
      toast.error(t(($) => $.noTargetRuntime));
      return;
    }
    migrateMutation.mutate({ agentId: agent.id, runtimeId: targetRuntime.id });
  }

  return (
    <div className="flex h-full flex-col">
      <CollectionPageHeader
        icon={Network}
        title={t(($) => $.title)}
        description={t(($) => $.description)}
      />
      {companyBases.length > 0 && (
        <div className="px-4 pt-4">
          <div className="rounded-lg border bg-card p-4 shadow-sm">
            <h3 className="text-sm font-semibold">正式基地注册表（决策 A 迁移）</h3>
            <div className="mt-2 flex flex-wrap gap-2">
              {companyBases.map((b) => (
                <span key={b.id} className="rounded-md border bg-surface-muted px-2 py-1 text-xs">
                  {b.name} · {b.device} · <span className="font-medium">{b.agents}</span> 员工
                </span>
              ))}
            </div>
          </div>
        </div>
      )}
      {isLoading ? (
        <div className="p-4 text-sm text-muted-foreground">{t(($) => $.loading)}</div>
      ) : (
        <div className="grid grid-cols-1 gap-4 p-4 sm:grid-cols-2 xl:grid-cols-3">
          {bases.map(({ machine, registeredBase, employees, runtimeCount }) => {
            const isOpen = expanded === machine.id;
            // 迁移目标只包含注册基地；未注册机器既不是目标也不发起迁移。
            const migrationTargets = bases.filter(
              (b) => b.registeredBase !== null && b.machine.id !== machine.id,
            );
            if (!registeredBase) {
              // 未匹配注册表的观测机器：保留为显式的通用未注册基地，
              // 不显示基地状态统计，也不授予排水/恢复/迁移权限。
              return (
                <div key={machine.id} className="rounded-lg border bg-card shadow-sm">
                  <div className="flex w-full items-center gap-2 p-4 text-left">
                    <Network className="size-5 shrink-0 text-muted-foreground" />
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center gap-2">
                        <h3 className="truncate text-base font-semibold">{machine.title}</h3>
                        <span className="shrink-0 rounded border px-1.5 py-0.5 text-[10px] text-muted-foreground">
                          未注册基地
                        </span>
                      </div>
                      <p className="mt-0.5 truncate text-xs text-muted-foreground">{machine.title}</p>
                    </div>
                  </div>
                  <p className="border-t px-4 py-3 text-xs text-muted-foreground">
                    该机器未登记于正式基地注册表：仅保留可见性，不显示基地状态统计，不参与排水/恢复与迁移。
                  </p>
                </div>
              );
            }
            // 排水/恢复以注册表 machine_title 为键，中点后缀形态同样命中。
            const drained = drainedMap.get(registeredBase.machine_title) ?? false;
            return (
              <div key={machine.id} className="rounded-lg border bg-card shadow-sm">
                <button
                  type="button"
                  onClick={() => setExpanded(isOpen ? null : machine.id)}
                  className="flex w-full items-center gap-2 p-4 text-left"
                  aria-expanded={isOpen}
                >
                  <Server className="size-5 shrink-0 text-muted-foreground" />
                  <div className="min-w-0 flex-1">
                    <h3 className="truncate text-base font-semibold">{registeredBase.name}</h3>
                    <p className="mt-0.5 truncate text-xs text-muted-foreground">{machine.title}</p>
                  </div>
                  <ChevronRight
                    className={"size-4 shrink-0 text-muted-foreground transition-transform " + (isOpen ? "rotate-90" : "")}
                  />
                </button>
                <div className="grid grid-cols-2 gap-2 px-4 pb-3 text-sm">
                  <div>
                    <div className="text-xs text-muted-foreground">{t(($) => $.runtimeOnline)}</div>
                    <div className="font-medium">{machine.onlineCount} / {runtimeCount}</div>
                  </div>
                  <div>
                    <div className="text-xs text-muted-foreground">{t(($) => $.employees)}</div>
                    <div className="font-medium">{employees.length}</div>
                  </div>
                  <div>
                    <div className="text-xs text-muted-foreground">{t(($) => $.running)}</div>
                    <div className="font-medium">{machine.runningCount}</div>
                  </div>
                  <div>
                    <div className="text-xs text-muted-foreground">{t(($) => $.status)}</div>
                    <div className={"font-medium " + (machine.onlineCount > 0 ? "text-emerald-600" : "text-red-500")}>
                      {machine.onlineCount > 0 ? t(($) => $.online) : t(($) => $.offline)}
                    </div>
                  </div>
                </div>
                <div className="flex items-center justify-between border-t px-4 py-2">
                  <span className="text-xs text-muted-foreground">
                    {drained ? t(($) => $.drained) : t(($) => $.active)}
                  </span>
                  <button
                    type="button"
                    onClick={() =>
                      drainMutation.mutate({
                        machineTitle: registeredBase.machine_title,
                        mode: drained ? "active" : "resting",
                      })
                    }
                    disabled={drainMutation.isPending}
                    className={
                      "rounded-md border px-2 py-1 text-xs transition-colors " +
                      (drained
                        ? "border-emerald-300 text-emerald-700 hover:bg-emerald-50"
                        : "border-amber-300 text-amber-700 hover:bg-amber-50")
                    }
                  >
                    {drained ? t(($) => $.resume) : t(($) => $.drain)}
                  </button>
                </div>
                {isOpen ? (
                  <div className="border-t px-4 py-3">
                    {employees.length === 0 ? (
                      <p className="text-xs text-muted-foreground">{t(($) => $.noEmployees)}</p>
                    ) : (
                      <ul className="space-y-2">
                        {employees.map((agent) => (
                          <li key={agent.id} className="flex items-center gap-2 text-sm">
                            <div className="min-w-0 flex-1">
                              <div className="truncate font-medium">{agent.name}</div>
                              <div className="truncate text-xs text-muted-foreground">
                                {t(($) => $.model)}: {agent.model}
                              </div>
                            </div>
                            <select
                              className="h-8 rounded-md border bg-background px-2 text-xs"
                              value=""
                              disabled={migrateMutation.isPending}
                              onChange={(e) => {
                                if (!e.target.value) return;
                                const target = migrationTargets.find((b) => b.machine.title === e.target.value);
                                if (target) migrate(agent, target.machine);
                              }}
                            >
                              <option value="" disabled>{t(($) => $.migrateTo)}</option>
                              {migrationTargets.map((b) => (
                                <option key={b.machine.id} value={b.machine.title}>
                                  {b.registeredBase?.name ?? b.machine.title}
                                </option>
                              ))}
                            </select>
                          </li>
                        ))}
                      </ul>
                    )}
                    {registeredBase.code === BASE_CODE_DGX ? <CockpitProjectionBlock cockpit={cockpit} /> : null}
                  </div>
                ) : null}
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}

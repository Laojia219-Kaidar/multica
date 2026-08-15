"use client";

import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronRight, Database, Monitor, Network, Server, Wrench } from "lucide-react";
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

/** 基地（base）= 一台受管理的物理执行机器，当前以 device_info 机器标题识别。 */
const KNOWN_BASES: { prefix: string; name: string; role: string; icon: typeof Monitor }[] = [
  { prefix: "HiveCosm Mac mini", name: "中枢基地", role: "控制面 / 调度 / 巡检 / 记忆 / 回退", icon: Server },
  { prefix: "HiveCrew MBP M5X", name: "工程基地", role: "架构 / 全栈 / 后端 / 数据库 / 平台 / 运维", icon: Monitor },
  { prefix: "HiveCrew MBP M4", name: "产品基地", role: "产品 / UIUX / 前端 / 客户端 / 消息批处理", icon: Monitor },
  { prefix: "HiveCrew MBA M4", name: "质量基地", role: "测试 / 独立审查 / 风险 / 返修集成", icon: Wrench },
  { prefix: "HiveCrew MB M2", name: "研究基地", role: "调研 / 知识工程 / 研究分析", icon: Monitor },
  { prefix: "HiveCosm DGX Spark", name: "底座基地", role: "开发母库 / 本地 27B 推理 / 敏感业务（合同·财务）", icon: Database },
  { prefix: "HiveCosm NAS HiveData", name: "存储基地", role: "归档 / 备份 / 数据集 / 冷存储", icon: Database },
];

const SECURE_PREFIX = "HiveCosm Secure ";

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
    return machines.map((m) => {
      const known = KNOWN_BASES.find((b) => m.title.startsWith(b.prefix));
      return {
        machine: m,
        baseName: known?.name ?? null,
        role: known?.role ?? null,
        icon: known?.icon ?? Network,
        employees: agentsByMachine.get(m.id) ?? [],
        registered: m.runtimes.length,
      };
    });
  }, [runtimes, agents]);

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
          {bases.map(({ machine, baseName, role, icon: Icon, employees, registered }) => {
            const isOpen = expanded === machine.id;
            const otherBases = bases.filter((b) => b.machine.id !== machine.id);
            return (
              <div key={machine.id} className="rounded-lg border bg-card shadow-sm">
                <button
                  type="button"
                  onClick={() => setExpanded(isOpen ? null : machine.id)}
                  className="flex w-full items-center gap-2 p-4 text-left"
                  aria-expanded={isOpen}
                >
                  <Icon className="size-5 text-muted-foreground" />
                  <div className="min-w-0 flex-1">
                    <h3 className="text-base font-semibold">{baseName ?? machine.title}</h3>
                    <p className="mt-0.5 truncate text-xs text-muted-foreground">{machine.title}</p>
                  </div>
                  <ChevronRight
                    className={"size-4 shrink-0 text-muted-foreground transition-transform " + (isOpen ? "rotate-90" : "")}
                  />
                </button>
                {role ? <p className="px-4 text-xs text-muted-foreground">{role}</p> : null}
                <div className="grid grid-cols-2 gap-2 px-4 pb-3 text-sm">
                  <div>
                    <div className="text-xs text-muted-foreground">{t(($) => $.runtimeOnline)}</div>
                    <div className="font-medium">{machine.onlineCount} / {registered}</div>
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
                {baseName ? (
                  <div className="flex items-center justify-between border-t px-4 py-2">
                    <span className="text-xs text-muted-foreground">
                      {(drainedMap.get(machine.title) ?? false) ? t(($) => $.drained) : t(($) => $.active)}
                    </span>
                    <button
                      type="button"
                      onClick={() =>
                        drainMutation.mutate({
                          machineTitle: machine.title,
                          mode: (drainedMap.get(machine.title) ?? false) ? "active" : "resting",
                        })
                      }
                      disabled={drainMutation.isPending}
                      className={
                        "rounded-md border px-2 py-1 text-xs transition-colors " +
                        ((drainedMap.get(machine.title) ?? false)
                          ? "border-emerald-300 text-emerald-700 hover:bg-emerald-50"
                          : "border-amber-300 text-amber-700 hover:bg-amber-50")
                      }
                    >
                      {(drainedMap.get(machine.title) ?? false) ? t(($) => $.resume) : t(($) => $.drain)}
                    </button>
                  </div>
                ) : null}
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
                                const target = otherBases.find((b) => b.machine.title === e.target.value);
                                if (target) migrate(agent, target.machine);
                              }}
                            >
                              <option value="" disabled>{t(($) => $.migrateTo)}</option>
                              {otherBases.map((b) => (
                                <option key={b.machine.id} value={b.machine.title}>
                                  {b.baseName ?? b.machine.title}
                                </option>
                              ))}
                            </select>
                          </li>
                        ))}
                      </ul>
                    )}
                    {baseName === "底座基地" ? <CockpitProjectionBlock cockpit={cockpit} /> : null}
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

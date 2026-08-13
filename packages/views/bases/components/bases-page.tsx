"use client";

import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { Monitor, Network, Server, Wrench } from "lucide-react";
import { useWorkspaceId } from "@multica/core/hooks";
import { runtimeListOptions } from "@multica/core/runtimes/queries";
import { agentListOptions } from "@multica/core/workspace/queries";
import { buildRuntimeMachines } from "../../runtimes/components/runtime-machines";
import { CollectionPageHeader } from "../../layout/collection-page";
import { useT } from "../../i18n";

/** 基地（base）= 一台受管理的物理执行机器，当前以 device_info 机器标题识别。 */
const KNOWN_BASES: { prefix: string; name: string; role: string; icon: typeof Monitor }[] = [
  { prefix: "HiveCosm Mac mini", name: "中枢基地", role: "控制面 / 调度 / 巡检 / 记忆 / 回退", icon: Server },
  { prefix: "HiveCrew MBP M5X", name: "工程基地", role: "架构 / 全栈 / 后端 / 数据库 / 平台 / 运维", icon: Monitor },
  { prefix: "HiveCrew MBP M4", name: "产品基地", role: "产品 / UIUX / 前端 / 客户端 / 消息批处理", icon: Monitor },
  { prefix: "HiveCrew MBA M4", name: "质量基地", role: "测试 / 独立审查 / 风险 / 返修集成", icon: Wrench },
  { prefix: "HiveCrew MB M2", name: "研究基地", role: "调研 / 知识工程 / 研究分析", icon: Monitor },
];

export function BasesPage() {
  const wsId = useWorkspaceId();
  const { t } = useT("bases");
  const { data: runtimes = [], isLoading } = useQuery(runtimeListOptions(wsId));
  const { data: agents = [] } = useQuery(agentListOptions(wsId));

  const bases = useMemo(() => {
    const machines = buildRuntimeMachines(runtimes, { now: Date.now() });
    const runtimeToMachine = new Map<string, number>();
    machines.forEach((m, i) => m.runtimes.forEach((r) => runtimeToMachine.set(r.id, i)));
    const machineAgents = new Map<number, number>();
    for (const a of agents) {
      const idx = runtimeToMachine.get(a.runtime_id);
      if (idx !== undefined) machineAgents.set(idx, (machineAgents.get(idx) ?? 0) + 1);
    }
    return machines.map((m, i) => {
      const known = KNOWN_BASES.find((b) => m.title.startsWith(b.prefix));
      return {
        machine: m,
        baseName: known?.name ?? null,
        role: known?.role ?? null,
        icon: known?.icon ?? Network,
        employees: machineAgents.get(i) ?? 0,
        registered: m.runtimes.length,
      };
    });
  }, [runtimes, agents]);

  return (
    <div className="flex h-full flex-col">
      <CollectionPageHeader
        icon={Network}
        title={t(($) => $.title)}
        description={t(($) => $.description)}
      />
      {isLoading ? (
        <div className="p-4 text-sm text-muted-foreground">{t(($) => $.loading)}</div>
      ) : (
        <div className="grid grid-cols-1 gap-4 p-4 sm:grid-cols-2 xl:grid-cols-3">
          {bases.map(({ machine, baseName, role, icon: Icon, employees, registered }) => (
            <div key={machine.id} className="rounded-lg border bg-card p-4 shadow-sm">
              <div className="flex items-center gap-2">
                <Icon className="size-5 text-muted-foreground" />
                <h3 className="text-base font-semibold">{baseName ?? machine.title}</h3>
              </div>
              <p className="mt-1 text-xs text-muted-foreground">{machine.title}</p>
              {role ? <p className="mt-1 text-xs text-muted-foreground">{role}</p> : null}
              <div className="mt-3 grid grid-cols-2 gap-2 text-sm">
                <div>
                  <div className="text-xs text-muted-foreground">{t(($) => $.runtimeOnline)}</div>
                  <div className="font-medium">{machine.onlineCount} / {registered}</div>
                </div>
                <div>
                  <div className="text-xs text-muted-foreground">{t(($) => $.employees)}</div>
                  <div className="font-medium">{employees}</div>
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
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

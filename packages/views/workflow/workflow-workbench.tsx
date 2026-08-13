"use client";

import type { WorkflowDefinition, WorkflowInstance } from "@multica/core/api/workflow";

const STATUS_LABEL: Record<string, string> = {
  running: "运行中",
  paused: "已暂停",
  stopped: "已停止",
  completed: "已完成",
  failed: "失败",
};

const RISK_LABEL: Record<string, string> = {
  fast: "快速",
  standard: "标准",
  owner: "Owner",
};

export interface WorkflowWorkbenchProps {
  instances: WorkflowInstance[];
  definitions: WorkflowDefinition[];
}

function stageName(def: WorkflowDefinition | undefined, idx: number): string {
  if (!def || idx < 0 || idx >= def.stages.length) return `阶段 ${idx + 1}`;
  return def.stages[idx]?.name ?? `阶段 ${idx + 1}`;
}

export function WorkflowWorkbench({ instances, definitions }: WorkflowWorkbenchProps) {
  const count = (s: string) => instances.filter((i) => i.status === s).length;

  return (
    <div className="flex flex-col gap-4" data-testid="workflow-workbench">
      <div className="flex flex-wrap items-center gap-3 text-xs">
        <span>实例 {instances.length}</span>
        <span>运行中 {count("running")}</span>
        <span>已暂停 {count("paused")}</span>
        <span>失败 {count("failed")}</span>
        <span>已完成 {count("completed")}</span>
      </div>

      <section>
        <h3 className="mb-2 text-sm font-semibold">工作流实例</h3>
        <div className="flex flex-col gap-2">
          {instances.length === 0 ? (
            <p className="text-xs text-zinc-500">暂无工作流实例</p>
          ) : (
            instances.map((i) => {
              const def = definitions.find((d) => d.id === i.definition_id);
              return (
                <div key={i.id} className="rounded-md border border-zinc-800 px-3 py-2 text-sm" data-testid="workflow-instance-row">
                  <div className="flex items-center gap-2">
                    <span className="font-mono">{i.id}</span>
                    <span className="text-xs text-zinc-400">{STATUS_LABEL[i.status] ?? i.status}</span>
                    <span className="ml-auto text-xs text-zinc-500">{i.stage_index + 1}/{def?.stages.length ?? "?"}</span>
                  </div>
                  <div className="mt-1 text-xs text-zinc-300">
                    阶段：{stageName(def, i.stage_index)} · {RISK_LABEL[def?.risk ?? "standard"]}
                  </div>
                </div>
              );
            })
          )}
        </div>
      </section>

      <section>
        <h3 className="mb-2 text-sm font-semibold">流程模板</h3>
        <div className="flex flex-col gap-2">
          {definitions.length === 0 ? (
            <p className="text-xs text-zinc-500">暂无流程模板</p>
          ) : (
            definitions.map((d) => (
              <div key={d.id} className="rounded-md border border-zinc-800 px-3 py-2 text-sm" data-testid="workflow-template-row">
                <div className="flex items-center gap-2">
                  <span className="font-mono">{d.id}</span>
                  <span className="text-xs text-zinc-400">v{d.version} · {RISK_LABEL[d.risk]}</span>
                </div>
                <div className="mt-1 text-xs text-zinc-300">{d.stages.map((s) => s.name).join(" → ")}</div>
              </div>
            ))
          )}
        </div>
      </section>
    </div>
  );
}

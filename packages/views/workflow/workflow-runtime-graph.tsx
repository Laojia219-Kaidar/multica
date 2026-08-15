"use client";

import { CheckCircle2, CircleDashed, CirclePause, CircleX, LoaderCircle, ShieldCheck } from "lucide-react";
import type { WorkflowGraph, WorkflowRuntime, WorkflowNodeStatus } from "@multica/core/workflow";

const statusLabel: Record<WorkflowNodeStatus, string> = {
  not_started: "未开始",
  ready: "就绪",
  running: "运行中",
  waiting_approval: "等待审批",
  blocked: "已阻塞",
  failed: "失败",
  passed: "已通过",
  stopped: "已停止",
  skipped: "已跳过",
};

function StatusIcon({ status }: { status: WorkflowNodeStatus }) {
  if (status === "running") return <LoaderCircle className="h-4 w-4 animate-spin text-primary" />;
  if (status === "passed") return <CheckCircle2 className="h-4 w-4 text-emerald-500" />;
  if (status === "failed" || status === "stopped") return <CircleX className="h-4 w-4 text-destructive" />;
  if (status === "waiting_approval") return <ShieldCheck className="h-4 w-4 text-amber-500" />;
  if (status === "blocked") return <CirclePause className="h-4 w-4 text-amber-500" />;
  return <CircleDashed className="h-4 w-4 text-muted-foreground" />;
}

export interface WorkflowRuntimeGraphProps {
  graph: WorkflowGraph;
  runtime: WorkflowRuntime;
  onSelectNode?: (nodeId: string) => void;
}

export function WorkflowRuntimeGraph({ graph, runtime, onSelectNode }: WorkflowRuntimeGraphProps) {
  const runtimeByNode = new Map(runtime.nodes.map((node) => [node.nodeId, node]));
  return (
    <section data-testid="workflow-runtime-graph" className="rounded-lg border bg-card p-3">
      <div className="mb-3 flex items-center justify-between gap-3">
        <div>
          <h3 className="text-sm font-semibold">运行态流程图</h3>
          <p className="text-xs text-muted-foreground">实例 {runtime.instanceId} · v{runtime.version} · {statusLabel[runtime.status]}</p>
        </div>
        <span className="rounded-full border px-2 py-1 text-[11px] text-muted-foreground">只读覆盖</span>
      </div>
      <div className="grid gap-2 md:grid-cols-2">
        {graph.nodes.map((node) => {
          const current = runtimeByNode.get(node.id);
          const status = current?.status ?? "not_started";
          return (
            <button type="button" key={node.id} onClick={() => onSelectNode?.(node.id)} className="flex items-start gap-2 rounded-md border p-2 text-left hover:bg-accent/50" data-testid={`runtime-node-${node.id}`}>
              <StatusIcon status={status} />
              <span className="min-w-0 flex-1">
                <span className="block truncate text-xs font-medium">{node.data.label}</span>
                <span className="block text-[11px] text-muted-foreground">{statusLabel[status]}{current?.employeeName ? ` · ${current.employeeName}` : ""}</span>
                {current?.taskId || current?.runId ? <span className="block truncate font-mono text-[10px] text-muted-foreground">{current.taskId ?? current.runId}</span> : null}
                {current?.error ? <span className="block text-[10px] text-destructive">{current.error}</span> : null}
              </span>
            </button>
          );
        })}
      </div>
    </section>
  );
}

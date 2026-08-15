"use client";

import type { WorkflowDefinition, WorkflowInstance } from "@multica/core/api/workflow";
import type {
  WorkflowDataState,
  WorkflowInstanceCreationState,
  WorkflowReceiptView,
  WorkflowRuntime,
  WorkflowNodeStatus,
} from "@multica/core/workflow";

const INSTANCE_STATUS_LABEL: Record<string, string> = {
  running: "运行中",
  paused: "已暂停",
  stopped: "已停止",
  completed: "已完成",
  failed: "失败",
};

const NODE_STATUS_LABEL: Record<WorkflowNodeStatus, string> = {
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

const RISK_LABEL: Record<string, string> = {
  fast: "快速",
  standard: "标准",
  owner: "Owner",
};

const RECEIPT_STATUS_LABEL: Record<WorkflowReceiptView["status"], string> = {
  accepted: "已接受",
  rejected: "已拒绝",
  observed: "已观测",
};

export interface WorkflowWorkbenchProps {
  /** Existing workflow-kernel read models. The workbench never writes these. */
  instances: WorkflowInstance[];
  definitions: WorkflowDefinition[];
  /** Optional node-level runtime read projection, keyed by instanceId. */
  runtimes?: WorkflowRuntime[];
  /** Existing event/control receipts mapped by the integrator to this view contract. */
  receipts?: WorkflowReceiptView[];
  instancesState?: WorkflowDataState;
  definitionsState?: WorkflowDataState;
  runtimesState?: WorkflowDataState;
  receiptsState?: WorkflowDataState;
  instancesError?: string;
  definitionsError?: string;
  runtimesError?: string;
  receiptsError?: string;
  onRetryInstances?: () => void;
  onRetryDefinitions?: () => void;
  onRetryRuntimes?: () => void;
  onRetryReceipts?: () => void;
  selectedInstanceId?: string;
  onSelectInstance?: (instanceId: string) => void;
  /** Compatibility seam for the future published-DAG instance endpoint. */
  onCreateInstance?: (definition: WorkflowDefinition) => void | Promise<void>;
  instanceCreationState?: WorkflowInstanceCreationState;
  instanceCreationError?: string;
}

function stageName(def: WorkflowDefinition | undefined, idx: number, status?: WorkflowInstance["status"]): string {
  if (status === "completed" && def && idx >= def.stages.length) return "流程完成";
  if (!def || idx < 0 || idx >= def.stages.length) return `阶段 ${idx + 1}`;
  return def.stages[idx]?.name ?? `阶段 ${idx + 1}`;
}

function statusTone(status: string): string {
  if (status === "running") return "border-blue-500/40 bg-blue-500/10 text-blue-700 dark:text-blue-300";
  if (status === "waiting_approval" || status === "paused" || status === "blocked") return "border-amber-500/40 bg-amber-500/10 text-amber-700 dark:text-amber-300";
  if (status === "failed" || status === "stopped" || status === "rejected") return "border-red-500/40 bg-red-500/10 text-red-700 dark:text-red-300";
  if (status === "completed" || status === "passed" || status === "accepted") return "border-emerald-500/40 bg-emerald-500/10 text-emerald-700 dark:text-emerald-300";
  return "border-border bg-muted text-muted-foreground";
}

function ErrorState({ message, onRetry, testId }: { message: string; onRetry?: () => void; testId: string }) {
  return (
    <div className="rounded-md border border-red-500/30 bg-red-500/5 p-3 text-xs text-red-700 dark:text-red-300" data-testid={testId}>
      <p>{message}</p>
      {onRetry ? <button type="button" className="mt-2 rounded border px-2 py-1 text-[11px] hover:bg-red-500/10" onClick={onRetry}>重试读取</button> : null}
    </div>
  );
}

function InstanceStageReadback({ instance, definition, runtime }: { instance: WorkflowInstance; definition?: WorkflowDefinition; runtime?: WorkflowRuntime }) {
  const nodeAwaitingApproval = runtime?.nodes.some((node) => node.status === "waiting_approval") ?? false;
  const effectiveStatus = nodeAwaitingApproval ? "waiting_approval" : instance.status;
  const currentStage = stageName(definition, instance.stage_index, instance.status);

  return (
    <div className="mt-3 rounded-md border bg-muted/20 p-3" data-testid={`workflow-stage-progress-${instance.id}`}>
      <div className="flex flex-wrap items-center gap-2 text-xs">
        <span className="font-medium">阶段回读</span>
        <span className={`rounded-full border px-2 py-0.5 ${statusTone(effectiveStatus)}`}>
          当前阶段：{currentStage} · {INSTANCE_STATUS_LABEL[effectiveStatus] ?? NODE_STATUS_LABEL[effectiveStatus as WorkflowNodeStatus] ?? effectiveStatus}
        </span>
        <span className="text-muted-foreground">{definition ? `${Math.min(instance.stage_index + 1, definition.stages.length)}/${definition.stages.length}` : "?"}</span>
      </div>
      {definition ? (
        <div className="mt-2 flex flex-wrap gap-1.5" aria-label="工作流阶段列表">
          {definition.stages.map((stage, index) => (
            <span key={`${instance.id}-stage-${index}`} className={`rounded border px-2 py-1 text-[11px] ${index === instance.stage_index ? "border-primary/50 bg-primary/10 font-medium" : "border-border text-muted-foreground"}`}>
              {index + 1}. {stage.name}{index === instance.stage_index ? " · 当前" : " · 阶段状态未回读"}
            </span>
          ))}
        </div>
      ) : <p className="mt-2 text-[11px] text-muted-foreground">对应的已发布定义尚未回读，无法推断阶段名称。</p>}
      {runtime ? (
        <div className="mt-3 border-t pt-2" data-testid={`workflow-node-readback-${instance.id}`}>
          <div className="mb-1 flex items-center justify-between gap-2 text-[11px]">
            <span className="font-medium">节点运行回执</span>
            <span className="text-muted-foreground">版本 v{runtime.version}</span>
          </div>
          {runtime.nodes.length === 0 ? <p className="text-[11px] text-muted-foreground">无节点回执</p> : (
            <div className="space-y-1">
              {runtime.nodes.map((node) => (
                <div key={`${instance.id}-${node.nodeId}`} className="flex flex-wrap items-center gap-2 text-[11px]">
                  <span className="font-mono">{node.nodeId}</span>
                  <span className={`rounded-full border px-1.5 py-0.5 ${statusTone(node.status)}`}>{NODE_STATUS_LABEL[node.status]}</span>
                  {node.employeeName ? <span className="text-muted-foreground">· {node.employeeName}</span> : null}
                  {node.taskId || node.runId ? <span className="font-mono text-muted-foreground">{node.taskId ?? node.runId}</span> : null}
                  {node.error ? <span className="text-red-600 dark:text-red-300">{node.error}</span> : null}
                </div>
              ))}
            </div>
          )}
        </div>
      ) : <p className="mt-2 text-[11px] text-muted-foreground">无节点回执；需由集成层读取实例运行事件后展示。</p>}
    </div>
  );
}

export function WorkflowWorkbench({
  instances,
  definitions,
  runtimes = [],
  receipts = [],
  instancesState = "ready",
  definitionsState = "ready",
  runtimesState = "ready",
  receiptsState = "ready",
  instancesError = "未知错误",
  definitionsError = "未知错误",
  runtimesError = "未知错误",
  receiptsError = "未知错误",
  onRetryInstances,
  onRetryDefinitions,
  onRetryRuntimes,
  onRetryReceipts,
  selectedInstanceId,
  onSelectInstance,
  onCreateInstance,
  instanceCreationState,
  instanceCreationError,
}: WorkflowWorkbenchProps) {
  const count = (status: string) => instances.filter((instance) => instance.status === status).length;
  const runtimeByInstance = new Map(runtimes.map((runtime) => [runtime.instanceId, runtime]));
  const approvalCount = instances.filter((instance) => runtimeByInstance.get(instance.id)?.nodes.some((node) => node.status === "waiting_approval")).length;
  const effectiveCreationState = instanceCreationState ?? (onCreateInstance ? "ready" : "unavailable");

  return (
    <div className="flex flex-col gap-4" data-testid="workflow-workbench">
      <div className="flex flex-wrap items-center gap-2 text-xs" data-testid="workflow-summary">
        <span className="rounded border px-2 py-1">实例 {instancesState === "loading" ? "加载中…" : instances.length}</span>
        <span className={`rounded border px-2 py-1 ${statusTone("running")}`}>运行中 {count("running")}</span>
        <span className={`rounded border px-2 py-1 ${statusTone("paused")}`}>已暂停 {count("paused")}</span>
        <span className={`rounded border px-2 py-1 ${statusTone("failed")}`}>失败 {count("failed")}</span>
        <span className={`rounded border px-2 py-1 ${statusTone("completed")}`}>已完成 {count("completed")}</span>
        <span className={`rounded border px-2 py-1 ${statusTone("waiting_approval")}`}>待审批 {approvalCount}</span>
      </div>

      <section data-testid="workflow-instances-section">
        <div className="mb-2 flex items-center justify-between gap-2">
          <h3 className="text-sm font-semibold">工作流实例</h3>
          {instancesState === "loading" ? <span className="text-[11px] text-muted-foreground">读取中…</span> : null}
        </div>
        {instancesState === "loading" ? <p className="text-xs text-muted-foreground">正在加载工作流实例…</p> : null}
        {instancesState === "error" ? <ErrorState message={`工作流实例加载失败：${instancesError}`} onRetry={onRetryInstances} testId="workflow-instances-error" /> : null}
        {instancesState === "ready" && instances.length === 0 ? <><p className="text-xs text-muted-foreground">暂无工作流实例</p><p className="mt-1 text-[11px] text-muted-foreground">请先从已发布工作流请求创建实例。</p></> : null}
        {instancesState === "ready" && instances.length > 0 ? (
          <div className="flex flex-col gap-2">
            {instances.map((instance) => {
              const definition = definitions.find((item) => item.id === instance.definition_id);
              const runtime = runtimeByInstance.get(instance.id);
              return (
                <article key={instance.id} className={`rounded-md border px-3 py-3 text-sm ${selectedInstanceId === instance.id ? "border-primary/60 bg-primary/5" : ""}`} data-testid={`workflow-instance-${instance.id}`}>
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="font-mono">{instance.id}</span>
                    <span className={`rounded-full border px-2 py-0.5 text-xs ${statusTone(instance.status)}`}>{INSTANCE_STATUS_LABEL[instance.status] ?? instance.status}</span>
                    {runtime?.nodes.some((node) => node.status === "waiting_approval") ? <span className={`rounded-full border px-2 py-0.5 text-xs ${statusTone("waiting_approval")}`}>等待审批</span> : null}
                    <span className="ml-auto text-xs text-muted-foreground">定义 v{instance.definition_version}</span>
                    {onSelectInstance ? <button type="button" className="rounded border px-2 py-1 text-[11px] hover:bg-accent" aria-pressed={selectedInstanceId === instance.id} onClick={() => onSelectInstance(instance.id)}>查看运行图</button> : null}
                  </div>
                  <div className="mt-1 text-xs text-muted-foreground">上下文：{instance.context.project_id ?? instance.context.issue_id ?? instance.context.outcome_id ?? "未提供"} · 风险：{RISK_LABEL[definition?.risk ?? "standard"]}</div>
                  <div className="mt-1 text-xs text-muted-foreground">阶段：{stageName(definition, instance.stage_index, instance.status)} · {RISK_LABEL[definition?.risk ?? "standard"]}</div>
                  <InstanceStageReadback instance={instance} definition={definition} runtime={runtime} />
                  {runtimesState === "loading" ? <p className="mt-2 text-[11px] text-muted-foreground">正在加载节点运行态…</p> : null}
                  {runtimesState === "error" ? <div className="mt-2 flex flex-wrap items-center gap-2 text-[11px] text-red-600 dark:text-red-300"><span>节点运行态加载失败：{runtimesError}</span>{onRetryRuntimes ? <button type="button" className="rounded border px-2 py-1 hover:bg-red-500/10" onClick={onRetryRuntimes}>重试读取</button> : null}</div> : null}
                </article>
              );
            })}
          </div>
        ) : null}
      </section>

      <section data-testid="workflow-definitions-section">
        <div className="mb-2 flex items-center justify-between gap-2">
          <h3 className="text-sm font-semibold">已发布流程模板</h3>
          {definitionsState === "loading" ? <span className="text-[11px] text-muted-foreground">读取中…</span> : null}
        </div>
        {definitionsState === "loading" ? <p className="text-xs text-muted-foreground">正在加载已发布流程模板…</p> : null}
        {definitionsState === "error" ? <ErrorState message={`工作流模板加载失败：${definitionsError}`} onRetry={onRetryDefinitions} testId="workflow-definitions-error" /> : null}
        {definitionsState === "ready" && definitions.length === 0 ? <p className="text-xs text-muted-foreground">暂无流程模板</p> : null}
        {definitionsState === "ready" && definitions.length > 0 ? (
          <div className="flex flex-col gap-2">
            {definitions.map((definition) => (
              <article key={`${definition.id}:${definition.version}`} className="rounded-md border px-3 py-2 text-sm" data-testid="workflow-template-row">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="font-mono">{definition.id}</span>
                  <span className="text-xs text-muted-foreground">v{definition.version} · {RISK_LABEL[definition.risk]}</span>
                  {onCreateInstance ? (
                    <button type="button" className="ml-auto rounded border px-2 py-1 text-[11px] hover:bg-accent disabled:cursor-not-allowed disabled:opacity-60" disabled={effectiveCreationState === "loading"} onClick={() => onCreateInstance(definition)}>
                      {effectiveCreationState === "loading" ? "请求中…" : "请求创建实例"}
                    </button>
                  ) : null}
                </div>
                <div className="mt-1 text-xs text-muted-foreground">{definition.stages.map((stage) => stage.name).join(" → ")}</div>
              </article>
            ))}
          </div>
        ) : null}
        {effectiveCreationState === "unavailable" ? <><p className="mt-2 text-[11px] text-muted-foreground">后端创建实例接口尚未接入</p><p className="text-[11px] text-muted-foreground">此候选页面不会伪造执行或成功回执。</p></> : null}
        {effectiveCreationState === "error" ? <p className="mt-2 text-[11px] text-red-600 dark:text-red-300">创建实例请求失败：{instanceCreationError ?? "未知错误"}</p> : null}
      </section>

      <section data-testid="workflow-receipts-section">
        <div className="mb-2 flex items-center justify-between gap-2">
          <h3 className="text-sm font-semibold">执行回执（只读）</h3>
          {receiptsState === "loading" ? <span className="text-[11px] text-muted-foreground">读取中…</span> : null}
        </div>
        {receiptsState === "loading" ? <p className="text-xs text-muted-foreground">正在加载执行回执…</p> : null}
        {receiptsState === "error" ? <ErrorState message={`执行回执加载失败：${receiptsError}`} onRetry={onRetryReceipts} testId="workflow-receipts-error" /> : null}
        {receiptsState === "ready" && receipts.length === 0 ? <p className="text-xs text-muted-foreground">暂无执行回执；页面不会把空回执解释为已执行。</p> : null}
        {receiptsState === "ready" && receipts.length > 0 ? (
          <div className="space-y-2">
            {receipts.map((receipt) => (
              <article key={receipt.id} className="rounded-md border px-3 py-2 text-xs" data-testid={`workflow-receipt-${receipt.id}`}>
                <div className="flex flex-wrap items-center gap-2">
                  <span className="font-medium">{receipt.label}</span>
                  <span className={`rounded-full border px-2 py-0.5 ${statusTone(receipt.status)}`}>{RECEIPT_STATUS_LABEL[receipt.status]}</span>
                  <span className="rounded-full border px-2 py-0.5 text-[10px] text-muted-foreground">只读回执</span>
                  <span className="ml-auto font-mono text-[10px] text-muted-foreground">实例 {receipt.instanceId}</span>
                </div>
                <div className="mt-1 flex flex-wrap gap-x-3 gap-y-1 text-[11px] text-muted-foreground">
                  {receipt.sourceRef ? <span>来源：{receipt.sourceRef}</span> : null}
                  {receipt.actor ? <span>Actor：{receipt.actor}</span> : null}
                  {receipt.idempotencyKey ? <span>幂等键：{receipt.idempotencyKey}</span> : null}
                  {receipt.occurredAt ? <span>发生：{receipt.occurredAt}</span> : null}
                  {receipt.reason ? <span>原因：{receipt.reason}</span> : null}
                </div>
              </article>
            ))}
          </div>
        ) : null}
        {receiptsState === "ready" && receipts.length > 0 && onRetryReceipts ? <button type="button" className="mt-2 rounded border px-2 py-1 text-[11px] hover:bg-accent" onClick={onRetryReceipts}>重新读取回执</button> : null}
      </section>
    </div>
  );
}

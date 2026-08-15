"use client";

/**
 * HIVECREW-WECHAT-REAL-OPERATIONS-V1 / WO-20 — WeChat content production
 * operations surface (candidate slice).
 *
 * This panel is a pure view: the integrator supplies the resolved authority
 * context, the published definition pins, and the server-side production read
 * models, and receives start/review callbacks. It never fetches, never
 * simulates execution, and never renders a control the backend does not
 * implement (no pause/resume/retry/publish buttons exist here).
 *
 * Submit path: compose the frozen request DTO from the form, validate it with
 * the SAME fail-closed contract validator the server re-runs, and only then
 * hand it to the integrator. The idempotency key is generated once per draft
 * and kept stable across retries, so a retry is a replay, never a duplicate
 * production.
 */

import { useMemo, useState } from "react";
import { FileCheck2, RefreshCw, SendHorizontal, ShieldCheck } from "lucide-react";
import {
  validateWechatContentProductionRequest,
  WECHAT_CONTENT_CHANNEL,
  WECHAT_CONTENT_NODE_KEYS,
  WECHAT_CONTENT_PRODUCTION_REQUEST_SCHEMA_VERSION,
  type WechatContentApprovalPolicy,
  type WechatContentNodeKey,
  type WechatContentProductionRequest,
} from "@multica/core/workflow";

// ---------------------------------------------------------------------------
// View-model types (mirror the server read model JSON exactly; snake_case).
// ---------------------------------------------------------------------------

export type WechatProductionNodeState = "pending" | "dispatched" | "completed" | "failed";

export interface WechatProductionNodeView {
  node: WechatContentNodeKey;
  order: number;
  work_order_ref?: string;
  command_id?: string;
  issue_id?: string;
  task_id?: string;
  candidate_id?: string;
  state: WechatProductionNodeState;
  live_state?: string;
  review_decision?: string;
  failure?: string;
}

export type WechatProductionStatus = "running" | "paused" | "stopped" | "completed" | "failed";

export interface WechatProductionView {
  instance_id: string;
  definition_id: string;
  definition_version: number;
  project_id: string;
  status: WechatProductionStatus;
  current_node?: WechatContentNodeKey;
  nodes: WechatProductionNodeView[];
  approval_state: "none" | "awaiting" | "approved" | "changes_requested";
  publication_state: "none" | "awaiting_publication";
}

/** Immutable published definition version pin the request must carry. */
export interface WechatPublishedDefinitionPin {
  definition_id: string;
  version: number;
  digest: string;
}

/**
 * Server-resolved authority context (existing CompanyOps refs). It identifies;
 * it never authorizes — authority is re-resolved server-side at dispatch.
 * `null` means the integrator could not resolve it: the panel fails closed.
 */
export interface WechatProductionAuthorityView {
  work_order_source_ref: string;
  employee_id: string;
  identity_binding_id: string;
  agent_id: string;
  session_id: string;
}

export type WechatProductionReviewDecision = "approved" | "changes_requested";

export interface WechatProductionStartReceipt {
  instance_id: string;
  idempotency_key: string;
  /** false means the start was an idempotent replay of an existing production. */
  changed?: boolean;
}

export interface WechatProductionPanelProps {
  /** Formal project id (e.g. PRJ-WECHAT-OPS); must match the work-order ref. */
  projectId: string;
  projectName?: string;
  authority: WechatProductionAuthorityView | null;
  publishedPins: WechatPublishedDefinitionPin[];
  productions: WechatProductionView[];
  productionsState?: "ready" | "loading" | "error";
  productionsError?: string;
  onRefreshProductions?: () => void;
  onStart?: (request: WechatContentProductionRequest) => void;
  startState?: "idle" | "submitting" | "error";
  startError?: string;
  startReceipt?: WechatProductionStartReceipt | null;
  onReview?: (input: { instanceId: string; decision: WechatProductionReviewDecision; reviewId: string }) => void;
  reviewState?: "idle" | "submitting" | "error";
  reviewError?: string;
  /** Jump to the existing Outcome Center for a completed production. */
  outcomeHref?: (instanceId: string) => string;
}

// ---------------------------------------------------------------------------
// Labels and helpers
// ---------------------------------------------------------------------------

const NODE_LABEL: Record<WechatContentNodeKey, string> = {
  "research-material-package": "资料包",
  "article-draft": "文章草稿",
  "editorial-review-report": "审校报告",
  "wechat-publication-package": "待发布包",
};

const NODE_STATE_LABEL: Record<WechatProductionNodeState, string> = {
  pending: "待启动",
  dispatched: "已派发",
  completed: "已完成",
  failed: "已失败",
};

const PRODUCTION_STATUS_LABEL: Record<WechatProductionStatus, string> = {
  running: "运行中",
  paused: "等待审批",
  stopped: "已停止",
  completed: "已完成",
  failed: "失败",
};

const APPROVAL_STATE_LABEL: Record<WechatProductionView["approval_state"], string> = {
  none: "无审批",
  awaiting: "等待 Owner 审批",
  approved: "审批通过",
  changes_requested: "已退回修改",
};

const FAILURE_LABEL: Record<string, string> = {
  run_failed: "运行失败",
  run_cancelled: "运行被取消",
  receipt_missing: "服务端执行回执缺失",
  materialize_failed: "产物为空或物化失败",
  candidate_missing: "候选产物缺失",
  authority_rejected: "权限/派发被服务端拒绝",
};

function nodeTone(state: WechatProductionNodeState): string {
  if (state === "completed") return "border-emerald-500/40 bg-emerald-500/10 text-emerald-700 dark:text-emerald-300";
  if (state === "failed") return "border-red-500/40 bg-red-500/10 text-red-700 dark:text-red-300";
  if (state === "dispatched") return "border-blue-500/40 bg-blue-500/10 text-blue-700 dark:text-blue-300";
  return "border-border bg-muted text-muted-foreground";
}

function statusTone(status: WechatProductionStatus): string {
  if (status === "running") return "border-blue-500/40 bg-blue-500/10 text-blue-700 dark:text-blue-300";
  if (status === "paused") return "border-amber-500/40 bg-amber-500/10 text-amber-700 dark:text-amber-300";
  if (status === "failed" || status === "stopped") return "border-red-500/40 bg-red-500/10 text-red-700 dark:text-red-300";
  if (status === "completed") return "border-emerald-500/40 bg-emerald-500/10 text-emerald-700 dark:text-emerald-300";
  return "border-border bg-muted text-muted-foreground";
}

function newUuid(): string {
  const c = globalThis.crypto;
  if (c && typeof c.randomUUID === "function") return c.randomUUID();
  // RFC4122 v4 fallback for environments without crypto.randomUUID.
  return "xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx".replace(/[xy]/g, (ch) => {
    const r = Math.floor(Math.random() * 16);
    const v = ch === "x" ? r : (r & 0x3) | 0x8;
    return v.toString(16);
  });
}

function defaultDeadline(): string {
  return new Date(Date.now() + 7 * 24 * 60 * 60 * 1000).toISOString();
}

// ---------------------------------------------------------------------------
// Panel
// ---------------------------------------------------------------------------

export function WechatProductionPanel({
  projectId,
  projectName,
  authority,
  publishedPins,
  productions,
  productionsState = "ready",
  productionsError = "未知错误",
  onRefreshProductions,
  onStart,
  startState = "idle",
  startError,
  startReceipt,
  onReview,
  reviewState = "idle",
  reviewError,
  outcomeHref,
}: WechatProductionPanelProps) {
  const [subject, setSubject] = useState("");
  const [objective, setObjective] = useState("");
  const [audience, setAudience] = useState("");
  const [sourceRefsText, setSourceRefsText] = useState("");
  const [tone, setTone] = useState("");
  const [deadline, setDeadline] = useState(defaultDeadline);
  const [approvalPolicy, setApprovalPolicy] = useState<WechatContentApprovalPolicy>("owner_approval");
  const [handoffNote, setHandoffNote] = useState("");
  const [pinIndex, setPinIndex] = useState(0);
  const [idempotencyKey, setIdempotencyKey] = useState(newUuid);
  const [validationIssues, setValidationIssues] = useState<string[]>([]);

  const selectedPin = publishedPins[pinIndex];
  const canSubmit = Boolean(onStart) && startState !== "submitting" && Boolean(authority) && Boolean(selectedPin);

  const submit = () => {
    if (!authority || !selectedPin || !onStart) return;
    const request: WechatContentProductionRequest = {
      schema_version: WECHAT_CONTENT_PRODUCTION_REQUEST_SCHEMA_VERSION,
      channel: WECHAT_CONTENT_CHANNEL,
      project_id: projectId,
      authority: { ...authority },
      definition: {
        definition_id: selectedPin.definition_id,
        version: selectedPin.version,
        digest: selectedPin.digest,
      },
      brief: {
        subject,
        objective,
        audience,
        source_refs: sourceRefsText.split("\n").map((line) => line.trim()).filter((line) => line.length > 0),
        tone,
        deadline,
        approval_policy: approvalPolicy,
        handoff_note: handoffNote,
      },
      idempotency_key: idempotencyKey,
    };
    const result = validateWechatContentProductionRequest(request);
    if (!result.ok) {
      setValidationIssues(result.issues.map((entry) => `${entry.path?.join(".") ?? ""} ${entry.message}`.trim()));
      return;
    }
    setValidationIssues([]);
    onStart(request);
  };

  const startNewDraft = () => {
    setIdempotencyKey(newUuid());
    setValidationIssues([]);
  };

  return (
    <div className="flex flex-col gap-4" data-testid="wechat-production-panel">
      <section className="rounded-lg border bg-card p-4" data-testid="wechat-production-launch">
        <div className="flex flex-wrap items-center gap-2">
          <SendHorizontal className="h-4 w-4 text-muted-foreground" />
          <h3 className="text-sm font-semibold">发起公众号内容生产</h3>
          <span className="text-[11px] text-muted-foreground">{projectName ?? projectId}</span>
        </div>
        <p className="mt-1 text-[11px] text-muted-foreground">
          请求将被服务端重新校验并派发到既有 Task/Run 执行链；本页面不伪造执行或成功回执，也不提供暂停/恢复/重试/发布按钮（后端未实现）。
        </p>

        {!authority ? (
          <p className="mt-3 rounded border border-amber-500/40 bg-amber-500/5 p-3 text-xs text-amber-700 dark:text-amber-300" data-testid="wechat-authority-unresolved">
            权限上下文未解析（工单 / 员工 / 身份绑定 / Agent / 会话缺失），无法发起生产。请在候选环境完成绑定后重试。
          </p>
        ) : publishedPins.length === 0 ? (
          <p className="mt-3 rounded border border-amber-500/40 bg-amber-500/5 p-3 text-xs text-amber-700 dark:text-amber-300" data-testid="wechat-no-published-pin">
            当前项目没有可锁定的已发布工作流版本；请先在「工作流」页发布定义后再发起生产。
          </p>
        ) : (
          <div className="mt-3 grid gap-3 md:grid-cols-2">
            <label className="flex flex-col gap-1 text-xs">
              <span className="text-muted-foreground">主题 *</span>
              <input aria-label="主题" value={subject} onChange={(event) => setSubject(event.target.value)} className="rounded border bg-background px-2 py-1.5" />
            </label>
            <label className="flex flex-col gap-1 text-xs">
              <span className="text-muted-foreground">目标 *</span>
              <input aria-label="目标" value={objective} onChange={(event) => setObjective(event.target.value)} className="rounded border bg-background px-2 py-1.5" />
            </label>
            <label className="flex flex-col gap-1 text-xs">
              <span className="text-muted-foreground">受众 *</span>
              <input aria-label="受众" value={audience} onChange={(event) => setAudience(event.target.value)} className="rounded border bg-background px-2 py-1.5" />
            </label>
            <label className="flex flex-col gap-1 text-xs">
              <span className="text-muted-foreground">语气 *</span>
              <input aria-label="语气" value={tone} onChange={(event) => setTone(event.target.value)} className="rounded border bg-background px-2 py-1.5" />
            </label>
            <label className="flex flex-col gap-1 text-xs">
              <span className="text-muted-foreground">截止时间（RFC3339，含时区）*</span>
              <input aria-label="截止时间" value={deadline} onChange={(event) => setDeadline(event.target.value)} className="rounded border bg-background px-2 py-1.5 font-mono" />
            </label>
            <label className="flex flex-col gap-1 text-xs">
              <span className="text-muted-foreground">审批策略 *</span>
              <select aria-label="审批策略" value={approvalPolicy} onChange={(event) => setApprovalPolicy(event.target.value as WechatContentApprovalPolicy)} className="rounded border bg-background px-2 py-1.5">
                <option value="owner_approval">owner_approval（Owner 审批，默认）</option>
                <option value="editorial_review">editorial_review（编辑审校）</option>
              </select>
            </label>
            <label className="flex flex-col gap-1 text-xs">
              <span className="text-muted-foreground">已发布版本（不可变锁定）*</span>
              <select aria-label="已发布版本" value={pinIndex} onChange={(event) => setPinIndex(Number(event.target.value))} className="rounded border bg-background px-2 py-1.5">
                {publishedPins.map((pin, index) => (
                  <option key={`${pin.definition_id}:${pin.version}`} value={index}>
                    {pin.definition_id} · v{pin.version} · {pin.digest.slice(0, 19)}…
                  </option>
                ))}
              </select>
            </label>
            <label className="flex flex-col gap-1 text-xs md:col-span-2">
              <span className="text-muted-foreground">资料引用（每行一条，至少一条）*</span>
              <textarea aria-label="资料引用" value={sourceRefsText} onChange={(event) => setSourceRefsText(event.target.value)} rows={3} className="rounded border bg-background px-2 py-1.5 font-mono" />
            </label>
            <label className="flex flex-col gap-1 text-xs md:col-span-2">
              <span className="text-muted-foreground">工作说明（派发给执行 Agent 的原文）*</span>
              <textarea aria-label="工作说明" value={handoffNote} onChange={(event) => setHandoffNote(event.target.value)} rows={3} className="rounded border bg-background px-2 py-1.5" />
            </label>

            <details className="text-[11px] text-muted-foreground md:col-span-2">
              <summary>权限上下文（服务端解析，只读）</summary>
              <div className="mt-1 space-y-0.5 font-mono" data-testid="wechat-authority-context">
                <p>work_order: {authority.work_order_source_ref}</p>
                <p>employee: {authority.employee_id} · binding: {authority.identity_binding_id}</p>
                <p>agent: {authority.agent_id} · session: {authority.session_id}</p>
              </div>
            </details>

            <div className="flex flex-wrap items-center gap-2 md:col-span-2">
              <button type="button" disabled={!canSubmit} onClick={submit} className="rounded bg-primary px-3 py-1.5 text-xs text-primary-foreground hover:bg-primary/90 disabled:cursor-not-allowed disabled:opacity-60" data-testid="wechat-start-submit">
                {startState === "submitting" ? "提交中…" : "发起生产（服务端校验后派发）"}
              </button>
              {startReceipt ? (
                <button type="button" onClick={startNewDraft} className="rounded border px-2 py-1.5 text-xs hover:bg-accent">
                  再发起新生产（新幂等键）
                </button>
              ) : null}
              <span className="font-mono text-[10px] text-muted-foreground" data-testid="wechat-idempotency-key">幂等键 {idempotencyKey}</span>
            </div>

            {validationIssues.length > 0 ? (
              <div role="alert" className="rounded border border-destructive/40 bg-destructive/5 p-2 text-xs text-destructive md:col-span-2" data-testid="wechat-validation-issues">
                <p className="font-medium">合同校验未通过，未发起任何请求：</p>
                <ul className="mt-1 list-inside list-disc">
                  {validationIssues.map((entry) => <li key={entry}>{entry}</li>)}
                </ul>
              </div>
            ) : null}
            {startState === "error" ? (
              <p role="alert" className="rounded border border-destructive/40 bg-destructive/5 p-2 text-xs text-destructive md:col-span-2" data-testid="wechat-start-error">
                发起失败：{startError ?? "未知错误"}。同一幂等键重试即为幂等重放，不会产生第二个生产实例。
              </p>
            ) : null}
            {startReceipt ? (
              <p className="rounded border border-emerald-500/30 bg-emerald-500/5 p-2 text-xs text-emerald-700 dark:text-emerald-300 md:col-span-2" data-testid="wechat-start-receipt">
                已受理：实例 {startReceipt.instance_id} · 幂等键 {startReceipt.idempotency_key}{startReceipt.changed === false ? "（幂等重放，未重复派发）" : ""}
              </p>
            ) : null}
          </div>
        )}
      </section>

      <section className="rounded-lg border bg-card p-4" data-testid="wechat-production-monitor">
        <div className="mb-2 flex flex-wrap items-center gap-2">
          <ShieldCheck className="h-4 w-4 text-muted-foreground" />
          <h3 className="text-sm font-semibold">生产运行状态（服务端对账回读）</h3>
          {onRefreshProductions ? (
            <button type="button" onClick={onRefreshProductions} className="ml-auto inline-flex items-center gap-1 rounded border px-2 py-1 text-[11px] hover:bg-accent" data-testid="wechat-refresh">
              <RefreshCw className="h-3 w-3" />刷新运行状态
            </button>
          ) : null}
        </div>

        {productionsState === "loading" ? <p className="text-xs text-muted-foreground">正在回读生产状态…</p> : null}
        {productionsState === "error" ? (
          <p role="alert" className="rounded border border-destructive/40 bg-destructive/5 p-3 text-xs text-destructive" data-testid="wechat-productions-error">
            生产状态回读失败：{productionsError}。不会把读取失败显示为零生产。
          </p>
        ) : null}
        {productionsState === "ready" && productions.length === 0 ? (
          <p className="text-xs text-muted-foreground">当前项目没有进行中的公众号内容生产。</p>
        ) : null}
        {productionsState === "ready" && productions.length > 0 ? (
          <div className="space-y-3">
            {productions.map((production) => (
              <WechatProductionCard
                key={production.instance_id}
                production={production}
                onReview={onReview}
                reviewState={reviewState}
                reviewError={reviewError}
                outcomeHref={outcomeHref}
              />
            ))}
          </div>
        ) : null}
      </section>
    </div>
  );
}

// ---------------------------------------------------------------------------
// One production card
// ---------------------------------------------------------------------------

function WechatProductionCard({
  production,
  onReview,
  reviewState,
  reviewError,
  outcomeHref,
}: {
  production: WechatProductionView;
  onReview?: WechatProductionPanelProps["onReview"];
  reviewState: "idle" | "submitting" | "error";
  reviewError?: string;
  outcomeHref?: (instanceId: string) => string;
}) {
  const sortedNodes = useMemo(
    () => [...production.nodes].sort((a, b) => a.order - b.order),
    [production.nodes],
  );
  const reviewable = production.status === "paused" && production.approval_state === "awaiting";

  return (
    <article className="rounded-md border p-3" data-testid={`wechat-production-${production.instance_id}`}>
      <div className="flex flex-wrap items-center gap-2 text-xs">
        <span className="font-mono">{production.instance_id}</span>
        <span className={`rounded-full border px-2 py-0.5 ${statusTone(production.status)}`}>
          {PRODUCTION_STATUS_LABEL[production.status] ?? production.status}
        </span>
        <span className="text-muted-foreground">定义 {production.definition_id} · v{production.definition_version}</span>
        {production.approval_state !== "none" ? (
          <span className="rounded-full border border-amber-500/40 bg-amber-500/10 px-2 py-0.5 text-amber-700 dark:text-amber-300">
            {APPROVAL_STATE_LABEL[production.approval_state]}
          </span>
        ) : null}
        {production.publication_state === "awaiting_publication" ? (
          <span className="rounded-full border border-emerald-500/40 bg-emerald-500/10 px-2 py-0.5 text-emerald-700 dark:text-emerald-300" data-testid={`wechat-publication-state-${production.instance_id}`}>
            待发布（无平台回执，绝不显示为已发布）
          </span>
        ) : null}
      </div>

      <div className="mt-2 space-y-1" aria-label="四节点状态">
        {sortedNodes.map((node) => (
          <div key={node.node} className="flex flex-wrap items-center gap-2 text-[11px]" data-testid={`wechat-node-${production.instance_id}-${node.node}`}>
            <span className="w-28 font-medium">{node.order}. {NODE_LABEL[node.node] ?? node.node}</span>
            <span className={`rounded-full border px-1.5 py-0.5 ${nodeTone(node.state)}`}>{NODE_STATE_LABEL[node.state] ?? node.state}</span>
            {node.live_state ? <span className="text-muted-foreground">实时：{node.live_state}</span> : null}
            {node.task_id ? <span className="font-mono text-muted-foreground">task {node.task_id}</span> : null}
            {node.command_id ? <span className="font-mono text-muted-foreground">cmd {node.command_id.slice(0, 8)}…</span> : null}
            {node.candidate_id ? (
              <span className="inline-flex items-center gap-1 font-mono text-muted-foreground">
                <FileCheck2 className="h-3 w-3" />candidate {node.candidate_id}
              </span>
            ) : null}
            {node.review_decision ? <span className="text-muted-foreground">审批：{node.review_decision}</span> : null}
            {node.failure ? (
              <span className="text-red-600 dark:text-red-300">失败原因：{FAILURE_LABEL[node.failure] ?? node.failure}</span>
            ) : null}
          </div>
        ))}
      </div>

      {reviewable && onReview ? (
        <div className="mt-3 flex flex-wrap items-center gap-2 border-t pt-2" data-testid={`wechat-review-controls-${production.instance_id}`}>
          <span className="text-[11px] text-muted-foreground">审校报告待 Owner 决策：</span>
          <button
            type="button"
            disabled={reviewState === "submitting"}
            onClick={() => onReview({ instanceId: production.instance_id, decision: "approved", reviewId: newUuid() })}
            className="rounded border border-emerald-500/50 px-2 py-1 text-[11px] text-emerald-700 hover:bg-emerald-500/10 disabled:opacity-60 dark:text-emerald-300"
          >
            审批通过
          </button>
          <button
            type="button"
            disabled={reviewState === "submitting"}
            onClick={() => onReview({ instanceId: production.instance_id, decision: "changes_requested", reviewId: newUuid() })}
            className="rounded border border-amber-500/50 px-2 py-1 text-[11px] text-amber-700 hover:bg-amber-500/10 disabled:opacity-60 dark:text-amber-300"
          >
            退回修改
          </button>
          {reviewState === "error" ? (
            <span role="alert" className="text-[11px] text-red-600 dark:text-red-300">审批提交失败：{reviewError ?? "未知错误"}</span>
          ) : null}
        </div>
      ) : null}

      {production.publication_state === "awaiting_publication" && outcomeHref ? (
        <a className="mt-2 inline-block text-xs underline" href={outcomeHref(production.instance_id)} data-testid={`wechat-outcome-link-${production.instance_id}`}>
          在成果中心查看待发布包
        </a>
      ) : null}
    </article>
  );
}

/** Ordered node keys for integrators building skeleton views. */
export const WECHAT_PRODUCTION_NODE_ORDER: readonly WechatContentNodeKey[] = WECHAT_CONTENT_NODE_KEYS;

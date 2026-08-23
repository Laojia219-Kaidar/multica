"use client";
/* eslint-disable i18next/no-literal-string */

import { useState } from "react";
import type {
  EmployeeLiveActivityV1,
  PresenceState,
} from "@multica/core/api/workwall";

// Self-contained Chinese labels for the first slice; formal i18n wiring into
// packages/views/locales is left to the mainline integrator.
const PRESENCE_LABEL: Record<PresenceState, string> = {
  offline: "离线",
  idle: "空闲",
  queued: "排队中",
  working: "工作中",
  waiting: "等待中",
  blocked: "阻塞",
  recently_completed: "刚完成",
  unknown: "未知",
};

const PRESENCE_ICON: Record<PresenceState, string> = {
  offline: "✖",
  idle: "○",
  queued: "▷",
  working: "▶",
  waiting: "◔",
  blocked: "⚠",
  recently_completed: "✓",
  unknown: "?",
};

const STAGE_LABEL: Record<string, string> = {
  planning: "规划",
  research: "研究",
  coding: "编码",
  testing: "测试",
  reviewing: "审核",
  repairing: "返修",
  integrating: "集成",
  operating: "运营",
  reporting: "报告",
  none: "无",
  unknown: "未知",
};

const FRESHNESS_LABEL: Record<string, string> = {
  fresh: "新鲜",
  stale: "陈旧",
  missing: "缺失",
  conflict: "冲突",
};

const FRESHNESS_COLOR: Record<string, string> = {
  fresh: "text-success",
  stale: "text-warning",
  missing: "text-destructive",
  conflict: "text-destructive",
};

function presenceText(p: PresenceState) {
  return `${PRESENCE_ICON[p]} ${PRESENCE_LABEL[p]}`;
}

function hasCurrentLinkage(e: EmployeeLiveActivityV1): boolean {
  return !!e.issue_id || !!e.task_id || !!e.run_id;
}

function isoAgeLabel(iso: string): string {
  const d = new Date(iso);
  const diff = Math.max(0, Math.floor((Date.now() - d.getTime()) / 1000));
  if (diff < 60) return `${diff} 秒前`;
  if (diff < 3600) return `${Math.floor(diff / 60)} 分钟前`;
  if (diff < 86400) return `${Math.floor(diff / 3600)} 小时前`;
  return `${Math.floor(diff / 86400)} 天前`;
}

export interface WorkWallProps {
  employees: EmployeeLiveActivityV1[];
}

export function WorkWall({ employees }: WorkWallProps) {
  const [expandedId, setExpandedId] = useState<string | null>(null);
  const [presenceFilter, setPresenceFilter] = useState<PresenceState | "all">("all");
  const [query, setQuery] = useState("");
  const [projectFilter, setProjectFilter] = useState<string>("all");
  const [runtimeFilter, setRuntimeFilter] = useState<string>("all");
  const [modelFilter, setModelFilter] = useState<string>("all");

  const projects = Array.from(
    new Set(employees.map((e) => e.project_title).filter((v): v is string => !!v)),
  ).sort();
  const runtimes = Array.from(
    new Set(employees.map((e) => e.runtime_provider).filter((v): v is string => !!v)),
  ).sort();
  const models = Array.from(
    new Set(employees.map((e) => e.model_name).filter((v): v is string => !!v)),
  ).sort();

  const q = query.trim().toLowerCase();
  const filtered = employees.filter((e) => {
    if (presenceFilter !== "all" && e.presence_state !== presenceFilter) return false;
    if (projectFilter !== "all" && e.project_title !== projectFilter) return false;
    if (runtimeFilter !== "all" && e.runtime_provider !== runtimeFilter) return false;
    if (modelFilter !== "all" && e.model_name !== modelFilter) return false;
    if (q !== "") {
      const hay = [
        e.display_name,
        e.employee_id,
        e.agent_id,
        e.project_title,
        e.issue_identifier,
        e.issue_title,
        e.model_name,
        e.runtime_provider,
        e.runtime_profile_name,
        e.blocked_reason,
        e.next_action,
      ]
        .filter((v): v is string => !!v)
        .join(" ")
        .toLowerCase();
      if (!hay.includes(q)) return false;
    }
    return true;
  });

  return (
    <div className="work-wall flex flex-col gap-3" data-testid="work-wall">
      <StatusBar employees={employees} />
      <div className="flex flex-wrap items-center gap-2 text-xs">
        {(["all", ...Object.keys(PRESENCE_LABEL)] as Array<PresenceState | "all">).map((p) => (
          <button
            key={p}
            type="button"
            className={`rounded-md border px-2 py-1 transition-colors ${presenceFilter === p ? "border-foreground/15 bg-foreground text-background" : "border-border bg-card text-muted-foreground hover:bg-accent hover:text-accent-foreground"}`}
            onClick={() => setPresenceFilter(p)}
          >
            {p === "all" ? `全部 ${employees.length}` : `${PRESENCE_ICON[p as PresenceState]} ${PRESENCE_LABEL[p as PresenceState]}`}
          </button>
        ))}
        <input
          type="search"
          placeholder="搜索员工/项目/议题/模型"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          className="rounded-md border border-input bg-background px-2 py-1 text-foreground outline-none placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring/30"
          data-testid="work-wall-search"
        />
        <FilterSelect label="项目" values={projects} value={projectFilter} onChange={setProjectFilter} />
        <FilterSelect label="Runtime" values={runtimes} value={runtimeFilter} onChange={setRuntimeFilter} />
        <FilterSelect label="模型" values={models} value={modelFilter} onChange={setModelFilter} />
      </div>

      <div className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3">
        {filtered.map((e) => (
          <OwnerCard
            key={e.agent_id}
            employee={e}
            expanded={expandedId === e.agent_id}
            onToggle={() =>
              setExpandedId((cur) => (cur === e.agent_id ? null : e.agent_id))
            }
          />
        ))}
      </div>
    </div>
  );
}

function FilterSelect({
  label,
  values,
  value,
  onChange,
}: {
  label: string;
  values: string[];
  value: string;
  onChange: (v: string) => void;
}) {
  if (values.length === 0) return null;
  return (
    <label className="flex items-center gap-1 text-muted-foreground">
      {label}
      <select
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className="rounded-md border border-input bg-background px-1 py-1 text-foreground outline-none focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring/30"
        data-testid={`work-wall-filter-${label}`}
      >
        <option value="all">全部</option>
        {values.map((v) => (
          <option key={v} value={v}>
            {v}
          </option>
        ))}
      </select>
    </label>
  );
}

function StatusBar({ employees }: { employees: EmployeeLiveActivityV1[] }) {
  const count = (p: PresenceState) =>
    employees.filter((e) => e.presence_state === p).length;
  const waitingBlocked = count("waiting") + count("blocked");
  const offlineUnknown = count("offline") + count("unknown");
  const totalTokens = employees.reduce((sum, e) => sum + (e.token_usage ?? 0), 0);

  return (
    <div
      className="flex flex-wrap items-center gap-3 rounded-lg border bg-card px-3 py-2 text-xs text-muted-foreground shadow-sm"
      data-testid="work-wall-status-bar"
    >
      <span>员工 {employees.length}</span>
      <span>工作中 {count("working")}</span>
      <span>排队中 {count("queued")}</span>
      <span>等待/阻塞 {waitingBlocked}</span>
      <span>空闲 {count("idle")}</span>
      <span>离线/未知 {offlineUnknown}</span>
      <span className="ml-auto font-mono tabular-nums">Token {totalTokens}</span>
    </div>
  );
}

const RECEIPT_STATUS_LABEL: Record<string, string> = {
  completed: "已完成",
  failed: "失败",
  cancelled: "已取消",
};

// ExecutionChainBlock renders the Owner-traceable execution chain
// (Project -> Issue -> Task -> Run -> Receipt + runtime profile) in the
// expanded card. Every value is an identifier the server resolved from an
// authoritative row; absent evidence renders nothing rather than a guess.
function ExecutionChainBlock({ employee: e }: { employee: EmployeeLiveActivityV1 }) {
  const hasAny =
    !!e.issue_id ||
    !!e.project_id ||
    !!e.task_id ||
    !!e.runtime_profile_id ||
    !!e.execution_receipt_ref;
  if (!hasAny) return null;
  return (
    <div className="mb-2 flex flex-col gap-0.5" data-testid="owner-card-chain">
      <div className="font-medium text-foreground">执行链</div>
      {e.project_id ? (
        <div className="truncate">
          Project {e.project_title ?? ""}
          <span className="text-muted-foreground"> {e.project_id}</span>
        </div>
      ) : null}
      {e.issue_id ? (
        <div className="truncate">
          Issue {e.issue_identifier ? `${e.issue_identifier} · ` : ""}
          {e.issue_title ?? ""}
          <span className="text-muted-foreground"> {e.issue_id}</span>
        </div>
      ) : null}
      {e.task_id ? (
        <div className="truncate">
          Task <span className="text-muted-foreground">{e.task_id}</span>
          {e.run_id ? (
            <>
              {" · Run "}
              <span className="text-muted-foreground">{e.run_id}</span>
            </>
          ) : (
            <span className="text-muted-foreground"> · 无独立 Run ID（直发任务）</span>
          )}
        </div>
      ) : null}
      {e.runtime_profile_id ? (
        <div className="truncate">
          Profile {e.runtime_profile_name ?? ""}
          <span className="text-muted-foreground"> {e.runtime_profile_id}</span>
        </div>
      ) : null}
      {e.execution_receipt_ref ? (
        <div className="truncate">
          Receipt {RECEIPT_STATUS_LABEL[e.execution_receipt_status ?? ""] ?? e.execution_receipt_status ?? ""}
          <span className="text-muted-foreground"> {e.execution_receipt_ref}</span>
        </div>
      ) : null}
    </div>
  );
}

// OwnerCard — Owner 视角的员工卡。严格按 DTO 展示注册身份、运行时/提供商/模型、
// 任务/运行/回执与新鲜度等所有 Owner 关心的字段。缺失证据时直接显示"无"，
// 绝不臆造或从 terminal agent_hint 反推身份关联。
function OwnerCard({
  employee: e,
  expanded,
  onToggle,
}: {
  employee: EmployeeLiveActivityV1;
  expanded: boolean;
  onToggle: () => void;
}) {
  return (
    <div
      className={`owner-card overflow-hidden rounded-lg border bg-card text-foreground shadow-sm transition-colors hover:border-foreground/20 ${expanded ? "col-span-full" : ""}`}
      data-testid="owner-card"
    >
      <button
        type="button"
        onClick={onToggle}
        className="flex w-full items-center gap-2 px-3 py-2 text-left"
        aria-expanded={expanded}
        data-testid="owner-card-header"
      >
        <span className="truncate text-sm font-semibold text-foreground">
          {e.display_name}
        </span>
        <span className="truncate font-mono text-[11px] text-muted-foreground">
          {e.employee_id}
        </span>
        <span className="ml-auto whitespace-nowrap text-xs">
          {presenceText(e.presence_state)}
        </span>
        <span className="text-xs text-muted-foreground">{expanded ? "−" : "+"}</span>
      </button>

      <div className="border-t bg-muted/20 px-3 py-2 text-xs">
        <div className="truncate" data-testid="owner-card-runtime">
          模型：{e.model_name ?? "未计量"} · 提供商：{e.runtime_provider ?? "无"}
        </div>
        {e.runtime_profile_id ? (
          <div className="truncate" data-testid="owner-card-profile">
            运行档案：{e.runtime_profile_name ?? e.runtime_profile_id}
            <span className="text-muted-foreground"> {e.runtime_profile_id}</span>
          </div>
        ) : null}
        {e.project_title ? (
          <div className="truncate">项目：{e.project_title}</div>
        ) : null}
        {e.issue_title ? (
          <div className="truncate">
            议题：{e.issue_identifier ? `${e.issue_identifier} · ` : ""}
            {e.issue_title}
          </div>
        ) : null}
        {!hasCurrentLinkage(e) ? (
          <div className="text-muted-foreground" data-testid="owner-card-link-unavailable">
            当前任务链接不可用
          </div>
        ) : null}
        {e.work_stage !== "none" ? (
          <div>工作阶段：{STAGE_LABEL[e.work_stage] ?? e.work_stage}</div>
        ) : null}
        {e.blocked_reason ? (
          <div className="text-warning" data-testid="owner-card-blocked">
            阻塞原因：{e.blocked_reason}
          </div>
        ) : null}
        {e.next_action ? (
          <div className="truncate" data-testid="owner-card-next">
            下一动作：{e.next_action}
          </div>
        ) : null}
        <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-muted-foreground">
          <span data-testid="owner-card-freshness" className={FRESHNESS_COLOR[e.freshness_state] ?? "text-muted-foreground"}>
            新鲜度：{FRESHNESS_LABEL[e.freshness_state] ?? e.freshness_state}
          </span>
          {e.last_heartbeat_at ? (
            <span data-testid="owner-card-heartbeat">
              心跳：{isoAgeLabel(e.last_heartbeat_at)}
            </span>
          ) : null}
          {e.token_usage != null ? (
            <span>Token：{e.token_usage}</span>
          ) : null}
        </div>
      </div>

      {expanded ? (
        <div className="border-t px-3 py-3 text-xs" data-testid="owner-card-expanded">
          <ExecutionChainBlock employee={e} />

          <div className="mb-2 grid grid-cols-1 gap-0.5 md:grid-cols-2" data-testid="owner-card-evidence">
            <div>
              <div className="font-medium text-foreground">身份</div>
              <div className="truncate">员工：{e.display_name}</div>
              <div className="truncate text-muted-foreground">employee_id {e.employee_id}</div>
              <div className="truncate text-muted-foreground">agent_id {e.agent_id}</div>
              {e.department_name ? (
                <div className="truncate">部门：{e.department_name}</div>
              ) : null}
              {e.position_name ? (
                <div className="truncate">职位：{e.position_name}</div>
              ) : null}
            </div>
            <div>
              <div className="font-medium text-foreground">运行时 / 模型</div>
              <div className="truncate">
                Runtime：{e.runtime_provider ?? "无"}
                {e.runtime_id ? <span className="text-muted-foreground"> {e.runtime_id}</span> : null}
              </div>
              <div className="truncate">模型：{e.model_name ?? "未计量"}</div>
              {e.base_name ? (
                <div className="truncate">
                  基座：{e.base_name}
                  {e.base_id ? <span className="text-muted-foreground"> {e.base_id}</span> : null}
                </div>
              ) : null}
              {e.runtime_profile_id ? (
                <div className="truncate">
                  档案：{e.runtime_profile_name ?? e.runtime_profile_id}
                </div>
              ) : null}
            </div>
            <div>
              <div className="font-medium text-foreground">任务 / 运行</div>
              {hasCurrentLinkage(e) ? (
                <>
                  {e.task_id ? (
                    <div className="truncate text-muted-foreground">task_id {e.task_id}</div>
                  ) : (
                    <div className="text-muted-foreground">无关联 Task</div>
                  )}
                  {e.run_id ? (
                    <div className="truncate text-muted-foreground">run_id {e.run_id}</div>
                  ) : e.task_id ? (
                    <div className="text-muted-foreground">直发任务（无独立 Run ID）</div>
                  ) : null}
                  {e.execution_receipt_ref ? (
                    <div className="truncate">
                      回执：{RECEIPT_STATUS_LABEL[e.execution_receipt_status ?? ""] ?? e.execution_receipt_status ?? ""}
                      <span className="text-muted-foreground"> {e.execution_receipt_ref}</span>
                    </div>
                  ) : (
                    <div className="text-muted-foreground">无执行回执</div>
                  )}
                </>
              ) : e.execution_receipt_ref ? (
                <div className="truncate">
                  回执：{RECEIPT_STATUS_LABEL[e.execution_receipt_status ?? ""] ?? e.execution_receipt_status ?? ""}
                  <span className="text-muted-foreground"> {e.execution_receipt_ref}</span>
                </div>
              ) : null}
            </div>
            <div>
              <div className="font-medium text-foreground">时间线</div>
              {e.queued_at ? <div className="truncate">排队：{e.queued_at}</div> : null}
              {e.started_at ? <div className="truncate">开始：{e.started_at}</div> : null}
              {e.last_heartbeat_at ? (
                <div className="truncate">心跳：{e.last_heartbeat_at}</div>
              ) : null}
              {e.completed_at ? (
                <div className="truncate">完成：{e.completed_at}</div>
              ) : null}
              {e.last_event_at ? (
                <div className="truncate">最近事件：{e.last_event_at}</div>
              ) : null}
              <div className="truncate">观测：{e.observed_at}</div>
            </div>
          </div>

          {e.recent_events.length > 0 ? (
            <div className="flex flex-col gap-1">
              <div className="font-medium text-foreground">最近事件</div>
              {e.recent_events.slice(-5).map((ev) => (
                <div key={ev.event_id} className="truncate">
                  <span className="text-muted-foreground">{ev.kind}</span> · {ev.safe_summary}
                </div>
              ))}
            </div>
          ) : (
            <div>暂无活动事件</div>
          )}
          <div className="mt-1 text-[11px] text-muted-foreground" data-testid="owner-card-source-refs">
            出处：{e.source_refs.join(" ")}
          </div>
        </div>
      ) : null}
    </div>
  );
}

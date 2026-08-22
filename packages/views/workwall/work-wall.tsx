"use client";

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

function presenceText(p: PresenceState) {
  return `${PRESENCE_ICON[p]} ${PRESENCE_LABEL[p]}`;
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
      const hay = [e.display_name, e.project_title, e.issue_identifier, e.issue_title, e.model_name]
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
            className={`rounded px-2 py-1 ${presenceFilter === p ? "bg-green-900 text-green-100" : "bg-zinc-800 text-zinc-300"}`}
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
          className="rounded border border-green-900 bg-black px-2 py-1 text-green-200"
          data-testid="work-wall-search"
        />
        <FilterSelect label="项目" values={projects} value={projectFilter} onChange={setProjectFilter} />
        <FilterSelect label="Runtime" values={runtimes} value={runtimeFilter} onChange={setRuntimeFilter} />
        <FilterSelect label="模型" values={models} value={modelFilter} onChange={setModelFilter} />
      </div>

      <div className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3">
        {filtered.map((e) => (
          <TerminalCard
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
    <label className="flex items-center gap-1 text-zinc-400">
      {label}
      <select
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className="rounded border border-green-900 bg-black px-1 py-1 text-green-200"
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
      className="flex flex-wrap items-center gap-3 border border-green-900 bg-black px-3 py-2 font-mono text-xs text-green-300"
      data-testid="work-wall-status-bar"
    >
      <span>员工 {employees.length}</span>
      <span>工作中 {count("working")}</span>
      <span>排队中 {count("queued")}</span>
      <span>等待/阻塞 {waitingBlocked}</span>
      <span>空闲 {count("idle")}</span>
      <span>离线/未知 {offlineUnknown}</span>
      <span className="ml-auto">Token {totalTokens}</span>
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
    <div className="mb-2 flex flex-col gap-0.5" data-testid="terminal-card-chain">
      <div className="text-green-500">执行链</div>
      {e.project_id ? (
        <div className="truncate">
          Project {e.project_title ?? ""}
          <span className="text-green-700"> {e.project_id}</span>
        </div>
      ) : null}
      {e.issue_id ? (
        <div className="truncate">
          Issue {e.issue_identifier ? `${e.issue_identifier} · ` : ""}
          {e.issue_title ?? ""}
          <span className="text-green-700"> {e.issue_id}</span>
        </div>
      ) : null}
      {e.task_id ? (
        <div className="truncate">
          Task <span className="text-green-700">{e.task_id}</span>
          {e.run_id ? (
            <>
              {" · Run "}
              <span className="text-green-700">{e.run_id}</span>
            </>
          ) : (
            <span className="text-green-700"> · 无独立 Run ID（直发任务）</span>
          )}
        </div>
      ) : null}
      {e.runtime_profile_id ? (
        <div className="truncate">
          Profile {e.runtime_profile_name ?? ""}
          <span className="text-green-700"> {e.runtime_profile_id}</span>
        </div>
      ) : null}
      {e.execution_receipt_ref ? (
        <div className="truncate">
          Receipt {RECEIPT_STATUS_LABEL[e.execution_receipt_status ?? ""] ?? e.execution_receipt_status ?? ""}
          <span className="text-green-700"> {e.execution_receipt_ref}</span>
        </div>
      ) : null}
    </div>
  );
}

function TerminalCard({
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
      className={`terminal-card rounded-md border border-green-800 bg-black font-mono text-green-300 ${expanded ? "col-span-full" : ""}`}
      data-testid="terminal-card"
    >
      <button
        type="button"
        onClick={onToggle}
        className="flex w-full items-center gap-2 px-3 py-2 text-left"
        aria-expanded={expanded}
      >
        <span className="truncate text-sm font-semibold text-green-100">
          {e.display_name}
        </span>
        <span className="ml-auto whitespace-nowrap text-xs">
          {presenceText(e.presence_state)}
        </span>
        <span className="text-xs text-green-500">{expanded ? "−" : "+"}</span>
      </button>

      <div className="border-t border-green-900 px-3 py-2 text-xs">
        <div className="truncate">
          {e.model_name ?? "未计量"} · {e.runtime_provider ?? "无 runtime"}
        </div>
        {e.project_title ? (
          <div className="truncate">项目：{e.project_title}</div>
        ) : null}
        {e.issue_title ? (
          <div className="truncate">
            议题：{e.issue_identifier ? `${e.issue_identifier} · ` : ""}
            {e.issue_title}
          </div>
        ) : null}
        {e.work_stage !== "none" ? (
          <div>阶段：{STAGE_LABEL[e.work_stage] ?? e.work_stage}</div>
        ) : null}
        {e.blocked_reason ? <div>阻塞：{e.blocked_reason}</div> : null}
        {e.next_action ? <div className="truncate">下一动作：{e.next_action}</div> : null}
      </div>

      {expanded ? (
        <div className="border-t border-green-900 px-3 py-2 text-xs" data-testid="terminal-card-expanded">
          <ExecutionChainBlock employee={e} />
          {e.recent_events.length > 0 ? (
            <div className="flex flex-col gap-1">
              {e.recent_events.slice(-5).map((ev) => (
                <div key={ev.event_id} className="truncate">
                  <span className="text-green-600">{ev.kind}</span> · {ev.safe_summary}
                </div>
              ))}
            </div>
          ) : (
            <div>暂无活动事件</div>
          )}
          {e.last_heartbeat_at ? (
            <div className="mt-1">心跳：{e.last_heartbeat_at}</div>
          ) : null}
          <div className="mt-1">出处：{e.source_refs.join(" ")}</div>
        </div>
      ) : null}
    </div>
  );
}

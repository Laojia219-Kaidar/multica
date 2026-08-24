"use client";

import { useMemo, useState } from "react";
import type {
  A2Pane,
  PresenceState,
  TerminalPane,
  WorkStage,
} from "@multica/core/api/workwall";
import { joinA2PanesByEmployee } from "@multica/core/api/workwall";
import type { EmployeeLiveActivityV1 } from "@multica/core/api/workwall";
import {
  Card,
  CardContent,
  CardHeader,
} from "@multica/ui/components/ui/card";
import { Badge } from "@multica/ui/components/ui/badge";
import { Input } from "@multica/ui/components/ui/input";
import { ScrollArea } from "@multica/ui/components/ui/scroll-area";

// PAGE_SIZE fixed at 8 for the 4×2 CEO worksite.
const PAGE_SIZE = 8;

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

const PRESENCE_VARIANT: Record<
  PresenceState,
  "default" | "secondary" | "outline" | "destructive"
> = {
  working: "default",
  queued: "secondary",
  waiting: "secondary",
  recently_completed: "secondary",
  idle: "outline",
  offline: "outline",
  unknown: "outline",
  blocked: "destructive",
};

const STAGE_LABEL: Record<WorkStage, string> = {
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

const EXECUTION_LABEL: Record<string, string> = {
  active: "执行中",
  replay: "重放",
  failed: "失败",
  cancelled: "已取消",
  issue_state_mismatch: "状态不一致",
  completed: "已完成",
};

export interface WorkWallProps {
  employees: EmployeeLiveActivityV1[];
  panes: A2Pane[];
  terminalPresence: TerminalPane[];
}

/**
 * A2 工作现场 — 4×2 CEO worksite.
 *
 * Employee roster remains primary. A2 panes attach per employee
 * (first server-ordered pane wins). Terminal and event_console are
 * mutually exclusive: Terminal is shown ONLY when surface_kind=terminal
 * AND session_id is nonempty AND a matching TerminalPane.session_name
 * exists in terminalPresence. Otherwise the event console shows safe
 * A2 activity/execution/freshness/work fields.
 *
 * Geometry: xl breakpoint → 4 columns × 2 rows = 8 cards per page.
 * Pagination resets when filters change.
 */
export function WorkWall({ employees, panes, terminalPresence }: WorkWallProps) {
  const [presenceFilter, setPresenceFilter] = useState<PresenceState | "all">(
    "all",
  );
  const [query, setQuery] = useState("");
  const [projectFilter, setProjectFilter] = useState<string>("all");
  const [page, setPage] = useState(0);

  const projects = useMemo(
    () =>
      Array.from(
        new Set(
          employees.map((e) => e.project_title).filter((v): v is string => !!v),
        ),
      ).sort(),
    [employees],
  );

  const join = useMemo(
    () => joinA2PanesByEmployee(employees, panes),
    [employees, panes],
  );

  // Index terminal presence by session_name for fast lookup.
  const terminalBySession = useMemo(() => {
    const m = new Map<string, TerminalPane>();
    for (const tp of terminalPresence) {
      if (!m.has(tp.session_name)) m.set(tp.session_name, tp);
    }
    return m;
  }, [terminalPresence]);

  const q = query.trim().toLowerCase();
  const filtered = useMemo(() => {
    return employees.filter((e) => {
      if (presenceFilter !== "all" && e.presence_state !== presenceFilter) {
        return false;
      }
      if (projectFilter !== "all" && e.project_title !== projectFilter) {
        return false;
      }
      if (q !== "") {
        const hay = [
          e.display_name,
          e.project_title,
          e.issue_title,
          e.model_name,
        ]
          .filter((v): v is string => !!v)
          .join(" ")
          .toLowerCase();
        if (!hay.includes(q)) return false;
      }
      return true;
    });
  }, [employees, presenceFilter, projectFilter, q]);

  // Reset page to 0 whenever any filter changes.
  const totalPages = Math.max(1, Math.ceil(filtered.length / PAGE_SIZE));
  const safePage = Math.min(page, totalPages - 1);
  const pageItems = filtered.slice(
    safePage * PAGE_SIZE,
    (safePage + 1) * PAGE_SIZE,
  );

  const handlePresenceChange = (v: PresenceState | "all") => {
    setPresenceFilter(v);
    setPage(0);
  };
  const handleProjectChange = (v: string) => {
    setProjectFilter(v);
    setPage(0);
  };
  const handleQueryChange = (v: string) => {
    setQuery(v);
    setPage(0);
  };

  const activeCount = filtered.length;
  const unmatchedCount = join.unmatchedCount;

  return (
    <div className="work-wall flex flex-col gap-4" data-testid="work-wall">
      <div className="flex flex-wrap items-center gap-2">
        <div className="flex flex-wrap items-center gap-1">
          {(
            [
              "all",
              "working",
              "queued",
              "waiting",
              "blocked",
              "idle",
              "offline",
              "unknown",
              "recently_completed",
            ] as Array<PresenceState | "all">
          ).map((p) => (
            <button
              key={p}
              type="button"
              className={
                presenceFilter === p
                  ? "inline-flex h-7 items-center rounded-full border border-border bg-secondary px-2.5 text-xs font-medium text-secondary-foreground"
                  : "inline-flex h-7 items-center rounded-full border border-transparent px-2.5 text-xs text-muted-foreground hover:bg-muted hover:text-foreground"
              }
              onClick={() => handlePresenceChange(p)}
              data-testid={`work-wall-presence-${p}`}
            >
              {p === "all" ? "全部" : PRESENCE_LABEL[p as PresenceState]}
            </button>
          ))}
        </div>
        <div className="ml-auto flex items-center gap-2">
          <Input
            type="search"
            placeholder="搜索员工 / 项目 / 议题 / 模型"
            value={query}
            onChange={(e) => handleQueryChange(e.target.value)}
            className="h-8 w-56 text-xs"
            data-testid="work-wall-search"
          />
          {projects.length > 0 ? (
            <select
              value={projectFilter}
              onChange={(e) => handleProjectChange(e.target.value)}
              className="h-8 w-40 rounded-md border border-border bg-background px-2 text-xs text-foreground"
              data-testid="work-wall-filter-project"
            >
              <option value="all">全部项目</option>
              {projects.map((p) => (
                <option key={p} value={p}>
                  {p}
                </option>
              ))}
            </select>
          ) : null}
        </div>
      </div>

      <div
        className="grid grid-cols-1 gap-3 md:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4"
        data-testid="work-wall-grid"
      >
        {pageItems.map((emp) => {
          const pane = join.matched.get(emp.employee_id) ?? null;
          const terminalPane =
            pane?.surface_kind === "terminal" && pane.session_id
              ? terminalBySession.get(pane.session_id) ?? null
              : null;
          return (
            <WorkSiteCard
              key={emp.employee_id}
              employee={emp}
              pane={pane}
              terminalPane={terminalPane}
            />
          );
        })}
      </div>

      <div
        className="flex items-center justify-between text-xs text-muted-foreground"
        data-testid="work-wall-pagination"
      >
        <div>
          显示 {safePage * PAGE_SIZE + 1}–
          {Math.min((safePage + 1) * PAGE_SIZE, activeCount)} 共 {activeCount} 人
          {unmatchedCount > 0 ? (
            <span className="ml-2">
              · {unmatchedCount} 个未匹配 pane
            </span>
          ) : null}
        </div>
        <div className="flex items-center gap-1">
          <button
            type="button"
            disabled={safePage === 0}
            onClick={() => setPage((p) => Math.max(0, p - 1))}
            className="rounded border border-border px-2 py-1 text-xs disabled:opacity-40"
            data-testid="work-wall-prev-page"
          >
            上一页
          </button>
          <span className="tabular-nums">
            {safePage + 1} / {totalPages}
          </span>
          <button
            type="button"
            disabled={safePage >= totalPages - 1}
            onClick={() => setPage((p) => Math.min(totalPages - 1, p + 1))}
            className="rounded border border-border px-2 py-1 text-xs disabled:opacity-40"
            data-testid="work-wall-next-page"
          >
            下一页
          </button>
        </div>
      </div>
    </div>
  );
}

function WorkSiteCard({
  employee,
  pane,
  terminalPane,
}: {
  employee: EmployeeLiveActivityV1;
  pane: A2Pane | null;
  terminalPane: TerminalPane | null;
}) {
  const variant = PRESENCE_VARIANT[employee.presence_state];
  // Terminal shown only when: surface_kind=terminal + session_id nonempty + matching TerminalPane
  const showTerminal =
    pane?.surface_kind === "terminal" &&
    !!pane.session_id &&
    terminalPane !== null;
  // Event console shown only when a pane exists but no terminal shown
  const showEventConsole = pane !== null && !showTerminal;

  return (
    <Card
      className="flex h-full flex-col overflow-hidden"
      data-testid="work-site-card"
      data-employee-id={employee.employee_id}
    >
      <CardHeader className="flex flex-row items-center gap-2 py-3">
        <div className="flex min-w-0 flex-1 flex-col">
          <div className="flex items-center gap-2">
            <span className="truncate text-sm font-medium">
              {employee.display_name}
            </span>
            <Badge variant={variant} className="h-4 text-[10px]">
              {PRESENCE_LABEL[employee.presence_state]}
            </Badge>
          </div>
          <div className="mt-0.5 truncate text-xs text-muted-foreground">
            {employee.position_name ?? employee.work_stage
              ? `${employee.position_name ?? ""}${
                  employee.position_name && employee.work_stage ? " · " : ""
                }${STAGE_LABEL[employee.work_stage] ?? employee.work_stage}`
              : employee.department_name ?? "—"}
          </div>
        </div>
      </CardHeader>
      <CardContent className="flex flex-1 flex-col gap-2 pb-3 pt-0">
        {employee.project_title ? (
          <div className="truncate text-xs text-muted-foreground">
            <span className="text-foreground/60">项目：</span>
            {employee.project_title}
          </div>
        ) : null}
        {employee.issue_title ? (
          <div className="truncate text-xs">
            <span className="text-muted-foreground">
              {employee.issue_identifier ? `${employee.issue_identifier} ` : ""}
            </span>
            {employee.issue_title}
          </div>
        ) : null}

        {/* Pane region: terminal or event_console — mutually exclusive.
            Terminal shown only with real terminal presence (session match).
            When no pane is joined, we never fabricate one. */}
        {showTerminal ? (
          <div className="mt-auto">
            <div className="mb-1 flex items-center justify-between text-[10px] uppercase tracking-wide text-muted-foreground">
              <span>Terminal</span>
              <span
                className="font-mono tabular-nums"
                data-testid="pane-session-id"
              >
                {terminalPane.session_name}
              </span>
            </div>
            <ScrollArea
              className="h-24 rounded-md border border-surface-border bg-surface p-2 text-[11px] leading-relaxed"
              data-testid="pane-tail"
            >
              <pre className="whitespace-pre-wrap break-words font-mono text-surface-foreground/80">
                {terminalPane.tail_text || "（无输出）"}
              </pre>
            </ScrollArea>
          </div>
        ) : showEventConsole ? (
          <div className="mt-auto">
            <div className="mb-1 flex items-center justify-between text-[10px] uppercase tracking-wide text-muted-foreground">
              <span>事件台</span>
              <span
                className="font-mono tabular-nums"
                data-testid="pane-work-ref"
              >
                {pane.work_ref.slice(0, 24)}…
              </span>
            </div>
            <div className="rounded-md border border-border bg-muted/30 p-2 text-[11px]">
              <div className="flex items-center gap-2">
                <span className="text-muted-foreground">执行：</span>
                <span className="font-medium">
                  {EXECUTION_LABEL[pane.execution_state] ?? pane.execution_state}
                </span>
                {pane.working ? (
                  <Badge variant="default" className="h-3.5 text-[9px]">
                    活跃
                  </Badge>
                ) : null}
              </div>
              {pane.activity_summary ? (
                <div className="mt-1 truncate text-muted-foreground">
                  {pane.activity_summary}
                </div>
              ) : null}
              <div className="mt-1 text-[10px] text-muted-foreground">
                新鲜度：{pane.freshness_state}
              </div>
            </div>
          </div>
        ) : (
          <div className="mt-auto rounded-md border border-dashed border-border px-2 py-3 text-center text-[11px] text-muted-foreground">
            暂无现场 pane
          </div>
        )}

        <div className="flex items-center justify-between text-[10px] text-muted-foreground">
          <span className="truncate">
            {employee.model_name ?? "未计量"}
            {employee.runtime_provider ? ` · ${employee.runtime_provider}` : ""}
          </span>
          {employee.token_usage ? (
            <span className="font-mono tabular-nums">
              {employee.token_usage.toLocaleString()} tok
            </span>
          ) : null}
        </div>
      </CardContent>
    </Card>
  );
}

"use client";

/**
 * HIV-367 (P0-E) — pipeline projection UI primitives.
 *
 * These components sit ON TOP of the existing project board: they do not
 * replace IssueSurface / BoardView / BoardColumn / BoardCardContent. They add
 * a per-card latest-task badge + explicit processing-state marker (§3, §4)
 * and a per-column breakdown chip (§2) by consuming the read-only pipeline
 * projection from GET /api/projects/{id}/pipeline.
 *
 * The board components already exist and own card layout / virtualization /
 * DnD — reusing them keeps the contract's "不重做底座" promise. The pipeline
 * payload is keyed by issue id so a card can look up its row in O(1).
 */

import { createContext, memo, useContext, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { AlertCircle, CheckCircle2, Clock, Loader2, MinusCircle, PauseCircle, PlugZap } from "lucide-react";
import { useWorkspaceId } from "@multica/core/hooks";
import { projectPipelineOptions } from "@multica/core/projects";
import type {
  PipelineProcessingState,
  PipelineTaskClass,
  ProjectPipelineCapabilityFlags,
  ProjectPipelineColumn,
  ProjectPipelineIssue,
  ProjectPipelineResponse,
} from "@multica/core/projects";

/**
 * useProjectPipeline subscribes to the read-only projection for a project.
 * Returns the TanStack Query result so consumers can branch on isLoading /
 * isError / data. Polling + WS invalidation are configured in
 * projectPipelineOptions (queries.ts).
 */
export function useProjectPipeline(projectId: string | undefined | null) {
  const wsId = useWorkspaceId();
  return useQuery({
    ...projectPipelineOptions(wsId, projectId ?? ""),
    enabled: !!projectId,
  });
}

/**
 * The projection's lifecycle state on a project surface (contract §8). These
 * are deliberately distinct from the board's own loading/empty so the owner
 * can tell "pipeline is loading" apart from "pipeline is unavailable" apart
 * from "pipeline loaded but the column is empty".
 *
 * - "none"        — no provider mounted (non-project surface). The caller
 *                   falls back to its existing plain-count UI.
 * - "loading"     — initial fetch in flight, no data yet.
 * - "ready"       — fetch succeeded. Columns/issues may still be empty — that
 *                   is the legitimate "empty" state, rendered explicitly by
 *                   PipelineColumnBreakdown (total=0).
 * - "unavailable" — fetch errored. The UI shows an explicit marker instead of
 *                   silently degrading to plain counts.
 */
export type PipelineProjectionStatus = "loading" | "ready" | "unavailable";

interface ProjectPipelineContextValue {
  data: ProjectPipelineResponse | null;
  status: PipelineProjectionStatus;
}

/**
 * ProjectPipelineContext lets a deeply-nested board card / column header read
 * the active projection without prop-drilling through IssueSurface. The
 * provider is set up once on ProjectDetail; consumers fall back to null when
 * no projection is available (e.g. on non-project surfaces) and render
 * nothing — the existing IssueAgentActivityIndicator badge still gives the
 * user a live signal in that case.
 */
const ProjectPipelineContext = createContext<ProjectPipelineContextValue | null>(null);

/** Exposed for tests that need to inject mock projection state. */
export const __testPipelineContext = ProjectPipelineContext;

/** Provider — mount once on the project page. */
export function ProjectPipelineProvider({
  projectId,
  children,
}: {
  projectId: string;
  children: ReactNode;
}) {
  const { data, isError, isLoading } = useProjectPipeline(projectId);
  const status: PipelineProjectionStatus = isError
    ? "unavailable"
    : isLoading
      ? "loading"
      : "ready";
  return (
    <ProjectPipelineContext.Provider value={{ data: data ?? null, status }}>
      {children}
    </ProjectPipelineContext.Provider>
  );
}

/** Consumer hook — returns the active projection data or null. */
export function useProjectPipelineContext(): ProjectPipelineResponse | null {
  return useContext(ProjectPipelineContext)?.data ?? null;
}

/**
 * Returns the projection lifecycle status, or "none" when no provider is
 * mounted (non-project surface). Callers use this to separate loading /
 * unavailable / ready so the board never silently fakes a state (§8).
 */
export function usePipelineProjectionStatus(): PipelineProjectionStatus | "none" {
  const ctx = useContext(ProjectPipelineContext);
  return ctx?.status ?? "none";
}

/** Look up the per-issue row for a card; returns undefined when not loaded. */
export function usePipelineIssueRow(issueId: string): ProjectPipelineIssue | undefined {
  return useContext(ProjectPipelineContext)?.data?.issues?.[issueId];
}

/** Look up a column breakdown; returns undefined when not loaded. */
export function usePipelineColumn(status: string): ProjectPipelineColumn | undefined {
  return useContext(ProjectPipelineContext)?.data?.columns?.[status];
}

/** Look up capability flags; returns undefined when not loaded. */
export function usePipelineCapabilities(): ProjectPipelineCapabilityFlags | undefined {
  return useContext(ProjectPipelineContext)?.data?.capability_flags;
}

/** Map a processing state to a stable, i18n-friendly label + tone. */
const PROCESSING_STATE_META: Record<
  PipelineProcessingState,
  { tone: "warn" | "danger" | "muted" | "ok"; key: string }
> = {
  stale_awaiting_dispatch: { tone: "warn", key: "stale_awaiting_dispatch" },
  review_not_started: { tone: "warn", key: "review_not_started" },
  blocked_unhandled: { tone: "danger", key: "blocked_unhandled" },
  active: { tone: "ok", key: "active" },
  unknown: { tone: "muted", key: "unknown" },
};

/** Map a task class to an icon node for the card badge. */
function taskClassIcon(cls: PipelineTaskClass) {
  switch (cls) {
    case "running":
      return <Loader2 className="h-3 w-3 animate-spin text-blue-500" aria-label="running" />;
    case "queued":
      return <Clock className="h-3 w-3 text-amber-500" aria-label="queued" />;
    case "waiting_local_directory":
      return <PauseCircle className="h-3 w-3 text-amber-500" aria-label="waiting" />;
    case "failed":
      return <AlertCircle className="h-3 w-3 text-destructive" aria-label="failed" />;
    case "terminal":
      return <CheckCircle2 className="h-3 w-3 text-muted-foreground" aria-label="terminal" />;
    case "no_task":
      return <MinusCircle className="h-3 w-3 text-muted-foreground" aria-label="no task" />;
    default:
      return <MinusCircle className="h-3 w-3 text-muted-foreground" aria-label="unknown" />;
  }
}

/**
 * PipelineCardBadge renders the latest task status icon + an explicit
 * processing-state marker on a board card. It is intentionally tiny: a single
 * icon + one short label. Detailed Task/Run/Receipt info stays on the
 * existing issue-detail inspector (IssueDetail + ExecutionLogSection), which
 * already keys off issueKeys.tasks(issueId) — no duplication.
 *
 * Pass the per-issue pipeline row in directly; the parent looks it up once
 * from the projection's flat `issues` map. When `row` is undefined (projection
 * still loading, or the issue is filtered out), render nothing — the existing
 * IssueAgentActivityIndicator badge still gives the user a live signal.
 */
export const PipelineCardBadge = memo(function PipelineCardBadge({
  row,
}: {
  row: ProjectPipelineIssue | undefined;
}) {
  if (!row) return null;

  // Only render a marker when the state is non-active: the active case is
  // already covered by IssueAgentActivityIndicator, and stacking two badges
  // on a healthy card is noise. The explicit non-active markers are exactly
  // what §4 demands ("停滞/待重新派工" etc.).
  if (row.processing_state === "active") {
    // Still render the latest task icon for terminal/failed cards so the
    // owner gets a one-glance read on the latest run.
    if (row.task_class === "running" || row.task_class === "queued") return null;
  }

  const meta = PROCESSING_STATE_META[row.processing_state] ?? PROCESSING_STATE_META.unknown;
  const toneClass =
    meta.tone === "warn"
      ? "text-amber-600 dark:text-amber-400"
      : meta.tone === "danger"
        ? "text-destructive"
        : meta.tone === "ok"
          ? "text-emerald-600 dark:text-emerald-400"
          : "text-muted-foreground";

  // i18n: the pipeline namespace ships empty until the locale bundles catch
  // up; fall back to the stable wire key (the projection is honest even
  // without a localized label, and the locale bundle is a separate concern).
  const labelKey = meta.key;

  return (
    <span
      className={`inline-flex items-center gap-1 text-[10px] leading-tight ${toneClass}`}
      data-pipeline-state={row.processing_state}
      data-task-class={row.task_class}
      title={row.failure_reason || row.wait_reason || undefined}
    >
      {taskClassIcon(row.task_class)}
      <span className="truncate">{labelKey}</span>
    </span>
  );
});

/**
 * PipelineColumnBreakdown renders the per-status column header chip required
 * by §2: total / running / queued / waiting / failed / terminal-no-writeback /
 * no-task. It collapses into a single "total" when the column is healthy
 * (everything is either running or no issues) so a quiet project stays quiet;
 * the moment any unhealthy counter rises — or any task is queued — the chip
 * expands to show the breakdown.
 *
 * Pass the column payload + a boolean indicating whether the owner wants the
 * verbose breakdown always (e.g. a "diagnostics" view toggle later).
 */
export const PipelineColumnBreakdown = memo(function PipelineColumnBreakdown({
  column,
  forceVerbose = false,
}: {
  column: ProjectPipelineColumn | undefined;
  forceVerbose?: boolean;
}) {
  if (!column) return null;

  const unhealthy =
    column.failed +
    column.terminal_no_writeback +
    column.no_task +
    column.unknown;
  const verbose = forceVerbose || unhealthy > 0 || column.waiting > 0 || column.queued > 0;

  if (!verbose) {
    // Healthy column — show total + running only.
    return (
      <span className="inline-flex items-center gap-1 text-[10px] text-muted-foreground">
        <span>{column.total}</span>
        {column.running > 0 && (
          <span className="inline-flex items-center gap-0.5 text-blue-500">
            <Loader2 className="h-2.5 w-2.5 animate-spin" />
            {column.running}
          </span>
        )}
      </span>
    );
  }

  return (
    <span
      className="inline-flex flex-wrap items-center gap-x-2 gap-y-0.5 text-[10px] text-muted-foreground"
      data-pipeline-column={column.status}
      data-pipeline-unhealthy={unhealthy}
    >
      <span className="font-medium text-foreground/80">{column.total}</span>
      {column.running > 0 && (
        <span className="inline-flex items-center gap-0.5 text-blue-500">
          <Loader2 className="h-2.5 w-2.5 animate-spin" />
          {column.running}
        </span>
      )}
      {column.queued > 0 && (
        <span
          className="inline-flex items-center gap-0.5 text-amber-500"
          data-task-class="queued"
        >
          <Clock className="h-2.5 w-2.5" />
          {column.queued}
        </span>
      )}
      {column.waiting > 0 && (
        <span className="inline-flex items-center gap-0.5 text-amber-500">
          <PauseCircle className="h-2.5 w-2.5" />
          {column.waiting}
        </span>
      )}
      {column.failed > 0 && (
        <span className="inline-flex items-center gap-0.5 text-destructive">
          <AlertCircle className="h-2.5 w-2.5" />
          {column.failed}
        </span>
      )}
      {column.terminal_no_writeback > 0 && (
        <span className="inline-flex items-center gap-0.5 text-amber-600 dark:text-amber-400">
          <CheckCircle2 className="h-2.5 w-2.5" />
          {column.terminal_no_writeback}
          <span className="hidden sm:inline">no-writeback</span>
        </span>
      )}
      {column.no_task > 0 && (
        <span className="inline-flex items-center gap-0.5 text-muted-foreground">
          <MinusCircle className="h-2.5 w-2.5" />
          {column.no_task}
        </span>
      )}
      {column.unknown > 0 && (
        <span className="inline-flex items-center gap-0.5 text-muted-foreground">
          ?{column.unknown}
        </span>
      )}
    </span>
  );
});

/**
 * Capability keys whose server-side command is NOT yet wired. Each entry is
 * rendered as an explicit "能力待接入" chip so the owner never sees a fake
 * working button for an action the backend cannot perform (contract §6). The
 * labels are short wire identifiers; the locale layer can localize them
 * later — the key point is the UI is honest about what is unavailable.
 */
const PENDING_CAPABILITY_LABELS: { key: keyof ProjectPipelineCapabilityFlags; label: string }[] = [
  { key: "dispatch_preview", label: "Dispatch Preview" },
  { key: "dispatch", label: "Dispatch" },
  { key: "project_start", label: "Project Start" },
];

/**
 * PipelineCapabilityBar renders the fail-closed capability surface (§6). It
 * consumes the projection's `capability_flags` and shows an explicit "能力待
 * 接入" chip for every canonical action the server does not yet wire. When
 * all actions are available (the eventual steady state), the bar renders
 * nothing. It NEVER fabricates a working button for an unavailable action.
 *
 * Mount on the project page so the owner can see at a glance which dispatch /
 * start commands are still pending integration.
 */
export const PipelineCapabilityBar = memo(function PipelineCapabilityBar() {
  const flags = usePipelineCapabilities();
  if (!flags) return null;

  const pending = PENDING_CAPABILITY_LABELS.filter((c) => !flags[c.key]);
  if (pending.length === 0) return null;

  return (
    <div
      className="flex flex-wrap items-center gap-1.5"
      data-pipeline-capability-bar
      data-pending-count={pending.length}
    >
      {pending.map((c) => (
        <span
          key={c.key}
          className="inline-flex items-center gap-1 rounded-full bg-amber-50 dark:bg-amber-950/30 px-2 py-0.5 text-[10px] font-medium text-amber-700 dark:text-amber-300 ring-1 ring-inset ring-amber-200 dark:ring-amber-800"
          data-capability={c.key}
          data-capability-available="false"
          title={`${c.label}: 能力待接入`}
        >
          <PlugZap className="h-3 w-3" />
          {c.label}
          <span className="opacity-70">· 能力待接入</span>
        </span>
      ))}
    </div>
  );
});

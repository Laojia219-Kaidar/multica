"use client";

import { Skeleton } from "@multica/ui/components/ui/skeleton";
import type {
  EmployeeStateExplanation,
  EmployeeStatus,
} from "@multica/core/agents";
import { useT } from "../../i18n";

// Compact staleness for the runtime-health line: "45s" / "3m" / "2h" / "5d".
// Capped at days — the explainer is an operator surface, not a clock.
export function formatStalenessCompact(ms: number): string {
  const sec = Math.max(0, Math.round(ms / 1000));
  if (sec < 60) return `${sec}s`;
  const min = Math.floor(sec / 60);
  if (min < 60) return `${min}m`;
  const hr = Math.floor(min / 60);
  if (hr < 24) return `${hr}h`;
  return `${Math.floor(hr / 24)}d`;
}

interface StatusVisual {
  dotClass: string;
  textClass: string;
}

// Colour language mirrors agent-presence-indicator.tsx: blue = active work,
// gray = nothing to act on, amber = worth a look. Waiting follows the same
// availability composition rule — on a healthy runtime it's a transient
// race (muted), otherwise a genuine stuck signal (amber).
function statusVisual(explanation: EmployeeStateExplanation): StatusVisual {
  const status: EmployeeStatus = explanation.status;
  if (status === "working") {
    return { dotClass: "bg-brand", textClass: "text-brand" };
  }
  if (status === "waiting") {
    return explanation.availability === "online"
      ? {
          dotClass: "bg-muted-foreground/40",
          textClass: "text-muted-foreground",
        }
      : { dotClass: "bg-warning", textClass: "text-warning" };
  }
  // idle + unavailable both read gray — the reason line carries the why.
  return {
    dotClass: "bg-muted-foreground/40",
    textClass: "text-muted-foreground",
  };
}

interface EmployeeStatusExplainerProps {
  // null/undefined = still loading (same contract as AgentPresenceIndicator).
  explanation: EmployeeStateExplanation | null | undefined;
  // Compact = one truncated line for dense list cells. Full (default) is the
  // multi-line operator explanation for detail surfaces.
  compact?: boolean;
}

/**
 * Employee status explanation surface: working / idle / waiting /
 * unavailable plus the exact reason, the current task/run on record,
 * capacity (fail-closed "unknown" when the quota field is unusable),
 * runtime-health staleness, and the next recovery action.
 *
 * Pure presentation — the explanation object is derived in
 * @multica/core/agents (derive-employee-state.ts) from server facts only;
 * this component never classifies on its own. All state is exposed through
 * data-* hooks (data-employee-status / data-state-reason / data-next-action
 * / data-current-task-id / data-capacity / data-runtime-health) so tests
 * assert state, not pixels.
 */
export function EmployeeStatusExplainer({
  explanation,
  compact,
}: EmployeeStatusExplainerProps) {
  const { t } = useT("agents");

  if (!explanation) {
    return compact ? (
      <Skeleton className="h-3 w-24 rounded" />
    ) : (
      <div className="space-y-1.5">
        <Skeleton className="h-3.5 w-28 rounded" />
        <Skeleton className="h-3 w-44 max-w-full rounded" />
        <Skeleton className="h-3 w-36 max-w-full rounded" />
      </div>
    );
  }

  const visual = statusVisual(explanation);
  const statusLabel = t(($) => $.status_explanation.status[explanation.status]);
  const reasonText = t(
    ($) => $.status_explanation.reason[explanation.reason],
    { count: explanation.runningCount || explanation.workspaceBacklogCount },
  );
  const actionText = t(
    ($) => $.status_explanation.action[explanation.nextAction],
  );

  if (compact) {
    return (
      <span
        className="inline-flex min-w-0 items-center gap-1"
        data-employee-status={explanation.status}
        data-state-reason={explanation.reason}
        data-next-action={explanation.nextAction}
        title={`${statusLabel} · ${reasonText} · ${actionText}`}
      >
        <span
          className={`h-1.5 w-1.5 shrink-0 rounded-full ${visual.dotClass}`}
        />
        <span className={`truncate text-[11px] ${visual.textClass}`}>
          {statusLabel}
          <span className="text-muted-foreground"> · {reasonText}</span>
        </span>
      </span>
    );
  }

  const staleness = explanation.runtimeStalenessMs;
  return (
    <div
      className="space-y-2 text-xs"
      data-employee-status-explainer
      data-employee-status={explanation.status}
    >
      <div className="flex items-center gap-1.5">
        <span
          className={`h-1.5 w-1.5 shrink-0 rounded-full ${visual.dotClass}`}
        />
        <span className={`font-medium ${visual.textClass}`}>{statusLabel}</span>
      </div>

      <p className="text-muted-foreground" data-state-reason={explanation.reason}>
        {reasonText}
      </p>

      {explanation.currentTask && (
        <p
          className="text-muted-foreground"
          data-current-task
          data-current-task-id={explanation.currentTask.id}
        >
          {t(($) => $.status_explanation.current_task)}
          {": "}
          <span className="font-mono tabular-nums">
            #{explanation.currentTask.id.slice(0, 8)}
          </span>
          {" · "}
          {explanation.currentTask.status}
        </p>
      )}

      <p
        className="text-muted-foreground"
        data-capacity={explanation.capacity ?? "unknown"}
      >
        {t(($) => $.status_explanation.capacity)}
        {": "}
        {explanation.capacity === null ? (
          <span>{t(($) => $.status_explanation.capacity_unknown)}</span>
        ) : (
          <span className="font-mono tabular-nums">
            {t(($) => $.status_explanation.capacity_value, {
              running: explanation.runningCount,
              capacity: explanation.capacity,
            })}
          </span>
        )}
      </p>

      <p
        className="text-muted-foreground"
        data-runtime-health={explanation.runtimeHealth}
        data-runtime-staleness-ms={staleness ?? ""}
      >
        {t(($) => $.status_explanation.runtime_health)}
        {": "}
        {t(($) => $.status_explanation.health[explanation.runtimeHealth])}
        {staleness !== null && explanation.runtimeHealth !== "online" && (
          <span>
            {" · "}
            {t(($) => $.status_explanation.last_seen, {
              ago: formatStalenessCompact(staleness),
            })}
          </span>
        )}
      </p>

      <p className="text-muted-foreground" data-next-action={explanation.nextAction}>
        {t(($) => $.status_explanation.next_action)}
        {": "}
        {actionText}
      </p>
    </div>
  );
}

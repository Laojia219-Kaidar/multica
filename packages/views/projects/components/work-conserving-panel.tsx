"use client";

import { AlertTriangle, CheckCircle2, CircleSlash2, ShieldCheck } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { cn } from "@multica/ui/lib/utils";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { useWorkspaceId } from "@multica/core/hooks";
import { projectWorkConservingOptions } from "@multica/core/projects/queries";
import type { WorkConservingProjection } from "@multica/core/types";
import { useT } from "../../i18n";

function StateIcon({ state }: { state: WorkConservingProjection["state"] }) {
  if (state === "ready") return <CheckCircle2 className="size-4 text-emerald-600" />;
  if (state === "blocked") return <CircleSlash2 className="size-4 text-amber-600" />;
  return <AlertTriangle className="size-4 text-muted-foreground" />;
}

function Metric({ label, value }: { label: string; value: number }) {
  return (
    <div className="rounded-md bg-muted/35 px-3 py-2">
      <div className="text-[11px] text-muted-foreground">{label}</div>
      <div className="mt-0.5 text-sm font-semibold tabular-nums">{value}</div>
    </div>
  );
}

export function WorkConservingPanel({ projectId }: { projectId: string }) {
  const { t } = useT("projects");
  const workspaceId = useWorkspaceId();
  const { data, isLoading, isError } = useQuery(projectWorkConservingOptions(workspaceId, projectId));

  if (isLoading) {
    return (
      <section className="mx-6 mt-4 rounded-lg border bg-card p-4" aria-busy="true">
        <Skeleton className="h-5 w-48" />
        <Skeleton className="mt-3 h-4 w-80" />
        <Skeleton className="mt-4 h-16 w-full" />
      </section>
    );
  }

  const projection = isError || !data
    ? {
        state: "source_gap" as const,
        blocked: true,
        goalId: null,
        authority: null,
        suggestions: [],
        blockedBacklog: [],
        mismatch: {
          openIssues: 0,
          plannedIssues: 0,
          blockedBacklog: 0,
          healthyIdleEmployees: 0,
          unmatchedHealthyIdleEmployees: 0,
          executableBacklog: 0,
          idleBacklogMismatch: 0,
        },
        total: 0,
        noWrite: true as const,
      }
    : data;
  const stateLabel = t(($) => $.detail.work_conserving.status[projection.state]);
  const stateDescription = projection.state === "ready"
    ? t(($) => $.detail.work_conserving.ready_description)
    : projection.state === "blocked"
      ? t(($) => $.detail.work_conserving.blocked_description)
      : t(($) => $.detail.work_conserving.source_gap_description);

  return (
    <section className="mx-6 mt-4 rounded-lg border bg-card p-4" aria-label={t(($) => $.detail.work_conserving.title)}>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex items-center gap-2 text-sm font-semibold">
            <StateIcon state={projection.state} />
            <span>{t(($) => $.detail.work_conserving.title)}</span>
            <span className={cn(
              "rounded-full border px-2 py-0.5 text-[11px] font-medium",
              projection.state === "ready" && "border-emerald-500/30 text-emerald-700",
              projection.state === "blocked" && "border-amber-500/30 text-amber-700",
              projection.state === "source_gap" && "border-muted text-muted-foreground",
            )}>
              {stateLabel}
            </span>
          </div>
          <p className="mt-1 text-xs text-muted-foreground">{stateDescription}</p>
        </div>
        <div className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
          <ShieldCheck className="size-3.5" />
          {t(($) => $.detail.work_conserving.no_write)}
        </div>
      </div>

      <div className="mt-4 grid grid-cols-2 gap-2 sm:grid-cols-4">
        <Metric label={t(($) => $.detail.work_conserving.metrics.open)} value={projection.mismatch.openIssues} />
        <Metric label={t(($) => $.detail.work_conserving.metrics.planned)} value={projection.mismatch.plannedIssues} />
        <Metric label={t(($) => $.detail.work_conserving.metrics.blocked)} value={projection.mismatch.blockedBacklog} />
        <Metric label={t(($) => $.detail.work_conserving.metrics.idle)} value={projection.mismatch.healthyIdleEmployees} />
      </div>

      {projection.authority && (
        <div className="mt-3 grid gap-1 text-[11px] text-muted-foreground sm:grid-cols-2">
          <div className="truncate">
            {t(($) => $.detail.work_conserving.authority.revision)}: {projection.authority.revision}
          </div>
          <div className="truncate">
            {t(($) => $.detail.work_conserving.authority.observed_at)}: {projection.authority.observedAt}
          </div>
        </div>
      )}

      {projection.state !== "source_gap" && (
        <div className="mt-3 border-t pt-3">
          <div className="text-xs font-medium">{t(($) => $.detail.work_conserving.plan_title)}</div>
          {projection.suggestions.length === 0 && projection.blockedBacklog.length === 0 ? (
            <p className="mt-1 text-xs text-muted-foreground">{t(($) => $.detail.work_conserving.empty_plan)}</p>
          ) : (
            <div className="mt-2 space-y-1.5">
              {projection.suggestions.slice(0, 5).map((suggestion) => (
                <div key={suggestion.issueId} className="rounded-md bg-muted/25 px-2.5 py-2 text-xs">
                  <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5">
                    <span className="font-medium">{suggestion.issueId}</span>
                    <span className="text-muted-foreground">{suggestion.employeeId}</span>
                    <span className="text-muted-foreground">{suggestion.receiver}</span>
                  </div>
                  <div className="mt-0.5 text-[11px] text-muted-foreground">
                    {t(($) => $.detail.work_conserving.wake_condition)}: {suggestion.wakeCondition}
                  </div>
                </div>
              ))}
              {projection.blockedBacklog.slice(0, 5).map((issue) => (
                <div key={issue.issueId} className="rounded-md bg-amber-500/5 px-2.5 py-2 text-xs">
                  <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5">
                    <span className="font-medium">{issue.issueId}</span>
                    <span className="text-muted-foreground">{issue.receiver}</span>
                  </div>
                  <div className="mt-0.5 text-[11px] text-muted-foreground">
                    {t(($) => $.detail.work_conserving.wake_condition)}: {issue.wakeCondition}
                  </div>
                </div>
              ))}
            </div>
          )}
        </div>
      )}
    </section>
  );
}

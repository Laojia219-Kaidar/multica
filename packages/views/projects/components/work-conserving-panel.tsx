"use client";

import { useMemo, useState } from "react";
import { AlertTriangle, CheckCircle2, CircleSlash2, ShieldCheck, Zap, Loader2 } from "lucide-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { cn } from "@multica/ui/lib/utils";
import { Button } from "@multica/ui/components/ui/button";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { api } from "@multica/core/api";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import { memberListOptions } from "@multica/core/workspace/queries";
import { projectKeys, projectWorkConservingOptions } from "@multica/core/projects/queries";
import type { WorkConservingDrainResult, WorkConservingProjection } from "@multica/core/types";
import { useT } from "../../i18n";

function StateIcon({ state }: { state: WorkConservingProjection["state"] }) {
  if (state === "ready") return <CheckCircle2 className="size-4 text-success" />;
  if (state === "blocked") return <CircleSlash2 className="size-4 text-warning" />;
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

function isOwnerOrAdmin(
  members: { user_id: string; role: string }[] | undefined,
  userId: string | null | undefined,
): boolean | null {
  if (!userId || !members) return null;
  const me = members.find((m) => m.user_id === userId);
  return me?.role === "owner" || me?.role === "admin" || false;
}

export function WorkConservingPanel({ projectId }: { projectId: string }) {
  const { t } = useT("projects");
  const workspaceId = useWorkspaceId();
  const qc = useQueryClient();
  const userId = useAuthStore((s) => s.user?.id);
  const { data: members, isLoading: membersLoading, isError: membersError } = useQuery({
    ...memberListOptions(workspaceId),
    enabled: !!workspaceId,
  });
  const { data, isLoading, isError } = useQuery(projectWorkConservingOptions(workspaceId, projectId));
  const [lastResult, setLastResult] = useState<WorkConservingDrainResult | null>(null);

  const isAdmin = useMemo(
    () => isOwnerOrAdmin(members, userId),
    [members, userId],
  );

  const drain = useMutation({
    mutationFn: () => api.drainProjectNextActions(projectId),
    onMutate: () => {
      setLastResult(null);
    },
    onSuccess: (result) => {
      qc.invalidateQueries({ queryKey: projectKeys.workConserving(workspaceId, projectId) });
      if (result.state === "source_gap") {
        toast.error(t(($) => $.detail.work_conserving.drain.toast_authority_gap));
        return;
      }
      if (result.dispatched === 0) {
        toast.info(t(($) => $.detail.work_conserving.drain.toast_no_ready));
        setLastResult(result);
        return;
      }
      toast.success(t(($) => $.detail.work_conserving.drain.toast_success));
      setLastResult(result);
    },
    onError: () => {
      toast.error(t(($) => $.detail.work_conserving.drain.toast_error));
    },
  });

  if (isLoading) {
    return (
      <section className="mx-6 mt-4 rounded-lg border bg-card p-4" aria-busy="true">
        <Skeleton className="h-5 w-48" />
        <Skeleton className="mt-3 h-4 w-80" />
        <Skeleton className="mt-4 h-16 w-full" />
      </section>
    );
  }

  const rawProjection = isError || !data
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
  const projection = rawProjection.state === "source_gap"
    ? {
        ...rawProjection,
        authority: null,
        suggestions: [],
        blockedBacklog: [],
        total: 0,
        mismatch: {
          openIssues: 0,
          plannedIssues: 0,
          blockedBacklog: 0,
          healthyIdleEmployees: 0,
          unmatchedHealthyIdleEmployees: 0,
          executableBacklog: 0,
          idleBacklogMismatch: 0,
        },
      }
    : rawProjection;
  const stateLabel = t(($) => $.detail.work_conserving.status[projection.state]);
  const stateDescription = projection.state === "ready"
    ? t(($) => $.detail.work_conserving.ready_description)
    : projection.state === "blocked"
      ? t(($) => $.detail.work_conserving.blocked_description)
      : t(($) => $.detail.work_conserving.source_gap_description);

  const canDrain = isAdmin === true && projection.state !== "source_gap" && projection.suggestions.length > 0;
  const drainDisabled = !canDrain || drain.isPending;

  return (
    <section className="mx-6 mt-4 rounded-lg border bg-card p-4" aria-label={t(($) => $.detail.work_conserving.title)}>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex items-center gap-2 text-sm font-semibold">
            <StateIcon state={projection.state} />
            <span>{t(($) => $.detail.work_conserving.title)}</span>
            <span className={cn(
              "rounded-full border px-2 py-0.5 text-[11px] font-medium",
              projection.state === "ready" && "border-success/30 text-success",
              projection.state === "blocked" && "border-warning/30 text-warning",
              projection.state === "source_gap" && "border-muted text-muted-foreground",
            )}>
              {stateLabel}
            </span>
          </div>
          <p className="mt-1 text-xs text-muted-foreground">{stateDescription}</p>
        </div>
        <div className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
          <ShieldCheck className="size-3.5" />
          {isAdmin === true && projection.state !== "source_gap"
            ? t(($) => $.detail.work_conserving.bounded_dispatch)
            : t(($) => $.detail.work_conserving.no_write)}
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

      {isAdmin === true && projection.state !== "source_gap" && !membersLoading && !membersError && (
        <div className="mt-3 border-t pt-3">
          <Button
            size="sm"
            variant="outline"
            className="h-7 gap-1.5 text-xs"
            disabled={drainDisabled}
            onClick={() => drain.mutate()}
          >
            {drain.isPending ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Zap className="h-3.5 w-3.5" />}
            {drain.isPending
              ? t(($) => $.detail.work_conserving.drain.button_pending)
              : t(($) => $.detail.work_conserving.drain.button)}
          </Button>
        </div>
      )}

      {lastResult && lastResult.state === "ready" && lastResult.results.length > 0 && (
        <div className="mt-3 border-t pt-3">
          <div className="flex flex-wrap gap-2 text-[11px]">
            <span className="rounded bg-muted/40 px-2 py-0.5">
              {t(($) => $.detail.work_conserving.drain.counters.dispatched)}: <strong>{lastResult.dispatched}</strong>
            </span>
            <span className="rounded bg-muted/40 px-2 py-0.5">
              {t(($) => $.detail.work_conserving.drain.counters.already_terminal)}: <strong>{lastResult.alreadyTerminal}</strong>
            </span>
            <span className="rounded bg-muted/40 px-2 py-0.5">
              {t(($) => $.detail.work_conserving.drain.counters.blocked)}: <strong>{lastResult.blocked}</strong>
            </span>
            <span className="rounded bg-muted/40 px-2 py-0.5">
              {t(($) => $.detail.work_conserving.drain.counters.conflicts)}: <strong>{lastResult.conflicts}</strong>
            </span>
            <span className="rounded bg-muted/40 px-2 py-0.5">
              {t(($) => $.detail.work_conserving.drain.counters.source_gaps)}: <strong>{lastResult.sourceGaps}</strong>
            </span>
            <span className="rounded bg-muted/40 px-2 py-0.5">
              {t(($) => $.detail.work_conserving.drain.counters.deferred)}: <strong>{lastResult.deferredSuggestions}</strong>
            </span>
          </div>
          <ul className="mt-2 space-y-1.5">
            {lastResult.results.map((row) => (
              <li key={row.issueId} className="rounded-md bg-muted/25 px-2.5 py-2 text-xs">
                <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5">
                  <span className="font-medium">{row.issueId}</span>
                  <span className="text-muted-foreground">{row.receiver}</span>
                  <span className={cn(
                    "rounded-full px-1.5 py-0 text-[10px] font-medium",
                    row.outcome === "dispatched" && "bg-success/15 text-success",
                    row.outcome === "already_terminal" && "bg-muted text-muted-foreground",
                    row.outcome === "blocked" && "bg-warning/15 text-warning",
                    row.outcome === "conflict" && "bg-rose-100 text-rose-700",
                    row.outcome === "source_gap" && "bg-muted text-muted-foreground",
                  )}>
                    {t(($) => $.detail.work_conserving.drain.outcome[row.outcome])}
                  </span>
                </div>
                {row.receipt && (
                  <div className="mt-0.5 truncate text-[11px] text-muted-foreground">
                    {t(($) => $.detail.work_conserving.drain.receipt_label)}: {row.receipt.taskId}
                  </div>
                )}
              </li>
            ))}
          </ul>
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
                <div key={issue.issueId} className="rounded-md bg-warning/5 px-2.5 py-2 text-xs">
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

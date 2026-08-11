"use client";

import { useMemo } from "react";
import {
  FileCheck2,
  FolderKanban,
  ListTodo,
  UserRound,
  Bot,
  Play,
  ExternalLink,
  GitBranch,
  History,
} from "lucide-react";
import { cn } from "@multica/ui/lib/utils";
import { Badge } from "@multica/ui/components/ui/badge";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { buttonVariants } from "@multica/ui/components/ui/button";
import { useWorkspacePaths } from "@multica/core/paths";
import type { CompanyOpsOutcomeDetail } from "@multica/core/types";
import { useT } from "../i18n";
import { isOutcomeFormal, isOutcomePromotable } from "./outcome-actions";
import { OutcomeSessionGate } from "./outcome-session-gate";
import type { OutcomeActions } from "./outcome-actions";

const STATUS_LOCALE_KEYS = {
  awaiting_claim: "awaiting_claim",
  running: "running",
  completed: "completed",
  failed: "failed",
  cancelled: "cancelled",
  submitted: "submitted",
  changes_requested: "changes_requested",
  approved: "approved",
  promotion_requested: "promotion_requested",
  promotion_succeeded: "promotion_succeeded",
  promotion_failed: "promotion_failed",
  authority_readback_confirmed: "authority_readback_confirmed",
} as const;

type OutcomeStatusKey = keyof typeof STATUS_LOCALE_KEYS;

function MetaRow({
  icon,
  label,
  children,
}: {
  icon: React.ReactNode;
  label: string;
  children: React.ReactNode;
}) {
  return (
    <div className="flex items-start gap-2.5">
      <span className="mt-0.5 shrink-0 text-muted-foreground">{icon}</span>
      <div className="min-w-0 flex-1">
        <div className="text-xs text-muted-foreground">{label}</div>
        <div className="mt-0.5 min-w-0 truncate text-sm">{children}</div>
      </div>
    </div>
  );
}

export interface OutcomeDetailProps {
  wsId: string;
  detail: CompanyOpsOutcomeDetail;
  sessionId: string;
  onSessionIdChange: (sessionId: string) => void;
  actions: OutcomeActions;
  onReread: () => void;
  rereading: boolean;
}

export function OutcomeDetail({
  wsId,
  detail,
  sessionId,
  onSessionIdChange,
  actions,
  onReread,
  rereading,
}: OutcomeDetailProps) {
  const { t } = useT("outcomes");
  const wsPaths = useWorkspacePaths();
  const summary = detail.summary;
  const candidate = summary.active_artifact ?? null;
  const promotable = useMemo(() => isOutcomePromotable(summary), [summary]);
  const formal = useMemo(() => isOutcomeFormal(summary), [summary]);
  const title = summary.issue
    ? `${summary.issue.identifier}: ${summary.issue.title}`
    : summary.id;

  const statusLabels = useMemo(() => {
    const labels: Record<OutcomeStatusKey, string> = {
      awaiting_claim: t(($) => $.statuses.awaiting_claim),
      running: t(($) => $.statuses.running),
      completed: t(($) => $.statuses.completed),
      failed: t(($) => $.statuses.failed),
      cancelled: t(($) => $.statuses.cancelled),
      submitted: t(($) => $.statuses.submitted),
      changes_requested: t(($) => $.statuses.changes_requested),
      approved: t(($) => $.statuses.approved),
      promotion_requested: t(($) => $.statuses.promotion_requested),
      promotion_succeeded: t(($) => $.statuses.promotion_succeeded),
      promotion_failed: t(($) => $.statuses.promotion_failed),
      authority_readback_confirmed: t(($) => $.statuses.authority_readback_confirmed),
    };
    return labels;
  }, [t]);

  const statusLabel = useMemo(
    () => (value: string): string =>
      value in statusLabels ? statusLabels[value as OutcomeStatusKey] : value,
    [statusLabels],
  );

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex-1 min-h-0 overflow-y-auto">
        <div className="max-w-2xl space-y-5 p-4">
          {/* Header */}
          <div>
            <div className="flex flex-wrap items-center gap-2">
              <FileCheck2 className="size-5 text-muted-foreground" />
              <h1 className="min-w-0 flex-1 truncate text-base font-semibold">
                {title}
              </h1>
            </div>
            <div className="mt-2 flex flex-wrap items-center gap-2">
              <Badge variant="outline">{statusLabel(summary.execution_state)}</Badge>
              {candidate && (
                <Badge variant="outline">
                  {statusLabel(candidate.status)}
                </Badge>
              )}
              {formal ? (
                <Badge>{t(($) => $.detail.formal_readback_confirmed)}</Badge>
              ) : (
                <Badge variant="secondary">
                  {t(($) => $.detail.formal_not_promoted)}
                </Badge>
              )}
              {promotable && (
                <Badge variant="outline" className="text-[10px]">
                  {t(($) => $.statuses.approved)}
                </Badge>
              )}
            </div>
            <p className="mt-2 truncate text-xs text-muted-foreground">
              {summary.id}
            </p>
          </div>

          {/* Meta */}
          <div className="grid grid-cols-1 gap-3 rounded-md border p-3 sm:grid-cols-2">
            <MetaRow icon={<ListTodo className="size-4" />} label={t(($) => $.detail.issue_label)}>
              {summary.issue ? (
                <a
                  href={wsPaths.issueDetail(summary.issue.id)}
                  className="text-foreground underline-offset-4 hover:underline"
                >
                  #{summary.issue.number} {summary.issue.title}
                </a>
              ) : (
                <span className="text-muted-foreground">—</span>
              )}
            </MetaRow>
            <MetaRow icon={<FolderKanban className="size-4" />} label={t(($) => $.detail.project_label)}>
              {summary.issue?.project_id ? (
                <a
                  href={wsPaths.projectDetail(summary.issue.project_id)}
                  className="text-foreground underline-offset-4 hover:underline"
                >
                  {summary.issue.project_id}
                </a>
              ) : (
                <span className="text-muted-foreground">—</span>
              )}
            </MetaRow>
            <MetaRow icon={<UserRound className="size-4" />} label={t(($) => $.detail.employee_label)}>
              {summary.employee.id}
            </MetaRow>
            <MetaRow icon={<Bot className="size-4" />} label={t(($) => $.detail.agent_label)}>
              {summary.current_agent_display.name}
            </MetaRow>
            <MetaRow icon={<Play className="size-4" />} label={t(($) => $.detail.run_label)}>
              {summary.current_task_id ? (
                <span className="tabular-nums">
                  {summary.current_task_id}
                  <span className="ml-1.5 text-xs text-muted-foreground">
                    {summary.execution_state}
                  </span>
                </span>
              ) : (
                <span className="text-muted-foreground">—</span>
              )}
            </MetaRow>
          </div>

          {/* Current candidate preview entry */}
          {candidate && (
            <div className="rounded-md border p-3">
              <div className="flex items-center gap-2 text-sm font-medium">
                <ExternalLink className="size-4 text-muted-foreground" />
                <span>{t(($) => $.detail.candidate_label)}</span>
              </div>
              <div className="mt-2 flex flex-wrap items-center gap-2">
                <Badge variant="secondary" className="text-[11px]">
                  {t(($) => $.detail.revision, { revision: candidate.revision })}
                </Badge>
                <span className="truncate text-xs text-muted-foreground">
                  {candidate.digest.slice(0, 16)}
                </span>
                {summary.issue && (
                  <a
                    href={wsPaths.issueDetail(summary.issue.id)}
                    className={buttonVariants({
                      size: "sm",
                      variant: "outline",
                      className: "ml-auto gap-1.5",
                    })}
                  >
                    <ExternalLink className="size-3.5" />
                    {t(($) => $.detail.candidate_preview)}
                  </a>
                )}
              </div>
            </div>
          )}

          {/* Formal status */}
          <div className="rounded-md border p-3">
            <div className="flex items-center gap-2 text-sm font-medium">
              <FileCheck2 className="size-4 text-muted-foreground" />
              <span>{t(($) => $.detail.formal_label)}</span>
            </div>
            <div className="mt-2 flex flex-wrap items-center gap-2 text-xs">
              {formal ? (
                <Badge>{t(($) => $.detail.formal_readback_confirmed)}</Badge>
              ) : (
                <Badge variant="secondary">
                  {t(($) => $.detail.formal_not_promoted)}
                </Badge>
              )}
              {formal && candidate?.formal_artifact_ref && (
                <span
                  className="min-w-0 truncate text-muted-foreground"
                  title={candidate.formal_artifact_ref}
                >
                  {t(($) => $.detail.formal_ref_label)}: {candidate.formal_artifact_ref}
                </span>
              )}
            </div>
          </div>

          {/* Session gate + review actions */}
          <OutcomeSessionGate
            wsId={wsId}
            summary={summary}
            sessionId={sessionId}
            onSessionIdChange={onSessionIdChange}
            actions={actions}
            onReread={onReread}
            rereading={rereading}
          />

          {/* Versions */}
          <section>
            <div className="flex items-center gap-2 text-sm font-medium">
              <GitBranch className="size-4 text-muted-foreground" />
              <span>{t(($) => $.detail.versions_label)}</span>
            </div>
            {detail.versions.length === 0 ? (
              <p className="mt-2 text-xs text-muted-foreground">
                {t(($) => $.detail.versions_empty)}
              </p>
            ) : (
              <ul className="mt-2 space-y-1.5">
                {detail.versions.map((v) => (
                  <li
                    key={v.id}
                    className={cn(
                      "flex items-center gap-2 rounded-md border px-2.5 py-1.5 text-sm",
                      candidate?.id === v.id && "border-primary/50 bg-muted/40",
                    )}
                  >
                    <Badge variant="secondary" className="text-[10px]">
                      {t(($) => $.detail.revision, { revision: v.revision })}
                    </Badge>
                    <span className="min-w-0 flex-1 truncate text-xs text-muted-foreground">
                      {v.digest.slice(0, 16)}
                    </span>
                    {v.content_type && (
                      <Badge variant="outline" className="text-[10px]">
                        {v.content_type}
                      </Badge>
                    )}
                  </li>
                ))}
              </ul>
            )}
          </section>

          {/* Events timeline */}
          <section>
            <div className="flex items-center gap-2 text-sm font-medium">
              <History className="size-4 text-muted-foreground" />
              <span>{t(($) => $.detail.events_label)}</span>
            </div>
            {detail.events.length === 0 ? (
              <p className="mt-2 text-xs text-muted-foreground">
                {t(($) => $.detail.events_empty)}
              </p>
            ) : (
              <ol className="mt-2 space-y-0 border-l pl-3">
                {detail.events.map((e) => (
                  <li key={e.id} className="relative pb-3 last:pb-0">
                    <span className="absolute -left-[17px] top-1.5 size-2 rounded-full bg-border" />
                    <div className="text-sm">{e.type}</div>
                    <div className="text-xs text-muted-foreground">
                      {t(($) => $.detail.revision, { revision: e.candidate_revision })}
                      {e.formal_artifact_ref ? ` · ${e.formal_artifact_ref}` : ""}
                    </div>
                  </li>
                ))}
              </ol>
            )}
          </section>

          {/* Runs */}
          <section>
            <div className="flex items-center gap-2 text-sm font-medium">
              <Play className="size-4 text-muted-foreground" />
              <span>{t(($) => $.detail.runs_label)}</span>
            </div>
            {detail.runs.length === 0 ? (
              <p className="mt-2 text-xs text-muted-foreground">
                {t(($) => $.detail.runs_empty)}
              </p>
            ) : (
              <ul className="mt-2 space-y-1.5">
                {detail.runs.map((r) => (
                  <li
                    key={r.task_id}
                    className="flex items-center gap-2 rounded-md border px-2.5 py-1.5 text-xs"
                  >
                    <span className="min-w-0 flex-1 truncate tabular-nums text-muted-foreground">
                      {r.task_id}
                    </span>
                    <Badge variant="outline" className="text-[10px]">
                      {r.status}
                    </Badge>
                  </li>
                ))}
              </ul>
            )}
          </section>
        </div>
      </div>
    </div>
  );
}

export function OutcomeDetailSkeleton() {
  const { t } = useT("outcomes");
  return (
    <div className="p-4">
      <Skeleton className="h-6 w-48" />
      <Skeleton className="mt-3 h-4 w-32" />
      <div className="mt-4 grid grid-cols-1 gap-3 sm:grid-cols-2">
        <Skeleton className="h-16 w-full" />
        <Skeleton className="h-16 w-full" />
      </div>
      <Skeleton className="mt-4 h-24 w-full" />
      <p className="mt-4 text-xs text-muted-foreground">{t(($) => $.page.loading)}</p>
    </div>
  );
}

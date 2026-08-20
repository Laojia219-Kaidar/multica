"use client";

import { ArrowUpRight, FileCheck2, ShieldAlert, ShieldCheck } from "lucide-react";
import type { ReviewQueueItem } from "@multica/core/types";
import { Badge } from "@multica/ui/components/ui/badge";
import { AppLink } from "../navigation";

export type ReviewQueueDisposition = "blocked" | "active" | "owner_decision";

export interface ReviewQueueReadiness {
  authorityReady: boolean;
  outcomeCenterReady: boolean;
}

const ACTIVE_REVIEW_TASK_STATUSES = new Set([
  "queued",
  "dispatched",
  "running",
  "waiting_local_directory",
]);

export function reviewQueueDisposition(
  item: ReviewQueueItem,
  readiness: ReviewQueueReadiness,
): ReviewQueueDisposition {
  if (!readiness.authorityReady || !readiness.outcomeCenterReady || !item.reviewState) return "blocked";
  if (item.reviewState === "owner_decision") return "owner_decision";
  if (item.reviewStateReason) return "blocked";
  if (
    !item.reviewerAgentId
    || !item.reviewTargetTaskId
    || !item.reviewTaskStatus
    || !ACTIVE_REVIEW_TASK_STATUSES.has(item.reviewTaskStatus)
  ) return "blocked";
  return "active";
}

export interface CommandReviewFrontierCopy {
  eyebrow: string;
  title: string;
  description: string;
  loading: string;
  empty: string;
  blockedTitle: string;
  blockedDescription: string;
  ownerReady: string;
  activeReview: string;
  blockedReview: string;
  openOwnerDecision: string;
  openDetail: string;
  reviewer: string;
  outcomeCenter: string;
}

export interface CommandReviewFrontierProps {
  issues: ReviewQueueItem[];
  loading: boolean;
  error: boolean;
  authorityReady: boolean;
  outcomeCenterReady: boolean;
  issueHref: (issueId: string) => string;
  outcomesHref: string;
  copy: CommandReviewFrontierCopy;
}

export function CommandReviewFrontier({
  issues,
  loading,
  error,
  authorityReady,
  outcomeCenterReady,
  issueHref,
  outcomesHref,
  copy,
}: CommandReviewFrontierProps) {
  const readiness = { authorityReady, outcomeCenterReady };
  const providerBlocked = !authorityReady || !outcomeCenterReady;
  const outcomeLinkReady = !error && outcomeCenterReady;

  const issueList = issues.length > 0 ? (
    <ol className="divide-y">
      {issues.map((issue) => {
        const disposition = reviewQueueDisposition(issue, readiness);
        const label = disposition === "owner_decision"
          ? copy.ownerReady
          : disposition === "active"
            ? copy.activeReview
            : copy.blockedReview;
        return (
          <li key={issue.issueId} className="grid gap-3 px-5 py-4 transition-colors hover:bg-muted/20 md:grid-cols-[minmax(0,1fr)_auto] md:items-center">
            <div className="min-w-0">
              <div className="flex flex-wrap items-center gap-2">
                <span className="font-mono text-xs font-semibold text-muted-foreground">{issue.identifier}</span>
                <Badge variant={disposition === "blocked" ? "destructive" : "outline"}>{label}</Badge>
                {issue.reviewState && <span className="text-xs text-muted-foreground">{issue.reviewState}</span>}
              </div>
              <p className="mt-1 truncate text-sm font-medium text-foreground">{issue.title}</p>
              <div className="mt-1 flex flex-wrap gap-x-3 gap-y-1 text-xs text-muted-foreground">
                {issue.reviewerName && <span>{copy.reviewer}: {issue.reviewerName}</span>}
                {issue.reviewStateReason && <code>{issue.reviewStateReason}</code>}
              </div>
            </div>
            <AppLink
              href={issueHref(issue.issueId)}
              className="inline-flex items-center gap-1 text-sm font-medium text-foreground underline-offset-4 hover:underline"
            >
              {disposition === "owner_decision" ? copy.openOwnerDecision : copy.openDetail}
              <ArrowUpRight className="size-3.5" />
            </AppLink>
          </li>
        );
      })}
    </ol>
  ) : null;

  return (
    <section
      aria-labelledby="command-review-frontier-title"
      className="overflow-hidden rounded-2xl border bg-card shadow-sm"
      data-testid="command-review-frontier"
    >
      <header className="flex flex-col gap-4 border-b bg-muted/20 px-5 py-5 sm:flex-row sm:items-end sm:justify-between">
        <div className="max-w-2xl">
          <p className="text-[11px] font-semibold uppercase tracking-[0.18em] text-muted-foreground">
            {copy.eyebrow}
          </p>
          <div className="mt-2 flex items-center gap-2">
            <ShieldCheck className="size-4 text-foreground" />
            <h3 id="command-review-frontier-title" className="text-lg font-semibold tracking-tight">
              {copy.title}
            </h3>
            {!loading && !error && <Badge variant="secondary">{issues.length}</Badge>}
          </div>
          <p className="mt-1 text-sm text-muted-foreground">{copy.description}</p>
        </div>
        {outcomeLinkReady ? (
          <AppLink
            href={outcomesHref}
            className="inline-flex items-center gap-1.5 text-sm font-medium text-foreground underline-offset-4 hover:underline"
          >
            <FileCheck2 className="size-4" />
            {copy.outcomeCenter}
            <ArrowUpRight className="size-3.5" />
          </AppLink>
        ) : (
          <span aria-disabled="true" className="inline-flex items-center gap-1.5 text-sm font-medium text-muted-foreground">
            <ShieldAlert className="size-4" />
            {copy.outcomeCenter}
          </span>
        )}
      </header>

      {loading ? (
        <p className="px-5 py-8 text-sm text-muted-foreground">{copy.loading}</p>
      ) : (
        <>
          {(error || providerBlocked) && (
            <div className="m-4 flex gap-3 rounded-xl border border-destructive/30 bg-destructive/5 p-4" data-testid="review-frontier-blocked">
              <ShieldAlert className="mt-0.5 size-5 shrink-0 text-destructive" />
              <div>
                <p className="text-sm font-semibold text-foreground">{copy.blockedTitle}</p>
                <p className="mt-1 text-sm text-muted-foreground">{copy.blockedDescription}</p>
              </div>
            </div>
          )}
          {!error && issueList}
          {!error && !providerBlocked && issues.length === 0 && (
            <p className="px-5 py-8 text-sm text-muted-foreground">{copy.empty}</p>
          )}
        </>
      )}
    </section>
  );
}

"use client";

import { cn } from "@multica/ui/lib/utils";
import type { ProjectHealth, ProjectLifecycleSnapshot } from "@multica/core/types";
import { useT } from "../../i18n";

// The five honest buckets from the goal's Slice 1. A project's derived A-G
// health maps onto exactly one bucket; owner_decision_required stays a badge.
export type HealthBucket = "active" | "review" | "blocked" | "stalled" | "ready";

export function healthBucketOf(s: ProjectLifecycleSnapshot): HealthBucket {
  switch (s.health) {
    case "active_with_frontier":
      return "active";
    case "duplicate_or_superseded":
      return "blocked"; // duplicate authority blocks further auto-work
    case "review_or_repair_blocked":
      return s.blocked_issue_count > 0 ? "blocked" : "review";
    case "stalled_no_open_task":
      return "stalled";
    case "source_gap":
      // Missing closure evidence blocks closure; surfacing it as "blocked" is
      // more honest than "ready to close" (Quinn review O1).
      return "blocked";
    default:
      return "ready"; // ready_for_closure

  }
}

const BUCKET_LABEL_KEY = {
  active: "bucket_active",
  review: "bucket_review",
  blocked: "bucket_blocked",
  stalled: "bucket_stalled",
  ready: "bucket_ready",
} as const;

const HEALTH_BADGE: Record<ProjectHealth, { bg: string; text: string }> = {
  active_with_frontier: { bg: "bg-emerald-500/10", text: "text-emerald-700" },
  stalled_no_open_task: { bg: "bg-amber-500/10", text: "text-amber-700" },
  review_or_repair_blocked: { bg: "bg-orange-500/10", text: "text-orange-700" },
  ready_for_closure: { bg: "bg-sky-500/10", text: "text-sky-700" },
  duplicate_or_superseded: { bg: "bg-purple-500/10", text: "text-purple-700" },
  source_gap: { bg: "bg-rose-500/10", text: "text-rose-700" },
};

// ProjectHealthBadge renders the derived health + an owner-decision flag. It is
// a READ-ONLY display: unlike the status dropdown it never mutates state.
export function ProjectHealthBadge({
  snapshot,
  className,
}: {
  snapshot: ProjectLifecycleSnapshot;
  className?: string;
}) {
  const { t } = useT("projects");
  const cfg = HEALTH_BADGE[snapshot.health];
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-xs font-medium",
        cfg.bg,
        cfg.text,
        className,
      )}
      title={snapshot.next_action}
    >
      {t(($) => $.health[snapshot.health])}
      {snapshot.owner_decision_required && (
        <span className="rounded bg-rose-100 px-1 text-[10px] font-semibold text-rose-700">
          {t(($) => $.health.owner_decision)}
        </span>
      )}
    </span>
  );
}

// HealthBucketSummary renders the five-bucket classification counts for the
// current portfolio. It is the Slice 1 "honest classification" surface.
export function HealthBucketSummary({ items }: { items: ProjectLifecycleSnapshot[] }) {
  const { t } = useT("projects");
  const buckets: HealthBucket[] = ["active", "review", "blocked", "stalled", "ready"];
  const counts: Record<HealthBucket, number> = {
    active: 0,
    review: 0,
    blocked: 0,
    stalled: 0,
    ready: 0,
  };
  for (const s of items) counts[healthBucketOf(s)] += 1;
  return (
    <div className="flex flex-wrap items-center gap-1.5 px-5 pb-2">
      {buckets.map((b) => (
        <span
          key={b}
          className="inline-flex items-center gap-1.5 rounded-full border px-2 py-0.5 text-[11px] text-muted-foreground"
        >
          {t(($) => $.health[BUCKET_LABEL_KEY[b]])}
          <span className="font-semibold tabular-nums text-foreground">{counts[b]}</span>
        </span>
      ))}
    </div>
  );
}

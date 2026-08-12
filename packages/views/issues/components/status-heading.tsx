import type { IssueStatus } from "@multica/core/types";
import { AlertCircle, Loader2 } from "lucide-react";
import { StatusIcon } from "./status-icon";
import { useT } from "../../i18n";
// HIV-367 (P0-E): pipeline column breakdown + projection lifecycle status.
// Both self-no-op outside a project surface (usePipelineColumn /
// usePipelineProjectionStatus return undefined / "none"), so a non-project
// board pays nothing.
import {
  PipelineColumnBreakdown,
  usePipelineColumn,
  usePipelineProjectionStatus,
} from "../../projects/components/pipeline-projection";

export function StatusHeading({
  status,
  count,
}: {
  status: IssueStatus;
  count: number;
}) {
  const { t } = useT("issues");
  // HIV-367 (P0-E): subscribe to the per-status column breakdown and the
  // projection lifecycle status. Contract §8 requires loading / empty /
  // unavailable to be explicitly separated — the board must never silently
  // fake a state by falling back to the plain count when the projection is
  // loading or errored.
  const pipelineColumn = usePipelineColumn(status);
  const projectionStatus = usePipelineProjectionStatus();

  let breakdownNode: React.ReactNode;
  switch (projectionStatus) {
    case "none":
      // Non-project surface — preserve the existing plain-count look.
      breakdownNode = (
        <span className="text-xs text-muted-foreground">{count}</span>
      );
      break;
    case "loading":
      // Projection is fetching for the first time — show a pulsing placeholder
      // instead of silently falling back to the plain count.
      breakdownNode = (
        <span
          className="inline-flex items-center gap-0.5 text-[10px] text-muted-foreground/60"
          data-pipeline-loading
        >
          <Loader2 className="h-2.5 w-2.5 animate-spin" />
        </span>
      );
      break;
    case "unavailable":
      // Projection query errored — show an explicit "unavailable" marker
      // alongside the plain count so the owner knows the breakdown is stale,
      // never a silent degradation (§8).
      breakdownNode = (
        <span
          className="inline-flex items-center gap-0.5 text-[10px] text-amber-600 dark:text-amber-400"
          data-pipeline-unavailable
          title="Pipeline projection unavailable — showing plain count"
        >
          <AlertCircle className="h-2.5 w-2.5" />
          <span className="text-xs text-muted-foreground">{count}</span>
        </span>
      );
      break;
    case "ready":
    default:
      // Projection loaded. PipelineColumnBreakdown renders the full breakdown
      // chip (or total=0 when the column is legitimately empty — that IS the
      // explicit empty state).
      breakdownNode = <PipelineColumnBreakdown column={pipelineColumn} />;
      break;
  }

  return (
    <div className="flex items-center gap-2">
      <span className="inline-flex items-center gap-1.5 text-xs font-semibold">
        <StatusIcon status={status} className="h-3 w-3" />
        {t(($) => $.status[status])}
      </span>
      {breakdownNode}
    </div>
  );
}

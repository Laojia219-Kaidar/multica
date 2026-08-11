"use client";

import { useCallback, useMemo } from "react";
import { FileCheck2, Search } from "lucide-react";
import { cn } from "@multica/ui/lib/utils";
import { Input } from "@multica/ui/components/ui/input";
import { NativeSelect, NativeSelectOption } from "@multica/ui/components/ui/native-select";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { Badge } from "@multica/ui/components/ui/badge";
import type { CompanyOpsOutcomeSummary } from "@multica/core/types";
import { useT } from "../i18n";
import { isOutcomeFormal } from "./outcome-actions";

export interface OutcomeListProps {
  outcomes: CompanyOpsOutcomeSummary[];
  total: number;
  loading: boolean;
  error: unknown;
  selectedCommandId: string;
  q: string;
  status: string;
  onQChange: (q: string) => void;
  onStatusChange: (status: string) => void;
  onSelect: (summary: CompanyOpsOutcomeSummary) => void;
}

const STATUS_OPTIONS = [
  "",
  "awaiting_claim",
  "running",
  "completed",
  "failed",
  "cancelled",
  "submitted",
  "changes_requested",
  "approved",
  "promotion_requested",
  "promotion_succeeded",
  "promotion_failed",
  "authority_readback_confirmed",
] as const;

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

export function OutcomeList({
  outcomes,
  total,
  loading,
  error,
  selectedCommandId,
  q,
  status,
  onQChange,
  onStatusChange,
  onSelect,
}: OutcomeListProps) {
  const { t } = useT("outcomes");

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

  const statusLabel = useCallback(
    (value: string): string => {
      if (!value) return "";
      if (value in statusLabels) {
        return statusLabels[value as OutcomeStatusKey];
      }
      return value;
    },
    [statusLabels],
  );

  const countingLabel = useMemo(
    () => t(($) => $.list.total, { count: total }),
    [t, total],
  );

  if (loading) {
    return (
      <div className="flex-1 min-h-0 overflow-y-auto space-y-1 p-2">
        {Array.from({ length: 5 }).map((_, i) => (
          <div key={i} className="flex items-center gap-3 px-2 py-2.5">
            <Skeleton className="h-7 w-7 shrink-0 rounded-md" />
            <div className="flex-1 space-y-2">
              <Skeleton className="h-4 w-3/4" />
              <Skeleton className="h-3 w-1/2" />
            </div>
          </div>
        ))}
      </div>
    );
  }

  if (error) {
    return (
      <div className="flex-1 min-h-0 overflow-y-auto">
        <div className="flex flex-col items-center justify-center py-16 text-muted-foreground">
          <Search className="mb-3 h-8 w-8 text-muted-foreground/40" />
          <p className="px-8 text-center text-sm">
            {t(($) => $.page.error_description)}
          </p>
        </div>
      </div>
    );
  }

  return (
    <div className="flex flex-1 min-h-0 flex-col">
      <div className="shrink-0 space-y-2 border-b p-2">
        <Input
          value={q}
          onChange={(e) => onQChange(e.target.value)}
          placeholder={t(($) => $.list.search_placeholder)}
          className="h-8"
          aria-label={t(($) => $.list.search_placeholder)}
        />
        <NativeSelect
          value={status}
          onChange={(e) => onStatusChange(e.target.value)}
          aria-label={t(($) => $.list.status_filter_label)}
        >
          <NativeSelectOption value="">{t(($) => $.list.status_all)}</NativeSelectOption>
          {STATUS_OPTIONS.filter(Boolean).map((s) => (
            <NativeSelectOption key={s} value={s}>
              {statusLabel(s)}
            </NativeSelectOption>
          ))}
        </NativeSelect>
      </div>

      <div className="flex shrink-0 items-center justify-between px-3 py-2 text-xs text-muted-foreground">
        <span className="tabular-nums">{countingLabel}</span>
      </div>

      <div className="flex-1 min-h-0 overflow-y-auto pb-2">
        {outcomes.length === 0 ? (
          <div className="flex flex-col items-center justify-center py-16 text-center text-muted-foreground">
            <FileCheck2 className="mb-3 h-8 w-8 text-muted-foreground/40" />
            <p className="px-8 text-sm">{t(($) => $.page.empty_description)}</p>
          </div>
        ) : (
          <ul className="space-y-0.5 px-1">
            {outcomes.map((outcome) => {
              const isActive = outcome.id === selectedCommandId;
              const title = outcome.issue
                ? `${outcome.issue.identifier}: ${outcome.issue.title}`
                : outcome.id;
              return (
                <li key={outcome.id}>
                  <button
                    type="button"
                    onClick={() => onSelect(outcome)}
                    className={cn(
                      "flex w-full items-start gap-2.5 rounded-md px-2 py-2 text-left transition-colors",
                      isActive
                        ? "bg-accent"
                        : "hover:bg-accent/50",
                    )}
                  >
                    <FileCheck2
                      className={cn(
                        "mt-0.5 size-4 shrink-0",
                        isActive ? "text-foreground" : "text-muted-foreground",
                      )}
                    />
                    <span className="min-w-0 flex-1">
                      <span className="block truncate text-sm font-medium">
                        {title}
                      </span>
                      <span className="mt-0.5 block truncate text-xs text-muted-foreground">
                        {outcome.id}
                      </span>
                      <span className="mt-1 flex flex-wrap items-center gap-1.5">
                        {outcome.issue && (
                          <Badge variant="outline" className="text-[10px]">
                            #{outcome.issue.number}
                          </Badge>
                        )}
                        <Badge variant="outline" className="text-[10px]">
                          {statusLabel(outcome.execution_state) || outcome.execution_state}
                        </Badge>
                        {isOutcomeFormal(outcome) && (
                          <Badge className="text-[10px]">
                            {t(($) => $.detail.formal_readback_confirmed)}
                          </Badge>
                        )}
                      </span>
                    </span>
                  </button>
                </li>
              );
            })}
          </ul>
        )}
      </div>
    </div>
  );
}
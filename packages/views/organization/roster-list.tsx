"use client";

import { useCallback, useMemo } from "react";
import { Search, UserRound } from "lucide-react";
import { cn } from "@multica/ui/lib/utils";
import { Input } from "@multica/ui/components/ui/input";
import {
  NativeSelect,
  NativeSelectOption,
} from "@multica/ui/components/ui/native-select";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import type { CompanyOpsRosterItem } from "@multica/core/types";
import { useT } from "../i18n";
import {
  ORGANIZATION_BINDING_STATES,
  organizationBindingFilterOptions,
} from "./binding-state";
import { BindingBadge } from "./binding-badge";
import { BindingStateExplanation } from "./source-gap";

export interface RosterListProps {
  items: CompanyOpsRosterItem[];
  total: number;
  loading: boolean;
  error: unknown;
  selectedEmployeeId: string;
  q: string;
  status: string;
  onQChange: (q: string) => void;
  onStatusChange: (status: string) => void;
  onSelect: (item: CompanyOpsRosterItem) => void;
}

export function RosterList({
  items,
  total,
  loading,
  error,
  selectedEmployeeId,
  q,
  status,
  onQChange,
  onStatusChange,
  onSelect,
}: RosterListProps) {
  const { t } = useT("organization");

  const statusLabels = useMemo(() => {
    const labels: Partial<Record<(typeof ORGANIZATION_BINDING_STATES)[number], string>> = {};
    for (const state of ORGANIZATION_BINDING_STATES) {
      labels[state] = t(($) => $.states[state]);
    }
    return labels;
  }, [t]);

  const statusLabel = useCallback(
    (value: string): string => {
      if (!value) return "";
      return statusLabels[value as (typeof ORGANIZATION_BINDING_STATES)[number]] ?? value;
    },
    [statusLabels],
  );

  const countingLabel = useMemo(
    () => t(($) => $.roster.total, { count: total }),
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
          placeholder={t(($) => $.roster.search_placeholder)}
          className="h-8"
          aria-label={t(($) => $.roster.search_placeholder)}
        />
        <NativeSelect
          value={status}
          onChange={(e) => onStatusChange(e.target.value)}
          aria-label={t(($) => $.roster.status_filter_label)}
        >
          {organizationBindingFilterOptions().map((value) => (
            <NativeSelectOption key={value || "all"} value={value}>
              {value ? statusLabel(value) : t(($) => $.roster.status_all)}
            </NativeSelectOption>
          ))}
        </NativeSelect>
      </div>

      <div className="flex shrink-0 items-center justify-between px-3 py-2 text-xs text-muted-foreground">
        <span className="tabular-nums">{countingLabel}</span>
      </div>

      <div className="flex-1 min-h-0 overflow-y-auto pb-2">
        {items.length === 0 ? (
          <div className="flex flex-col items-center justify-center py-16 text-center text-muted-foreground">
            <UserRound className="mb-3 h-8 w-8 text-muted-foreground/40" />
            <p className="px-8 text-sm">{t(($) => $.roster.empty_description)}</p>
          </div>
        ) : (
          <ul className="space-y-0.5 px-1">
            {items.map((item) => {
              const isActive = item.employee_id === selectedEmployeeId;
              return (
                <li key={item.employee_id}>
                  <button
                    type="button"
                    onClick={() => onSelect(item)}
                    className={cn(
                      "flex w-full items-start gap-2.5 rounded-md px-2 py-2 text-left transition-colors",
                      isActive ? "bg-accent" : "hover:bg-accent/50",
                    )}
                  >
                    <UserRound
                      className={cn(
                        "mt-0.5 size-4 shrink-0",
                        isActive ? "text-foreground" : "text-muted-foreground",
                      )}
                    />
                    <span className="min-w-0 flex-1">
                      <span className="block truncate text-sm font-medium">
                        {item.display_name ?? item.employee_id}
                      </span>
                      <span className="mt-0.5 block truncate text-xs text-muted-foreground">
                        {item.employee_id}
                      </span>
                      <span className="mt-1.5 flex flex-wrap items-center gap-1.5">
                        <BindingBadge state={item.binding_state} />
                        {item.workforce_agent_id && (
                          <span className="rounded bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground">
                            {item.workforce_agent_id}
                          </span>
                        )}
                      </span>
                    </span>
                  </button>
                  {item.binding_state !== "available" && (
                    <div className="px-2 pb-1.5">
                      <BindingStateExplanation state={item.binding_state} />
                    </div>
                  )}
                </li>
              );
            })}
          </ul>
        )}
      </div>
    </div>
  );
}
"use client";

import { Building2, ChevronRight, FolderKanban, UserRound } from "lucide-react";
import { cn } from "@multica/ui/lib/utils";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@multica/ui/components/ui/collapsible";
import type { CompanyOpsDepartmentNode } from "@multica/core/types";
import { useT } from "../i18n";
import { formatAuthorityTime } from "./format-time";

interface OrgTreeProps {
  departments: CompanyOpsDepartmentNode[];
  observedAt?: string;
  loading: boolean;
  error: unknown;
  onSelectEmployee: (employeeId: string) => void;
}

export type { OrgTreeProps };

export function OrgTree({
  departments,
  observedAt,
  loading,
  error,
  onSelectEmployee,
}: OrgTreeProps) {
  const { t } = useT("organization");

  if (loading) {
    return (
      <div className="flex-1 min-h-0 overflow-y-auto space-y-3 p-3">
        {Array.from({ length: 3 }).map((_, i) => (
          <div key={i} className="space-y-2">
            <Skeleton className="h-5 w-2/3" />
            <Skeleton className="ml-4 h-4 w-1/2" />
            <Skeleton className="ml-8 h-4 w-1/3" />
          </div>
        ))}
      </div>
    );
  }

  if (error) {
    return (
      <div className="flex-1 min-h-0 overflow-y-auto p-4">
        <div className="flex flex-col items-center justify-center gap-3 py-16 text-center text-muted-foreground">
          <Building2 className="size-8 text-muted-foreground/40" />
          <p className="px-8 text-sm">{t(($) => $.page.error_description)}</p>
        </div>
      </div>
    );
  }

  if (departments.length === 0) {
    return (
      <div className="flex-1 min-h-0 overflow-y-auto p-4">
        <div className="flex flex-col items-center justify-center gap-3 py-16 text-center text-muted-foreground">
          <Building2 className="size-8 text-muted-foreground/40" />
          <p className="px-8 text-sm">{t(($) => $.page.empty_description)}</p>
        </div>
      </div>
    );
  }

  return (
    <div className="flex-1 min-h-0 overflow-y-auto">
      <div className="space-y-4 p-3">
        {departments.map((department) => (
          <Collapsible key={department.department_id} defaultOpen>
            <CollapsibleTrigger className="group flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm font-medium hover:bg-accent/50">
              <ChevronRight className="size-4 shrink-0 text-muted-foreground transition-transform group-data-[panel-open]:rotate-90" />
              <Building2 className="size-4 shrink-0 text-muted-foreground" />
              <span className="min-w-0 flex-1 truncate">{department.name}</span>
              <span className="shrink-0 text-[10px] text-muted-foreground">
                {department.department_id}
              </span>
            </CollapsibleTrigger>
            <CollapsibleContent>
              {department.positions.length === 0 ? (
                <p className="px-8 py-1 text-xs text-muted-foreground">
                  {t(($) => $.tree.no_positions)}
                </p>
              ) : (
                department.positions.map((position) => (
                  <Collapsible
                    key={position.position_id}
                    defaultOpen
                    className="ml-4"
                  >
                    <CollapsibleTrigger className="group flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm hover:bg-accent/50">
                      <ChevronRight className="size-4 shrink-0 text-muted-foreground transition-transform group-data-[panel-open]:rotate-90" />
                      <FolderKanban className="size-4 shrink-0 text-muted-foreground" />
                      <span className="min-w-0 flex-1 truncate">
                        {position.title}
                      </span>
                      <span className="shrink-0 text-[10px] text-muted-foreground">
                        {position.position_id}
                      </span>
                    </CollapsibleTrigger>
                    <CollapsibleContent>
                      {position.appointments.length === 0 ? (
                        <p className="px-8 py-1 text-xs text-muted-foreground">
                          {t(($) => $.tree.no_appointments)}
                        </p>
                      ) : (
                        <ul className="space-y-0.5 px-4">
                          {position.appointments.map((appointment) => (
                            <li key={appointment.appointment_id}>
                              <button
                                type="button"
                                onClick={() =>
                                  onSelectEmployee(appointment.employee_id)
                                }
                                className={cn(
                                  "flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm transition-colors hover:bg-accent/50",
                                )}
                              >
                                <UserRound className="size-4 shrink-0 text-muted-foreground" />
                                <span className="min-w-0 flex-1 truncate">
                                  {appointment.employee_id}
                                </span>
                              </button>
                            </li>
                          ))}
                        </ul>
                      )}
                    </CollapsibleContent>
                  </Collapsible>
                ))
              )}
            </CollapsibleContent>
          </Collapsible>
        ))}
        {observedAt && (
          <p className="px-2 pt-1 text-[10px] text-muted-foreground">
            {t(($) => $.tree.observed_at, { at: formatAuthorityTime(observedAt) })}
          </p>
        )}
      </div>
    </div>
  );
}
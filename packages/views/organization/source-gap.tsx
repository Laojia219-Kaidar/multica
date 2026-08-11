"use client";

import { AlertTriangle } from "lucide-react";
import type { CompanyOpsBindingState } from "@multica/core/types";
import { useT } from "../i18n";

/**
 * Fail-closed surface for non-operable binding states. Explains *why* the
 * employee cannot be assigned (or why the projection is unavailable) without
 * ever enabling a link. Used both in the roster rows and the dossier.
 */
export function BindingStateExplanation({
  state,
}: {
  state: CompanyOpsBindingState;
}) {
  const { t } = useT("organization");
  if (state === "available") return null;
  return (
    <div className="rounded-md border border-muted bg-muted/30 px-3 py-2 text-xs text-muted-foreground">
      <div className="flex items-start gap-2">
        <AlertTriangle className="mt-0.5 size-3.5 shrink-0 text-muted-foreground" />
        <p>{t(($) => $.state_reasons[state])}</p>
      </div>
    </div>
  );
}

export function SourceGapBanner() {
  const { t } = useT("organization");
  return (
    <div className="rounded-md border border-destructive/40 bg-destructive/10 px-4 py-3 text-sm">
      <div className="flex items-start gap-2.5">
        <AlertTriangle className="mt-0.5 size-4 shrink-0 text-destructive" />
        <div>
          <p className="font-medium text-destructive">
            {t(($) => $.page.source_gap_title)}
          </p>
          <p className="mt-1 text-xs text-muted-foreground">
            {t(($) => $.page.source_gap_description)}
          </p>
        </div>
      </div>
    </div>
  );
}
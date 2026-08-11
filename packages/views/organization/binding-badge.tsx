"use client";

import { Badge } from "@multica/ui/components/ui/badge";
import type { CompanyOpsBindingState } from "@multica/core/types";
import { useT } from "../i18n";

const BADGE_VARIANT: Record<CompanyOpsBindingState, "default" | "secondary" | "outline" | "destructive"> = {
  available: "default",
  none: "secondary",
  inactive_only: "outline",
  multiple_active_conflict: "destructive",
  local_agent_missing_or_invalid: "destructive",
  source_gap: "outline",
};

export function BindingBadge({ state }: { state: CompanyOpsBindingState }) {
  const { t } = useT("organization");
  return <Badge variant={BADGE_VARIANT[state]}>{t(($) => $.states[state])}</Badge>;
}
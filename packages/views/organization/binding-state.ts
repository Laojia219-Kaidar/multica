import type { CompanyOpsBindingState } from "@multica/core/types";

/**
 * The six binding-visible states a roster row / employee dossier can carry,
 * mirroring the frozen P4 contract. Only `available` may enable Agent
 * settings / Chat / assignment links; every other state is fail-closed.
 */
export const ORGANIZATION_BINDING_STATES: readonly CompanyOpsBindingState[] = [
  "available",
  "none",
  "inactive_only",
  "multiple_active_conflict",
  "local_agent_missing_or_invalid",
  "source_gap",
] as const;

/** True only when the exact execution binding resolves and is operable. */
export function isBindingOperable(
  state: CompanyOpsBindingState | undefined,
): boolean {
  return state === "available";
}

/** Stable ordering for the roster binding-state filter (empty = all). */
export function organizationBindingFilterOptions(): ReadonlyArray<
  CompanyOpsBindingState | ""
> {
  return ["", ...ORGANIZATION_BINDING_STATES];
}
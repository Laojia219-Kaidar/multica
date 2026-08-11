import { describe, expect, it } from "vitest";
import {
  ORGANIZATION_BINDING_STATES,
  isBindingOperable,
  organizationBindingFilterOptions,
} from "./binding-state";

describe("binding-state contract", () => {
  it("freezes exactly the six visible binding states", () => {
    expect(ORGANIZATION_BINDING_STATES).toEqual([
      "available",
      "none",
      "inactive_only",
      "multiple_active_conflict",
      "local_agent_missing_or_invalid",
      "source_gap",
    ]);
  });

  it("only available is operable (links may be constructed)", () => {
    expect(isBindingOperable("available")).toBe(true);
    for (const state of ORGANIZATION_BINDING_STATES) {
      if (state === "available") continue;
      expect(isBindingOperable(state), state).toBe(false);
    }
  });

  it("undefined is never operable", () => {
    expect(isBindingOperable(undefined)).toBe(false);
  });

  it("filter options are all-states plus the empty (all) option first", () => {
    expect(organizationBindingFilterOptions()).toEqual([
      "",
      ...ORGANIZATION_BINDING_STATES,
    ]);
  });
});
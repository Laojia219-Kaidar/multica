import { describe, expect, it } from "vitest";
import type { ProjectLifecycleSnapshot } from "@multica/core/types";
import { healthBucketOf } from "./project-health";

function snap(over: Partial<ProjectLifecycleSnapshot>): ProjectLifecycleSnapshot {
  return {
    project_id: "p1",
    status: "in_progress",
    health: "active_with_frontier",
    owner_decision_required: false,
    flags: [],
    lead_type: null,
    lead_id: null,
    frontier_issue_ids: [],
    frontier_tasks: [],
    active_task_count: 0,
    nonterminal_issue_count: 0,
    blocked_issue_count: 0,
    review_issue_count: 0,
    terminal_issue_count: 0,
    last_progress_at: null,
    next_action: "",
    outcome_confirmed: 0,
    outcome_total: 0,
    closure_ready: false,
    closure_blockers: [],
    duplicate_of_project_id: null,
    ...over,
  };
}

describe("healthBucketOf", () => {
  it("maps active_with_frontier to the active bucket", () => {
    expect(healthBucketOf(snap({ health: "active_with_frontier" }))).toBe("active");
  });

  it("maps in_progress with no live task but review backlog to review", () => {
    expect(
      healthBucketOf(snap({ health: "review_or_repair_blocked", review_issue_count: 17 })),
    ).toBe("review");
  });

  it("maps blocked issues to the blocked bucket", () => {
    expect(
      healthBucketOf(snap({ health: "review_or_repair_blocked", blocked_issue_count: 5 })),
    ).toBe("blocked");
  });

  it("maps duplicate_or_superseded to blocked (owner decision required)", () => {
    expect(healthBucketOf(snap({ health: "duplicate_or_superseded" }))).toBe("blocked");
  });

  it("maps stalled_no_open_task to stalled", () => {
    expect(healthBucketOf(snap({ health: "stalled_no_open_task" }))).toBe("stalled");
  });

  it("maps source_gap to blocked (closure evidence missing blocks closure)", () => {
    expect(healthBucketOf(snap({ health: "source_gap" }))).toBe("blocked");
  });

  it("maps ready_for_closure to ready", () => {
    expect(healthBucketOf(snap({ health: "ready_for_closure" }))).toBe("ready");
  });
});

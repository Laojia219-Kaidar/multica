import { describe, expect, it } from "vitest";
import { buildCommandMetrics } from "./command-page";

describe("buildCommandMetrics", () => {
  it("derives the owner command counts from live-domain states", () => {
    expect(
      buildCommandMetrics(
        [{ status: "todo" }, { status: "in_review" }, { status: "done" }, { status: "cancelled" }],
        [{ status: "in_progress" }, { status: "paused" }],
        [{ status: "working" }, { status: "idle" }],
        [{ status: "online" }, { status: "offline" }],
      ),
    ).toEqual({
      openWork: 2,
      inReview: 1,
      activeProjects: 1,
      onlineRuntimes: 1,
      workingAgents: 1,
    });
  });
});

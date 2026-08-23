import { describe, expect, it } from "vitest";
import { buildCommandMetrics } from "./command-page";

describe("buildCommandMetrics", () => {
  it("derives the owner command counts from live-domain states", () => {
    expect(
      buildCommandMetrics(
        { openWork: 83, inReview: 7 },
        [{ status: "in_progress" }, { status: "paused" }],
        [{ status: "working" }, { status: "idle" }],
        [{ status: "online" }, { status: "offline" }],
      ),
    ).toEqual({
      openWork: 83,
      inReview: 7,
      activeProjects: 1,
      onlineRuntimes: 1,
      workingAgents: 1,
    });
  });
});

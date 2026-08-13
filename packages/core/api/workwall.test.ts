import { describe, expect, it } from "vitest";
import {
  ActivityEventKindSchema,
  EmployeeLiveActivityV1Schema,
  parseWorkWallSnapshot,
} from "./workwall";

const valid = {
  schema_version: "hivecrew.employee-live-activity.v1",
  workspace_id: "ws-1",
  employee_id: "emp-1",
  agent_id: "agt-1",
  display_name: "Emory",
  avatar_url: "https://cdn/e.png",
  presence_state: "working",
  work_stage: "coding",
  recent_events: [
    {
      event_id: "ev-1",
      kind: "run.started",
      safe_summary: "run started",
      occurred_at: "2026-08-13T12:00:00Z",
    },
  ],
  runtime_id: "rt-1",
  runtime_provider: "prime",
  model_name: "deepseek-v4",
  token_usage: 1234,
  source_refs: ["agent://agt-1", "runtime://rt-1"],
  observed_at: "2026-08-13T12:00:00Z",
  freshness_state: "fresh",
};

describe("EmployeeLiveActivityV1Schema", () => {
  it("accepts a valid snapshot entry", () => {
    expect(EmployeeLiveActivityV1Schema.parse(valid).agent_id).toBe("agt-1");
  });

  it("rejects unknown keys (strict wire)", () => {
    expect(() => EmployeeLiveActivityV1Schema.parse({ ...valid, secret_field: "x" })).toThrow();
  });

  it("rejects invalid presence_state", () => {
    expect(() => EmployeeLiveActivityV1Schema.parse({ ...valid, presence_state: "napping" })).toThrow();
  });

  it("rejects invalid work_stage", () => {
    expect(() => EmployeeLiveActivityV1Schema.parse({ ...valid, work_stage: "reviewing_hard" })).toThrow();
  });

  it("rejects non-array recent_events", () => {
    expect(() => EmployeeLiveActivityV1Schema.parse({ ...valid, recent_events: {} })).toThrow();
  });


  it("codifies the 19-kind event protocol", () => {
    const kinds = [
      "task.queued", "task.dispatched", "run.started", "run.heartbeat",
      "tool.started", "tool.completed", "command.started", "command.completed",
      "test.started", "test.result", "artifact.created", "review.requested",
      "review.verdict", "repair.requested", "run.waiting", "run.blocked",
      "run.completed", "run.failed", "runtime.offline",
    ];
    expect(kinds).toHaveLength(19);
    for (const k of kinds) {
      expect(ActivityEventKindSchema.parse(k)).toBe(k);
    }
    expect(() => ActivityEventKindSchema.parse("run.stopped")).toThrow();
  });
  it("parses a full snapshot list", () => {
    const parsed = parseWorkWallSnapshot([valid]);
    expect(parsed).toHaveLength(1);
    expect(parsed[0]?.presence_state).toBe("working");
  });
});

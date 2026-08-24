import { describe, expect, it } from "vitest";
import {
  ActivityEventKindSchema,
  EmployeeLiveActivityV1Schema,
  joinA2PanesByEmployee,
  parseA2WorkWallSnapshot,
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

describe("A2 work wall snapshot schema", () => {
  const validPane = {
    schema_version: "hivecrew.workwall.a2-pane.v1",
    pane_id: "pane-1",
    employee_id: "emp-1",
    session_id: "sess-1",
    kind: "terminal",
    display_name: "Pixel",
    presence_state: "working",
    work_stage: "coding",
    tail_text: "npm test\nPASS",
    observed_at: "2026-08-24T12:00:00Z",
    freshness_state: "fresh",
  };

  const validSnapshot = {
    schema_version: "hivecrew.workwall.a2-snapshot.v1",
    workspace_id: "ws-1",
    cursor: "cur-1",
    observed_at: "2026-08-24T12:00:00Z",
    event_limit: 100,
    panes: [validPane],
  };

  it("accepts a valid A2 snapshot envelope", () => {
    const snap = parseA2WorkWallSnapshot(validSnapshot);
    expect(snap.schema_version).toBe("hivecrew.workwall.a2-snapshot.v1");
    expect(snap.panes).toHaveLength(1);
    expect(snap.panes[0]?.kind).toBe("terminal");
  });

  it("rejects unknown keys on the envelope (strict wire)", () => {
    expect(() => parseA2WorkWallSnapshot({ ...validSnapshot, secret_field: "x" })).toThrow();
  });

  it("rejects unknown keys on a pane (strict wire)", () => {
    const badPane = { ...validPane, extra_field: "oops" };
    expect(() => parseA2WorkWallSnapshot({ ...validSnapshot, panes: [badPane] })).toThrow();
  });

  it("rejects invalid schema_version (wrong literal)", () => {
    expect(() => parseA2WorkWallSnapshot({ ...validSnapshot, schema_version: "hivecrew.workwall.a2.v2" })).toThrow();
  });

  it("rejects invalid pane kind", () => {
    const badPane = { ...validPane, kind: "browser" };
    expect(() => parseA2WorkWallSnapshot({ ...validSnapshot, panes: [badPane] })).toThrow();
  });

  it("rejects invalid presence_state", () => {
    const badPane = { ...validPane, presence_state: "napping" };
    expect(() => parseA2WorkWallSnapshot({ ...validSnapshot, panes: [badPane] })).toThrow();
  });

  it("rejects non-integer event_limit", () => {
    expect(() => parseA2WorkWallSnapshot({ ...validSnapshot, event_limit: 1.5 })).toThrow();
  });

  it("rejects negative event_limit", () => {
    expect(() => parseA2WorkWallSnapshot({ ...validSnapshot, event_limit: -1 })).toThrow();
  });

  it("accepts both terminal and event_console pane kinds", () => {
    const consolePane = { ...validPane, pane_id: "pane-2", kind: "event_console" };
    const snap = parseA2WorkWallSnapshot({ ...validSnapshot, panes: [validPane, consolePane] });
    expect(snap.panes).toHaveLength(2);
  });
});

describe("joinA2PanesByEmployee", () => {
  const employees = [
    { employee_id: "emp-1" },
    { employee_id: "emp-2" },
    { employee_id: "emp-3" },
  ];

  function pane(over: Partial<import("./workwall").A2Pane> = {}): import("./workwall").A2Pane {
    return {
      schema_version: "hivecrew.workwall.a2-pane.v1",
      pane_id: "p-1",
      employee_id: "emp-1",
      session_id: "s-1",
      kind: "terminal",
      display_name: "Pixel",
      presence_state: "working",
      work_stage: "coding",
      tail_text: "",
      observed_at: "2026-08-24T12:00:00Z",
      freshness_state: "fresh",
      ...over,
    };
  }

  it("matches panes to employees by employee_id", () => {
    const panes = [pane({ employee_id: "emp-1" }), pane({ pane_id: "p-2", employee_id: "emp-2" })];
    const result = joinA2PanesByEmployee(employees, panes);
    expect(result.matched.size).toBe(2);
    expect(result.matched.get("emp-1")?.pane_id).toBe("p-1");
    expect(result.matched.get("emp-2")?.pane_id).toBe("p-2");
    expect(result.unmatchedCount).toBe(0);
  });

  it("counts unmatched panes (not in the employee roster)", () => {
    const panes = [pane({ employee_id: "emp-orphan", pane_id: "orphan-1" })];
    const result = joinA2PanesByEmployee(employees, panes);
    expect(result.matched.size).toBe(0);
    expect(result.unmatchedCount).toBe(1);
  });

  it("terminal wins over event_console for the same employee", () => {
    const panes = [
      pane({ employee_id: "emp-1", kind: "event_console", pane_id: "ev-1", observed_at: "2026-08-24T12:00:00Z" }),
      pane({ employee_id: "emp-1", kind: "terminal", pane_id: "tm-1", observed_at: "2026-08-24T11:00:00Z" }),
    ];
    const result = joinA2PanesByEmployee(employees, panes);
    expect(result.matched.get("emp-1")?.kind).toBe("terminal");
    expect(result.matched.get("emp-1")?.pane_id).toBe("tm-1");
  });

  it("picks the newest pane within the same kind", () => {
    const panes = [
      pane({ employee_id: "emp-1", kind: "terminal", pane_id: "old", observed_at: "2026-08-24T10:00:00Z" }),
      pane({ employee_id: "emp-1", kind: "terminal", pane_id: "new", observed_at: "2026-08-24T14:00:00Z" }),
    ];
    const result = joinA2PanesByEmployee(employees, panes);
    expect(result.matched.get("emp-1")?.pane_id).toBe("new");
  });

  it("mutually exclusive: never creates a fake employee or fake terminal", () => {
    const result = joinA2PanesByEmployee(employees, []);
    expect(result.matched.size).toBe(0);
    // Every employee in the roster is present in the result as a key only if they have a pane.
    for (const emp of employees) {
      const has = result.matched.has(emp.employee_id);
      // No fake panes: only those with real matching panes are in the map.
      expect(has).toBe(false);
    }
  });
});

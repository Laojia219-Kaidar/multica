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

describe("A2 work wall snapshot schema (A2PaneV1 exact)", () => {
  const validPane = {
    schema_version: "hivecrew.workwall.a2-pane.v1",
    workspace_id: "ws-1",
    work_ref: "work_ref_1",
    source_event_id: "evt-1",
    session_id: "pixel:0.1",
    run_id: "run-1",
    employee_id: "emp-1",
    employee_name: "Pixel",
    dispatch_command_id: "cmd-1",
    project_id: "prj-1",
    issue_id: "iss-1",
    issue_state: "in_progress",
    task_id: "task-1",
    execution_state: "active",
    working: true,
    surface_kind: "terminal",
    freshness_state: "fresh",
    last_heartbeat_at: "2026-08-24T12:00:00Z",
    last_event_at: "2026-08-24T11:59:55Z",
    observed_at: "2026-08-24T12:00:00Z",
    activity_kind: "workevent.progress",
    activity_summary: "执行中",
    source_refs: ["work_event://evt-1"],
  };

  const validSnapshot = {
    schema_version: "hivecrew.workwall.a2-snapshot.v1",
    workspace_id: "ws-1",
    cursor: "a".repeat(64),
    observed_at: "2026-08-24T12:00:00Z",
    event_limit: 100,
    panes: [validPane],
  };

  it("accepts a valid A2 snapshot envelope", () => {
    const snap = parseA2WorkWallSnapshot(validSnapshot);
    expect(snap.schema_version).toBe("hivecrew.workwall.a2-snapshot.v1");
    expect(snap.panes).toHaveLength(1);
    expect(snap.panes[0]?.surface_kind).toBe("terminal");
    expect(snap.panes[0]?.execution_state).toBe("active");
    expect(snap.panes[0]?.working).toBe(true);
  });

  it("accepts a minimal pane (optional fields omitted)", () => {
    const minimalPane = {
      schema_version: "hivecrew.workwall.a2-pane.v1",
      workspace_id: "ws-1",
      work_ref: "work_ref_min",
      source_event_id: "evt-min",
      execution_state: "completed",
      working: false,
      surface_kind: "event_console",
      freshness_state: "fresh",
      observed_at: "2026-08-24T12:00:00Z",
      source_refs: ["work_event://evt-min"],
    };
    const snap = parseA2WorkWallSnapshot({
      ...validSnapshot,
      cursor: "b".repeat(64),
      panes: [minimalPane],
    });
    expect(snap.panes[0]?.surface_kind).toBe("event_console");
    expect(snap.panes[0]?.session_id).toBeUndefined();
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

  it("rejects invalid surface_kind", () => {
    const badPane = { ...validPane, surface_kind: "browser" };
    expect(() => parseA2WorkWallSnapshot({ ...validSnapshot, panes: [badPane] })).toThrow();
  });

  it("rejects invalid execution_state", () => {
    const badPane = { ...validPane, execution_state: "napping" };
    expect(() => parseA2WorkWallSnapshot({ ...validSnapshot, panes: [badPane] })).toThrow();
  });

  it("rejects invalid freshness_state", () => {
    const badPane = { ...validPane, freshness_state: "sparkling" };
    expect(() => parseA2WorkWallSnapshot({ ...validSnapshot, panes: [badPane] })).toThrow();
  });

  it("rejects non-integer event_limit", () => {
    expect(() => parseA2WorkWallSnapshot({ ...validSnapshot, event_limit: 1.5 })).toThrow();
  });

  it("rejects zero event_limit (must be 1..1000)", () => {
    expect(() => parseA2WorkWallSnapshot({ ...validSnapshot, event_limit: 0 })).toThrow();
  });

  it("rejects negative event_limit", () => {
    expect(() => parseA2WorkWallSnapshot({ ...validSnapshot, event_limit: -1 })).toThrow();
  });

  it("rejects event_limit above 1000", () => {
    expect(() => parseA2WorkWallSnapshot({ ...validSnapshot, event_limit: 1001 })).toThrow();
  });

  it("accepts event_limit = 1 (lower bound)", () => {
    const snap = parseA2WorkWallSnapshot({ ...validSnapshot, event_limit: 1 });
    expect(snap.event_limit).toBe(1);
  });

  it("accepts event_limit = 1000 (upper bound)", () => {
    const snap = parseA2WorkWallSnapshot({ ...validSnapshot, event_limit: 1000 });
    expect(snap.event_limit).toBe(1000);
  });

  it("rejects uppercase cursor hex (must be lowercase)", () => {
    expect(() => parseA2WorkWallSnapshot({
      ...validSnapshot,
      cursor: "A".repeat(64),
    })).toThrow();
  });

  it("rejects cursor with mixed-case hex", () => {
    expect(() => parseA2WorkWallSnapshot({
      ...validSnapshot,
      cursor: "Ab".repeat(32),
    })).toThrow();
  });

  it("rejects cursor with wrong length", () => {
    expect(() => parseA2WorkWallSnapshot({
      ...validSnapshot,
      cursor: "a".repeat(60),
    })).toThrow();
  });

  it("rejects cursor with sha256: prefix (raw hex only)", () => {
    expect(() => parseA2WorkWallSnapshot({
      ...validSnapshot,
      cursor: "sha256:" + "a".repeat(64),
    })).toThrow();
  });

  it("rejects invented A2 fields: pane_id", () => {
    const badPane = { ...validPane, pane_id: "pane-1" };
    expect(() => parseA2WorkWallSnapshot({ ...validSnapshot, panes: [badPane] })).toThrow();
  });

  it("rejects invented A2 fields: kind", () => {
    const badPane = { ...validPane, kind: "terminal" };
    expect(() => parseA2WorkWallSnapshot({ ...validSnapshot, panes: [badPane] })).toThrow();
  });

  it("rejects invented A2 fields: display_name", () => {
    const badPane = { ...validPane, display_name: "Pixel" };
    expect(() => parseA2WorkWallSnapshot({ ...validSnapshot, panes: [badPane] })).toThrow();
  });

  it("rejects invented A2 fields: position_name", () => {
    const badPane = { ...validPane, position_name: "Engineer" };
    expect(() => parseA2WorkWallSnapshot({ ...validSnapshot, panes: [badPane] })).toThrow();
  });

  it("rejects invented A2 fields: department_name", () => {
    const badPane = { ...validPane, department_name: "Eng" };
    expect(() => parseA2WorkWallSnapshot({ ...validSnapshot, panes: [badPane] })).toThrow();
  });

  it("rejects invented A2 fields: avatar_url", () => {
    const badPane = { ...validPane, avatar_url: "https://x" };
    expect(() => parseA2WorkWallSnapshot({ ...validSnapshot, panes: [badPane] })).toThrow();
  });

  it("rejects invented A2 fields: presence_state", () => {
    const badPane = { ...validPane, presence_state: "working" };
    expect(() => parseA2WorkWallSnapshot({ ...validSnapshot, panes: [badPane] })).toThrow();
  });

  it("rejects invented A2 fields: work_stage", () => {
    const badPane = { ...validPane, work_stage: "coding" };
    expect(() => parseA2WorkWallSnapshot({ ...validSnapshot, panes: [badPane] })).toThrow();
  });

  it("rejects invented A2 fields: issue_identifier", () => {
    const badPane = { ...validPane, issue_identifier: "HIV-1" };
    expect(() => parseA2WorkWallSnapshot({ ...validSnapshot, panes: [badPane] })).toThrow();
  });

  it("rejects invented A2 fields: issue_title", () => {
    const badPane = { ...validPane, issue_title: "fix bug" };
    expect(() => parseA2WorkWallSnapshot({ ...validSnapshot, panes: [badPane] })).toThrow();
  });

  it("rejects invented A2 fields: model_name", () => {
    const badPane = { ...validPane, model_name: "gpt-5" };
    expect(() => parseA2WorkWallSnapshot({ ...validSnapshot, panes: [badPane] })).toThrow();
  });

  it("rejects invented A2 fields: runtime_provider", () => {
    const badPane = { ...validPane, runtime_provider: "prime" };
    expect(() => parseA2WorkWallSnapshot({ ...validSnapshot, panes: [badPane] })).toThrow();
  });

  it("rejects invented A2 fields: tail_text", () => {
    const badPane = { ...validPane, tail_text: "output" };
    expect(() => parseA2WorkWallSnapshot({ ...validSnapshot, panes: [badPane] })).toThrow();
  });

  it("accepts both terminal and event_console surface kinds", () => {
    const consolePane = { ...validPane, work_ref: "wr-2", source_event_id: "evt-2", surface_kind: "event_console", session_id: undefined };
    const snap = parseA2WorkWallSnapshot({
      ...validSnapshot,
      cursor: "c".repeat(64),
      panes: [validPane, consolePane],
    });
    expect(snap.panes).toHaveLength(2);
  });

  it("rejects panes without required work_ref", () => {
    const { work_ref: _, ...badPane } = validPane;
    expect(() => parseA2WorkWallSnapshot({ ...validSnapshot, panes: [badPane as typeof validPane] })).toThrow();
  });

  it("rejects panes without required source_event_id", () => {
    const { source_event_id: _, ...badPane } = validPane;
    expect(() => parseA2WorkWallSnapshot({ ...validSnapshot, panes: [badPane as typeof validPane] })).toThrow();
  });

  it("rejects panes without required working boolean", () => {
    const { working: _, ...badPane } = validPane;
    expect(() => parseA2WorkWallSnapshot({ ...validSnapshot, panes: [badPane as typeof validPane] })).toThrow();
  });
});

describe("joinA2PanesByEmployee (roster-primary, first server-ordered pane)", () => {
  const employees = [
    { employee_id: "emp-1" },
    { employee_id: "emp-2" },
    { employee_id: "emp-3" },
  ];

  function pane(over: Partial<import("./workwall").A2Pane> = {}): import("./workwall").A2Pane {
    return {
      schema_version: "hivecrew.workwall.a2-pane.v1",
      workspace_id: "ws-1",
      work_ref: "wr-1",
      source_event_id: "evt-1",
      employee_id: "emp-1",
      execution_state: "active",
      working: true,
      surface_kind: "terminal",
      freshness_state: "fresh",
      observed_at: "2026-08-24T12:00:00Z",
      source_refs: ["work_event://evt-1"],
      ...over,
    };
  }

  it("matches panes to employees by employee_id", () => {
    const panes = [pane({ employee_id: "emp-1" }), pane({ work_ref: "wr-2", source_event_id: "evt-2", employee_id: "emp-2" })];
    const result = joinA2PanesByEmployee(employees, panes);
    expect(result.matched.size).toBe(2);
    expect(result.matched.get("emp-1")?.employee_id).toBe("emp-1");
    expect(result.matched.get("emp-2")?.employee_id).toBe("emp-2");
    expect(result.unmatchedCount).toBe(0);
  });

  it("counts unmatched panes (not in the employee roster)", () => {
    const panes = [pane({ employee_id: "emp-orphan" })];
    const result = joinA2PanesByEmployee(employees, panes);
    expect(result.matched.size).toBe(0);
    expect(result.unmatchedCount).toBe(1);
  });

  it("panes without employee_id count as unmatched (never invent an employee)", () => {
    const panes = [pane({ employee_id: undefined })];
    const result = joinA2PanesByEmployee(employees, panes);
    expect(result.matched.size).toBe(0);
    expect(result.unmatchedCount).toBe(1);
  });

  it("first server-ordered pane per employee wins (terminal not special)", () => {
    const panes = [
      pane({ employee_id: "emp-1", surface_kind: "event_console", work_ref: "wr-first", source_event_id: "evt-first" }),
      pane({ employee_id: "emp-1", surface_kind: "terminal", work_ref: "wr-second", source_event_id: "evt-second" }),
    ];
    const result = joinA2PanesByEmployee(employees, panes);
    // First (event_console) wins — no terminal preference in join itself.
    expect(result.matched.get("emp-1")?.surface_kind).toBe("event_console");
    expect(result.matched.get("emp-1")?.work_ref).toBe("wr-first");
  });

  it("never creates a fake employee or fake terminal", () => {
    const result = joinA2PanesByEmployee(employees, []);
    expect(result.matched.size).toBe(0);
    for (const emp of employees) {
      expect(result.matched.has(emp.employee_id)).toBe(false);
    }
  });
});

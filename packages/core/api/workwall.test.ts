import { describe, expect, it } from "vitest";
import {
  ActivityEventKindSchema,
  EmployeeLiveActivityV1Schema,
  parseWorkWallSnapshot,
  subscribeWorkWallStream,
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


  it("parses the execution-chain projection fields (HIV-797)", () => {
    const parsed = EmployeeLiveActivityV1Schema.parse({
      ...valid,
      issue_id: "issue-797",
      issue_identifier: "HIV-797",
      issue_title: "[DEV] Work Wall complete execution-chain projection",
      project_id: "proj-1",
      project_title: "HIVECREW 自我开发项目",
      task_id: "task-1",
      runtime_profile_id: "profile-1",
      runtime_profile_name: "glm-5.3 运行档案",
      execution_receipt_ref: "receipt://task-1",
      execution_receipt_status: "completed",
    });
    expect(parsed.issue_identifier).toBe("HIV-797");
    expect(parsed.runtime_profile_name).toBe("glm-5.3 运行档案");
    expect(parsed.execution_receipt_status).toBe("completed");
  });

  it("keeps chain fields optional so missing evidence parses as absent", () => {
    const parsed = EmployeeLiveActivityV1Schema.parse(valid);
    expect(parsed.issue_identifier).toBeUndefined();
    expect(parsed.runtime_profile_id).toBeUndefined();
    expect(parsed.execution_receipt_ref).toBeUndefined();
  });

  it("rejects non-string chain fields", () => {
    expect(() =>
      EmployeeLiveActivityV1Schema.parse({ ...valid, execution_receipt_status: 42 }),
    ).toThrow();
    expect(() =>
      EmployeeLiveActivityV1Schema.parse({ ...valid, runtime_profile_id: {} }),
    ).toThrow();
  });


  it("treats the compatibility runtime_provider key as the runtime carrier", () => {
    const parsed = EmployeeLiveActivityV1Schema.parse({
      ...valid,
      runtime_provider: "volcengine",
    });
    expect(parsed.runtime_provider).toBe("volcengine");
  });

  it("accepts llm_provider as an optional independent field (HIV-911)", () => {
    const withoutLLM = EmployeeLiveActivityV1Schema.parse(valid);
    expect(withoutLLM.llm_provider).toBeUndefined();

    const withLLM = EmployeeLiveActivityV1Schema.parse({
      ...valid,
      llm_provider: "volcengine-ark",
    });
    expect(withLLM.llm_provider).toBe("volcengine-ark");
    expect(withLLM.runtime_provider).not.toBe(withLLM.llm_provider);
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

class FakeEventSource {
  readonly listeners = new Map<string, EventListener[]>();
  closed = false;

  addEventListener(type: string, listener: EventListenerOrEventListenerObject) {
    const fn = typeof listener === "function" ? listener : listener.handleEvent.bind(listener);
    this.listeners.set(type, [...(this.listeners.get(type) ?? []), fn]);
  }

  emit(type: string, event: Event) {
    for (const listener of this.listeners.get(type) ?? []) listener(event);
  }

  close() {
    this.closed = true;
  }
}

describe("subscribeWorkWallStream", () => {
  it("uses the governed same-origin endpoint and accepts snapshot events", () => {
    const source = new FakeEventSource();
    const states: string[] = [];
    const snapshots: unknown[] = [];
    let requestedURL = "";

    const unsubscribe = subscribeWorkWallStream(
      "team/space",
      {
        onSnapshot: (snapshot) => snapshots.push(snapshot),
        onStateChange: (state) => states.push(state),
      },
      (url) => {
        requestedURL = url;
        return source as unknown as EventSource;
      },
    );

    source.emit("open", new Event("open"));
    source.emit(
      "snapshot",
      new MessageEvent("snapshot", { data: JSON.stringify([valid]) }),
    );

    expect(requestedURL).toBe("/api/work-wall/stream?workspace_slug=team%2Fspace");
    expect(requestedURL).not.toContain("token");
    expect(states).toEqual(["connecting", "open", "open"]);
    expect(snapshots).toEqual([[valid]]);
    unsubscribe();
    expect(source.closed).toBe(true);
  });

  it("rejects malformed or non-contract frames and leaves fallback enabled", () => {
    const source = new FakeEventSource();
    const states: string[] = [];
    const errors: unknown[] = [];
    const snapshots: unknown[] = [];

    subscribeWorkWallStream(
      "acme",
      {
        onSnapshot: (snapshot) => snapshots.push(snapshot),
        onStateChange: (state) => states.push(state),
        onError: (error) => errors.push(error),
      },
      () => source as unknown as EventSource,
    );

    source.emit("snapshot", new MessageEvent("snapshot", { data: "not-json" }));
    source.emit(
      "snapshot",
      new MessageEvent("snapshot", {
        data: JSON.stringify([{ ...valid, secret_field: "must-not-pass" }]),
      }),
    );

    expect(snapshots).toEqual([]);
    expect(errors).toHaveLength(2);
    expect(states).toEqual(["connecting", "error", "error"]);
  });

  it("forwards the SSE event id via onEventID when present", () => {
    const source = new FakeEventSource();
    const eventIDs: string[] = [];
    const snapshots: unknown[] = [];

    subscribeWorkWallStream(
      "ws",
      {
        onSnapshot: (s) => snapshots.push(s),
        onEventID: (id) => eventIDs.push(id),
      },
      () => source as unknown as EventSource,
    );

    source.emit(
      "snapshot",
      new MessageEvent("snapshot", {
        data: JSON.stringify([valid]),
        lastEventId: "1724300000000000000",
      }),
    );
    source.emit(
      "snapshot",
      new MessageEvent("snapshot", {
        data: JSON.stringify([valid]),
        lastEventId: "1724300005000000000",
      }),
    );

    expect(snapshots).toHaveLength(2);
    expect(eventIDs).toEqual([
      "1724300000000000000",
      "1724300005000000000",
    ]);
  });

  it("does not call onEventID when lastEventId is empty", () => {
    const source = new FakeEventSource();
    const eventIDs: string[] = [];

    subscribeWorkWallStream(
      "ws",
      {
        onSnapshot: () => {},
        onEventID: (id) => eventIDs.push(id),
      },
      () => source as unknown as EventSource,
    );

    source.emit(
      "snapshot",
      new MessageEvent("snapshot", { data: JSON.stringify([valid]) }),
    );

    expect(eventIDs).toEqual([]);
  });
});

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
});

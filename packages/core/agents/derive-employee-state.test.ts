import { describe, expect, it } from "vitest";
import type { Agent, AgentRuntime, AgentTask } from "../types";
import {
  buildEmployeeStateMap,
  deriveEmployeeStateExplanation,
} from "./derive-employee-state";

function makeAgent(overrides: Partial<Agent> = {}): Agent {
  return {
    id: "agent-1",
    workspace_id: "ws-1",
    runtime_id: "rt-1",
    name: "Test Agent",
    description: "",
    instructions: "",
    avatar_url: null,
    runtime_mode: "local",
    runtime_config: {},
    custom_args: [],
    visibility: "workspace",
    permission_mode: "public_to",
    invocation_targets: [{ target_type: "workspace", target_id: null }],
    status: "idle",
    max_concurrent_tasks: 4,
    model: "",
    owner_id: null,
    skills: [],
    created_at: "2026-04-01T00:00:00Z",
    updated_at: "2026-04-01T00:00:00Z",
    archived_at: null,
    archived_by: null,
    ...overrides,
  };
}

function makeRuntime(overrides: Partial<AgentRuntime> = {}): AgentRuntime {
  return {
    id: "rt-1",
    workspace_id: "ws-1",
    daemon_id: "daemon-1",
    name: "Test Runtime",
    runtime_mode: "local",
    provider: "claude",
    launch_header: "",
    status: "online",
    device_info: "",
    metadata: {},
    owner_id: null,
    visibility: "private",
    last_seen_at: "2026-04-27T11:59:50Z",
    created_at: "2026-04-01T00:00:00Z",
    updated_at: "2026-04-01T00:00:00Z",
    ...overrides,
  };
}

const NOW = new Date("2026-04-27T12:00:00Z").getTime();

function makeTask(overrides: Partial<AgentTask> = {}): AgentTask {
  return {
    id: "task-1",
    agent_id: "agent-1",
    runtime_id: "rt-1",
    issue_id: "issue-1",
    status: "queued",
    priority: 0,
    dispatched_at: null,
    started_at: null,
    completed_at: null,
    result: null,
    error: null,
    created_at: "2026-04-27T11:00:00Z",
    ...overrides,
  };
}

describe("deriveEmployeeStateExplanation", () => {
  it("working: running task wins, carries the earliest-started run", () => {
    const explanation = deriveEmployeeStateExplanation({
      agent: makeAgent(),
      runtime: makeRuntime(),
      tasks: [
        makeTask({
          id: "task-later",
          status: "running",
          started_at: "2026-04-27T11:30:00Z",
        }),
        makeTask({
          id: "task-earlier",
          status: "running",
          started_at: "2026-04-27T11:10:00Z",
        }),
      ],
      now: NOW,
    });
    expect(explanation.status).toBe("working");
    expect(explanation.reason).toBe("running_tasks");
    expect(explanation.nextAction).toBe("none");
    expect(explanation.runningCount).toBe(2);
    expect(explanation.currentTask?.id).toBe("task-earlier");
    expect(explanation.capacity).toBe(4);
  });

  it("waiting: queued task on a healthy runtime awaits dispatch", () => {
    const explanation = deriveEmployeeStateExplanation({
      agent: makeAgent(),
      runtime: makeRuntime(),
      tasks: [makeTask({ status: "queued" })],
      now: NOW,
    });
    expect(explanation.status).toBe("waiting");
    expect(explanation.reason).toBe("awaiting_dispatch");
    expect(explanation.nextAction).toBe("await_dispatch");
    expect(explanation.currentTask?.id).toBe("task-1");
  });

  it("waiting: dispatched task awaits the daemon start", () => {
    const explanation = deriveEmployeeStateExplanation({
      agent: makeAgent(),
      runtime: makeRuntime(),
      tasks: [
        makeTask({
          status: "dispatched",
          dispatched_at: "2026-04-27T11:59:00Z",
        }),
      ],
      now: NOW,
    });
    expect(explanation.status).toBe("waiting");
    expect(explanation.reason).toBe("dispatched_awaiting_start");
    expect(explanation.nextAction).toBe("await_daemon_start");
  });

  it("waiting: waiting_local_directory explains the path lock", () => {
    const explanation = deriveEmployeeStateExplanation({
      agent: makeAgent(),
      runtime: makeRuntime(),
      tasks: [
        makeTask({ id: "task-queued", status: "queued" }),
        makeTask({ id: "task-parked", status: "waiting_local_directory" }),
      ],
      now: NOW,
    });
    expect(explanation.status).toBe("waiting");
    expect(explanation.reason).toBe("waiting_local_directory");
    expect(explanation.nextAction).toBe("await_path_lock");
  });

  it("idle: healthy with an empty plate and no backlog", () => {
    const explanation = deriveEmployeeStateExplanation({
      agent: makeAgent(),
      runtime: makeRuntime(),
      tasks: [],
      now: NOW,
    });
    expect(explanation.status).toBe("idle");
    expect(explanation.reason).toBe("idle_ready");
    expect(explanation.nextAction).toBe("none");
    expect(explanation.currentTask).toBeNull();
  });

  it("idle with backlog: free capacity surfaces assign_work", () => {
    const explanation = deriveEmployeeStateExplanation({
      agent: makeAgent(),
      runtime: makeRuntime(),
      tasks: [],
      now: NOW,
      workspaceBacklogCount: 3,
    });
    expect(explanation.status).toBe("idle");
    expect(explanation.reason).toBe("idle_backlog_waiting");
    expect(explanation.nextAction).toBe("assign_work");
    expect(explanation.workspaceBacklogCount).toBe(3);
  });

  it("unavailable: long-offline runtime, with staleness and restore action", () => {
    const explanation = deriveEmployeeStateExplanation({
      agent: makeAgent(),
      runtime: makeRuntime({
        status: "offline",
        last_seen_at: "2026-04-27T10:00:00Z",
      }),
      // A stale "running" row must not read as current work on a dead
      // runtime — availability precedes workload.
      tasks: [makeTask({ status: "running", started_at: "2026-04-27T09:00:00Z" })],
      now: NOW,
    });
    expect(explanation.status).toBe("unavailable");
    expect(explanation.reason).toBe("runtime_offline");
    expect(explanation.nextAction).toBe("restore_runtime");
    expect(explanation.runtimeHealth).toBe("offline");
    expect(explanation.runtimeStalenessMs).toBe(2 * 3600 * 1000);
    expect(explanation.currentTask).toBeNull();
  });

  it("unavailable: recently-lost runtime asks for monitoring, not restart", () => {
    const explanation = deriveEmployeeStateExplanation({
      agent: makeAgent(),
      runtime: makeRuntime({
        status: "offline",
        last_seen_at: "2026-04-27T11:58:00Z",
      }),
      tasks: [],
      now: NOW,
    });
    expect(explanation.status).toBe("unavailable");
    expect(explanation.reason).toBe("runtime_recently_lost");
    expect(explanation.nextAction).toBe("monitor_runtime");
    expect(explanation.runtimeHealth).toBe("recently_lost");
  });

  it("unavailable: about-to-GC runtime is called out distinctly", () => {
    const explanation = deriveEmployeeStateExplanation({
      agent: makeAgent(),
      runtime: makeRuntime({
        status: "offline",
        last_seen_at: "2026-04-21T00:00:00Z",
      }),
      tasks: [],
      now: NOW,
    });
    expect(explanation.status).toBe("unavailable");
    expect(explanation.reason).toBe("runtime_about_to_gc");
    expect(explanation.nextAction).toBe("restore_runtime");
  });

  it("unavailable: missing runtime row", () => {
    const explanation = deriveEmployeeStateExplanation({
      agent: makeAgent({ runtime_id: "" }),
      runtime: null,
      tasks: [],
      now: NOW,
    });
    expect(explanation.status).toBe("unavailable");
    expect(explanation.reason).toBe("runtime_missing");
    expect(explanation.runtimeHealth).toBe("missing");
    expect(explanation.runtimeStalenessMs).toBeNull();
    expect(explanation.nextAction).toBe("restore_runtime");
  });

  it("archived wins over a healthy runtime and live tasks", () => {
    const explanation = deriveEmployeeStateExplanation({
      agent: makeAgent({ archived_at: "2026-04-20T00:00:00Z" }),
      runtime: makeRuntime(),
      tasks: [makeTask({ status: "running", started_at: "2026-04-27T11:00:00Z" })],
      now: NOW,
    });
    expect(explanation.status).toBe("unavailable");
    expect(explanation.reason).toBe("agent_archived");
    expect(explanation.nextAction).toBe("unarchive");
  });

  it("quota unknown: non-positive capacity stays null, never guessed", () => {
    for (const max_concurrent_tasks of [0, -1, Number.NaN]) {
      const explanation = deriveEmployeeStateExplanation({
        agent: makeAgent({ max_concurrent_tasks }),
        runtime: makeRuntime(),
        tasks: [makeTask({ status: "running", started_at: "2026-04-27T11:00:00Z" })],
        now: NOW,
      });
      expect(explanation.status).toBe("working");
      expect(explanation.capacity).toBeNull();
    }
  });
});

describe("buildEmployeeStateMap", () => {
  it("derives every agent and threads the workspace queued backlog", () => {
    const busy = makeAgent({ id: "agent-busy" });
    const free = makeAgent({ id: "agent-free" });
    const map = buildEmployeeStateMap({
      agents: [busy, free],
      runtimes: [makeRuntime()],
      snapshot: [
        makeTask({ id: "t1", agent_id: "agent-busy", status: "queued" }),
        makeTask({
          id: "t2",
          agent_id: "agent-busy",
          status: "waiting_local_directory",
        }),
        makeTask({
          id: "t3",
          agent_id: "agent-free",
          status: "completed",
          completed_at: "2026-04-27T11:30:00Z",
        }),
      ],
      now: NOW,
    });
    expect(map.size).toBe(2);
    const busyExplanation = map.get("agent-busy");
    const freeExplanation = map.get("agent-free");
    expect(busyExplanation?.status).toBe("waiting");
    expect(busyExplanation?.reason).toBe("waiting_local_directory");
    expect(busyExplanation?.workspaceBacklogCount).toBe(2);
    // Terminal rows never count as backlog or workload.
    expect(freeExplanation?.status).toBe("idle");
    expect(freeExplanation?.reason).toBe("idle_backlog_waiting");
    expect(freeExplanation?.nextAction).toBe("assign_work");
  });

  it("missing runtime rows resolve to unavailable, not a crash", () => {
    const map = buildEmployeeStateMap({
      agents: [makeAgent({ runtime_id: "rt-gone" })],
      runtimes: [],
      snapshot: [],
      now: NOW,
    });
    expect(map.get("agent-1")?.reason).toBe("runtime_missing");
  });
});

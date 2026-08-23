import { createElement as h } from "react";
import { describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import type { Agent, AgentRuntime, AgentTask } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { AgentStack, buildWorkloadIndex, HealthCell } from "./runtime-list";

// The agent stack under test only needs the avatar's presence; the hover
// card / profile surfaces belong to ActorAvatar's own tests.
vi.mock("../../common/actor-avatar", async () => {
  const { createElement: h } = await import("react");
  return {
    ActorAvatar: () => h("span", { "data-testid": "avatar" }),
  };
});

function makeAgent(overrides: Partial<Agent> = {}): Agent {
  return {
    id: "agent-1",
    workspace_id: "ws-1",
    runtime_id: "runtime-1",
    name: "Agent",
    description: "",
    instructions: "",
    avatar_url: null,
    runtime_mode: "local",
    runtime_config: {},
    custom_args: [],
    visibility: "private",
    permission_mode: "private",
    invocation_targets: [],
    status: "idle",
    max_concurrent_tasks: 1,
    model: "gpt-5.4",
    owner_id: "user-1",
    skills: [],
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    archived_at: null,
    archived_by: null,
    ...overrides,
  };
}

function makeTask(overrides: Partial<AgentTask> = {}): AgentTask {
  return {
    id: "task-1",
    agent_id: "agent-1",
    issue_id: "issue-1",
    status: "running",
    priority: 1,
    dispatched_at: null,
    started_at: null,
    completed_at: null,
    result: null,
    error: null,
    created_at: "2026-01-01T00:00:00Z",
    runtime_id: "runtime-1",
    attempt: 1,
    ...overrides,
  };
}

function makeRuntime(overrides: Partial<AgentRuntime> = {}): AgentRuntime {
  return {
    id: "runtime-1",
    workspace_id: "ws-1",
    daemon_id: null,
    name: "Runtime",
    runtime_mode: "local",
    provider: "claude",
    launch_header: "",
    status: "online",
    device_info: "",
    metadata: {},
    owner_id: "user-1",
    visibility: "private",
    profile_id: null,
    last_seen_at: null,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    ...overrides,
  };
}

describe("buildWorkloadIndex", () => {
  it("excludes archived agents from runtime agent counts and workload", () => {
    const activeAgent = makeAgent({ id: "active-agent" });
    const archivedAgent = makeAgent({
      id: "archived-agent",
      archived_at: "2026-01-02T00:00:00Z",
    });

    const tasks = [
      makeTask({ id: "active-task", agent_id: activeAgent.id, status: "running" }),
      makeTask({ id: "archived-task", agent_id: archivedAgent.id, status: "queued" }),
    ];

    const workload = buildWorkloadIndex([activeAgent, archivedAgent], tasks).get("runtime-1");

    expect(workload).toEqual({
      agentIds: [activeAgent.id],
      runningCount: 1,
      queuedCount: 0,
    });
  });

  it("attributes a running task to the task's own runtime_id, not the employee's current binding", () => {
    // The rebound-employee drift case: the task was dispatched to
    // runtime-1, but the employee now binds to runtime-2. The old runtime
    // keeps the workload it actually ran; the new one gets the employee
    // but none of the old runtime's traffic.
    const employee = makeAgent({
      id: "employee",
      runtime_id: "runtime-2",
    });
    const task = makeTask({
      id: "task-on-old-runtime",
      agent_id: employee.id,
      runtime_id: "runtime-1",
      status: "running",
    });

    const workload = buildWorkloadIndex([employee], [task]);

    expect(workload.get("runtime-2")).toEqual({
      agentIds: [employee.id],
      runningCount: 0,
      queuedCount: 0,
    });
    expect(workload.get("runtime-1")).toEqual({
      agentIds: [],
      runningCount: 1,
      queuedCount: 0,
    });
  });

  it("counts every active status on the task's own runtime_id and never counts terminal outcomes", () => {
    const employee = makeAgent({ id: "e1", runtime_id: "runtime-shared" });
    const tasks = [
      makeTask({
        id: "t-queued",
        agent_id: employee.id,
        status: "queued",
        runtime_id: "runtime-q",
      }),
      makeTask({
        id: "t-dispatched",
        agent_id: employee.id,
        status: "dispatched",
        runtime_id: "runtime-d",
      }),
      makeTask({
        id: "t-waiting",
        agent_id: employee.id,
        status: "waiting_local_directory",
        runtime_id: "runtime-w",
      }),
      makeTask({
        id: "t-running",
        agent_id: employee.id,
        status: "running",
        runtime_id: "runtime-r",
      }),
      makeTask({
        id: "t-completed",
        agent_id: employee.id,
        status: "completed",
        runtime_id: "runtime-shared",
      }),
      makeTask({
        id: "t-failed",
        agent_id: employee.id,
        status: "failed",
        runtime_id: "runtime-shared",
      }),
      makeTask({
        id: "t-cancelled",
        agent_id: employee.id,
        status: "cancelled",
        runtime_id: "runtime-shared",
      }),
      // A task whose owner is not in the agent list at all (unknown agent)
      // must not count either.
      makeTask({
        id: "t-unknown-owner",
        agent_id: "ghost",
        status: "running",
        runtime_id: "runtime-shared",
      }),
    ];

    const workload = buildWorkloadIndex([employee], tasks);

    expect(workload.get("runtime-q")).toEqual({
      agentIds: [],
      runningCount: 0,
      queuedCount: 1,
    });
    expect(workload.get("runtime-d")).toEqual({
      agentIds: [],
      runningCount: 0,
      queuedCount: 1,
    });
    expect(workload.get("runtime-w")).toEqual({
      agentIds: [],
      runningCount: 0,
      queuedCount: 1,
    });
    expect(workload.get("runtime-r")).toEqual({
      agentIds: [],
      runningCount: 1,
      queuedCount: 0,
    });
    // Terminal outcomes and unknown owners leave only the binding behind.
    expect(workload.get("runtime-shared")).toEqual({
      agentIds: [employee.id],
      runningCount: 0,
      queuedCount: 0,
    });
  });
});

describe("runtime agents column", () => {
  it("renders an explicit localized unbound label, not a bare dash, in every supported locale", () => {
    const cases = [
      ["en", "No agents bound"],
      ["zh-Hans", "未绑定智能体"],
      ["ja", "バインドされたエージェントなし"],
      ["ko", "연결된 에이전트 없음"],
    ] as const;

    for (const [locale, label] of cases) {
      const view = renderWithI18n(h(AgentStack, { agentIds: [] }), { locale });
      expect(screen.getByText(label)).toBeInTheDocument();
      expect(screen.queryByText("—")).not.toBeInTheDocument();
      expect(screen.queryAllByTestId("avatar")).toHaveLength(0);
      view.unmount();
    }
  });

  it("replaces the unbound label with avatars once employees are bound", () => {
    renderWithI18n(
      h(AgentStack, { agentIds: ["agent-1", "agent-2", "agent-3", "agent-4"] }),
    );

    expect(screen.queryByText("No agents bound")).not.toBeInTheDocument();
    // Capped stack of 3 avatars plus the "+1" pill.
    expect(screen.getAllByTestId("avatar")).toHaveLength(3);
    expect(screen.getByText("+1")).toBeInTheDocument();
  });
});

describe("runtime health column", () => {
  it("keeps an online zero-bound runtime pure health — no workload suffix, no assignment implication", () => {
    renderWithI18n(
      h(HealthCell, {
        runtime: makeRuntime({ status: "online" }),
        workload: { agentIds: [], runningCount: 0, queuedCount: 0 },
        now: Date.now(),
      }),
    );

    expect(screen.getByText("Online")).toBeInTheDocument();
    expect(screen.queryByText(/task/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/·/)).not.toBeInTheDocument();
  });

  it("shows the task-count suffix once an active task is attributed to this runtime", () => {
    renderWithI18n(
      h(HealthCell, {
        runtime: makeRuntime({ status: "online" }),
        workload: { agentIds: [], runningCount: 1, queuedCount: 0 },
        now: Date.now(),
      }),
    );

    expect(screen.getByText("Online")).toBeInTheDocument();
    expect(screen.getByText(/1 task/i)).toBeInTheDocument();
  });
});

import { createElement as h } from "react";
import { describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import type { Agent, AgentRuntime, AgentTask } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import {
  AgentStack,
  buildLatestFailureIndex,
  buildWorkloadIndex,
  HealthCell,
  LatestFailureBadge,
} from "./runtime-list";

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

describe("buildLatestFailureIndex", () => {
  it("attributes a failed task to the task's own runtime_id, not the agent's current binding", () => {
    const employee = makeAgent({
      id: "employee",
      runtime_id: "runtime-2",
    });
    const failedTask = makeTask({
      id: "failed-on-old",
      agent_id: employee.id,
      runtime_id: "runtime-1",
      status: "failed",
      failure_reason: "timeout",
      completed_at: "2026-01-03T00:00:00Z",
    });

    const index = buildLatestFailureIndex([employee], [failedTask]);

    expect(index.get("runtime-1")).toBe("timeout");
    expect(index.has("runtime-2")).toBe(false);
  });

  it("returns null-equivalent (no entry) when the latest terminal run is completed — newer completed clears older failed", () => {
    const agent = makeAgent({ id: "a1", runtime_id: "runtime-1" });
    const olderFailed = makeTask({
      id: "t-old",
      agent_id: "a1",
      runtime_id: "runtime-1",
      status: "failed",
      failure_reason: "timeout",
      completed_at: "2026-01-02T00:00:00Z",
    });
    const newerCompleted = makeTask({
      id: "t-new",
      agent_id: "a1",
      runtime_id: "runtime-1",
      status: "completed",
      completed_at: "2026-01-03T00:00:00Z",
    });

    const index = buildLatestFailureIndex([agent], [
      olderFailed,
      newerCompleted,
    ]);

    expect(index.has("runtime-1")).toBe(false);
  });

  it("shows the failure class when the latest terminal run is failed (newer failed after an older completed)", () => {
    const agent = makeAgent({ id: "a1", runtime_id: "runtime-1" });
    const olderCompleted = makeTask({
      id: "t-old",
      agent_id: "a1",
      runtime_id: "runtime-1",
      status: "completed",
      completed_at: "2026-01-02T00:00:00Z",
    });
    const newerFailed = makeTask({
      id: "t-new",
      agent_id: "a1",
      runtime_id: "runtime-1",
      status: "failed",
      failure_reason: "timeout",
      completed_at: "2026-01-03T00:00:00Z",
    });

    const index = buildLatestFailureIndex([agent], [
      olderCompleted,
      newerFailed,
    ]);

    expect(index.get("runtime-1")).toBe("timeout");
  });

  it("sorts deterministically: completed_at first, then created_at, then id", () => {
    const agent = makeAgent({ id: "a1", runtime_id: "runtime-1" });
    // Two failed tasks with same completed_at — the one with the later
    // created_at should win the tie.
    const olderCreated = makeTask({
      id: "t-aa",
      agent_id: "a1",
      runtime_id: "runtime-1",
      status: "failed",
      failure_reason: "timeout",
      completed_at: "2026-01-03T00:00:00Z",
      created_at: "2026-01-01T00:00:00Z",
    });
    const newerCreated = makeTask({
      id: "t-bb",
      agent_id: "a1",
      runtime_id: "runtime-1",
      status: "failed",
      failure_reason: "runtime_offline",
      completed_at: "2026-01-03T00:00:00Z",
      created_at: "2026-01-02T00:00:00Z",
    });
    // newerCreated has later created_at → it is the latest → runtime error.
    const index = buildLatestFailureIndex([agent], [olderCreated, newerCreated]);
    expect(index.get("runtime-1")).toBe("runtime");

    // Same completed_at + same created_at — id breaks the tie
    // (lexicographic, larger id = "newer").
    const a = makeTask({
      id: "t-a",
      agent_id: "a1",
      runtime_id: "runtime-2",
      status: "failed",
      failure_reason: "timeout",
      completed_at: "2026-01-03T00:00:00Z",
      created_at: "2026-01-01T00:00:00Z",
    });
    const b = makeTask({
      id: "t-b",
      agent_id: "a1",
      runtime_id: "runtime-2",
      status: "failed",
      failure_reason: "runtime_offline",
      completed_at: "2026-01-03T00:00:00Z",
      created_at: "2026-01-01T00:00:00Z",
    });
    const index2 = buildLatestFailureIndex([agent], [a, b]);
    expect(index2.get("runtime-2")).toBe("runtime");
  });

  it("folds each of the seven failure classes correctly", () => {
    const agent = makeAgent({ id: "a1", runtime_id: "runtime-1" });
    const cases: [string, string][] = [
      ["agent_error.provider_auth_or_access", "auth"],
      ["agent_error.provider_capacity_or_rate_limit", "rate_limit"],
      ["timeout", "timeout"],
      ["agent_error.provider_server_error", "provider"],
      ["runtime_offline", "runtime"],
      ["agent_error.process_failure", "agent"],
      ["agent_error.unknown", "other"],
    ];

    for (const [reason, expectedClass] of cases) {
      const task = makeTask({
        id: `t-${reason}`,
        agent_id: "a1",
        runtime_id: `runtime-${reason}`,
        status: "failed",
        failure_reason: reason as AgentTask["failure_reason"],
        completed_at: "2026-01-03T00:00:00Z",
      });
      const index = buildLatestFailureIndex([agent], [task]);
      expect(index.get(`runtime-${reason}`)).toBe(expectedClass);
    }
  });

  it("maps an unknown failure reason to 'other' without leaking the raw reason", () => {
    const agent = makeAgent({ id: "a1", runtime_id: "runtime-1" });
    const task = makeTask({
      id: "t1",
      agent_id: "a1",
      runtime_id: "runtime-1",
      status: "failed",
      failure_reason: "some_future_unknown_reason" as AgentTask["failure_reason"],
      completed_at: "2026-01-03T00:00:00Z",
    });

    const index = buildLatestFailureIndex([agent], [task]);

    expect(index.get("runtime-1")).toBe("other");
  });

  it("maps a missing/empty failure_reason to 'other' and never crashes", () => {
    const agent = makeAgent({ id: "a1", runtime_id: "runtime-1" });
    const noReason = makeTask({
      id: "t1",
      agent_id: "a1",
      runtime_id: "runtime-1",
      status: "failed",
      completed_at: "2026-01-03T00:00:00Z",
    });
    const emptyReason = makeTask({
      id: "t2",
      agent_id: "a1",
      runtime_id: "runtime-2",
      status: "failed",
      failure_reason: "",
      completed_at: "2026-01-03T00:00:00Z",
    });

    const index = buildLatestFailureIndex([agent], [noReason, emptyReason]);

    expect(index.get("runtime-1")).toBe("other");
    expect(index.get("runtime-2")).toBe("other");
  });

  it("excludes non-terminal states — queued, dispatched, waiting_local_directory, running", () => {
    const agent = makeAgent({ id: "a1", runtime_id: "runtime-1" });
    const nonTerminalStatuses: AgentTask["status"][] = [
      "queued",
      "dispatched",
      "waiting_local_directory",
      "running",
    ];
    const tasks = nonTerminalStatuses.map((status, i) =>
      makeTask({
        id: `t-${i}`,
        agent_id: "a1",
        runtime_id: `runtime-${status}`,
        status,
        failure_reason: "timeout",
      }),
    );

    const index = buildLatestFailureIndex([agent], tasks);

    for (const status of nonTerminalStatuses) {
      expect(index.has(`runtime-${status}`)).toBe(false);
    }
  });

  it("does not attribute failures of archived agents", () => {
    const archived = makeAgent({
      id: "archived",
      runtime_id: "runtime-1",
      archived_at: "2026-01-02T00:00:00Z",
    });
    const task = makeTask({
      id: "t1",
      agent_id: "archived",
      runtime_id: "runtime-1",
      status: "failed",
      failure_reason: "timeout",
      completed_at: "2026-01-03T00:00:00Z",
    });

    const index = buildLatestFailureIndex([archived], [task]);

    expect(index.has("runtime-1")).toBe(false);
  });

  it("keeps Task runtime lineage when an active agent is currently unbound", () => {
    const unbound = makeAgent({ id: "unbound", runtime_id: null });
    const historicalTask = makeTask({
      id: "t-unbound-history",
      agent_id: "unbound",
      runtime_id: "runtime-historical",
      status: "failed",
      failure_reason: "timeout",
      completed_at: "2026-01-03T00:00:00Z",
    });

    const index = buildLatestFailureIndex([unbound], [historicalTask]);

    expect(index.get("runtime-historical")).toBe("timeout");
  });

  it("does not attribute failures of unknown (missing) agents", () => {
    const active = makeAgent({ id: "active", runtime_id: "runtime-1" });
    const ghostTask = makeTask({
      id: "ghost",
      agent_id: "ghost-agent",
      runtime_id: "runtime-1",
      status: "failed",
      failure_reason: "timeout",
      completed_at: "2026-01-03T00:00:00Z",
    });

    const index = buildLatestFailureIndex([active], [ghostTask]);

    expect(index.has("runtime-1")).toBe(false);
  });

  it("cancelled tasks are terminal but not a failure — they do not produce a failure badge", () => {
    const agent = makeAgent({ id: "a1", runtime_id: "runtime-1" });
    const cancelled = makeTask({
      id: "t1",
      agent_id: "a1",
      runtime_id: "runtime-1",
      status: "cancelled",
      failure_reason: "",
      completed_at: "2026-01-03T00:00:00Z",
    });

    const index = buildLatestFailureIndex([agent], [cancelled]);

    expect(index.has("runtime-1")).toBe(false);
  });

  it("does not leak the raw failure_reason, error, or any provider/credential text into the returned map", () => {
    const agent = makeAgent({ id: "a1", runtime_id: "runtime-1" });
    const task = makeTask({
      id: "t1",
      agent_id: "a1",
      runtime_id: "runtime-1",
      status: "failed",
      failure_reason: "agent_error.provider_auth_or_access" as AgentTask["failure_reason"],
      error: "invalid token sk-abc123-secret",
      completed_at: "2026-01-03T00:00:00Z",
    });

    const index = buildLatestFailureIndex([agent], [task]);
    const value = index.get("runtime-1");

    expect(value).toBe("auth");
    expect(value).not.toContain("sk-abc123");
    expect(value).not.toContain("invalid token");
    expect(value).not.toContain("provider_auth_or_access");
  });
});

describe("LatestFailureBadge", () => {
  it("renders nothing when failureClass is null", () => {
    const view = renderWithI18n(
      h(LatestFailureBadge, { failureClass: null }),
    );
    expect(screen.queryByTestId("latest-failure-badge")).not.toBeInTheDocument();
    view.unmount();
  });

  it("renders a localized label for each of the seven failure classes", () => {
    const cases = [
      ["en", "auth", "Auth error"],
      ["en", "rate_limit", "Rate limited"],
      ["en", "timeout", "Timed out"],
      ["en", "provider", "Provider error"],
      ["en", "runtime", "Runtime error"],
      ["en", "agent", "Agent error"],
      ["en", "other", "Run failed"],
      ["zh-Hans", "auth", "鉴权错误"],
      ["zh-Hans", "timeout", "运行超时"],
      ["ja", "auth", "認証エラー"],
      ["ja", "timeout", "タイムアウト"],
      ["ko", "auth", "인증 오류"],
      ["ko", "timeout", "시간 초과"],
    ] as const;

    for (const [locale, cls, label] of cases) {
      const view = renderWithI18n(
        h(LatestFailureBadge, { failureClass: cls }),
        { locale },
      );
      const badge = screen.getByTestId("latest-failure-badge");
      expect(badge).toBeInTheDocument();
      expect(badge).toHaveTextContent(label);
      expect(badge.dataset.failureClass).toBe(cls);
      view.unmount();
    }
  });

  it("never renders the raw failure reason string in the DOM", () => {
    const view = renderWithI18n(
      h(LatestFailureBadge, { failureClass: "auth" }),
    );
    const badge = screen.getByTestId("latest-failure-badge");
    const text = badge.textContent ?? "";
    expect(text).not.toContain("provider_auth");
    expect(text).not.toContain("agent_error");
    expect(text).not.toContain("failure_reason");
    view.unmount();
  });
});

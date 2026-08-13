import { describe, expect, it } from "vitest";
import type { EmployeeStateExplanation } from "@multica/core/agents";
import en from "../../locales/en/agents.json";
import zhHans from "../../locales/zh-Hans/agents.json";
import ja from "../../locales/ja/agents.json";
import ko from "../../locales/ko/agents.json";
import { renderWithI18n } from "../../test/i18n";
import {
  EmployeeStatusExplainer,
  formatStalenessCompact,
} from "./employee-status-explainer";

function makeExplanation(
  overrides: Partial<EmployeeStateExplanation> = {},
): EmployeeStateExplanation {
  return {
    status: "idle",
    reason: "idle_ready",
    nextAction: "none",
    availability: "online",
    workload: "idle",
    runningCount: 0,
    queuedCount: 0,
    capacity: 4,
    currentTask: null,
    runtimeHealth: "online",
    runtimeLastSeenAt: "2026-04-27T11:59:50Z",
    runtimeStalenessMs: 10_000,
    workspaceBacklogCount: 0,
    ...overrides,
  };
}

describe("EmployeeStatusExplainer", () => {
  it("healthy idle with backlog: idle status, backlog reason, assign_work action", () => {
    const { container } = renderWithI18n(
      <EmployeeStatusExplainer
        explanation={makeExplanation({
          status: "idle",
          reason: "idle_backlog_waiting",
          nextAction: "assign_work",
          workspaceBacklogCount: 3,
        })}
      />,
    );
    const root = container.querySelector("[data-employee-status-explainer]")!;
    expect(root.getAttribute("data-employee-status")).toBe("idle");
    expect(
      container.querySelector('[data-state-reason="idle_backlog_waiting"]'),
    ).not.toBeNull();
    expect(root.textContent).toContain("3 tasks wait in the workspace queue");
    expect(
      container.querySelector('[data-next-action="assign_work"]'),
    ).not.toBeNull();
    expect(root.textContent).toContain("Assign backlog work");
    // Healthy idle shows a real capacity ratio, not an unknown.
    expect(container.querySelector('[data-capacity="4"]')).not.toBeNull();
    expect(root.textContent).toContain("0 / 4");
  });

  it("unhealthy runtime: unavailable with offline reason and staleness", () => {
    const { container } = renderWithI18n(
      <EmployeeStatusExplainer
        explanation={makeExplanation({
          status: "unavailable",
          reason: "runtime_offline",
          nextAction: "restore_runtime",
          availability: "offline",
          runtimeHealth: "offline",
          runtimeLastSeenAt: "2026-04-27T10:00:00Z",
          runtimeStalenessMs: 2 * 3600 * 1000,
        })}
      />,
    );
    const root = container.querySelector("[data-employee-status-explainer]")!;
    expect(root.getAttribute("data-employee-status")).toBe("unavailable");
    expect(
      container.querySelector('[data-state-reason="runtime_offline"]'),
    ).not.toBeNull();
    expect(
      container.querySelector('[data-runtime-health="offline"]'),
    ).not.toBeNull();
    expect(
      container
        .querySelector("[data-runtime-staleness-ms]")
        ?.getAttribute("data-runtime-staleness-ms"),
    ).toBe(String(2 * 3600 * 1000));
    expect(root.textContent).toContain("last seen 2h ago");
    expect(
      container.querySelector('[data-next-action="restore_runtime"]'),
    ).not.toBeNull();
    // No current run is claimed while the runtime is unreachable.
    expect(container.querySelector("[data-current-task]")).toBeNull();
  });

  it("quota unknown: capacity renders fail-closed, never a guessed number", () => {
    const { container } = renderWithI18n(
      <EmployeeStatusExplainer
        explanation={makeExplanation({
          status: "working",
          reason: "running_tasks",
          workload: "working",
          runningCount: 1,
          capacity: null,
          currentTask: {
            id: "task-quota-1",
            issueId: "issue-1",
            status: "running",
            createdAt: "2026-04-27T11:00:00Z",
            dispatchedAt: "2026-04-27T11:01:00Z",
            startedAt: "2026-04-27T11:02:00Z",
          },
        })}
      />,
    );
    const capacity = container.querySelector('[data-capacity="unknown"]');
    expect(capacity).not.toBeNull();
    expect(capacity!.textContent).toContain("Quota unknown");
    expect(capacity!.textContent).not.toMatch(/\d+ \/ \d+/);
  });

  it("active work: working status with the current run on record", () => {
    const { container } = renderWithI18n(
      <EmployeeStatusExplainer
        explanation={makeExplanation({
          status: "working",
          reason: "running_tasks",
          workload: "working",
          runningCount: 2,
          currentTask: {
            id: "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
            issueId: "issue-9",
            status: "running",
            createdAt: "2026-04-27T11:00:00Z",
            dispatchedAt: "2026-04-27T11:01:00Z",
            startedAt: "2026-04-27T11:02:00Z",
          },
        })}
      />,
    );
    const root = container.querySelector("[data-employee-status-explainer]")!;
    expect(root.getAttribute("data-employee-status")).toBe("working");
    const current = container.querySelector("[data-current-task]")!;
    expect(current.getAttribute("data-current-task-id")).toBe(
      "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
    );
    expect(current.textContent).toContain("#a1b2c3d4");
    expect(current.textContent).toContain("running");
    expect(root.textContent).toContain("Running 2 tasks");
    expect(
      container.querySelector('[data-next-action="none"]'),
    ).not.toBeNull();
    expect(container.querySelector('[data-capacity="4"]')!.textContent).toContain(
      "2 / 4",
    );
  });

  it("compact variant carries the same state hooks in one line", () => {
    const { container } = renderWithI18n(
      <EmployeeStatusExplainer
        compact
        explanation={makeExplanation({
          status: "waiting",
          reason: "awaiting_dispatch",
          nextAction: "await_dispatch",
          workload: "queued",
          queuedCount: 1,
        })}
      />,
    );
    const el = container.querySelector("[data-employee-status]")!;
    expect(el.getAttribute("data-employee-status")).toBe("waiting");
    expect(el.getAttribute("data-state-reason")).toBe("awaiting_dispatch");
    expect(el.getAttribute("data-next-action")).toBe("await_dispatch");
    expect(el.getAttribute("title")).toContain("Waiting");
  });

  it("renders a skeleton while the explanation is loading", () => {
    const { container } = renderWithI18n(
      <EmployeeStatusExplainer explanation={null} />,
    );
    expect(
      container.querySelector("[data-employee-status-explainer]"),
    ).toBeNull();
  });

  it("localized reason copy resolves in zh-Hans", () => {
    const { container } = renderWithI18n(
      <EmployeeStatusExplainer
        explanation={makeExplanation({
          status: "unavailable",
          reason: "runtime_offline",
          nextAction: "restore_runtime",
          availability: "offline",
          runtimeHealth: "offline",
        })}
      />,
      { locale: "zh-Hans" },
    );
    expect(container.textContent).toContain("不可用");
    expect(container.textContent).toContain("运行时离线");
    expect(container.textContent).toContain("检查或重启运行时守护进程");
  });
});

describe("formatStalenessCompact", () => {
  it("buckets seconds / minutes / hours / days", () => {
    expect(formatStalenessCompact(45_000)).toBe("45s");
    expect(formatStalenessCompact(3 * 60_000)).toBe("3m");
    expect(formatStalenessCompact(2 * 3600_000)).toBe("2h");
    expect(formatStalenessCompact(5 * 24 * 3600_000)).toBe("5d");
    expect(formatStalenessCompact(-1000)).toBe("0s");
  });
});

// Same drift guard as agents-i18n-parity.test.ts: every status_explanation
// key must exist in all 4 locales so no locale silently renders raw keys.
describe("status_explanation i18n parity across all 4 locales", () => {
  const LOCALES = { en, "zh-Hans": zhHans, ja, ko } as const;

  const REASON_BASE_KEYS = [
    "agent_archived",
    "runtime_missing",
    "runtime_recently_lost",
    "runtime_offline",
    "runtime_about_to_gc",
    "waiting_local_directory",
    "dispatched_awaiting_start",
    "awaiting_dispatch",
    "idle_ready",
  ];
  const PLURAL_REASON_KEYS = ["running_tasks", "idle_backlog_waiting"];
  const STATUS_KEYS = ["working", "idle", "waiting", "unavailable"];
  const HEALTH_KEYS = ["online", "recently_lost", "offline", "about_to_gc", "missing"];
  const ACTION_KEYS = [
    "none",
    "unarchive",
    "restore_runtime",
    "monitor_runtime",
    "await_path_lock",
    "await_daemon_start",
    "await_dispatch",
    "assign_work",
  ];
  const SCALAR_KEYS = [
    "title",
    "current_task",
    "capacity",
    "capacity_value",
    "capacity_unknown",
    "runtime_health",
    "last_seen",
    "next_action",
  ];

  it("every key is present and non-empty in all locales", () => {
    for (const [name, loc] of Object.entries(LOCALES)) {
      const node = loc.status_explanation as Record<string, unknown>;
      expect(node, `${name}: status_explanation missing`).toBeDefined();
      const reason = node.reason as Record<string, string>;
      for (const key of REASON_BASE_KEYS) {
        expect(reason[key], `${name}: reason.${key}`).toBeTruthy();
      }
      for (const key of PLURAL_REASON_KEYS) {
        const hasPluralForm = Object.keys(reason).some((k) =>
          k.startsWith(`${key}_`),
        );
        expect(hasPluralForm, `${name}: reason.${key}_*`).toBe(true);
      }
      for (const key of STATUS_KEYS) {
        expect(
          (node.status as Record<string, string>)[key],
          `${name}: status.${key}`,
        ).toBeTruthy();
      }
      for (const key of HEALTH_KEYS) {
        expect(
          (node.health as Record<string, string>)[key],
          `${name}: health.${key}`,
        ).toBeTruthy();
      }
      for (const key of ACTION_KEYS) {
        expect(
          (node.action as Record<string, string>)[key],
          `${name}: action.${key}`,
        ).toBeTruthy();
      }
      for (const key of SCALAR_KEYS) {
        expect(node[key], `${name}: ${key}`).toBeTruthy();
      }
    }
  });
});

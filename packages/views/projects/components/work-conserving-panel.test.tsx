import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import enProjects from "../../locales/en/projects.json";
import type { WorkConservingProjection } from "@multica/core/types";

const mockQuery = vi.hoisted(() => ({
  result: null as { data?: WorkConservingProjection; isLoading?: boolean; isError?: boolean } | null,
}));

vi.mock("@tanstack/react-query", () => ({
  useQuery: () => mockQuery.result ?? { isLoading: false, isError: true },
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "workspace-1",
}));

vi.mock("@multica/core/projects/queries", () => ({
  projectWorkConservingOptions: () => ({ queryKey: ["projection"] }),
}));

import { WorkConservingPanel } from "./work-conserving-panel";

function renderPanel() {
  return render(
    <I18nProvider locale="en" resources={{ en: { projects: enProjects } }}>
      <WorkConservingPanel projectId="project-1" />
    </I18nProvider>,
  );
}

function projection(state: WorkConservingProjection["state"]): WorkConservingProjection {
  return {
    schemaVersion: "hivecrew.work-conserving-projection/v1",
    state,
    blocked: state === "blocked",
    goalId: "goal-1",
    authority: {
      workspaceId: "workspace-1",
      projectId: "project-1",
      sourceRef: "hivecosm://goal/goal-1",
      revision: "rev-1",
      observedAt: "2026-08-15T00:00:00Z",
      expiresAt: "2026-08-15T00:15:00Z",
    },
    suggestions: [],
    blockedBacklog: [],
    mismatch: {
      openIssues: 0,
      plannedIssues: 0,
      blockedBacklog: 0,
      healthyIdleEmployees: 0,
      unmatchedHealthyIdleEmployees: 0,
      executableBacklog: 0,
      idleBacklogMismatch: 0,
    },
    total: 0,
    limit: 50,
    offset: 0,
    noWrite: true,
  };
}

describe("WorkConservingPanel", () => {
  it.each([
    ["ready", "Ready"],
    ["blocked", "Blocked"],
    ["source_gap", "Source gap"],
  ] as const)("renders the %s state without write controls", (state, label) => {
    mockQuery.result = { data: projection(state), isLoading: false, isError: false };
    renderPanel();
    expect(screen.getByText(label)).toBeInTheDocument();
    expect(screen.getByText(/Read-only/)).toBeInTheDocument();
    expect(screen.queryByRole("button")).toBeNull();
  });

  it("does not render stale source_gap metrics, Authority, or plan entries", () => {
    const base = projection("source_gap");
    const invalidSourceGap = {
      ...base,
      total: 9,
      authority: {
        workspaceId: "workspace-1",
        projectId: "project-1",
        sourceRef: "hivecosm://goal/stale",
        revision: "stale-rev",
        observedAt: "2026-08-15T00:00:00Z",
        expiresAt: "2026-08-15T00:15:00Z",
      },
      mismatch: { ...base.mismatch, openIssues: 9 },
      suggestions: [{
        issueId: "stale-issue",
        goalId: "goal-1",
        employeeId: "employee-1",
        agentId: "agent-1",
        runtimeId: "runtime-1",
        score: 1,
        receiver: "receiver",
        wakeCondition: "wake",
      }],
    };
    mockQuery.result = { data: invalidSourceGap, isLoading: false, isError: false };
    renderPanel();
    expect(screen.queryByText("9")).toBeNull();
    expect(screen.queryByText(/Authority revision/)).toBeNull();
    expect(screen.queryByText("Current plan evidence")).toBeNull();
  });
});

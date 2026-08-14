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
});

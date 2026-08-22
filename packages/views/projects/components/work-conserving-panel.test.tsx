import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { I18nProvider } from "@multica/core/i18n/react";
import { Toaster, toast } from "sonner";
import enProjects from "../../locales/en/projects.json";
import type { WorkConservingDrainResult, WorkConservingProjection } from "@multica/core/types";

const mockQueryResult = vi.hoisted(() => ({
  workConserving: null as
    | { data?: WorkConservingProjection; isLoading?: boolean; isError?: boolean }
    | null,
  members: null as
    | { data?: { user_id: string; role: string }[]; isLoading?: boolean; isError?: boolean }
    | null,
}));

const mutationShared = vi.hoisted(() => ({
  isPending: false,
  invalidateQueries: vi.fn(),
  onSuccessCalled: 0,
}));

type MutationCallbacks = {
  onMutate?: () => void;
  onSuccess?: (result: WorkConservingDrainResult) => void;
  onError?: (error: unknown) => void;
  mutationFn: () => Promise<WorkConservingDrainResult>;
};

const mutationRef = vi.hoisted(() => ({
  forceUpdate: null as (() => void) | null,
  callbacks: null as MutationCallbacks | null,
}));

vi.mock("@tanstack/react-query", () => {
  const useMutation = (cb: MutationCallbacks) => {
    mutationRef.callbacks = cb;
    const mutate = () => {
      mutationShared.isPending = true;
      cb.onMutate?.();
      cb.mutationFn()
        .then(async (value) => {
          mutationShared.isPending = false;
          await Promise.resolve();
          mutationShared.onSuccessCalled++;
          await act(async () => {
            cb.onSuccess?.(value);
            mutationRef.forceUpdate?.();
          });
        })
        .catch(async (error) => {
          mutationShared.isPending = false;
          await act(async () => {
            cb.onError?.(error);
            mutationRef.forceUpdate?.();
          });
        });
      mutationRef.forceUpdate?.();
    };
    return { isPending: mutationShared.isPending, mutate };
  };

  return {
    useQuery: (options: { queryKey?: unknown[] } = {}) => {
      const key = options.queryKey as string[] | undefined;
      if (key?.includes("work-conserving")) {
        return mockQueryResult.workConserving ?? { isLoading: false, isError: true };
      }
      if (key?.includes("members")) {
        return mockQueryResult.members ?? { isLoading: false, isError: true };
      }
      return { isLoading: false, isError: true };
    },
    useMutation,
    useQueryClient: () => ({ invalidateQueries: mutationShared.invalidateQueries }),
  };
});

type DrainProjectNextActionsFn = (projectId: string) => Promise<WorkConservingDrainResult>;

const mockDrain = vi.hoisted(() => vi.fn<DrainProjectNextActionsFn>());

vi.mock("@multica/core/api", () => ({
  api: {
    drainProjectNextActions: (id: string) => mockDrain(id),
  },
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "workspace-1",
}));

vi.mock("@multica/core/workspace/queries", () => ({
  memberListOptions: (wsId: string) => ({ queryKey: ["members", wsId] }),
}));

vi.mock("@multica/core/projects/queries", () => ({
  projectKeys: {
    workConserving: (wsId: string, id: string) => ["project", wsId, id, "work-conserving"],
  },
  projectWorkConservingOptions: (wsId: string, id: string) => ({
    queryKey: ["project", wsId, id, "work-conserving"],
  }),
}));

vi.mock("@multica/core/auth", () => ({
  useAuthStore: (selector?: (s: { user?: { id: string } }) => unknown) => {
    const state = { user: { id: "user-1" } };
    return selector ? selector(state) : state;
  },
}));

import { WorkConservingPanel } from "./work-conserving-panel";

function RenderDriver() {
  const [, setTick] = useState(0);
  mutationRef.forceUpdate = () => setTick((t) => t + 1);
  return <WorkConservingPanel projectId="project-1" />;
}

function renderPanel() {
  return render(
    <I18nProvider locale="en" resources={{ en: { projects: enProjects } }}>
      <Toaster />
      <RenderDriver />
    </I18nProvider>,
  );
}

function projection(
  state: WorkConservingProjection["state"],
  organizationSourceState: WorkConservingProjection["organizationSourceState"] = null,
): WorkConservingProjection {
  return {
    schemaVersion: "hivecrew.work-conserving-projection/v1",
    state,
    blocked: state === "blocked",
    organizationSourceState,
    goalId: "goal-1",
    authority: {
      workspaceId: "workspace-1",
      projectId: "project-1",
      sourceRef: "hivecosm://goal/goal-1",
      revision: "rev-1",
      observedAt: "2026-08-15T00:00:00Z",
      expiresAt: "2026-08-15T00:15:00Z",
    },
    suggestions: [
      {
        issueId: "issue-1",
        goalId: "goal-1",
        employeeId: "employee-1",
        agentId: "agent-1",
        runtimeId: "runtime-1",
        score: 1,
        receiver: "receiver",
        wakeCondition: "wake",
      },
    ],
    blockedBacklog: [],
    mismatch: {
      openIssues: 1,
      plannedIssues: 0,
      blockedBacklog: 0,
      healthyIdleEmployees: 1,
      unmatchedHealthyIdleEmployees: 0,
      executableBacklog: 1,
      idleBacklogMismatch: 0,
    },
    total: 1,
    limit: 50,
    offset: 0,
    noWrite: true,
  };
}

function successDrainResult(): WorkConservingDrainResult {
  return {
    state: "ready",
    projectionState: "ready",
    goalId: "goal-1",
    authority: {
      workspaceId: "workspace-1",
      projectId: "project-1",
      sourceRef: "hivecosm://goal/goal-1",
      revision: "rev-1",
      observedAt: "2026-08-15T00:00:00Z",
      expiresAt: "2026-08-15T00:15:00Z",
    },
    batchSize: 1,
    results: [
      {
        issueId: "issue-drain-1",
        goalId: "goal-1",
        employeeId: "employee-1",
        outcome: "dispatched",
        receiver: "receiver",
        wakeCondition: "wake",
        receipt: {
          identity: {
            workspaceId: "workspace-1",
            issueId: "issue-drain-1",
            stage: "stage-1",
            candidateRevision: "rev-1",
            generation: "gen-1",
          },
          taskId: "task-1",
          employeeRef: "employee-1",
          localAgentId: "agent-1",
          runtimeId: "runtime-1",
          model: "model-1",
          accountRef: "account-1",
          requestDigest: "digest-1",
        },
      },
    ],
    deferredSuggestions: 0,
    dispatched: 1,
    alreadyTerminal: 0,
    blocked: 0,
    conflicts: 0,
    sourceGaps: 0,
  };
}

function sourceGapDrainResult(): WorkConservingDrainResult {
  return {
    state: "source_gap",
    reasonCode: "authority_missing",
    goalId: null,
    authority: null,
    batchSize: 0,
    results: [],
    deferredSuggestions: 0,
    dispatched: 0,
    alreadyTerminal: 0,
    blocked: 0,
    conflicts: 0,
    sourceGaps: 0,
  };
}

function noReadyDrainResult(): WorkConservingDrainResult {
  return {
    state: "ready",
    projectionState: "ready",
    goalId: "goal-1",
    authority: {
      workspaceId: "workspace-1",
      projectId: "project-1",
      sourceRef: "hivecosm://goal/goal-1",
      revision: "rev-1",
      observedAt: "2026-08-15T00:00:00Z",
      expiresAt: "2026-08-15T00:15:00Z",
    },
    batchSize: 1,
    results: [
      {
        issueId: "issue-drain-1",
        goalId: "goal-1",
        employeeId: "employee-1",
        outcome: "already_terminal",
        receiver: "receiver",
        wakeCondition: "wake",
      },
    ],
    deferredSuggestions: 1,
    dispatched: 0,
    alreadyTerminal: 1,
    blocked: 0,
    conflicts: 0,
    sourceGaps: 0,
  };
}

describe("WorkConservingPanel", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mutationShared.isPending = false;
    mutationShared.onSuccessCalled = 0;
    mutationShared.invalidateQueries.mockClear();
    mutationRef.forceUpdate = null;
    mutationRef.callbacks = null;
    toast.dismiss();
  });

  it.each([
    ["ready", "Ready"],
    ["blocked", "Blocked"],
    ["source_gap", "Source gap"],
  ] as const)("renders the %s state without write controls for non-admin", (state, label) => {
    mockQueryResult.workConserving = { data: projection(state), isLoading: false, isError: false };
    mockQueryResult.members = {
      data: [{ user_id: "user-1", role: "member" }],
      isLoading: false,
      isError: false,
    };
    renderPanel();
    expect(screen.getByText(label)).toBeInTheDocument();
    expect(screen.getByText("Read-only · no Task, dispatch, or database write")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /dispatch next action/i })).toBeNull();
  });

  it("shows bounded dispatch badge for owner with ready projection", () => {
    mockQueryResult.workConserving = { data: projection("ready"), isLoading: false, isError: false };
    mockQueryResult.members = {
      data: [{ user_id: "user-1", role: "owner" }],
      isLoading: false,
      isError: false,
    };
    renderPanel();
    expect(screen.getByText("Bounded dispatch · one action at a time, fail-closed")).toBeInTheDocument();
    expect(screen.queryByText("Read-only · no Task, dispatch, or database write")).toBeNull();
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
    mockQueryResult.workConserving = { data: invalidSourceGap, isLoading: false, isError: false };
    mockQueryResult.members = {
      data: [{ user_id: "user-1", role: "owner" }],
      isLoading: false,
      isError: false,
    };
    renderPanel();
    expect(screen.queryByText("9")).toBeNull();
    expect(screen.queryByText(/Authority revision/)).toBeNull();
    expect(screen.queryByText("Current plan evidence")).toBeNull();
  });

  describe("Owner/Admin drain action visibility", () => {
    it("shows drain button for owner role with ready projection", () => {
      mockQueryResult.workConserving = { data: projection("ready"), isLoading: false, isError: false };
      mockQueryResult.members = {
        data: [{ user_id: "user-1", role: "owner" }],
        isLoading: false,
        isError: false,
      };
      renderPanel();
      expect(screen.getByRole("button", { name: /dispatch next action/i })).toBeInTheDocument();
    });

    it("shows drain button for admin role with ready projection", () => {
      mockQueryResult.workConserving = { data: projection("ready"), isLoading: false, isError: false };
      mockQueryResult.members = {
        data: [{ user_id: "user-1", role: "admin" }],
        isLoading: false,
        isError: false,
      };
      renderPanel();
      expect(screen.getByRole("button", { name: /dispatch next action/i })).toBeInTheDocument();
    });

    it("hides drain button for member role", () => {
      mockQueryResult.workConserving = { data: projection("ready"), isLoading: false, isError: false };
      mockQueryResult.members = {
        data: [{ user_id: "user-1", role: "member" }],
        isLoading: false,
        isError: false,
      };
      renderPanel();
      expect(screen.queryByRole("button", { name: /dispatch next action/i })).toBeNull();
    });

    it("hides drain button when user is not a member", () => {
      mockQueryResult.workConserving = { data: projection("ready"), isLoading: false, isError: false };
      mockQueryResult.members = {
        data: [{ user_id: "other-user", role: "owner" }],
        isLoading: false,
        isError: false,
      };
      renderPanel();
      expect(screen.queryByRole("button", { name: /dispatch next action/i })).toBeNull();
    });

    it("hides drain button while membership is loading (fail-closed)", () => {
      mockQueryResult.workConserving = { data: projection("ready"), isLoading: false, isError: false };
      mockQueryResult.members = { isLoading: true, isError: false };
      renderPanel();
      expect(screen.queryByRole("button", { name: /dispatch next action/i })).toBeNull();
    });

    it("hides drain button when membership errors (fail-closed)", () => {
      mockQueryResult.workConserving = { data: projection("ready"), isLoading: false, isError: false };
      mockQueryResult.members = { isLoading: false, isError: true };
      renderPanel();
      expect(screen.queryByRole("button", { name: /dispatch next action/i })).toBeNull();
    });

    it("hides drain button when projection is source_gap", () => {
      mockQueryResult.workConserving = { data: projection("source_gap"), isLoading: false, isError: false };
      mockQueryResult.members = {
        data: [{ user_id: "user-1", role: "owner" }],
        isLoading: false,
        isError: false,
      };
      renderPanel();
      expect(screen.queryByRole("button", { name: /dispatch next action/i })).toBeNull();
    });

    it("disables drain button when no ready suggestions exist", () => {
      const emptyReady = { ...projection("ready"), suggestions: [] };
      mockQueryResult.workConserving = { data: emptyReady, isLoading: false, isError: false };
      mockQueryResult.members = {
        data: [{ user_id: "user-1", role: "owner" }],
        isLoading: false,
        isError: false,
      };
      renderPanel();
      const button = screen.getByRole("button", { name: /dispatch next action/i });
      expect(button).toBeDisabled();
    });
  });

  describe("Drain mutation behavior", () => {
    it("calls api.drainProjectNextActions with only projectId (no selectors)", async () => {
      mockQueryResult.workConserving = { data: projection("ready"), isLoading: false, isError: false };
      mockQueryResult.members = {
        data: [{ user_id: "user-1", role: "owner" }],
        isLoading: false,
        isError: false,
      };
      mockDrain.mockResolvedValue(successDrainResult());
      renderPanel();
      const button = screen.getByRole("button", { name: /dispatch next action/i });
      act(() => {
        fireEvent.click(button);
      });
      await vi.waitFor(() => {
        expect(mockDrain).toHaveBeenCalledTimes(1);
      });
      expect(mockDrain).toHaveBeenCalledWith("project-1");
      expect(mockDrain.mock.calls[0]!.length).toBe(1);
    });

    it("disables button while pending (duplicate-click guard)", async () => {
      let resolveDrain: (value: WorkConservingDrainResult) => void;
      const pendingPromise = new Promise<WorkConservingDrainResult>((resolve) => {
        resolveDrain = resolve;
      });
      mockDrain.mockImplementation(() => pendingPromise);
      mockQueryResult.workConserving = { data: projection("ready"), isLoading: false, isError: false };
      mockQueryResult.members = {
        data: [{ user_id: "user-1", role: "owner" }],
        isLoading: false,
        isError: false,
      };
      renderPanel();
      const button = screen.getByRole("button", { name: /dispatch next action/i });
      act(() => {
        fireEvent.click(button);
      });
      expect(button).toBeDisabled();
      act(() => {
        resolveDrain(successDrainResult());
      });
      await vi.waitFor(() => {
        expect(button).not.toBeDisabled();
      });
    });

    it("shows success counters and per-issue outcome with receipt on valid result", async () => {
      mockQueryResult.workConserving = { data: projection("ready"), isLoading: false, isError: false };
      mockQueryResult.members = {
        data: [{ user_id: "user-1", role: "owner" }],
        isLoading: false,
        isError: false,
      };
      mockDrain.mockResolvedValue(successDrainResult());
      renderPanel();
      const button = screen.getByRole("button", { name: /dispatch next action/i });
      fireEvent.click(button);
      await vi.waitFor(() => {
        expect(mutationShared.onSuccessCalled).toBe(1);
      });
      expect(document.body.textContent).toContain("issue-drain-1");
      expect(document.body.textContent).toContain("task-1");
      expect(document.body.textContent).not.toContain("account-1");
      expect(document.body.textContent).not.toContain("digest-1");
      expect(document.body.textContent).not.toContain("model-1");
    });

    it("shows neutral no-ready toast with safe counters when dispatched=0 and batchSize>0", async () => {
      mockQueryResult.workConserving = { data: projection("ready"), isLoading: false, isError: false };
      mockQueryResult.members = {
        data: [{ user_id: "user-1", role: "owner" }],
        isLoading: false,
        isError: false,
      };
      mockDrain.mockResolvedValue(noReadyDrainResult());
      renderPanel();
      const button = screen.getByRole("button", { name: /dispatch next action/i });
      fireEvent.click(button);
      expect(await screen.findByText("No ready suggestions to dispatch")).toBeInTheDocument();
      await vi.waitFor(() => {
        expect(mutationShared.onSuccessCalled).toBe(1);
      });
      expect(screen.queryByText("Dispatch completed")).toBeNull();
      expect(document.body.textContent).toContain("Dispatched");
      expect(document.body.textContent).toContain("Already terminal");
      expect(document.body.textContent).toContain("Deferred");
      expect(screen.queryByText("task-1")).toBeNull();
    });

    it("shows authority gap toast for source_gap drain result", async () => {
      mockQueryResult.workConserving = { data: projection("ready"), isLoading: false, isError: false };
      mockQueryResult.members = {
        data: [{ user_id: "user-1", role: "owner" }],
        isLoading: false,
        isError: false,
      };
      mockDrain.mockResolvedValue(sourceGapDrainResult());
      renderPanel();
      const button = screen.getByRole("button", { name: /dispatch next action/i });
      act(() => {
        fireEvent.click(button);
      });
      expect(await screen.findByText(/dispatch unavailable.*authority gap/i)).toBeInTheDocument();
    });

    it("shows safe error toast on mutation error (no raw body)", async () => {
      mockQueryResult.workConserving = { data: projection("ready"), isLoading: false, isError: false };
      mockQueryResult.members = {
        data: [{ user_id: "user-1", role: "owner" }],
        isLoading: false,
        isError: false,
      };
      mockDrain.mockRejectedValue({ status: 503, body: "sensitive-error-details" });
      renderPanel();
      const button = screen.getByRole("button", { name: /dispatch next action/i });
      act(() => {
        fireEvent.click(button);
      });
      expect(await screen.findByText("Dispatch failed")).toBeInTheDocument();
      expect(screen.queryByText("sensitive-error-details")).toBeNull();
      expect(screen.queryByText("503")).toBeNull();
    });
  });
});

describe("WorkConservingPanel organization source notice", () => {
  beforeEach(() => {
    mockQueryResult.workConserving = null;
    mockQueryResult.members = null;
    mutationShared.isPending = false;
    mutationShared.invalidateQueries.mockClear();
    mutationShared.onSuccessCalled = 0;
    mockDrain.mockReset();
  });

  const UNHEALTHY = [
    "base_missing",
    "base_invalid",
    "token_unavailable",
    "tenant_missing",
    "directory_constructor_error",
    "directory_request_error",
    "empty_authoritative_workforce",
  ] as const;

  it.each([...UNHEALTHY])("renders the localized notice for %s without raw diagnostics", (state) => {
    mockQueryResult.workConserving = {
      data: projection("blocked", state),
      isLoading: false,
      isError: false,
    };
    mockQueryResult.members = {
      data: [{ user_id: "user-1", role: "owner" }],
      isLoading: false,
      isError: false,
    };
    const { container } = renderPanel();

    const notice = screen.getByTestId("organization-source-notice");
    expect(notice).toHaveAttribute("data-organization-source-state", state);
    expect(enProjects.detail.work_conserving.organization_source.label[state]).toBeTruthy();
    expect(
      screen.getByText(enProjects.detail.work_conserving.organization_source.label[state]),
    ).toBeInTheDocument();
    expect(
      screen.getByText(enProjects.detail.work_conserving.organization_source.explanation[state]),
    ).toBeInTheDocument();
    // No raw error, URL, tenant value or credential material may leak: the
    // notice may name the classification (which includes the word
    // "credential" in its localized label) but must not carry URLs or
    // secret-shaped values anywhere in the panel.
    expect(notice.textContent).not.toMatch(/https?:\/\//i);
    expect(notice.textContent).not.toMatch(/(sk-|Bearer\s|keychain:|tenant[=:]\s*\S)/i);
    expect(container.textContent).not.toMatch(/https?:\/\//i);
  });

  it("healthy state renders no notice and never implies dispatch", () => {
    mockQueryResult.workConserving = {
      data: projection("ready", "healthy"),
      isLoading: false,
      isError: false,
    };
    mockQueryResult.members = {
      data: [{ user_id: "user-1", role: "owner" }],
      isLoading: false,
      isError: false,
    };
    renderPanel();

    expect(screen.queryByTestId("organization-source-notice")).toBeNull();
    // Drain remains gated on suggestions; healthy alone does not enable it.
    expect(screen.queryByRole("button", { name: /dispatch next action/i })).toBeInTheDocument();
  });

  it("a degraded query error renders the generic source-gap treatment with no specific state", () => {
    mockQueryResult.workConserving = { isLoading: false, isError: true };
    mockQueryResult.members = {
      data: [{ user_id: "user-1", role: "owner" }],
      isLoading: false,
      isError: false,
    };
    renderPanel();

    expect(screen.queryByTestId("organization-source-notice")).toBeNull();
    expect(screen.getByText(/source gap/i)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /dispatch next action/i })).toBeNull();
  });

  it("hides the drain action on a source-gap projection even with an unhealthy organization state", () => {
    mockQueryResult.workConserving = {
      data: projection("source_gap", "tenant_missing"),
      isLoading: false,
      isError: false,
    };
    mockQueryResult.members = {
      data: [{ user_id: "user-1", role: "owner" }],
      isLoading: false,
      isError: false,
    };
    renderPanel();

    expect(screen.getByTestId("organization-source-notice")).toHaveAttribute(
      "data-organization-source-state",
      "tenant_missing",
    );
    expect(screen.queryByRole("button", { name: /dispatch next action/i })).toBeNull();
  });
});

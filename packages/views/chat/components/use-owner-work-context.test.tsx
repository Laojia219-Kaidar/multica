// @vitest-environment jsdom

import type { ReactNode } from "react";
import { act, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setApiInstance } from "@multica/core/api";
import type { ApiClient } from "@multica/core/api/client";
import type { CompanyOpsOwnerWorkContext } from "@multica/core/types";
import {
  ownerWorkContextKeys,
  useOwnerWorkContext,
} from "./use-owner-work-context";

const WS_ID = "workspace-1";
const AGENT_ID = "d34db33f-4ef7-4fe1-a32d-8f24c57b07b1";
const OTHER_AGENT_ID = "92a74f2a-1d15-46b2-a839-d1f1277ce2b9";
const SESSION_ID = "01972f7e-7e8d-77ef-a13d-1b0ce3e9c001";
const COMMAND_ID = "44444444-4444-4444-8444-444444444444";
const ISSUE_ID = "55555555-5555-4555-8555-555555555555";
const TASK_ID = "66666666-6666-4666-8666-666666666666";
const REQUEST = {
  work_order_source_ref:
    "hive://hivecosm/delivery/project/PRJ-HIVECREW-P2/work-order/WO-P2-001",
  employee_id: "employee-01JOWNER",
  identity_binding_id: "binding-01JOWNER",
  agent_id: AGENT_ID,
  session_id: SESSION_ID,
};

const authority = (kind: string, sourceRef: string) => ({
  kind,
  source_ref: sourceRef,
  revision: `${kind}-revision-1`,
  content_digest: `${kind}-digest-1`,
  freshness: "current",
});

const RESOLVED_CONTEXT: CompanyOpsOwnerWorkContext = {
  schema_version: "hivecrew.owner-work-context.v1",
  request: REQUEST,
  work_order: authority("WorkOrder", REQUEST.work_order_source_ref),
  employee: {
    employee_id: REQUEST.employee_id,
    authority: authority(
      "Employee",
      `hivecosm://employees/${REQUEST.employee_id}`,
    ),
  },
  identity_binding: {
    identity_binding_id: REQUEST.identity_binding_id,
    employee_ref: `hivecosm://employees/${REQUEST.employee_id}`,
    agent_ref: `/api/agents/${AGENT_ID}`,
    active: true,
    authority: authority(
      "IdentityBinding",
      `hivecosm://identity-bindings/${REQUEST.identity_binding_id}`,
    ),
  },
  agent: {
    id: AGENT_ID,
    name: "Atlas",
    status: "idle",
    runtime_mode: "local",
    model: "gpt-5.6",
    authority: authority("Agent", `/api/agents/${AGENT_ID}`),
  },
  session: { id: SESSION_ID, agent_id: AGENT_ID, status: "active" },
  issue: null,
  projection_state: "not_projected",
  observed_at: "2026-08-11T10:00:00Z",
};

function searchParams(overrides: Record<string, string> = {}) {
  return new URLSearchParams({ ...REQUEST, ...overrides });
}

function wrapper(queryClient: QueryClient) {
  return function Wrapper({ children }: { children: ReactNode }) {
    return (
      <QueryClientProvider client={queryClient}>
        {children}
      </QueryClientProvider>
    );
  };
}

describe("useOwnerWorkContext", () => {
  let queryClient: QueryClient;
  let getCompanyOpsWorkContext: ReturnType<typeof vi.fn>;
  let createCompanyOpsAssignment: ReturnType<typeof vi.fn>;
  let reviewCompanyOpsArtifact: ReturnType<typeof vi.fn>;
  let promoteCompanyOpsArtifact: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    });
    getCompanyOpsWorkContext = vi.fn().mockResolvedValue(RESOLVED_CONTEXT);
    createCompanyOpsAssignment = vi.fn().mockResolvedValue({
      schema_version: "hivecrew.assignment-dispatch.v1",
      command_id: COMMAND_ID,
      issue_id: ISSUE_ID,
      initial_task_id: TASK_ID,
      execution_receipt: { state: "awaiting_claim", task_id: TASK_ID },
    });
    reviewCompanyOpsArtifact = vi.fn().mockResolvedValue({
      schema_version: "hivecrew.artifact-review.v1",
      review_id: COMMAND_ID,
      event_id: "77777777-7777-4777-8777-777777777777",
      sequence: 2,
      decision: "changes_requested",
      candidate_id: TASK_ID,
      rework_task_id: "88888888-8888-4888-8888-888888888888",
    });
    promoteCompanyOpsArtifact = vi.fn().mockResolvedValue({
      schema_version: "hivecrew.formal-artifact-promotion.v1",
      promotion_id: COMMAND_ID,
      candidate_id: TASK_ID,
      lifecycle_status: "promotion_requested",
      formal_visible: false,
      write_performed: false,
      event_id: "77777777-7777-4777-8777-777777777777",
      sequence: 3,
    });
    setApiInstance({
      getCompanyOpsWorkContext,
      createCompanyOpsAssignment,
      reviewCompanyOpsArtifact,
      promoteCompanyOpsArtifact,
    } as unknown as ApiClient);
    vi.stubGlobal("crypto", { randomUUID: vi.fn(() => COMMAND_ID) });
  });

  afterEach(() => {
    queryClient.clear();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("loads the exact authority tuple under a workspace-scoped query key", async () => {
    const { result } = renderHook(
      () =>
        useOwnerWorkContext({
          wsId: WS_ID,
          pathname: "/acme/chat",
          searchParams: searchParams(),
          session: { id: SESSION_ID, agent_id: AGENT_ID },
          sessionsLoaded: true,
        }),
      { wrapper: wrapper(queryClient) },
    );

    await waitFor(() => expect(result.current?.context.state).toBe("ready"));
    expect(getCompanyOpsWorkContext).toHaveBeenCalledWith(REQUEST);
    expect(
      queryClient.getQueryData(ownerWorkContextKeys.detail(WS_ID, REQUEST)),
    ).toEqual(RESOLVED_CONTEXT);
  });

  it("does not read authority when the current session Agent conflicts with the URL", () => {
    const { result } = renderHook(
      () =>
        useOwnerWorkContext({
          wsId: WS_ID,
          pathname: "/acme/chat",
          searchParams: searchParams(),
          session: { id: SESSION_ID, agent_id: OTHER_AGENT_ID },
          sessionsLoaded: true,
        }),
      { wrapper: wrapper(queryClient) },
    );

    expect(result.current?.context).toMatchObject({ state: "conflict" });
    expect(getCompanyOpsWorkContext).not.toHaveBeenCalled();
  });

  it("keeps the context loading until the requested session list has settled", () => {
    const { result } = renderHook(
      () =>
        useOwnerWorkContext({
          wsId: WS_ID,
          pathname: "/acme/chat",
          searchParams: searchParams(),
          session: null,
          sessionsLoaded: false,
        }),
      { wrapper: wrapper(queryClient) },
    );

    expect(result.current?.context).toMatchObject({ state: "loading" });
    expect(getCompanyOpsWorkContext).not.toHaveBeenCalled();
  });

  it("fails closed when the authority response changes an exact selector", async () => {
    getCompanyOpsWorkContext.mockResolvedValue({
      ...RESOLVED_CONTEXT,
      request: { ...REQUEST, employee_id: "employee-other" },
    });
    const { result } = renderHook(
      () =>
        useOwnerWorkContext({
          wsId: WS_ID,
          pathname: "/acme/chat",
          searchParams: searchParams(),
          session: { id: SESSION_ID, agent_id: AGENT_ID },
          sessionsLoaded: true,
        }),
      { wrapper: wrapper(queryClient) },
    );

    await waitFor(() => expect(result.current?.context.state).toBe("conflict"));
    expect(result.current?.context).toMatchObject({
      reason: expect.stringMatching(/employee_id.*exact URL selector/i),
    });
  });

  it("reuses one command UUID when an unchanged assignment retries after failure", async () => {
    createCompanyOpsAssignment
      .mockRejectedValueOnce(new TypeError("network unavailable"))
      .mockResolvedValueOnce({
        schema_version: "hivecrew.assignment-dispatch.v1",
        command_id: COMMAND_ID,
        issue_id: ISSUE_ID,
        initial_task_id: TASK_ID,
        execution_receipt: { state: "awaiting_claim", task_id: TASK_ID },
      });
    const { result } = renderHook(
      () =>
        useOwnerWorkContext({
          wsId: WS_ID,
          pathname: "/acme/chat",
          searchParams: searchParams(),
          session: { id: SESSION_ID, agent_id: AGENT_ID },
          sessionsLoaded: true,
        }),
      { wrapper: wrapper(queryClient) },
    );
    await waitFor(() => expect(result.current?.context.state).toBe("ready"));
    const command = { ...REQUEST, handoff_note: "Run the bounded P2 slice." };
    const projectedContext: CompanyOpsOwnerWorkContext = {
      ...RESOLVED_CONTEXT,
      issue: {
        id: ISSUE_ID,
        number: 71,
        title: "WO-P2-001",
        status: "todo",
        assignee_id: AGENT_ID,
      },
      projection_state: "projected",
    };
    getCompanyOpsWorkContext.mockResolvedValueOnce(projectedContext);

    await expect(
      act(async () => result.current!.onConfirmAssignment(command)),
    ).rejects.toThrow("network unavailable");
    await expect(
      act(async () => result.current!.onConfirmAssignment(command)),
    ).resolves.toMatchObject({ command_id: COMMAND_ID, issue_id: ISSUE_ID });

    expect(createCompanyOpsAssignment).toHaveBeenCalledTimes(2);
    expect(createCompanyOpsAssignment.mock.calls[0]?.[0].command_id).toBe(
      COMMAND_ID,
    );
    expect(createCompanyOpsAssignment.mock.calls[1]?.[0].command_id).toBe(
      COMMAND_ID,
    );
    expect(globalThis.crypto.randomUUID).toHaveBeenCalledTimes(1);
    expect(getCompanyOpsWorkContext).toHaveBeenCalledTimes(2);
    expect(
      queryClient.getQueryData(ownerWorkContextKeys.detail(WS_ID, REQUEST)),
    ).toEqual(projectedContext);
  });

  it("writes an exact artifact review and reuses its UUID on network retry", async () => {
    const withArtifact: CompanyOpsOwnerWorkContext = {
      ...RESOLVED_CONTEXT,
      issue: {
        id: ISSUE_ID,
        number: 72,
        title: "WO-P2-001",
        status: "in_progress",
        assignee_id: AGENT_ID,
      },
      projection_state: "projected",
      outcome: {
        command_id: COMMAND_ID,
        issue_id: ISSUE_ID,
        initial_task_id: TASK_ID,
        current_task_id: TASK_ID,
        execution_state: "completed",
        artifact: {
          id: TASK_ID,
          revision: 1,
          durable_object_ref: "/uploads/artifact.md",
          digest: "sha256:artifact",
          status: "submitted",
          formal_visible: false,
        },
      },
    };
    getCompanyOpsWorkContext.mockResolvedValue(withArtifact);
    reviewCompanyOpsArtifact
      .mockRejectedValueOnce(new TypeError("network unavailable"))
      .mockResolvedValueOnce({
        schema_version: "hivecrew.artifact-review.v1",
        review_id: COMMAND_ID,
        event_id: "77777777-7777-4777-8777-777777777777",
        sequence: 2,
        decision: "changes_requested",
        candidate_id: TASK_ID,
        rework_task_id: "88888888-8888-4888-8888-888888888888",
      });
    const { result } = renderHook(
      () =>
        useOwnerWorkContext({
          wsId: WS_ID,
          pathname: "/acme/chat",
          searchParams: searchParams(),
          session: { id: SESSION_ID, agent_id: AGENT_ID },
          sessionsLoaded: true,
        }),
      { wrapper: wrapper(queryClient) },
    );
    await waitFor(() => expect(result.current?.context.state).toBe("ready"));
    const review = {
      ...REQUEST,
      candidate_id: TASK_ID,
      decision: "changes_requested" as const,
      feedback: "补充浏览器验收证据",
    };
    await expect(
      act(async () => result.current!.onReviewArtifact(review)),
    ).rejects.toThrow("network unavailable");
    await expect(
      act(async () => result.current!.onReviewArtifact(review)),
    ).resolves.toMatchObject({ review_id: COMMAND_ID, candidate_id: TASK_ID });
    expect(reviewCompanyOpsArtifact).toHaveBeenCalledTimes(2);
    expect(reviewCompanyOpsArtifact.mock.calls[0]?.[0].review_id).toBe(COMMAND_ID);
    expect(reviewCompanyOpsArtifact.mock.calls[1]?.[0].review_id).toBe(COMMAND_ID);
  });

  it("promotes an approved artifact and reuses its UUID on network retry", async () => {
    const withApprovedArtifact: CompanyOpsOwnerWorkContext = {
      ...RESOLVED_CONTEXT,
      issue: {
        id: ISSUE_ID,
        number: 73,
        title: "WO-P2-001",
        status: "in_progress",
        assignee_id: AGENT_ID,
      },
      projection_state: "projected",
      outcome: {
        command_id: COMMAND_ID,
        issue_id: ISSUE_ID,
        initial_task_id: TASK_ID,
        current_task_id: TASK_ID,
        execution_state: "completed",
        artifact: {
          id: TASK_ID,
          revision: 1,
          durable_object_ref: "/uploads/artifact.md",
          digest: "sha256:artifact",
          status: "approved",
          formal_visible: false,
        },
      },
    };
    getCompanyOpsWorkContext.mockResolvedValue(withApprovedArtifact);
    promoteCompanyOpsArtifact
      .mockRejectedValueOnce(new TypeError("network unavailable"))
      .mockResolvedValueOnce({
        schema_version: "hivecrew.formal-artifact-promotion.v1",
        promotion_id: COMMAND_ID,
        candidate_id: TASK_ID,
        lifecycle_status: "promotion_requested",
        formal_visible: false,
        write_performed: false,
        event_id: "77777777-7777-4777-8777-777777777777",
        sequence: 3,
      });
    const { result } = renderHook(
      () =>
        useOwnerWorkContext({
          wsId: WS_ID,
          pathname: "/acme/chat",
          searchParams: searchParams(),
          session: { id: SESSION_ID, agent_id: AGENT_ID },
          sessionsLoaded: true,
        }),
      { wrapper: wrapper(queryClient) },
    );
    await waitFor(() => expect(result.current?.context.state).toBe("ready"));
    const promotion = {
      ...REQUEST,
      candidate_id: TASK_ID,
    };
    await expect(
      act(async () => result.current!.onPromoteArtifact(promotion)),
    ).rejects.toThrow("network unavailable");
    await expect(
      act(async () => result.current!.onPromoteArtifact(promotion)),
    ).resolves.toMatchObject({ promotion_id: COMMAND_ID, candidate_id: TASK_ID });
    expect(promoteCompanyOpsArtifact).toHaveBeenCalledTimes(2);
    expect(promoteCompanyOpsArtifact.mock.calls[0]?.[0].promotion_id).toBe(COMMAND_ID);
    expect(promoteCompanyOpsArtifact.mock.calls[1]?.[0].promotion_id).toBe(COMMAND_ID);
    expect(globalThis.crypto.randomUUID).toHaveBeenCalledTimes(1);
  });

  it("refreshes the exact work context after a mutation success", async () => {
    const withApprovedArtifact: CompanyOpsOwnerWorkContext = {
      ...RESOLVED_CONTEXT,
      issue: {
        id: ISSUE_ID,
        number: 73,
        title: "WO-P2-001",
        status: "in_progress",
        assignee_id: AGENT_ID,
      },
      projection_state: "projected",
      outcome: {
        command_id: COMMAND_ID,
        issue_id: ISSUE_ID,
        initial_task_id: TASK_ID,
        current_task_id: TASK_ID,
        execution_state: "completed",
        artifact: {
          id: TASK_ID,
          revision: 1,
          durable_object_ref: "/uploads/artifact.md",
          digest: "sha256:artifact",
          status: "approved",
          formal_visible: false,
        },
      },
    };
    getCompanyOpsWorkContext.mockResolvedValue(withApprovedArtifact);
    const { result } = renderHook(
      () =>
        useOwnerWorkContext({
          wsId: WS_ID,
          pathname: "/acme/chat",
          searchParams: searchParams(),
          session: { id: SESSION_ID, agent_id: AGENT_ID },
          sessionsLoaded: true,
        }),
      { wrapper: wrapper(queryClient) },
    );
    await waitFor(() => expect(result.current?.context.state).toBe("ready"));

    const withPromoted: CompanyOpsOwnerWorkContext = {
      ...withApprovedArtifact,
      outcome: {
        ...withApprovedArtifact.outcome!,
        artifact: {
          ...withApprovedArtifact.outcome!.artifact!,
          status: "promotion_requested",
        },
      },
    };
    getCompanyOpsWorkContext.mockResolvedValueOnce(withPromoted);

    await act(async () => {
      await result.current!.onPromoteArtifact({ ...REQUEST, candidate_id: TASK_ID });
    });

    expect(getCompanyOpsWorkContext.mock.calls.length).toBeGreaterThanOrEqual(2);
    expect(
      queryClient.getQueryData(ownerWorkContextKeys.detail(WS_ID, REQUEST)),
    ).toEqual(withPromoted);
  });

  it("refreshes the exact work context after a POST-success/readback-fail mutation error", async () => {
    // Scenario: the POST reaches the backend and records promotion_succeeded
    // on the artifact, but the HTTP response returns an error because the
    // readback step failed. The hook's onError must invalidate the exact
    // context so the UI observes the persisted promotion_succeeded outcome.
    const withApprovedArtifact: CompanyOpsOwnerWorkContext = {
      ...RESOLVED_CONTEXT,
      issue: {
        id: ISSUE_ID,
        number: 73,
        title: "WO-P2-001",
        status: "in_progress",
        assignee_id: AGENT_ID,
      },
      projection_state: "projected",
      outcome: {
        command_id: COMMAND_ID,
        issue_id: ISSUE_ID,
        initial_task_id: TASK_ID,
        current_task_id: TASK_ID,
        execution_state: "completed",
        artifact: {
          id: TASK_ID,
          revision: 1,
          durable_object_ref: "/uploads/artifact.md",
          digest: "sha256:artifact",
          status: "approved",
          formal_visible: false,
        },
      },
    };
    getCompanyOpsWorkContext.mockResolvedValue(withApprovedArtifact);
    const { result } = renderHook(
      () =>
        useOwnerWorkContext({
          wsId: WS_ID,
          pathname: "/acme/chat",
          searchParams: searchParams(),
          session: { id: SESSION_ID, agent_id: AGENT_ID },
          sessionsLoaded: true,
        }),
      { wrapper: wrapper(queryClient) },
    );
    await waitFor(() => expect(result.current?.context.state).toBe("ready"));

    const withPromoted: CompanyOpsOwnerWorkContext = {
      ...withApprovedArtifact,
      outcome: {
        ...withApprovedArtifact.outcome!,
        artifact: {
          ...withApprovedArtifact.outcome!.artifact!,
          status: "promotion_succeeded",
        },
      },
    };
    promoteCompanyOpsArtifact.mockRejectedValueOnce(
      new TypeError("readback failed"),
    );
    getCompanyOpsWorkContext.mockResolvedValueOnce(withPromoted);

    await expect(
      act(async () =>
        result.current!.onPromoteArtifact({ ...REQUEST, candidate_id: TASK_ID }),
      ),
    ).rejects.toThrow("readback failed");

    // The onError handler invalidated the exact context query; the refetch
    // must now expose the persisted promotion_succeeded outcome.
    await waitFor(() => {
      expect(
        queryClient.getQueryData(ownerWorkContextKeys.detail(WS_ID, REQUEST)),
      ).toEqual(withPromoted);
    });
    expect(getCompanyOpsWorkContext.mock.calls.length).toBeGreaterThanOrEqual(2);

    const firstPromotionId = promoteCompanyOpsArtifact.mock.calls[0]?.[0]?.promotion_id;
    const withConfirmed: CompanyOpsOwnerWorkContext = {
      ...withPromoted,
      outcome: {
        ...withPromoted.outcome!,
        artifact: {
          ...withPromoted.outcome!.artifact!,
          status: "authority_readback_confirmed",
          formal_visible: true,
          formal_artifact_ref:
            "hive://hivecosm/delivery/project/PRJ-HIVECREW-P2/work-order/WO-P2-001/formal-artifact/FA-001",
        },
      },
    };
    promoteCompanyOpsArtifact.mockResolvedValueOnce({
      schema_version: "hivecrew.formal-artifact-promotion.v1",
      promotion_id: firstPromotionId,
      candidate_id: TASK_ID,
      lifecycle_status: "authority_readback_confirmed",
      formal_visible: true,
      formal_artifact_ref:
        "hive://hivecosm/delivery/project/PRJ-HIVECREW-P2/work-order/WO-P2-001/formal-artifact/FA-001",
      write_performed: false,
      event_id: "77777777-7777-4777-8777-777777777777",
      sequence: 4,
    });
    getCompanyOpsWorkContext.mockResolvedValueOnce(withConfirmed);

    await act(async () => {
      await result.current!.onPromoteArtifact({
        ...REQUEST,
        candidate_id: TASK_ID,
      });
    });

    expect(promoteCompanyOpsArtifact).toHaveBeenCalledTimes(2);
    expect(promoteCompanyOpsArtifact.mock.calls[1]?.[0]?.promotion_id).toBe(
      firstPromotionId,
    );
    await waitFor(() => {
      expect(
        queryClient.getQueryData(ownerWorkContextKeys.detail(WS_ID, REQUEST)),
      ).toEqual(withConfirmed);
    });
  });

  it("rejects a promotion receipt whose promotion_id does not match the command", async () => {
    const withApprovedArtifact: CompanyOpsOwnerWorkContext = {
      ...RESOLVED_CONTEXT,
      issue: {
        id: ISSUE_ID,
        number: 73,
        title: "WO-P2-001",
        status: "in_progress",
        assignee_id: AGENT_ID,
      },
      projection_state: "projected",
      outcome: {
        command_id: COMMAND_ID,
        issue_id: ISSUE_ID,
        initial_task_id: TASK_ID,
        current_task_id: TASK_ID,
        execution_state: "completed",
        artifact: {
          id: TASK_ID,
          revision: 1,
          durable_object_ref: "/uploads/artifact.md",
          digest: "sha256:artifact",
          status: "approved",
          formal_visible: false,
        },
      },
    };
    getCompanyOpsWorkContext.mockResolvedValue(withApprovedArtifact);
    promoteCompanyOpsArtifact.mockResolvedValueOnce({
      schema_version: "hivecrew.formal-artifact-promotion.v1",
      promotion_id: "99999999-9999-4999-8999-999999999999",
      candidate_id: TASK_ID,
      lifecycle_status: "promotion_requested",
      formal_visible: false,
      write_performed: false,
      event_id: "77777777-7777-4777-8777-777777777777",
      sequence: 3,
    });
    const { result } = renderHook(
      () =>
        useOwnerWorkContext({
          wsId: WS_ID,
          pathname: "/acme/chat",
          searchParams: searchParams(),
          session: { id: SESSION_ID, agent_id: AGENT_ID },
          sessionsLoaded: true,
        }),
      { wrapper: wrapper(queryClient) },
    );
    await waitFor(() => expect(result.current?.context.state).toBe("ready"));

    await expect(
      act(async () =>
        result.current!.onPromoteArtifact({ ...REQUEST, candidate_id: TASK_ID }),
      ),
    ).rejects.toThrow("Artifact promotion receipt does not match the exact command.");
  });
});

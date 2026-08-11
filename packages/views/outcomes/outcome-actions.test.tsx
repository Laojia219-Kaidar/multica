// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { setApiInstance } from "@multica/core/api";
import type { ApiClient } from "@multica/core/api/client";
import type { ReactNode } from "react";
import type { CompanyOpsOutcomeSummary } from "@multica/core/types";
import {
  isOutcomeFormal,
  isOutcomePromotable,
  outcomeCandidateId,
  outcomeCandidatePreviewRef,
  outcomeSelectors,
  useOutcomeActions,
} from "./outcome-actions";

const agentId = "d34db33f-4ef7-4fe1-a32d-8f24c57b07b1";
const commandId = "44444444-4444-4444-8444-444444444444";
const sessionId = "01972f7e-7e8d-77ef-a13d-1b0ce3e9c001";

const baseSummary: CompanyOpsOutcomeSummary = {
  id: commandId,
  issue: null,
  work_order: {
    source_ref:
      "hive://hivecosm/delivery/project/PRJ-HIVECREW-P2/work-order/WO-P2-001",
    revision: "work-order-revision-1",
    digest: "work-order-digest-1",
  },
  employee: {
    source_ref: "hivecosm://employees/employee-01JOWNER",
    id: "employee-01JOWNER",
  },
  identity_binding: {
    source_ref: "hivecosm://identity-bindings/binding-01JOWNER",
    id: "binding-01JOWNER",
  },
  execution_target: {
    local_agent_id: agentId,
    agent_ref: `/api/agents/${agentId}`,
    agent_revision: "agent-revision-1",
    agent_digest: "agent-digest-1",
  },
  current_agent_display: {
    name: "Atlas",
    model: "gpt-5.6",
    status: "idle",
  },
  initial_task_id: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
  current_task_id: "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
  execution_state: "completed",
  active_artifact: {
    id: "66666666-6666-4666-8666-666666666666",
    revision: 2,
    durable_object_ref: "hive://hivecosm/artifacts/66666666-6666-4666-8666-666666666666",
    digest: "sha256:abc123",
    content_type: "application/json",
    status: "approved",
    formal_visible: false,
  },
  version_count: 2,
  latest_event_at: "2026-08-11T10:00:00Z",
};

const mockReview = vi.hoisted(() => vi.fn());
const mockPromote = vi.hoisted(() => vi.fn());

function renderOutcomeActions() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
  );
  return renderHook(() => useOutcomeActions("ws-1", commandId), { wrapper });
}

beforeEach(() => {
  vi.clearAllMocks();
  mockReview.mockImplementation((command: { review_id: string }) =>
    Promise.resolve({
      schema_version: "hivecrew.artifact-review.v1",
      review_id: command.review_id,
      event_id: "event-1",
      sequence: 2,
      decision: "approved",
      candidate_id: baseSummary.active_artifact!.id,
    }),
  );
  mockPromote.mockImplementation((command: { promotion_id: string }) =>
    Promise.resolve({
      schema_version: "hivecrew.formal-artifact-promotion.v1",
      promotion_id: command.promotion_id,
      candidate_id: baseSummary.active_artifact!.id,
      lifecycle_status: "promotion_requested",
      formal_visible: false,
      write_performed: false,
      event_id: "event-2",
      sequence: 3,
    }),
  );
  setApiInstance({
    reviewCompanyOpsArtifact: mockReview,
    promoteCompanyOpsArtifact: mockPromote,
  } as unknown as ApiClient);
});

describe("outcomeSelectors / outcomeCandidateId / isOutcomePromotable / isOutcomeFormal", () => {
  it("builds the exact WorkOrder/Employee/Binding/ExecutionTarget selectors with the explicit session", () => {
    expect(outcomeSelectors(baseSummary, sessionId)).toEqual({
      work_order_source_ref: baseSummary.work_order.source_ref,
      employee_id: baseSummary.employee.id,
      identity_binding_id: baseSummary.identity_binding.id,
      agent_id: baseSummary.execution_target.local_agent_id,
      session_id: sessionId,
    });
  });

  it("returns the active candidate artifact id", () => {
    expect(outcomeCandidateId(baseSummary)).toBe(baseSummary.active_artifact!.id);
    expect(
      outcomeCandidateId({ ...baseSummary, active_artifact: undefined }),
    ).toBeNull();
  });

  it.each([
    ["approved", true],
    ["promotion_failed", true],
    ["promotion_succeeded", true],
    ["submitted", false],
    ["changes_requested", false],
    ["promotion_requested", false],
  ] as const)("promotable gate is %s for status %s", (status, expected) => {
    expect(
      isOutcomePromotable({
        ...baseSummary,
        active_artifact: { ...baseSummary.active_artifact!, status },
      }),
    ).toBe(expected);
  });

  it("isOutcomeFormal only when all three formal conditions hold at once", () => {
    const confirmed = {
      ...baseSummary,
      active_artifact: {
        ...baseSummary.active_artifact!,
        status: "authority_readback_confirmed" as const,
        formal_visible: true,
        formal_artifact_ref: "hivecosm://formal-artifacts/FA-001",
      },
    };
    expect(isOutcomeFormal(confirmed)).toBe(true);
    expect(
      isOutcomeFormal({
        ...confirmed,
        active_artifact: { ...confirmed.active_artifact!, status: "approved" },
      }),
    ).toBe(false);
    expect(
      isOutcomeFormal({
        ...confirmed,
        active_artifact: { ...confirmed.active_artifact!, formal_visible: false },
      }),
    ).toBe(false);
    expect(
      isOutcomeFormal({
        ...confirmed,
        active_artifact: { ...confirmed.active_artifact!, formal_artifact_ref: undefined },
      }),
    ).toBe(false);
    expect(isOutcomeFormal({ ...baseSummary, active_artifact: undefined })).toBe(false);
  });
});

describe("outcomeCandidatePreviewRef", () => {
  const wsId = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa";
  const candidateId = baseSummary.active_artifact!.id;
  const digestHex = "a".repeat(64);
  const validRef = `/uploads/workspaces/${wsId}/artifact-candidates/${candidateId}/${digestHex}`;
  const previewSummary: CompanyOpsOutcomeSummary = {
    ...baseSummary,
    active_artifact: {
      ...baseSummary.active_artifact!,
      durable_object_ref: validRef,
      digest: `sha256:${digestHex}`,
    },
  };

  it("returns the exact same-origin candidate object ref", () => {
    expect(outcomeCandidatePreviewRef(previewSummary, wsId)).toBe(validRef);
  });

  it.each([
    ["cross workspace", validRef.replace(wsId, "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")],
    ["wrong candidate", validRef.replace(candidateId, "77777777-7777-4777-8777-777777777777")],
    ["wrong digest", `${validRef.slice(0, -64)}${"b".repeat(64)}`],
    ["absolute URL", `https://evil.invalid${validRef}`],
    ["javascript URL", "javascript:alert(1)"],
    ["path traversal", `${validRef}/../secret`],
    ["query", `${validRef}?download=1`],
    ["fragment", `${validRef}#preview`],
    ["encoded traversal", validRef.replace("/artifact-candidates/", "/artifact-candidates/%2e%2e/")],
    ["encoded separator", validRef.replace(candidateId, `${candidateId}%2fescape`)],
  ])("rejects %s", (_name, durableObjectRef) => {
    expect(
      outcomeCandidatePreviewRef(
        {
          ...previewSummary,
          active_artifact: {
            ...previewSummary.active_artifact!,
            durable_object_ref: durableObjectRef,
          },
        },
        wsId,
      ),
    ).toBeNull();
  });
});

describe("useOutcomeActions", () => {
  it("reuses a stable review UUID across a retry of the same command", async () => {
    let calls = 0;
    mockReview.mockImplementation((command: { review_id: string }) => {
      calls += 1;
      if (calls === 1) return Promise.reject(new Error("network"));
      return Promise.resolve({
        schema_version: "hivecrew.artifact-review.v1",
        review_id: command.review_id,
        event_id: "event-1",
        sequence: 2,
        decision: "approved",
        candidate_id: baseSummary.active_artifact!.id,
      });
    });
    const { result } = renderOutcomeActions();

    await expect(
      result.current.onReviewArtifact({
        summary: baseSummary,
        sessionId: sessionId,
        decision: "approved",
        feedback: "",
      }),
    ).rejects.toThrow("network");
    await result.current.onReviewArtifact({
      summary: baseSummary,
      sessionId: sessionId,
      decision: "approved",
      feedback: "",
    });

    const callsList = mockReview.mock.calls.map((c) => c[0] as { review_id: string });
    expect(callsList).toHaveLength(2);
    expect(callsList[0]!.review_id).toBe(callsList[1]!.review_id);
    expect(callsList[0]!.review_id).toMatch(/^[0-9a-f-]{36}$/);
  });

  it("rejects a review when the outcome has no artifact candidate", async () => {
    const { result } = renderOutcomeActions();
    await expect(
      result.current.onReviewArtifact({
        summary: { ...baseSummary, active_artifact: undefined },
        sessionId: sessionId,
        decision: "approved",
        feedback: "",
      }),
    ).rejects.toThrow("no active artifact candidate");
    expect(mockReview).not.toHaveBeenCalled();
  });

  it("rejects a promotion when the artifact is not in a promotable state", async () => {
    const { result } = renderOutcomeActions();
    await expect(
      result.current.onPromoteArtifact({
        summary: { ...baseSummary, active_artifact: { ...baseSummary.active_artifact!, status: "submitted" } },
        sessionId: sessionId,
      }),
    ).rejects.toThrow("not in a promotable state");
    expect(mockPromote).not.toHaveBeenCalled();
  });

  it("reuses a stable promotion UUID across a retry and verifies the receipt", async () => {
    let calls = 0;
    mockPromote.mockImplementation((command: { promotion_id: string }) => {
      calls += 1;
      if (calls === 1) return Promise.reject(new Error("network"));
      return Promise.resolve({
        schema_version: "hivecrew.formal-artifact-promotion.v1",
        promotion_id: command.promotion_id,
        candidate_id: baseSummary.active_artifact!.id,
        lifecycle_status: "promotion_requested",
        formal_visible: false,
        write_performed: false,
        event_id: "event-2",
        sequence: 3,
      });
    });
    const { result } = renderOutcomeActions();

    await expect(
      result.current.onPromoteArtifact({ summary: baseSummary, sessionId: sessionId }),
    ).rejects.toThrow("network");
    await result.current.onPromoteArtifact({ summary: baseSummary, sessionId: sessionId });

    const callsList = mockPromote.mock.calls.map((c) => c[0] as { promotion_id: string });
    expect(callsList).toHaveLength(2);
    expect(callsList[0]!.promotion_id).toBe(callsList[1]!.promotion_id);
  });

  it("throws when the promotion receipt does not echo the exact command", async () => {
    mockPromote.mockResolvedValue({
      schema_version: "hivecrew.formal-artifact-promotion.v1",
      promotion_id: "promo-stable",
      candidate_id: "11111111-1111-4111-8111-111111111111",
      lifecycle_status: "promotion_requested",
      formal_visible: false,
      write_performed: false,
      event_id: "event-2",
      sequence: 3,
    });
    const { result } = renderOutcomeActions();
    await expect(
      result.current.onPromoteArtifact({ summary: baseSummary, sessionId: sessionId }),
    ).rejects.toThrow("does not match the exact command");
  });

  it("invalidates the detail cache after a successful review", async () => {
    const { result } = renderOutcomeActions();
    await result.current.onReviewArtifact({
      summary: baseSummary,
      sessionId: sessionId,
      decision: "approved",
      feedback: "",
    });
    await waitFor(() => expect(mockReview).toHaveBeenCalledTimes(1));
  });
});

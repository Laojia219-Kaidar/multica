// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { setApiInstance, ApiError } from "@multica/core/api";
import type { ApiClient } from "@multica/core/api/client";
import { renderWithI18n } from "../test/i18n";
import {
  NavigationProvider,
  type NavigationAdapter,
} from "../navigation";
import { OutcomesPage } from "./outcomes-page";

const agentId = "d34db33f-4ef7-4fe1-a32d-8f24c57b07b1";
const commandId = "44444444-4444-4444-8444-444444444444";
const sessionId = "01972f7e-7e8d-77ef-a13d-1b0ce3e9c001";
const otherSessionId = "a1b2c3d4-0000-4000-8000-000000000009";


const summary = {
  id: commandId,
  issue: {
    id: "55555555-5555-4555-8555-555555555555",
    number: 42,
    identifier: "MUL-42",
    title: "Deliver bounded P2 slice",
    status: "in_review",
    project_id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  },
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

const detail = {
  schema_version: "hivecrew.outcome-center.v1" as const,
  summary,
  versions: [
    {
      id: "77777777-7777-4777-8777-777777777777",
      revision: 1,
      durable_object_ref: "hive://hivecosm/artifacts/77777777-7777-4777-8777-777777777777",
      digest: "sha256:first",
      content_type: "text/markdown",
    },
  ],
  events: [
    {
      id: "88888888-8888-4888-8888-888888888888",
      sequence: 1,
      type: "submitted",
      candidate_id: "77777777-7777-4777-8777-777777777777",
      candidate_revision: 1,
    },
  ],
  runs: [
    {
      task_id: "99999999-9999-4999-8999-999999999999",
      status: "completed",
      completed_at: "2026-08-11T10:00:00Z",
    },
  ],
};

const mockList = vi.hoisted(() => vi.fn());
const mockDetail = vi.hoisted(() => vi.fn());
const mockSessionList = vi.hoisted(() => vi.fn());
const mockReview = vi.hoisted(() => vi.fn());
const mockPromote = vi.hoisted(() => vi.fn());
const compactRef = vi.hoisted(() => ({ current: false }));

vi.mock("react-resizable-panels", () => ({
  useDefaultLayout: () => ({
    defaultLayout: undefined,
    onLayoutChanged: vi.fn(),
  }),
  usePanelRef: () => ({ current: { isCollapsed: () => false, collapse: vi.fn(), expand: vi.fn() } }),
}));

vi.mock("@multica/ui/components/ui/resizable", () => ({
  ResizablePanelGroup: ({ children }: { children: React.ReactNode }) => (
    <div>{children}</div>
  ),
  ResizablePanel: ({ children }: { children: React.ReactNode }) => (
    <div>{children}</div>
  ),
  ResizableHandle: () => null,
}));

vi.mock("@multica/ui/hooks/use-mobile", () => ({
  useIsMobile: () => false,
}));

vi.mock("./use-outcomes-compact", () => ({
  useOutcomesCompact: () => compactRef.current,
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

vi.mock("./outcome-actions", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./outcome-actions")>();
  return {
    ...actual,
    useOutcomeActions: () => ({
      onReviewArtifact: (input: {
        summary: typeof summary;
        sessionId: string;
        decision: string;
        feedback: string;
      }) =>
        mockReview({
          review_id: "review-1",
          candidate_id: input.summary.active_artifact?.id ?? undefined,
          session_id: input.sessionId,
          agent_id: input.summary.execution_target.local_agent_id,
          work_order_source_ref: input.summary.work_order.source_ref,
          employee_id: input.summary.employee.id,
          identity_binding_id: input.summary.identity_binding.id,
          decision: input.decision,
          feedback: input.feedback,
        }),
      onPromoteArtifact: (input: {
        summary: typeof summary;
        sessionId: string;
      }) =>
        mockPromote({
          promotion_id: "promo-1",
          candidate_id: input.summary.active_artifact?.id ?? undefined,
          session_id: input.sessionId,
          agent_id: input.summary.execution_target.local_agent_id,
          work_order_source_ref: input.summary.work_order.source_ref,
          employee_id: input.summary.employee.id,
          identity_binding_id: input.summary.identity_binding.id,
        }),
      reviewPending: false,
      promotionPending: false,
    }),
  };
});

vi.mock("@multica/core/paths", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/paths")>();
  return {
    ...actual,
    useWorkspacePaths: () => ({
      outcomes: () => "/acme/outcomes",
      issueDetail: (id: string) => `/acme/issues/${id}`,
      projectDetail: (id: string) => `/acme/projects/${id}`,
      chat: () => "/acme/chat",
    }),
  };
});

vi.mock("@multica/core/chat/queries", () => ({
  chatSessionsOptions: () => ({
    queryKey: ["chat", "ws-1", "sessions"],
    queryFn: () => mockSessionList(),
    staleTime: Infinity,
  }),
}));

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

function renderPage(searchParams: URLSearchParams) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  let view: ReturnType<typeof renderWithI18n> | null = null;
  let navigation: NavigationAdapter = {
    push: vi.fn(),
    replace: vi.fn(),
    back: vi.fn(),
    pathname: "/acme/outcomes",
    searchParams,
    getShareableUrl: (path) => path,
  };
  // Simulate the router: a URL write replaces the adapter with a NEW
  // searchParams-bearing object and re-renders the page (the real router
  // commits the new location and Next / react-router re-render the route).
  // This is what makes click-driven URL transitions observable in the DOM.
  const replace = vi.fn((path: string) => {
    const url = new URL(path, "https://hivecrew.invalid");
    searchParams = new URLSearchParams(url.search);
    navigation = { ...navigation, searchParams };
    view?.rerender(
      <QueryClientProvider client={queryClient}>
        <NavigationProvider value={navigation}>
          <OutcomesPage />
        </NavigationProvider>
      </QueryClientProvider>,
    );
  });
  navigation = { ...navigation, replace };

  view = renderWithI18n(
    <QueryClientProvider client={queryClient}>
      <NavigationProvider value={navigation}>
        <OutcomesPage />
      </NavigationProvider>
    </QueryClientProvider>,
  );
  return { replace, ...view };
}

beforeEach(() => {
  vi.clearAllMocks();
  compactRef.current = false;
  mockList.mockResolvedValue({
    schema_version: "hivecrew.outcome-center.v1",
    items: [summary],
    total: 1,
    limit: 50,
    offset: 0,
  });
  mockDetail.mockResolvedValue(detail);
  mockSessionList.mockResolvedValue([
    { id: sessionId, agent_id: agentId, status: "active", title: "Atlas chat", creator_id: "user-1", workspace_id: "ws-1", has_unread: false, created_at: "2026-08-11T00:00:00Z", updated_at: "2026-08-11T10:00:00Z" },
    { id: otherSessionId, agent_id: "92a74f2a-1d15-46b2-a839-d1f1277ce2b9", status: "active", title: "Other agent", creator_id: "user-1", workspace_id: "ws-1", has_unread: false, created_at: "2026-08-11T00:00:00Z", updated_at: "2026-08-11T10:00:00Z" },
  ]);
  mockReview.mockResolvedValue({
    schema_version: "hivecrew.artifact-review.v1",
    review_id: "review-1",
    event_id: "event-1",
    sequence: 2,
    decision: "approved",
    candidate_id: summary.active_artifact!.id,
  });
  mockPromote.mockRejectedValue(new Error("not promotable"));
  setApiInstance({
    listCompanyOpsOutcomes: mockList,
    getCompanyOpsOutcome: mockDetail,
    listChatSessions: mockSessionList,
    reviewCompanyOpsArtifact: mockReview,
    promoteCompanyOpsArtifact: mockPromote,
  } as unknown as ApiClient);
});

describe("OutcomesPage", () => {
  it("requests the real list API and renders summary rows", async () => {
    renderPage(new URLSearchParams());
    expect(await screen.findByText("MUL-42: Deliver bounded P2 slice")).toBeInTheDocument();
    expect(mockList).toHaveBeenCalledWith(
      expect.objectContaining({ limit: 50, offset: 0 }),
    );
    expect(screen.queryByText("Select an outcome to view its details")).toBeInTheDocument();
  });

  it("deep-links an outcome query param into the detail request", async () => {
    renderPage(new URLSearchParams({ outcome: commandId }));
    expect(mockDetail).toHaveBeenCalledWith(commandId);
    expect(await screen.findByText("#42")).toBeInTheDocument();
    expect(screen.getByText(summary.issue.project_id!)).toBeInTheDocument();
    expect(screen.getByText("Atlas")).toBeInTheDocument();
  });

  it("shows an explicit not-found for an invalid/deleted outcome id", async () => {
    mockDetail.mockRejectedValue(
      new ApiError("missing", 404, "Not Found"),
    );
    renderPage(new URLSearchParams({ outcome: commandId }));
    expect(await screen.findByText("Outcome not found")).toBeInTheDocument();
    // The detail must not silently render the first list row's content.
    expect(screen.queryByText(summary.issue.project_id!)).not.toBeInTheDocument();
  });

  it("shows the loading skeleton while the detail is pending", async () => {
    let release: (v: typeof detail) => void = () => {};
    mockDetail.mockImplementation(
      () => new Promise<typeof detail>((resolve) => { release = resolve; }),
    );
    renderPage(new URLSearchParams({ outcome: commandId }));
    expect(await screen.findByText("Loading outcomes…")).toBeInTheDocument();
    release(detail);
    await waitFor(() => expect(screen.getByText("#42")).toBeInTheDocument());
  });

  it("shows an error state when the list request fails", async () => {
    mockList.mockRejectedValue(new Error("boom"));
    renderPage(new URLSearchParams());
    expect(
      await screen.findByText("The outcome service is unavailable. Try again in a moment."),
    ).toBeInTheDocument();
  });

  it("writes search and status filters into the URL and re-requests the API", async () => {
    const { replace } = renderPage(new URLSearchParams());
    await screen.findByText("MUL-42: Deliver bounded P2 slice");
    const search = await screen.findByLabelText("Search outcomes");
    await userEvent.type(search, "bounded");
    await waitFor(() =>
      expect(replace).toHaveBeenCalledWith("/acme/outcomes?q=bounded"),
    );
    // The URL write re-requests the list for the new q; wait for the settled
    // list before interacting with the status filter.
    await screen.findByText("MUL-42: Deliver bounded P2 slice");
    const status = await screen.findByLabelText("Status");
    await userEvent.selectOptions(status, "completed");
    // The status write preserves the already-committed q filter (real router
    // behavior: both params accumulate in the URL).
    expect(replace).toHaveBeenCalledWith("/acme/outcomes?q=bounded&status=completed");
  });

it("enables review actions only after an explicit qualified session is selected", async () => {
    renderPage(new URLSearchParams({ outcome: commandId }));
    await screen.findByText("#42");

    // Actions are gated until a session is explicitly selected.
    const sessionGate = await screen.findByText(
      "Review and promotion actions require an explicitly selected session.",
    );
    expect(screen.queryByRole("button", { name: "Approve" })).not.toBeInTheDocument();
    expect(sessionGate).toBeInTheDocument();

    const select = await screen.findByLabelText("Select a session");
    await userEvent.selectOptions(select, sessionId);
    expect(await screen.findByRole("button", { name: "Approve" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Request rework" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Promote" })).toBeInTheDocument();
  });

  it("does not offer sessions bound to a different agent", async () => {
    renderPage(new URLSearchParams({ outcome: commandId }));
    await screen.findByText("#42");

    const select = await screen.findByLabelText("Select a session");
    const options = within(select).getAllByRole("option");
    const labels = options.map((o) => o.textContent);
    expect(labels).toContain("Atlas chat");
    expect(labels).not.toContain("Other agent");
  });

  it("keeps review feedback when a review write fails", async () => {
    mockReview.mockRejectedValue(new Error("authority unavailable"));
    renderPage(new URLSearchParams({ outcome: commandId, session_id: sessionId }));
    await screen.findByText("#42");

    const feedback = await screen.findByLabelText("Feedback");
    await userEvent.type(feedback, "请在浏览器补充验收证据");
    await userEvent.click(screen.getByRole("button", { name: "Request rework" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Review failed. Your feedback was kept.",
    );
    expect(screen.getByLabelText("Feedback")).toHaveValue("请在浏览器补充验收证据");
  });

  it("surfaces a promotion failure and keeps the outcome re-readable", async () => {
    renderPage(new URLSearchParams({ outcome: commandId, session_id: sessionId }));
    await screen.findByText("#42");
    await userEvent.click(await screen.findByRole("button", { name: "Promote" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Promotion failed. Your action was kept for retry.",
    );
    expect(mockPromote).toHaveBeenCalledWith(
      expect.objectContaining({
candidate_id: summary.active_artifact!.id,
        session_id: sessionId,
        agent_id: agentId,
        work_order_source_ref: summary.work_order.source_ref,
        employee_id: summary.employee.id,
        identity_binding_id: summary.identity_binding.id,
      }),
    );
  });

  it("shows the open-conversation action when no qualified session exists", async () => {
    mockSessionList.mockResolvedValue([]);
    renderPage(new URLSearchParams({ outcome: commandId }));
    await screen.findByText("#42");
    expect(await screen.findByText("No active conversation")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Open or start a conversation" })).toHaveAttribute(
      "href",
      "/acme/chat?agent=" + agentId,
    );
  });

  it("regression: normal list without an outcome keeps the detail select prompt", async () => {
    const { replace } = renderPage(new URLSearchParams());
    await screen.findByText("MUL-42: Deliver bounded P2 slice");
    expect(
      screen.getByText("Select an outcome to view its details"),
    ).toBeInTheDocument();
    expect(replace).not.toHaveBeenCalled();
  });

  it("shows formal status only when all three formal conditions hold", async () => {
    const formalArtifact = {
      ...summary,
      active_artifact: {
        ...summary.active_artifact!,
        status: "authority_readback_confirmed",
        formal_visible: true,
        formal_artifact_ref: "hivecosm://formal-artifacts/FA-001",
      },
    };
    mockDetail.mockResolvedValue({ ...detail, summary: formalArtifact });
    renderPage(new URLSearchParams({ outcome: commandId }));
    await screen.findByText("Formal reference: hivecosm://formal-artifacts/FA-001");
    // The formal badge renders in the detail (the status filter <option> also
    // carries the same label, so scope to the badge element).
    const badges = screen.getAllByText("Formal readback confirmed");
    expect(badges.some((b) => b.tagName === "SPAN")).toBe(true);
  });

  it("does not show formal status when the artifact is only approved", async () => {
    renderPage(new URLSearchParams({ outcome: commandId }));
    await screen.findByText("#42");
    const badges = screen.getAllByText("Formal readback confirmed");
    expect(badges.every((b) => b.tagName === "OPTION")).toBe(true);
    // "Not yet formal" appears in the header and the formal section (both
    // badges) — either way, no readback-confirmed badge is rendered.
    expect(screen.getAllByText("Not yet formal").length).toBeGreaterThan(0);
  });
});

describe("OutcomesPage compact (≤720px single column)", () => {
  beforeEach(() => {
    compactRef.current = true;
  });

  it("shows only the list on initial narrow-screen load — no detail", async () => {
    renderPage(new URLSearchParams());
    await screen.findByText("MUL-42: Deliver bounded P2 slice");
    // Compact list-only: the desktop empty-state detail prompt is absent, and
    // the row is present. The detail issue link (which renders as a single
    // "#42 Deliver bounded P2 slice" element) must not be there.
    expect(
      screen.queryByText("Select an outcome to view its details"),
    ).not.toBeInTheDocument();
    expect(screen.queryByText("#42 Deliver bounded P2 slice")).not.toBeInTheDocument();
    expect(screen.getByLabelText("Search outcomes")).toBeInTheDocument();
  });

  it("clicking an outcome shows only the detail and keeps the exact outcome URL", async () => {
    const { replace } = renderPage(new URLSearchParams());
    await screen.findByText("MUL-42: Deliver bounded P2 slice");

    await userEvent.click(screen.getByText("MUL-42: Deliver bounded P2 slice"));

    expect(replace).toHaveBeenCalledWith(`/acme/outcomes?outcome=${commandId}`);
    // Detail-only: the row list is gone, detail content is present, the back
    // button is present, and the URL kept the exact canonical command UUID.
    expect(
      await screen.findByText("#42 Deliver bounded P2 slice"),
    ).toBeInTheDocument();
    expect(screen.queryByText("Search outcomes")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Back to outcomes" })).toBeInTheDocument();
  });

  it("clicking back clears outcome and restores the list", async () => {
    const { replace } = renderPage(
      new URLSearchParams({ outcome: commandId }),
    );
    await screen.findByText("#42 Deliver bounded P2 slice");

    await userEvent.click(
      screen.getByRole("button", { name: "Back to outcomes" }),
    );

    expect(replace).toHaveBeenCalledWith("/acme/outcomes");
    expect(await screen.findByText("MUL-42: Deliver bounded P2 slice")).toBeInTheDocument();
    expect(screen.queryByText("#42 Deliver bounded P2 slice")).not.toBeInTheDocument();
    expect(
      screen.queryByText("Select an outcome to view its details"),
    ).not.toBeInTheDocument();
  });

  it("desktop two-panel still renders the detail select prompt on list-only load", async () => {
    compactRef.current = false;
    renderPage(new URLSearchParams());
    await screen.findByText("MUL-42: Deliver bounded P2 slice");
    // Desktop keeps the empty-state detail panel (select prompt) next to the
    // list — the compact single-column branch must not leak into it.
    expect(
      screen.getByText("Select an outcome to view its details"),
    ).toBeInTheDocument();
  });
});

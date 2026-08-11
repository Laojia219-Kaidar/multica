// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { setApiInstance } from "@multica/core/api";
import type { ApiClient } from "@multica/core/api/client";
import { I18nProvider } from "@multica/core/i18n/react";
import { buildHiveCrewWorkContextUrl } from "@multica/core/paths";
import enCommon from "../locales/en/common.json";
import enChat from "../locales/en/chat.json";
import {
  NavigationProvider,
  type NavigationAdapter,
} from "../navigation";
import type { OwnerWorkContextBinding } from "./components/use-owner-work-context";

const FIXTURE = vi.hoisted(() => ({
  workspaceSlug: "acme",
	sessionId: "01972f7e-7e8d-77ef-a13d-1b0ce3e9c001",
  employeeId: "employee-01JOWNER",
  bindingId: "binding-01JOWNER",
  agentId: "d34db33f-4ef7-4fe1-a32d-8f24c57b07b1",
  otherAgentId: "92a74f2a-1d15-46b2-a839-d1f1277ce2b9",
	sourceRef:
    "hive://hivecosm/delivery/project/PRJ-HIVECREW-P2/work-order/WO-P2-001",
}));

const sessionAgentIdRef = vi.hoisted(() => ({ current: FIXTURE.agentId }));
const storeRef = vi.hoisted(() => ({
  current: { activeSessionId: FIXTURE.sessionId as string | null },
}));
const mockSetActiveSession = vi.hoisted(() => vi.fn());
const mockConfirmOwnerAssignment = vi.hoisted(() =>
	vi.fn().mockResolvedValue({
		command_id: "44444444-4444-4444-8444-444444444444",
		issue_id: "55555555-5555-4555-8555-555555555555",
		initial_task_id: "66666666-6666-4666-8666-666666666666",
		execution_receipt: {
      state: "awaiting_claim" as const,
      task_id: "66666666-6666-4666-8666-666666666666",
    },
		schema_version: "hivecrew.assignment-dispatch.v1" as const,
	}),
);
const mockGetCompanyOpsWorkContext = vi.hoisted(() => vi.fn());
const ownerWorkContextOverrideRef = vi.hoisted(() => ({
  current: null as OwnerWorkContextBinding | null,
}));
const mockUseOwnerWorkContext = vi.hoisted(() => vi.fn());

vi.mock("./components/chat-message-list", () => ({
  ChatMessageList: () => <div data-testid="chat-message-list">chat-message-list</div>,
  ChatMessageSkeleton: () => <div>chat-message-skeleton</div>,
}));
vi.mock("./components/chat-input", () => ({
  ChatInput: () => <div>chat-input</div>,
}));
vi.mock("./components/chat-thread-list", () => ({
  ChatThreadList: () => <div>chat-thread-list</div>,
}));
vi.mock("./components/chat-session-header", () => ({
  ChatSessionHeader: () => (
    <div data-testid="chat-session-header">chat-session-header</div>
  ),
}));
vi.mock("./components/chat-empty-state", () => ({
  EmptyState: () => <div>chat-empty-state</div>,
}));
vi.mock("./components/new-chat-button", () => ({
  NewChatButton: () => <div>new-chat-button</div>,
}));
vi.mock("./components/offline-banner", () => ({
  OfflineBanner: () => null,
}));
vi.mock("./components/no-agent-banner", () => ({
  NoAgentBanner: () => null,
}));
vi.mock("./components/archived-agent-banner", () => ({
  ArchivedAgentBanner: () => null,
}));
vi.mock("react-resizable-panels", () => ({
  useDefaultLayout: () => ({
    defaultLayout: undefined,
    onLayoutChanged: vi.fn(),
  }),
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
vi.mock("@multica/core/paths", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/paths")>();
  return {
    ...actual,
    useWorkspacePaths: () => ({ chat: () => "/acme/chat" }),
  };
});
vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));
vi.mock("@multica/core/chat", () => ({
  useChatStore: Object.assign(
    (selector?: (state: { activeSessionId: string | null }) => unknown) =>
      selector ? selector(storeRef.current) : storeRef.current,
    { getState: () => storeRef.current },
  ),
}));
vi.mock("./components/use-owner-work-context", async (importOriginal) => {
  const actual = await importOriginal<
    typeof import("./components/use-owner-work-context")
  >();
  mockUseOwnerWorkContext.mockImplementation(
    (options: Parameters<typeof actual.useOwnerWorkContext>[0]) =>
      ownerWorkContextOverrideRef.current ??
      actual.useOwnerWorkContext(options),
  );
  return {
    ...actual,
    useOwnerWorkContext: mockUseOwnerWorkContext,
  };
});
vi.mock("./components/use-chat-controller", () => ({
  useChatController: () => ({
    wsId: "ws-1",
    user: { id: "user-1" },
    agents: [{ id: FIXTURE.agentId, name: "Atlas" }],
    availableAgents: [{ id: FIXTURE.agentId, name: "Atlas" }],
    agentsSettled: true,
    sessionsLoaded: true,
    sessions: [
      { id: FIXTURE.sessionId, agent_id: sessionAgentIdRef.current },
    ],
    activeSessionId: storeRef.current.activeSessionId,
    selectedAgentId: FIXTURE.agentId,
    currentSession: {
      id: FIXTURE.sessionId,
      agent_id: sessionAgentIdRef.current,
    },
    isSessionArchived: false,
    isAgentArchived: false,
    activeAgent: { id: FIXTURE.agentId, name: "Atlas" },
    noAgent: false,
    availability: "online",
    messages: [{ id: "message-1", content: "Existing chat remains visible." }],
    pendingTask: null,
    pendingTaskId: null,
    showSkeleton: false,
    hasMessages: true,
    firstItemIndex: 0,
    hasOlderMessages: false,
    isFetchingOlderMessages: false,
    fetchOlderMessages: vi.fn(),
    restoreDraftRequest: null,
    handleRestoreDraftApplied: vi.fn(),
    focusInputRequest: 0,
    handleSend: vi.fn(),
    handleStop: vi.fn(),
    handleNewChat: vi.fn(),
    handleStartNewChat: vi.fn(),
    handleSelectSession: vi.fn(),
    advanceSelectionAfterArchive: vi.fn(),
    archiveSession: vi.fn(),
    setActiveSession: mockSetActiveSession,
    setSelectedAgentId: vi.fn(),
    uploadEnabled: true,
    projects: [],
    activeProjectId: null,
    projectContextUnsupported: false,
    handleProjectChange: vi.fn(),
    isProjectUpdating: false,
  }),
}));

import { ChatPage } from "./chat-page";

const TEST_RESOURCES = { en: { common: enCommon, chat: enChat } };

function completeWorkContextSearch(): URLSearchParams {
  const url = buildHiveCrewWorkContextUrl({
    workspaceSlug: FIXTURE.workspaceSlug,
    employee_id: FIXTURE.employeeId,
    identity_binding_id: FIXTURE.bindingId,
    agent_id: FIXTURE.agentId,
    work_order_source_ref: FIXTURE.sourceRef,
    session_id: FIXTURE.sessionId,
  });
  return new URL(url, "https://hivecrew.invalid").searchParams;
}

function renderPage(searchParams: URLSearchParams) {
  const replace = vi.fn();
  const navigation: NavigationAdapter = {
    push: vi.fn(),
    replace,
    back: vi.fn(),
    pathname: "/acme/chat",
    searchParams,
    getShareableUrl: (path) => path,
  };

  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  render(
    <QueryClientProvider client={queryClient}>
      <I18nProvider locale="en" resources={TEST_RESOURCES}>
        <NavigationProvider value={navigation}>
          <ChatPage />
        </NavigationProvider>
      </I18nProvider>
    </QueryClientProvider>,
  );

  return { replace };
}

beforeEach(() => {
  vi.clearAllMocks();
  storeRef.current = { activeSessionId: FIXTURE.sessionId };
  sessionAgentIdRef.current = FIXTURE.agentId;
  mockGetCompanyOpsWorkContext.mockReset();
  mockGetCompanyOpsWorkContext.mockRejectedValue(
    new Error("Owner work-context authority is unavailable."),
  );
  setApiInstance({
    getCompanyOpsWorkContext: mockGetCompanyOpsWorkContext,
    createCompanyOpsAssignment: vi.fn(),
  } as unknown as ApiClient);
  const authority = (kind: string, sourceRef: string) => ({
    kind,
    source_ref: sourceRef,
    revision: `${kind}-revision-1`,
    content_digest: `${kind}-digest-1`,
    freshness: "current",
  });
  ownerWorkContextOverrideRef.current = {
    contextKey: "ws-1:owner-context",
    context: {
      state: "ready",
      data: {
        schema_version: "hivecrew.owner-work-context.v1",
        request: {
          work_order_source_ref: FIXTURE.sourceRef,
          employee_id: FIXTURE.employeeId,
          identity_binding_id: FIXTURE.bindingId,
          agent_id: FIXTURE.agentId,
          session_id: FIXTURE.sessionId,
        },
        work_order: authority("WorkOrder", FIXTURE.sourceRef),
        employee: {
          employee_id: FIXTURE.employeeId,
          authority: authority(
            "Employee",
            `hivecosm://employees/${FIXTURE.employeeId}`,
          ),
        },
        identity_binding: {
          identity_binding_id: FIXTURE.bindingId,
          employee_ref: `hivecosm://employees/${FIXTURE.employeeId}`,
          agent_ref: `/api/agents/${FIXTURE.agentId}`,
          active: true,
          authority: authority(
            "IdentityBinding",
            `hivecosm://identity-bindings/${FIXTURE.bindingId}`,
          ),
        },
        agent: {
          id: FIXTURE.agentId,
          name: "Atlas",
          status: "idle",
          runtime_mode: "local",
          model: "gpt-5.6",
          authority: authority("Agent", `/api/agents/${FIXTURE.agentId}`),
        },
        session: {
          id: FIXTURE.sessionId,
          agent_id: FIXTURE.agentId,
          status: "active",
        },
        issue: null,
        projection_state: "not_projected",
        observed_at: "2026-08-11T10:00:00Z",
      },
    },
    onConfirmAssignment: mockConfirmOwnerAssignment,
    onReviewArtifact: vi.fn(),
    onPromoteArtifact: vi.fn(),
  };
});

describe("ChatPage owner work-context integration", () => {
  it("preserves the complete stable URL and renders its exact authority provenance", () => {
    const searchParams = completeWorkContextSearch();
    const originalSearch = searchParams.toString();
    const { replace } = renderPage(searchParams);

    expect(screen.getByLabelText("Owner work context")).toBeInTheDocument();
    expect(screen.getByText(FIXTURE.sourceRef)).toBeInTheDocument();
    expect(screen.getByText(FIXTURE.employeeId)).toBeInTheDocument();
    expect(screen.getByText(FIXTURE.bindingId)).toBeInTheDocument();
    expect(screen.getByText(FIXTURE.agentId)).toBeInTheDocument();
    expect(mockUseOwnerWorkContext).toHaveBeenCalledWith({
      wsId: "ws-1",
      pathname: "/acme/chat",
      searchParams,
      session: { id: FIXTURE.sessionId, agent_id: FIXTURE.agentId },
      sessionsLoaded: true,
    });
    expect(searchParams.toString()).toBe(originalSearch);
    expect(replace).not.toHaveBeenCalled();
  });

  it("fails closed when the stable work-context URL is incomplete", () => {
    ownerWorkContextOverrideRef.current = null;
    const searchParams = completeWorkContextSearch();
    searchParams.delete("identity_binding_id");
    renderPage(searchParams);

    expect(screen.getByRole("alert")).toHaveTextContent(
      /missing required identity_binding_id/i,
    );
    expect(screen.queryByRole("button", { name: "派给这名员工" })).not.toBeInTheDocument();
  });

  it("fails closed when stable URL parameters conflict", () => {
    ownerWorkContextOverrideRef.current = null;
    const searchParams = completeWorkContextSearch();
    searchParams.append("agent_id", FIXTURE.otherAgentId);
    renderPage(searchParams);

    expect(screen.getByRole("alert")).toHaveTextContent(
      /conflicting agent_id values/i,
    );
    expect(screen.queryByRole("button", { name: "派给这名员工" })).not.toBeInTheDocument();
  });

  it("disables assignment when the session Agent is not the exact URL Agent", () => {
    ownerWorkContextOverrideRef.current = null;
    sessionAgentIdRef.current = FIXTURE.otherAgentId;
    renderPage(completeWorkContextSearch());

    const alert = screen.getByRole("alert");
    expect(alert).toHaveTextContent(/session.*agent.*mismatch/i);
    expect(alert).toHaveTextContent(FIXTURE.agentId);
    expect(alert).toHaveTextContent(FIXTURE.otherAgentId);
    expect(screen.queryByRole("button", { name: "派给这名员工" })).not.toBeInTheDocument();
  });

  it("places the work-context card after the session header and before messages", () => {
    renderPage(completeWorkContextSearch());

    const header = screen.getByTestId("chat-session-header");
    const card = screen.getByLabelText("Owner work context");
    const messages = screen.getByTestId("chat-message-list");
    expect(header.compareDocumentPosition(card) & Node.DOCUMENT_POSITION_FOLLOWING)
      .toBeTruthy();
    expect(card.compareDocumentPosition(messages) & Node.DOCUMENT_POSITION_FOLLOWING)
      .toBeTruthy();
  });

  it("fails closed when authority and the assignment writer are not connected", async () => {
    ownerWorkContextOverrideRef.current = null;
    renderPage(completeWorkContextSearch());

    await waitFor(() =>
      expect(screen.getByRole("alert")).toHaveTextContent(
        "Owner work-context authority is unavailable.",
      ),
    );
    expect(screen.queryByRole("button", { name: "派给这名员工" })).not.toBeInTheDocument();
  });

  it("leaves the existing Chat surface unchanged without work-context params", () => {
    const { replace } = renderPage(
      new URLSearchParams({ session: FIXTURE.sessionId }),
    );

    expect(screen.queryByLabelText("Owner work context")).not.toBeInTheDocument();
    expect(screen.getByTestId("chat-session-header")).toBeInTheDocument();
    expect(screen.getByTestId("chat-message-list")).toBeInTheDocument();
    expect(screen.getByText("chat-input")).toBeInTheDocument();
    expect(mockUseOwnerWorkContext).not.toHaveBeenCalled();
    expect(replace).not.toHaveBeenCalled();
  });
});

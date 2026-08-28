// @vitest-environment jsdom
//
// HIV-1251: the World Library status line must render the honest bridge
// verdict. Pending and query errors fail closed to the same
// `source_available_runtime_unavailable` copy — a stale "connected" verdict
// must never survive a source failure. Only `state: "runtime_available"`
// from a successful read shows the connected copy.

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { setApiInstance } from "@multica/core/api";
import type { ApiClient } from "@multica/core/api/client";
import { WorldLibraryStatusLine } from "./world-library-status";

const mockWorldLibraryStatus = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

const UNAVAILABLE_COPY =
  "知识权威 World Library 运行时未接通（source_available_runtime_unavailable）";
const AVAILABLE_COPY = "知识权威 World Library 运行时已接通";
const PENDING_COPY = "权威状态检查中…";

function renderLine() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={queryClient}>
      <WorldLibraryStatusLine />
    </QueryClientProvider>,
  );
}

const unavailable = {
  authority: "World Library",
  source_ref: "noah-ark-4",
  local_role: "execution_projection",
  state: "source_available_runtime_unavailable",
  bridge: { configured: false, reachable: null },
} as const;

const available = {
  ...unavailable,
  state: "runtime_available",
  bridge: {
    configured: true,
    reachable: true,
    checked_at: "2026-08-28T10:00:00Z",
  },
} as const;

beforeEach(() => {
  vi.clearAllMocks();
  setApiInstance({
    worldLibraryStatus: mockWorldLibraryStatus,
  } as unknown as ApiClient);
});

describe("WorldLibraryStatusLine", () => {
  it("renders the honest unavailable verdict by default", async () => {
    mockWorldLibraryStatus.mockResolvedValue(unavailable);
    renderLine();
    expect(await screen.findByText(UNAVAILABLE_COPY)).toBeInTheDocument();
    expect(screen.queryByText(AVAILABLE_COPY)).not.toBeInTheDocument();
  });

  it("fails closed to the unavailable copy on query error", async () => {
    mockWorldLibraryStatus.mockRejectedValue(new Error("status failed"));
    renderLine();
    expect(await screen.findByText(UNAVAILABLE_COPY)).toBeInTheDocument();
    expect(screen.getByText("状态查询失败，按未接通处理")).toBeInTheDocument();
    expect(screen.queryByText(AVAILABLE_COPY)).not.toBeInTheDocument();
  });

  it("shows the pending copy while the verdict is in flight", () => {
    mockWorldLibraryStatus.mockReturnValue(new Promise(() => {}));
    renderLine();
    expect(screen.getByText(PENDING_COPY)).toBeInTheDocument();
    expect(screen.queryByText(UNAVAILABLE_COPY)).not.toBeInTheDocument();
  });

  it("shows the connected copy only for a successful runtime_available read", async () => {
    mockWorldLibraryStatus.mockResolvedValue(available);
    renderLine();
    expect(await screen.findByText(AVAILABLE_COPY)).toBeInTheDocument();
    expect(screen.queryByText(UNAVAILABLE_COPY)).not.toBeInTheDocument();
  });

  it("keeps the unavailable copy when a configured bridge fails its probe", async () => {
    mockWorldLibraryStatus.mockResolvedValue({
      ...unavailable,
      bridge: { configured: true, reachable: false, detail: "probe status 500" },
    });
    renderLine();
    expect(await screen.findByText(UNAVAILABLE_COPY)).toBeInTheDocument();
    expect(screen.getByText("已配置桥接地址但探测未通过")).toBeInTheDocument();
  });
});

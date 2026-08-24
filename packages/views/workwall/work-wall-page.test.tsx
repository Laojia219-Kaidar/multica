import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { WorkWallPage } from "./work-wall-page";
import type { EmployeeLiveActivityV1, A2Pane } from "@multica/core/api/workwall";

// Mock the API module
const mockWorkWallSnapshot = vi.fn(() => Promise.resolve<EmployeeLiveActivityV1[]>([]));
const mockGetA2WorkWallSnapshot = vi.fn(() => Promise.resolve<unknown>({}));
const mockUseWorkspaceId = vi.fn(() => "ws-1");

vi.mock("@multica/core/api", () => ({
  api: {
    workWallSnapshot: () => mockWorkWallSnapshot(),
    getA2WorkWallSnapshot: () => mockGetA2WorkWallSnapshot(),
  },
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => mockUseWorkspaceId(),
}));

function makeTestQueryClient() {
  return new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
}

function renderPage() {
  const client = makeTestQueryClient();
  render(
    <QueryClientProvider client={client}>
      <WorkWallPage />
    </QueryClientProvider>,
  );
}

const sampleEmployee: EmployeeLiveActivityV1 = {
  schema_version: "hivecrew.employee-live-activity.v1",
  workspace_id: "ws-1",
  employee_id: "emp-1",
  agent_id: "agt-1",
  display_name: "Pixel",
  presence_state: "working",
  work_stage: "coding",
  recent_events: [],
  source_refs: ["agent://agt-1"],
  observed_at: "2026-08-24T12:00:00Z",
  freshness_state: "fresh",
};

const samplePane: A2Pane = {
  schema_version: "hivecrew.workwall.a2-pane.v1",
  pane_id: "pane-1",
  employee_id: "emp-1",
  session_id: "pixel:0.1",
  kind: "terminal",
  display_name: "Pixel",
  presence_state: "working",
  work_stage: "coding",
  tail_text: "npm test\nPASS",
  observed_at: "2026-08-24T12:00:00Z",
  freshness_state: "fresh",
};

describe("WorkWallPage", () => {
  beforeEach(() => {
    mockWorkWallSnapshot.mockReset();
    mockGetA2WorkWallSnapshot.mockReset();
  });

  it("renders the work wall with employee data from both endpoints", async () => {
    mockWorkWallSnapshot.mockResolvedValue([sampleEmployee]);
    mockGetA2WorkWallSnapshot.mockResolvedValue({
      schema_version: "hivecrew.workwall.a2-snapshot.v1",
      workspace_id: "ws-1",
      cursor: "cur-1",
      observed_at: "2026-08-24T12:00:00Z",
      event_limit: 100,
      panes: [samplePane],
    });

    renderPage();

    // Employee card renders with joined pane
    const card = await screen.findByTestId("work-site-card");
    expect(card).toBeDefined();
    expect(screen.getByText("Pixel")).toBeDefined();
    // No error when both endpoints return valid data
    expect(screen.queryByTestId("a2-snapshot-error")).toBeNull();
  });

  it("gracefully degrades when A2 endpoint is not yet live (no fake data)", async () => {
    mockWorkWallSnapshot.mockResolvedValue([sampleEmployee]);
    mockGetA2WorkWallSnapshot.mockRejectedValue(new Error("404 Not Found"));

    renderPage();

    // Employee roster still renders
    expect(await screen.findByText("Pixel")).toBeDefined();
    // Error banner visible for debugging
    const error = await screen.findByTestId("a2-snapshot-error");
    expect(error).toBeDefined();
  });

  it("uses p-5 page padding (consistent with collection pages)", () => {
    mockWorkWallSnapshot.mockResolvedValue([]);
    mockGetA2WorkWallSnapshot.mockResolvedValue({
      schema_version: "hivecrew.workwall.a2-snapshot.v1",
      workspace_id: "ws-1",
      cursor: "cur-1",
      observed_at: "2026-08-24T12:00:00Z",
      event_limit: 100,
      panes: [],
    });

    const { container } = render(
      <QueryClientProvider client={makeTestQueryClient()}>
        <WorkWallPage />
      </QueryClientProvider>,
    );

    // The outer page wrapper has p-5
    const outer = container.firstElementChild;
    expect(outer?.className).toContain("p-5");
  });
});

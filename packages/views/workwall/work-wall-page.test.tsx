import { act, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { WorkWallPage } from "./work-wall-page";

const apiMock = vi.hoisted(() => ({
  workWallSnapshot: vi.fn(async () => []),
  listTerminalPresence: vi.fn(async () => []),
}));

vi.mock("@multica/core/api", () => ({ api: apiMock }));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/paths", () => ({ useWorkspaceSlug: () => "team/space" }));

class FakeEventSource {
  static instances: FakeEventSource[] = [];

  readonly listeners = new Map<string, EventListener[]>();
  closed = false;

  constructor(
    readonly url: string,
    readonly init?: EventSourceInit,
  ) {
    FakeEventSource.instances.push(this);
  }

  addEventListener(type: string, listener: EventListenerOrEventListenerObject) {
    const fn = typeof listener === "function" ? listener : listener.handleEvent.bind(listener);
    this.listeners.set(type, [...(this.listeners.get(type) ?? []), fn]);
  }

  emit(type: string, event: Event) {
    for (const listener of this.listeners.get(type) ?? []) listener(event);
  }

  close() {
    this.closed = true;
  }
}

const liveEmployee = {
  schema_version: "hivecrew.employee-live-activity.v1",
  workspace_id: "ws-1",
  employee_id: "emp-pixel",
  agent_id: "agent-pixel",
  display_name: "Pixel SSE",
  presence_state: "working",
  work_stage: "coding",
  recent_events: [],
  source_refs: ["agent://agent-pixel"],
  observed_at: "2026-08-22T12:00:00Z",
  freshness_state: "fresh",
};

describe("WorkWallPage SSE integration", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    apiMock.workWallSnapshot.mockClear();
    apiMock.listTerminalPresence.mockClear();
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("updates the existing Work Wall from a governed snapshot stream", async () => {
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const view = render(
      <QueryClientProvider client={client}>
        <WorkWallPage />
      </QueryClientProvider>,
    );

    await waitFor(() => expect(apiMock.workWallSnapshot).toHaveBeenCalledTimes(1));
    expect(screen.getByText("Terminal 现场")).toBeDefined();

    const source = FakeEventSource.instances[0];
    expect(source?.url).toBe("/api/work-wall/stream?workspace_slug=team%2Fspace");
    expect(source?.init).toEqual({ withCredentials: true });

    act(() => {
      source?.emit(
        "snapshot",
        new MessageEvent("snapshot", { data: JSON.stringify([liveEmployee]) }),
      );
    });

    expect(await screen.findByText("Pixel SSE")).toBeDefined();
    view.unmount();
    expect(source?.closed).toBe(true);
  });
});

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

describe("WorkWallPage SSE fallback polling", () => {
  function setup() {
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false, gcTime: Infinity, staleTime: 0 } },
    });
    const view = render(
      <QueryClientProvider client={client}>
        <WorkWallPage />
      </QueryClientProvider>,
    );
    const source = FakeEventSource.instances[0];
    return { client, view, source };
  }

  beforeEach(() => {
    FakeEventSource.instances = [];
    apiMock.workWallSnapshot.mockClear();
    apiMock.listTerminalPresence.mockClear();
    vi.stubGlobal("EventSource", FakeEventSource);
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.useRealTimers();
  });

  it("uses 5000ms snapshot polling while connecting (initial state)", async () => {
    const { source } = setup();
    expect(source).toBeDefined();

    await act(async () => {
      vi.advanceTimersToNextTimer();
    });

    const initialCallCount = apiMock.workWallSnapshot.mock.calls.length;
    expect(initialCallCount).toBeGreaterThanOrEqual(1);

    await act(async () => {
      vi.advanceTimersByTime(5000);
    });

    expect(apiMock.workWallSnapshot).toHaveBeenCalledTimes(initialCallCount + 1);
  });

  it("stops snapshot polling after SSE open", async () => {
    const { source } = setup();

    await act(async () => {
      vi.advanceTimersToNextTimer();
    });

    const beforeOpen = apiMock.workWallSnapshot.mock.calls.length;
    expect(beforeOpen).toBeGreaterThanOrEqual(1);

    act(() => {
      source?.emit("open", new Event("open"));
    });

    await act(async () => {
      vi.advanceTimersByTime(20000);
    });

    expect(apiMock.workWallSnapshot).toHaveBeenCalledTimes(beforeOpen);
  });

  it("restores 5000ms snapshot polling after an SSE transport error", async () => {
    const { source } = setup();

    act(() => {
      source?.emit("open", new Event("open"));
    });

    await act(async () => {
      vi.advanceTimersByTime(10000);
    });
    const afterOpen = apiMock.workWallSnapshot.mock.calls.length;

    act(() => {
      source?.emit("error", new Event("error"));
    });

    await act(async () => {
      vi.advanceTimersByTime(5000);
    });

    expect(apiMock.workWallSnapshot).toHaveBeenCalledTimes(afterOpen + 1);
  });

  it("restores 5000ms snapshot polling after a malformed snapshot frame", async () => {
    const { source } = setup();

    act(() => {
      source?.emit("open", new Event("open"));
    });

    await act(async () => {
      vi.advanceTimersByTime(10000);
    });
    const afterOpen = apiMock.workWallSnapshot.mock.calls.length;

    act(() => {
      source?.emit(
        "snapshot",
        new MessageEvent("snapshot", { data: "not-json" }),
      );
    });

    await act(async () => {
      vi.advanceTimersByTime(5000);
    });

    expect(apiMock.workWallSnapshot).toHaveBeenCalledTimes(afterOpen + 1);
  });

  it("closes the EventSource on unmount", () => {
    const { view, source } = setup();
    expect(source?.closed).toBe(false);
    view.unmount();
    expect(source?.closed).toBe(true);
  });
});

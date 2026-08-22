/**
 * @vitest-environment jsdom
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook } from "@testing-library/react";
import type { ReactNode } from "react";
import { describe, expect, it, vi, beforeAll, beforeEach, afterEach } from "vitest";
import type { WSClient } from "../api/ws-client";
import { defaultStorage } from "../platform/storage";
import { issueKeys } from "../issues/queries";
import { agentTaskSnapshotKeys, workspaceWorkingAgentsKeys } from "../agents/queries";
import { workspaceKeys } from "../workspace/queries";
import { projectKeys } from "../projects/queries";
import type { ProjectPipelineResponse } from "../projects/pipeline-types";
import type { ProjectLifecycleSnapshot } from "../types";
import {
  markWorkspaceDeletePending,
  unmarkWorkspaceDeletePending,
} from "../workspace/pending-delete";
import { useRealtimeSync, type RealtimeSyncStores } from "./use-realtime-sync";

vi.mock("../platform/workspace-storage", () => ({
  getCurrentWsId: () => "ws-1",
  getCurrentSlug: () => "test-ws",
  // Draft stores are now loaded transitively (storage-cleanup → register-all-drafts)
  // so their persist wiring must resolve against this mock.
  createWorkspaceAwareStorage: (adapter: unknown) => adapter,
  registerForWorkspaceRehydration: () => {},
}));

vi.mock("../paths", () => ({
  useHasOnboarded: () => true,
  resolvePostAuthDestination: () => "/",
}));

// Node 25 ships a partial `localStorage` shim under jsdom that's missing
// `clear`/`removeItem`; replace it with a real in-memory Storage so persist
// can round-trip values.
beforeAll(() => {
  if (typeof globalThis.localStorage?.clear !== "function") {
    const values = new Map<string, string>();
    const storage: Storage = {
      get length() { return values.size; },
      clear: () => values.clear(),
      getItem: (k) => values.get(k) ?? null,
      key: (i) => Array.from(values.keys())[i] ?? null,
      removeItem: (k) => { values.delete(k); },
      setItem: (k, v) => { values.set(k, v); },
    };
    Object.defineProperty(globalThis, "localStorage", { configurable: true, value: storage });
    Object.defineProperty(window, "localStorage", { configurable: true, value: storage });
  }
});

function createMockWs(): WSClient {
  return {
    on: vi.fn(() => () => {}),
    onAny: vi.fn(() => () => {}),
    onReconnect: vi.fn(() => () => {}),
  } as unknown as WSClient;
}

function createStores(): RealtimeSyncStores {
  return {
    authStore: Object.assign(() => ({}), {
      getState: () => ({ user: { id: "u1" } }),
      subscribe: () => () => {},
      setState: () => {},
      destroy: () => {},
    }),
  } as unknown as RealtimeSyncStores;
}

function createWrapper(qc: QueryClient) {
  // Named function (not arrow) so react/display-name lint rule passes —
  // anonymous render-fn components break that rule even in test files.
  return function Wrapper({ children }: { children: ReactNode }) {
    return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
  };
}

describe("useRealtimeSync — ws instance change", () => {
  let qc: QueryClient;
  let stores: RealtimeSyncStores;
  let invalidateSpy: ReturnType<typeof vi.spyOn>;

  beforeEach(() => {
    qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    stores = createStores();
    invalidateSpy = vi.spyOn(qc, "invalidateQueries");
  });

  it("skips invalidation on first non-null ws instance", () => {
    const ws = createMockWs();
    renderHook(() => useRealtimeSync(ws, stores), {
      wrapper: createWrapper(qc),
    });

    // The main effect calls invalidateQueries for its own setup, but the
    // ws-instance-change effect should NOT have fired invalidation.
    // The only invalidateQueries calls should come from the main effect's
    // event handlers, not from the instance-change effect.
    // We verify by checking that no call was made with workspaceKeys.list()
    // pattern from the instance-change path (it logs a specific message).
    // Simpler: count calls — first mount with a ws should not trigger the
    // workspace-scoped bulk invalidation.
    expect(invalidateSpy).not.toHaveBeenCalled();
  });

  it("does not invalidate when ws goes from instance to null", () => {
    const ws1 = createMockWs();
    const { rerender } = renderHook(
      ({ ws }) => useRealtimeSync(ws, stores),
      { initialProps: { ws: ws1 as WSClient | null }, wrapper: createWrapper(qc) },
    );

    invalidateSpy.mockClear();
    rerender({ ws: null });

    expect(invalidateSpy).not.toHaveBeenCalled();
  });

  it("invalidates exactly once when a new ws instance appears after null gap", () => {
    const ws1 = createMockWs();
    const { rerender } = renderHook(
      ({ ws }) => useRealtimeSync(ws, stores),
      { initialProps: { ws: ws1 as WSClient | null }, wrapper: createWrapper(qc) },
    );

    // Simulate workspace switch: ws -> null -> new ws
    invalidateSpy.mockClear();
    rerender({ ws: null });
    expect(invalidateSpy).not.toHaveBeenCalled();

    const ws2 = createMockWs();
    rerender({ ws: ws2 });

    // Should have called invalidateQueries for all workspace-scoped keys
    // (16 workspace-scoped [incl. property definitions] + 6 per-issue
    // prefixes + the workspace working-agents projection + 5 per-chat
    // prefixes + 1 workspaceKeys.list() + 1 cross-workspace inbox unread
    // summary = 30 calls)
    expect(invalidateSpy).toHaveBeenCalledTimes(30);
  });

  it("does not re-invalidate when rerendered with the same ws instance", () => {
    const ws1 = createMockWs();
    const { rerender } = renderHook(
      ({ ws }) => useRealtimeSync(ws, stores),
      { initialProps: { ws: ws1 as WSClient | null }, wrapper: createWrapper(qc) },
    );

    invalidateSpy.mockClear();
    // Rerender with same instance
    rerender({ ws: ws1 });

    expect(invalidateSpy).not.toHaveBeenCalled();
  });

  it("invalidates chat, pins, labels, and invitations queries on ws instance change", () => {
    const ws1 = createMockWs();
    const { rerender } = renderHook(
      ({ ws }) => useRealtimeSync(ws, stores),
      { initialProps: { ws: ws1 as WSClient | null }, wrapper: createWrapper(qc) },
    );

    invalidateSpy.mockClear();
    rerender({ ws: null });

    const ws2 = createMockWs();
    rerender({ ws: ws2 });

    const calls = invalidateSpy.mock.calls.map((call: [{ queryKey?: unknown }, ...unknown[]]) => call[0].queryKey);
    expect(calls).toContainEqual(["chat", "ws-1"]);
    expect(calls).toContainEqual(["labels", "ws-1"]);
    expect(calls).toContainEqual(["workspaces", "ws-1", "invitations"]);
  });

  it("invalidates per-issue caches (no wsId in key) on ws instance change", () => {
    // These keys are not under the ["issues", wsId] prefix, so they need
    // their own invalidation on recovery — otherwise events missed while
    // disconnected leave them stale forever (staleTime: Infinity, #3953).
    const ws1 = createMockWs();
    const { rerender } = renderHook(
      ({ ws }) => useRealtimeSync(ws, stores),
      { initialProps: { ws: ws1 as WSClient | null }, wrapper: createWrapper(qc) },
    );

    invalidateSpy.mockClear();
    rerender({ ws: null });

    const ws2 = createMockWs();
    rerender({ ws: ws2 });

    const calls = invalidateSpy.mock.calls.map((call: [{ queryKey?: unknown }, ...unknown[]]) => call[0].queryKey);
    expect(calls).toContainEqual(["issues", "timeline"]);
    expect(calls).toContainEqual(["issues", "reactions"]);
    expect(calls).toContainEqual(["issues", "subscribers"]);
    expect(calls).toContainEqual(["issues", "usage"]);
    expect(calls).toContainEqual(["issues", "attachments"]);
    expect(calls).toContainEqual(["issues", "tasks"]);
  });

  it("invalidates per-chat-session caches (no wsId in key) on ws instance change", () => {
    // These keys are not under the ["chat", wsId] prefix, so they need their
    // own recovery invalidation when reconnecting after missed chat/task events.
    const ws1 = createMockWs();
    const { rerender } = renderHook(
      ({ ws }) => useRealtimeSync(ws, stores),
      { initialProps: { ws: ws1 as WSClient | null }, wrapper: createWrapper(qc) },
    );

    invalidateSpy.mockClear();
    rerender({ ws: null });

    const ws2 = createMockWs();
    rerender({ ws: ws2 });

    const calls = invalidateSpy.mock.calls.map((call: [{ queryKey?: unknown }, ...unknown[]]) => call[0].queryKey);
    expect(calls).toContainEqual(["chat", "messages"]);
    expect(calls).toContainEqual(["chat", "messages-page"]);
    expect(calls).toContainEqual(["chat", "pending-task"]);
    expect(calls).toContainEqual(["task-messages"]);
  });
});

// Seed payloads for Project read models — just enough of the wire shape for
// setQueryData to register the query so a later isInvalidated check is
// meaningful (invalidateQueries only flips caches that exist).
function pipelineResponse(projectId: string): ProjectPipelineResponse {
  return {
    project_id: projectId,
    project_status: "in_progress",
    project_title: `Project ${projectId}`,
    updated_at: "2026-08-22T00:00:00Z",
    columns: {},
    issues: {},
    capability_flags: {
      cancel_task: false,
      rerun_issue: false,
      update_status: false,
      dispatch_preview: false,
      dispatch: false,
      project_start: false,
    },
  };
}

function lifecycleSnapshot(projectId: string): ProjectLifecycleSnapshot {
  return {
    project_id: projectId,
    status: "in_progress",
    health: "active_with_frontier",
    owner_decision_required: false,
    flags: [],
    lead_type: null,
    lead_id: null,
    frontier_issue_ids: [],
    frontier_tasks: [],
    active_task_count: 0,
    nonterminal_issue_count: 0,
    blocked_issue_count: 0,
    review_issue_count: 0,
    terminal_issue_count: 0,
    last_progress_at: null,
    next_action: "",
    outcome_confirmed: 0,
    outcome_total: 0,
    closure_ready: false,
    closure_blockers: [],
    duplicate_of_project_id: null,
    terminal_projection_inconsistent: false,
  };
}

describe("useRealtimeSync — Table server membership invalidation", () => {
  let qc: QueryClient;
  let stores: RealtimeSyncStores;

  beforeEach(() => {
    qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    stores = createStores();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("invalidates Table queries after a task lifecycle event", () => {
    vi.useFakeTimers();
    const ws = createMockWs();
    const invalidate = vi.spyOn(qc, "invalidateQueries");
    // Seed Project read models so the invalidation has a live cache to flip:
    // the pipeline board, the lifecycle detail, and another workspace's
    // pipeline to prove the scope stays pinned to the exact workspace.
    const pipelineKey = projectKeys.pipeline("ws-1", "proj-1");
    const lifecycleKey = projectKeys.lifecycleDetail("ws-1", "proj-1");
    const otherWsPipelineKey = projectKeys.pipeline("ws-2", "proj-1");
    qc.setQueryData(pipelineKey, pipelineResponse("proj-1"));
    qc.setQueryData(lifecycleKey, lifecycleSnapshot("proj-1"));
    qc.setQueryData(otherWsPipelineKey, pipelineResponse("proj-1"));
    renderHook(() => useRealtimeSync(ws, stores), {
      wrapper: createWrapper(qc),
    });
    const onAny = vi.mocked(ws.onAny).mock.calls[0]?.[0];
    expect(onAny).toBeDefined();

    onAny!({ type: "task:completed", payload: {} } as never);
    // Not yet — the 100ms debounce has not elapsed.
    expect(qc.getQueryState(pipelineKey)?.isInvalidated).toBe(false);
    vi.advanceTimersByTime(100);

    expect(invalidate).toHaveBeenCalledWith({
      queryKey: issueKeys.tableAll("ws-1"),
    });
    expect(invalidate).toHaveBeenCalledWith({
      queryKey: workspaceWorkingAgentsKeys.all("ws-1"),
    });
    // Project rollup: the lifecycle event invalidates projectKeys.all for
    // the exact workspace after the lifecycle-gated 100ms debounce, so
    // list / detail / pipeline / lifecycle / work-conserving refetch
    // server truth.
    expect(invalidate).toHaveBeenCalledWith({
      queryKey: projectKeys.all("ws-1"),
    });
    expect(qc.getQueryState(pipelineKey)?.isInvalidated).toBe(true);
    expect(qc.getQueryState(lifecycleKey)?.isInvalidated).toBe(true);
    // Exact workspace scope: another workspace's project cache stays valid.
    expect(qc.getQueryState(otherWsPipelineKey)?.isInvalidated).toBe(false);
  });

  it("does not invalidate Project queries from task:message streaming", () => {
    // task:message fires per streamed message during long runs and must stay
    // out of the task-prefix path — invalidating project read models there
    // would recreate the invalidation storm the exclusion exists to prevent.
    vi.useFakeTimers();
    const ws = createMockWs();
    const invalidate = vi.spyOn(qc, "invalidateQueries");
    const pipelineKey = projectKeys.pipeline("ws-1", "proj-1");
    qc.setQueryData(pipelineKey, pipelineResponse("proj-1"));
    renderHook(() => useRealtimeSync(ws, stores), {
      wrapper: createWrapper(qc),
    });
    const onAny = vi.mocked(ws.onAny).mock.calls[0]?.[0];
    expect(onAny).toBeDefined();

    onAny!({ type: "task:message", payload: {} } as never);
    vi.advanceTimersByTime(500);

    const calls = invalidate.mock.calls.map((call) => call[0]?.queryKey);
    expect(
      calls.some((key) => Array.isArray(key) && key[0] === "projects"),
    ).toBe(false);
    expect(qc.getQueryState(pipelineKey)?.isInvalidated).toBe(false);
  });

  it("does not invalidate Project queries from task:progress streaming", () => {
    // task:progress is streaming telemetry — it fires per progress report
    // during a long run and shares the `task:` prefix with the lifecycle
    // events, so the Project refresh must stay gated on the explicit
    // lifecycle set and never ride the generic prefix path.
    vi.useFakeTimers();
    const ws = createMockWs();
    const invalidate = vi.spyOn(qc, "invalidateQueries");
    const pipelineKey = projectKeys.pipeline("ws-1", "proj-1");
    qc.setQueryData(pipelineKey, pipelineResponse("proj-1"));
    renderHook(() => useRealtimeSync(ws, stores), {
      wrapper: createWrapper(qc),
    });
    const onAny = vi.mocked(ws.onAny).mock.calls[0]?.[0];
    expect(onAny).toBeDefined();

    for (let i = 0; i < 5; i += 1) {
      onAny!({ type: "task:progress", payload: {} } as never);
    }
    vi.advanceTimersByTime(500);

    const calls = invalidate.mock.calls.map((call) => call[0]?.queryKey);
    expect(
      calls.some((key) => Array.isArray(key) && key[0] === "projects"),
    ).toBe(false);
    expect(qc.getQueryState(pipelineKey)?.isInvalidated).toBe(false);
    // The pre-existing non-Project task-prefix behavior is preserved:
    // progress still refreshes the agent presence snapshot.
    expect(invalidate).toHaveBeenCalledWith({
      queryKey: agentTaskSnapshotKeys.list("ws-1"),
    });
  });

  it("coalesces a lifecycle burst into exactly one Project invalidation", () => {
    // Multiple qualifying lifecycle events inside one debounce window must
    // collapse to a single projectKeys.all(wsId) invalidation, and a
    // task:progress event mixed into the same burst must add nothing.
    vi.useFakeTimers();
    const ws = createMockWs();
    const invalidate = vi.spyOn(qc, "invalidateQueries");
    const pipelineKey = projectKeys.pipeline("ws-1", "proj-1");
    const otherWsPipelineKey = projectKeys.pipeline("ws-2", "proj-1");
    qc.setQueryData(pipelineKey, pipelineResponse("proj-1"));
    qc.setQueryData(otherWsPipelineKey, pipelineResponse("proj-1"));
    renderHook(() => useRealtimeSync(ws, stores), {
      wrapper: createWrapper(qc),
    });
    const onAny = vi.mocked(ws.onAny).mock.calls[0]?.[0];
    expect(onAny).toBeDefined();

    const lifecycleBurst = [
      "task:queued",
      "task:dispatch",
      "task:running",
      "task:waiting_local_directory",
      "task:completed",
      "task:failed",
    ];
    for (const type of lifecycleBurst) {
      onAny!({ type, payload: {} } as never);
    }
    // Streaming noise inside the same window must not add a Project hit.
    onAny!({ type: "task:progress", payload: {} } as never);
    vi.advanceTimersByTime(100);

    const projectInvalidations = () =>
      invalidate.mock.calls
        .map((call) => call[0]?.queryKey)
        .filter(
          (key) =>
            Array.isArray(key) &&
            key.length === 2 &&
            key[0] === "projects" &&
            key[1] === "ws-1",
        );
    expect(projectInvalidations()).toHaveLength(1);
    expect(qc.getQueryState(pipelineKey)?.isInvalidated).toBe(true);
    // Exact workspace scope holds across the burst: another workspace's
    // project cache stays valid.
    expect(qc.getQueryState(otherWsPipelineKey)?.isInvalidated).toBe(false);

    // A lifecycle event in a LATER window refreshes again — coalescing is
    // per window, not a one-shot suppression.
    onAny!({ type: "task:cancelled", payload: {} } as never);
    vi.advanceTimersByTime(100);
    expect(projectInvalidations()).toHaveLength(2);
  });

  it("invalidates Table queries after a property definition changes", () => {
    const ws = createMockWs();
    const invalidate = vi.spyOn(qc, "invalidateQueries");
    renderHook(() => useRealtimeSync(ws, stores), {
      wrapper: createWrapper(qc),
    });
    const propertyUpdated = vi
      .mocked(ws.on)
      .mock.calls.find(([event]) => event === "property:updated")?.[1];
    expect(propertyUpdated).toBeDefined();

    (propertyUpdated as (payload: unknown) => void)({});

    expect(invalidate).toHaveBeenCalledWith({
      queryKey: issueKeys.tableAll("ws-1"),
    });
  });
});

describe("useRealtimeSync — workspace:deleted self-initiated suppression", () => {
  let qc: QueryClient;
  let stores: RealtimeSyncStores;

  beforeEach(() => {
    qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    stores = createStores();
  });

  afterEach(() => {
    unmarkWorkspaceDeletePending("ws-2");
    window.localStorage.clear();
  });

  // getCurrentWsId is mocked to "ws-1" at module level, so deleting "ws-2"
  // never enters the relocate branch — these tests only exercise the
  // storage-cleanup path, which is the observable difference between a
  // handled and a suppressed event.
  const dispatchWorkspaceDeleted = (ws: WSClient, workspaceId: string) => {
    const call = vi
      .mocked(ws.on)
      .mock.calls.find(([event]) => event === "workspace:deleted");
    expect(call).toBeDefined();
    (call![1] as (p: unknown) => void)({ workspace_id: workspaceId });
  };

  it("ignores the event for a delete this client initiated", () => {
    const ws = createMockWs();
    renderHook(() => useRealtimeSync(ws, stores), {
      wrapper: createWrapper(qc),
    });
    qc.setQueryData(workspaceKeys.list(), [{ id: "ws-2", slug: "delete-me" }]);
    defaultStorage.setItem("multica_issue_draft:delete-me", "draft");

    markWorkspaceDeletePending("ws-2");
    dispatchWorkspaceDeleted(ws, "ws-2");

    // useDeleteWorkspace.onSuccess owns cleanup for self-initiated deletes;
    // the handler must not have touched storage.
    expect(defaultStorage.getItem("multica_issue_draft:delete-me")).toBe("draft");
  });

  it("still cleans up for a delete initiated elsewhere", () => {
    const ws = createMockWs();
    renderHook(() => useRealtimeSync(ws, stores), {
      wrapper: createWrapper(qc),
    });
    qc.setQueryData(workspaceKeys.list(), [{ id: "ws-2", slug: "delete-me" }]);
    defaultStorage.setItem("multica_issue_draft:delete-me", "draft");

    dispatchWorkspaceDeleted(ws, "ws-2");

    expect(defaultStorage.getItem("multica_issue_draft:delete-me")).toBeNull();
  });
});

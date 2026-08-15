/**
 * @vitest-environment jsdom
 */
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { setApiInstance } from "../api";
import type { ApiClient } from "../api/client";
import { setCurrentWorkspace } from "../platform/workspace-storage";
import {
  getIssueSurfaceViewStore,
  pruneIssueSurfaceViewStates,
} from "../issues/stores/surface-view-store";
import { useDeleteProject } from "./mutations";

vi.mock("../hooks", () => ({
  useWorkspaceId: () => "ws-1",
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

function createWrapper(qc: QueryClient) {
  return function Wrapper({ children }: { children: ReactNode }) {
    return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
  };
}

describe("useDeleteProject", () => {
  let qc: QueryClient;
  let deleteProject: ReturnType<typeof vi.fn<() => Promise<void>>>;

  beforeEach(() => {
    qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    deleteProject = vi.fn().mockResolvedValue(undefined);
    setApiInstance({ deleteProject } as unknown as ApiClient);
    setCurrentWorkspace("acme", "ws-1");
  });

  afterEach(() => {
    qc.clear();
    pruneIssueSurfaceViewStates([]);
    setCurrentWorkspace(null, null);
    vi.restoreAllMocks();
  });

  it("clears the deleted project's issue surface view state", async () => {
    const store = getIssueSurfaceViewStore("project:p1");
    store.getState().setViewMode("list");
    expect(store.getState().viewMode).toBe("list");

    const { result } = renderHook(() => useDeleteProject(), {
      wrapper: createWrapper(qc),
    });

    await act(async () => {
      await result.current.mutateAsync("p1");
    });

    expect(deleteProject).toHaveBeenCalledWith("p1");
    expect(store.getState().viewMode).toBe("board");
  });
});

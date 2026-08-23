import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setApiInstance } from "../api";
import type { ApiClient } from "../api/client";
import {
  projectKeys,
  projectPipelineOptions,
  projectWorkConservingOptions,
} from "./queries";

/**
 * HIV-941 (P1): the work-conserving projection must poll every 15s so
 * scheduler outcomes that do not emit a Task/WebSocket event still
 * converge in the UI. The poll is GET-only, scoped to a single
 * workspace+project, and must never dispatch a mutation or drain.
 */
describe("projectWorkConservingOptions", () => {
  let getProjection: ReturnType<typeof vi.fn>;
  // Any method on ApiClient whose name implies mutation — we attach a
  // spy-shaped stub so the test can prove the queryFn never calls it.
  let drainSpy: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    getProjection = vi.fn().mockResolvedValue({
      projectId: "p1",
      state: "ready",
      nextActions: [],
    });
    drainSpy = vi.fn();
    setApiInstance({
      getProjectWorkConservingProjection: getProjection,
      drainProjectNextActions: drainSpy,
    } as unknown as ApiClient);
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("pins refetchInterval to exactly 15_000 ms for this projection only", () => {
    const opts = projectWorkConservingOptions("ws-1", "p1");
    expect(opts.queryKey).toEqual(projectKeys.workConserving("ws-1", "p1"));
    expect(opts.refetchInterval).toBe(15_000);
  });

  it("does not leak the 15s interval onto the 5s pipeline projection", () => {
    const pipeline = projectPipelineOptions("ws-1", "p1");
    expect(pipeline.refetchInterval).toBe(5_000);
  });

  it("uses a workspace+project isolated query key (cross-project isolation)", () => {
    const a = projectWorkConservingOptions("ws-1", "p1");
    const b = projectWorkConservingOptions("ws-1", "p2");
    const c = projectWorkConservingOptions("ws-2", "p1");
    expect(a.queryKey).not.toEqual(b.queryKey);
    expect(a.queryKey).not.toEqual(c.queryKey);
    expect(b.queryKey).not.toEqual(c.queryKey);
    // key shape: ["projects", wsId, "detail", projectId, "work-conserving"]
    expect(a.queryKey).toEqual([
      "projects",
      "ws-1",
      "detail",
      "p1",
      "work-conserving",
    ]);
  });

  it("queryFn calls only the GET projection endpoint and never drains or dispatches", async () => {
    const opts = projectWorkConservingOptions("ws-1", "p1");
    // The query function takes a QueryFunctionContext; the projection call
    // only uses the project id baked into the closure.
    await (opts.queryFn as (ctx: unknown) => Promise<unknown>)({
      queryKey: opts.queryKey,
    });
    expect(getProjection).toHaveBeenCalledTimes(1);
    expect(getProjection).toHaveBeenCalledWith("p1");
    expect(drainSpy).not.toHaveBeenCalled();
  });

  it("re-invokes the GET endpoint on every poll tick without any mutation side effect", async () => {
    const opts = projectWorkConservingOptions("ws-1", "p1");
    const qf = opts.queryFn as (ctx: unknown) => Promise<unknown>;
    const ctx = { queryKey: opts.queryKey };
    await qf(ctx);
    await qf(ctx);
    await qf(ctx);
    expect(getProjection).toHaveBeenCalledTimes(3);
    expect(getProjection).toHaveBeenNthCalledWith(1, "p1");
    expect(getProjection).toHaveBeenNthCalledWith(2, "p1");
    expect(getProjection).toHaveBeenNthCalledWith(3, "p1");
    expect(drainSpy).not.toHaveBeenCalled();
  });
});

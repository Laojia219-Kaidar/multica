import { act, renderHook, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { useCursorPage } from "./use-cursor-page";

describe("useCursorPage (activity/history layering)", () => {
  it("loads the activity page then appends history pages via cursor", async () => {
    const fetchPage = vi
      .fn()
      .mockResolvedValueOnce({
        items: [{ id: "a" }],
        total: 3,
        nextCursor: "cur-1",
        hasMore: true,
      })
      .mockResolvedValueOnce({
        items: [{ id: "b" }],
        total: 3,
        nextCursor: "cur-2",
        hasMore: true,
      })
      .mockResolvedValueOnce({
        items: [{ id: "c" }],
        total: 3,
        nextCursor: null,
        hasMore: false,
      });

    const { result } = renderHook(() =>
      useCursorPage<string>({ fetchPage, resetKey: "q|status" }),
    );

    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.items).toEqual([{ id: "a" }]);
    expect(result.current.total).toBe(3);
    expect(result.current.hasMore).toBe(true);

    act(() => result.current.loadMore());
    await waitFor(() => expect(result.current.loadingMore).toBe(false));
    expect(result.current.items).toEqual([{ id: "a" }, { id: "b" }]);
    expect(result.current.hasMore).toBe(true);

    act(() => result.current.loadMore());
    await waitFor(() => expect(result.current.loadingMore).toBe(false));
    expect(result.current.items).toEqual([{ id: "a" }, { id: "b" }, { id: "c" }]);
    expect(result.current.hasMore).toBe(false);

    // No further fetch when the last page is reached.
    act(() => result.current.loadMore());
    expect(fetchPage).toHaveBeenCalledTimes(3);
  });

  it("resets to the activity page when the filter key changes", async () => {
    const fetchPage = vi
      .fn()
      .mockResolvedValueOnce({
        items: [{ id: "first" }],
        total: 1,
        nextCursor: null,
        hasMore: false,
      })
      .mockResolvedValueOnce({
        items: [{ id: "second" }],
        total: 1,
        nextCursor: null,
        hasMore: false,
      });

    const { result, rerender } = renderHook(
      ({ resetKey }) => useCursorPage<string>({ fetchPage, resetKey }),
      { initialProps: { resetKey: "one" } },
    );

    await waitFor(() => expect(result.current.items).toEqual([{ id: "first" }]));

    rerender({ resetKey: "two" });
    await waitFor(() => expect(result.current.items).toEqual([{ id: "second" }]));
    expect(fetchPage).toHaveBeenCalledTimes(2);
  });
});

"use client";

import { useCallback, useEffect, useRef, useState } from "react";

/**
 * Generic server-cursor list hook backing the outcome center's activity /
 * history layering. A surface supplies a fetchPage(cursor) that resolves to one
 * page of items plus the opaque next cursor; the hook keeps the first (active)
 * page in memory and appends subsequent (history) pages on demand.
 *
 * Reusable for any surface whose list endpoint follows the same
 * { items, total, next_cursor, has_more } contract (Inbox / Projects / Issues
 * / Tasks / Outcomes) — only fetchPage differs.
 */

export interface CursorPage<TItem> {
  items: TItem[];
  total: number;
  nextCursor: string | null | undefined;
  hasMore: boolean;
}

export interface UseCursorPageArgs<TItem> {
  fetchPage: (cursor?: string) => Promise<CursorPage<TItem>>;
  /** When this value changes the hook discards accumulated history and reloads
   *  the first (activity) page. Use the serialized filter signature. */
  resetKey: string;
  enabled?: boolean;
}

export interface UseCursorPageState<TItem> {
  items: TItem[];
  total: number;
  hasMore: boolean;
  loading: boolean;
  loadingMore: boolean;
  error: unknown;
  loadMore: () => void;
  reload: () => void;
}

export function useCursorPage<TItem>({
  fetchPage,
  resetKey,
  enabled = true,
}: UseCursorPageArgs<TItem>): UseCursorPageState<TItem> {
  const [items, setItems] = useState<TItem[]>([]);
  const [total, setTotal] = useState(0);
  const [hasMore, setHasMore] = useState(false);
  const [loading, setLoading] = useState(enabled);
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState<unknown>(null);

  const nextCursorRef = useRef<string | null | undefined>(undefined);
  const requestSeqRef = useRef(0);

  const loadFirstPage = useCallback(async () => {
    const seq = ++requestSeqRef.current;
    setLoading(true);
    setError(null);
    try {
      const page = await fetchPage(undefined);
      if (seq !== requestSeqRef.current) return;
      setItems(page.items);
      setTotal(page.total);
      setHasMore(page.hasMore);
      nextCursorRef.current = page.nextCursor;
    } catch (err) {
      if (seq !== requestSeqRef.current) return;
      setItems([]);
      setTotal(0);
      setHasMore(false);
      setError(err);
    } finally {
      if (seq === requestSeqRef.current) setLoading(false);
    }
  }, [fetchPage]);

  const loadMore = useCallback(() => {
    if (loadingMore || !hasMore || nextCursorRef.current == null) return;
    const seq = ++requestSeqRef.current;
    setLoadingMore(true);
    void fetchPage(nextCursorRef.current)
      .then((page) => {
        if (seq !== requestSeqRef.current) return;
        setItems((prev) => [...prev, ...page.items]);
        setTotal(page.total);
        setHasMore(page.hasMore);
        nextCursorRef.current = page.nextCursor;
      })
      .catch((err) => {
        if (seq !== requestSeqRef.current) return;
        setError(err);
      })
      .finally(() => {
        if (seq === requestSeqRef.current) setLoadingMore(false);
      });
  }, [fetchPage, hasMore, loadingMore]);

  const reload = useCallback(() => {
    void loadFirstPage();
  }, [loadFirstPage]);

  useEffect(() => {
    if (!enabled) return;
    void loadFirstPage();
  }, [enabled, loadFirstPage, resetKey]);

  return { items, total, hasMore, loading, loadingMore, error, loadMore, reload };
}

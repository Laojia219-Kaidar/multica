"use client";

import { useCallback, useMemo } from "react";
import { api } from "@multica/core/api";
import type { CompanyOpsOutcomeListParams, CompanyOpsOutcomeSummary } from "@multica/core/types";
import { useCursorPage } from "./use-cursor-page";

export interface OutcomeCursorList {
  outcomes: CompanyOpsOutcomeSummary[];
  total: number;
  hasMore: boolean;
  loading: boolean;
  loadingMore: boolean;
  error: unknown;
  loadMore: () => void;
  reload: () => void;
}

/**
 * Outcome-center list over the keyset-cursor endpoint. The first page is the
 * active layer (newest outcomes); `loadMore` walks next_cursor into the
 * history layer (older outcomes) and appends.
 */
export function useOutcomesCursor(
  wsId: string,
  params: Omit<CompanyOpsOutcomeListParams, "cursor"> = {},
): OutcomeCursorList {
  const resetKey = useMemo(
    () =>
      [
        wsId,
        params.q ?? "",
        params.status ?? "",
        params.limit ?? 50,
        params.offset ?? 0,
      ].join("|"),
    [wsId, params.q, params.status, params.limit, params.offset],
  );

  const fetchPage = useCallback(
    async (cursor?: string) => {
      const page = await api.listCompanyOpsOutcomes({
        q: params.q,
        status: params.status,
        limit: params.limit ?? 50,
        offset: params.offset ?? 0,
        cursor,
      });
      return {
        items: page.items,
        total: page.total,
        nextCursor: page.next_cursor,
        hasMore: !!page.has_more,
      };
    },
    [params.q, params.status, params.limit, params.offset],
  );

  const state = useCursorPage<CompanyOpsOutcomeSummary>({
    fetchPage,
    resetKey,
    enabled: !!wsId,
  });

  return {
    outcomes: state.items,
    total: state.total,
    hasMore: state.hasMore,
    loading: state.loading,
    loadingMore: state.loadingMore,
    error: state.error,
    loadMore: state.loadMore,
    reload: state.reload,
  };
}

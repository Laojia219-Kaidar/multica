"use client";

/* eslint-disable i18next/no-literal-string */

import { useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import { api } from "@multica/core/api";
import type { WorldLibraryStatus } from "@multica/core/api/client";

/**
 * Honest authority status line for the data & knowledge column (HIV-1251).
 *
 * The World Library is the declared knowledge authority; the runtime bridge
 * is owner-gated (HIVECREW_WORLD_LIBRARY_URL) and the server fails closed to
 * `source_available_runtime_unavailable`. This component renders that verdict
 * verbatim — a query error also fails closed to the same unavailable copy, a
 * stale "available" verdict must never survive a source failure.
 */
export function WorldLibraryStatusLine() {
  const wsId = useWorkspaceId();
  const { data, isPending, isError } = useQuery({
    queryKey: ["world-library", wsId],
    queryFn: () => api.worldLibraryStatus(),
  });

  const connected = !!data && data.state === "runtime_available";
  const dotClass = isPending
    ? "bg-muted-foreground/50"
    : connected
      ? "bg-emerald-500"
      : "bg-amber-500";
  const copy = isPending
    ? "权威状态检查中…"
    : connected
      ? "知识权威 World Library 运行时已接通"
      : "知识权威 World Library 运行时未接通（source_available_runtime_unavailable）";
  const detail = isError
    ? "状态查询失败，按未接通处理"
    : !data
      ? undefined
      : data.bridge.configured && !connected
        ? "已配置桥接地址但探测未通过"
        : undefined;

  return (
    <div
      data-slot="world-library-status"
      className="mt-1 flex items-center gap-1.5 text-xs"
      title={detail}
    >
      <span aria-hidden="true" className={`inline-block size-1.5 shrink-0 rounded-full ${dotClass}`} />
      <span className="text-muted-foreground" data-slot="world-library-copy">{copy}</span>
      {detail ? <span className="text-amber-600">{detail}</span> : null}
    </div>
  );
}

export type { WorldLibraryStatus };

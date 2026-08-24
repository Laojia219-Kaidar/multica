"use client";

import { useQuery } from "@tanstack/react-query";
import { Monitor } from "lucide-react";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { parseA2WorkWallSnapshot } from "@multica/core/api/workwall";
import { CollectionPageHeader } from "../layout/collection-page";
import { WorkWall } from "./work-wall";

/**
 * A2 CEO 工作现场页面。
 *
 * 两个数据源：
 * 1. 员工花名册（EmployeeLiveActivityV1[]）— 主列表，决定谁出现在墙上。
 * 2. A2 快照 pane（hivecrew.workwall.a2-snapshot.v1）— 按 employee_id 拼接到员工上，
 *    每个员工显示最新 pane；terminal 与 event_console 互斥（terminal 优先）。
 *
 * 严格协议：A2 快照经 parseA2WorkWallSnapshot 解析，未知字段/非法枚举直接拒绝。
 * 接口未上线时，组件以空 pane 列表安全降级，不会伪造数据。
 */
export function WorkWallPage() {
  const wsId = useWorkspaceId();

  // Employee roster — primary data source.
  const { data: employees = [] } = useQuery({
    queryKey: ["work-wall", wsId, "snapshot"],
    queryFn: () => api.workWallSnapshot(),
    refetchInterval: 5000,
  });

  // A2 pane snapshot — strict fail-closed parse.
  const { data: a2Data, error } = useQuery({
    queryKey: ["work-wall", wsId, "a2-snapshot"],
    queryFn: async () => {
      const raw = await api.getA2WorkWallSnapshot();
      return parseA2WorkWallSnapshot(raw);
    },
    refetchInterval: 5000,
    // If the endpoint is not yet live, gracefully degrade — the wall still
    // renders the employee roster with empty pane slots. We never fake data.
    retry: 1,
  });

  const panes = a2Data?.panes ?? [];

  return (
    <div className="flex min-h-0 flex-col p-5">
      <CollectionPageHeader
        icon={Monitor}
        title="工作现场"
        count={employees.length}
        description="A2 CEO 工作墙 · 4 列 × 2 行 · 实时观察数字员工执行现场"
      />

      <div className="mt-2 flex-1">
        <WorkWall employees={employees} panes={panes} />
      </div>

      {error ? (
        <div
          className="mt-3 rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-xs text-destructive"
          data-testid="a2-snapshot-error"
        >
          A2 快照加载失败：{error instanceof Error ? error.message : String(error)}
        </div>
      ) : null}
    </div>
  );
}

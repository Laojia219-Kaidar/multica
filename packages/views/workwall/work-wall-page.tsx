"use client";

import { useQuery } from "@tanstack/react-query";
import { Monitor } from "lucide-react";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { CollectionPageHeader } from "../layout/collection-page";
import { WorkWall } from "./work-wall";

/**
 * A2 CEO 工作现场页面。
 *
 * 三个数据源：
 * 1. 员工花名册（EmployeeLiveActivityV1[]）— 主列表，决定谁出现在墙上。
 *    花名册是唯一的员工来源，绝不从 A2 pane 伪造员工。
 * 2. A2 快照 pane（hivecrew.workwall.a2-snapshot.v1）— 按 employee_id 拼接到员工上，
 *    第一片服务端顺序的 pane 胜出；由 ApiClient 内部严格解析。
 * 3. 终端现场（TerminalPane[]）— 当 pane 的 surface_kind=terminal 且
 *    session_id 与 TerminalPane.session_name 完全匹配时，才显示终端输出。
 *    终端输出只来自 TerminalPane.tail_text。
 *
 * Terminal 与事件台互斥：有匹配终端则显示终端，否则显示 A2 事件台安全字段。
 * 接口未上线时，组件以空数据安全降级，不会伪造数据。
 */
export function WorkWallPage() {
  const wsId = useWorkspaceId();

  // Employee roster — primary data source.
  const { data: employees = [] } = useQuery({
    queryKey: ["work-wall", wsId, "snapshot"],
    queryFn: () => api.workWallSnapshot(),
    refetchInterval: 5000,
  });

  // A2 pane snapshot — strict parse inside ApiClient (fail-closed).
  const { data: a2Data, error: a2Error } = useQuery({
    queryKey: ["work-wall", wsId, "a2-snapshot"],
    queryFn: () => api.getA2WorkWallSnapshot(),
    refetchInterval: 5000,
    retry: 1,
  });

  // Terminal presence — parsed strictly inside ApiClient.
  const { data: terminalPresence = [], error: terminalError } = useQuery({
    queryKey: ["work-wall", wsId, "terminal-presence"],
    queryFn: () => api.listTerminalPresence(),
    refetchInterval: 5000,
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
        <WorkWall
          employees={employees}
          panes={panes}
          terminalPresence={terminalPresence}
        />
      </div>

      {a2Error || terminalError ? (
        <div
          className="mt-3 rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-xs text-destructive"
          data-testid="a2-snapshot-error"
        >
          {a2Error ? (
            <div>A2 快照加载失败：{a2Error instanceof Error ? a2Error.message : String(a2Error)}</div>
          ) : null}
          {terminalError ? (
            <div>终端现场加载失败：{terminalError instanceof Error ? terminalError.message : String(terminalError)}</div>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

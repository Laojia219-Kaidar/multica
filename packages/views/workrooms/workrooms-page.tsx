"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Network, Plus } from "lucide-react";
import { toast } from "sonner";
import { useWorkspaceId } from "@multica/core/hooks";
import { api } from "@multica/core/api";
import { CollectionPageHeader } from "../layout/collection-page";

type Workroom = { id: string; name: string; project_id?: string; issue_id?: string; work_order_id?: string; created_by: string };

export function WorkroomsPage() {
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const { data: workrooms = [], isLoading } = useQuery({
    queryKey: ["workrooms", wsId],
    queryFn: () => api.listWorkrooms(),
  });
  const [name, setName] = useState("");
  const [issueId, setIssueId] = useState("");

  const create = useMutation({
    mutationFn: (data: { name: string; issue_id?: string }) => api.createWorkroom(data),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["workrooms", wsId] });
      setName(""); setIssueId("");
      toast.success("协作空间已创建");
    },
    onError: () => toast.error("创建失败"),
  });

  return (
    <div className="flex h-full flex-col">
      <CollectionPageHeader icon={Network} title="协作空间" description="QM Workroom：人类与数字员工的协作上下文，绑定项目/议题/工单，不另建真源。" />
      <div className="grid grid-cols-1 gap-4 p-4 lg:grid-cols-3">
        <div className="rounded-lg border bg-card p-4 shadow-sm">
          <h3 className="text-sm font-semibold">新建协作空间</h3>
          <input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="空间名称"
            className="mt-2 w-full rounded-md border px-2 py-1.5 text-sm"
          />
          <input
            value={issueId}
            onChange={(e) => setIssueId(e.target.value)}
            placeholder="议题 ID（可选，8 位前缀）"
            className="mt-2 w-full rounded-md border px-2 py-1.5 text-sm"
          />
          <button
            type="button"
            disabled={!name.trim() || create.isPending}
            onClick={() => create.mutate({ name: name.trim(), issue_id: issueId.trim() || undefined })}
            className="mt-3 flex items-center gap-1 rounded-md bg-primary px-3 py-1.5 text-sm text-primary-foreground disabled:opacity-50"
          >
            <Plus className="size-4" /> 创建
          </button>
        </div>
        <div className="lg:col-span-2 rounded-lg border bg-card p-4 shadow-sm">
          <h3 className="text-sm font-semibold">协作空间列表</h3>
          {isLoading ? (
            <p className="mt-2 text-sm text-muted-foreground">加载中…</p>
          ) : workrooms.length === 0 ? (
            <p className="mt-2 text-sm text-muted-foreground">暂无协作空间</p>
          ) : (
            <ul className="mt-2 space-y-2">
              {workrooms.map((wr: Workroom) => (
                <li key={wr.id} className="rounded-md border p-3 text-sm">
                  <div className="font-medium">{wr.name}</div>
                  <div className="mt-1 text-xs text-muted-foreground">
                    id: {wr.id.slice(0, 8)}
                    {wr.issue_id ? ` · 议题 ${wr.issue_id.slice(0, 8)}` : ""}
                    {wr.project_id ? ` · 项目 ${wr.project_id.slice(0, 8)}` : ""}
                    {wr.work_order_id ? ` · 工单 ${wr.work_order_id.slice(0, 8)}` : ""}
                  </div>
                </li>
              ))}
            </ul>
          )}
        </div>
      </div>
    </div>
  );
}

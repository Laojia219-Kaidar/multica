"use client";

/* eslint-disable i18next/no-literal-string */

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertCircle, Network, Plus } from "lucide-react";
import { toast } from "sonner";
import { useWorkspaceId } from "@multica/core/hooks";
import { api } from "@multica/core/api";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@multica/ui/components/ui/card";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import {
  CollectionPageHeader,
  CollectionPageState,
} from "../layout/collection-page";

type Workroom = {
  id: string;
  name: string;
  project_id?: string;
  issue_id?: string;
  work_order_id?: string;
  created_by: string;
};

function safeRef(id?: string) {
  if (!id) return null;
  return id.slice(0, 8);
}

function WorkroomListItem({ wr }: { wr: Workroom }) {
  const idRef = safeRef(wr.id);
  const issueRef = safeRef(wr.issue_id);
  const projectRef = safeRef(wr.project_id);
  const woRef = safeRef(wr.work_order_id);

  const meta = [
    idRef ? `id ${idRef}` : null,
    issueRef ? `议题 ${issueRef}` : null,
    projectRef ? `项目 ${projectRef}` : null,
    woRef ? `工单 ${woRef}` : null,
  ].filter(Boolean);

  return (
    <li
      data-slot="workroom-item"
      className="rounded-lg border border-surface-border bg-surface p-3 text-sm shadow-[var(--surface-shadow)]"
    >
      <div className="font-medium text-surface-foreground">{wr.name}</div>
      {meta.length > 0 ? (
        <div className="mt-1 text-xs text-muted-foreground">
          {meta.join(" · ")}
        </div>
      ) : null}
    </li>
  );
}

function WorkroomListSkeleton({ count = 3 }: { count?: number }) {
  return (
    <ul data-slot="workroom-list-skeleton" className="space-y-2">
      {Array.from({ length: count }).map((_, i) => (
        <li key={i} className="rounded-md border p-3">
          <Skeleton className="h-4 w-2/3" />
          <Skeleton className="mt-1.5 h-3 w-1/2" />
        </li>
      ))}
    </ul>
  );
}

export function WorkroomsPage() {
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const {
    data: workrooms = [],
    isLoading,
    isError,
    refetch,
  } = useQuery({
    queryKey: ["workrooms", wsId],
    queryFn: () => api.listWorkrooms(),
  });
  const [name, setName] = useState("");
  const [issueId, setIssueId] = useState("");

  const create = useMutation({
    mutationFn: (data: { name: string; issue_id?: string }) =>
      api.createWorkroom(data),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["workrooms", wsId] });
      setName("");
      setIssueId("");
      toast.success("协作空间已创建");
    },
    onError: () => toast.error("创建失败"),
  });

  const handleCreate = () => {
    create.mutate({
      name: name.trim(),
      issue_id: issueId.trim() || undefined,
    });
  };

  return (
    <div className="flex h-full flex-col">
      <CollectionPageHeader
        icon={Network}
        title="协作空间"
        count={isLoading ? undefined : workrooms.length}
        description="QM Workroom：人类与数字员工的协作上下文，绑定项目/议题/工单，不另建真源。"
      />
      <div className="grid grid-cols-1 gap-4 p-4 lg:grid-cols-3">
        <Card size="sm" data-slot="create-workroom-card">
          <CardHeader>
            <CardTitle className="text-sm">新建协作空间</CardTitle>
            <CardDescription>
              空间名称必填，议题 ID 可选
            </CardDescription>
          </CardHeader>
          <CardContent className="space-y-3">
            <Input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="空间名称"
              disabled={create.isPending}
            />
            <Input
              value={issueId}
              onChange={(e) => setIssueId(e.target.value)}
              placeholder="议题 ID（可选，8 位前缀）"
              disabled={create.isPending}
            />
            <Button
              type="button"
              size="sm"
              disabled={!name.trim() || create.isPending}
              onClick={handleCreate}
              className="w-full"
            >
              <Plus aria-hidden="true" className="size-3.5" />
              {create.isPending ? "创建中…" : "创建"}
            </Button>
          </CardContent>
        </Card>

        <div className="lg:col-span-2">
          {isError ? (
            <CollectionPageState
              icon={AlertCircle}
              tone="destructive"
              role="alert"
              title="加载失败"
              description="无法加载协作空间列表，请重试。"
              actions={
                <Button variant="outline" size="sm" onClick={() => refetch()}>
                  重试
                </Button>
              }
            />
          ) : isLoading ? (
            <Card size="sm" data-slot="workroom-list-card">
              <CardHeader>
                <CardTitle className="text-sm">协作空间列表</CardTitle>
              </CardHeader>
              <CardContent>
                <WorkroomListSkeleton />
              </CardContent>
            </Card>
          ) : workrooms.length === 0 ? (
            <CollectionPageState
              icon={Network}
              title="暂无协作空间"
              description="创建第一个协作空间，开启人机协作。"
            />
          ) : (
            <Card size="sm" data-slot="workroom-list-card">
              <CardHeader>
                <CardTitle className="text-sm">协作空间列表</CardTitle>
              </CardHeader>
              <CardContent>
                <ul className="space-y-2">
                  {workrooms.map((wr: Workroom) => (
                    <WorkroomListItem key={wr.id} wr={wr} />
                  ))}
                </ul>
              </CardContent>
            </Card>
          )}
        </div>
      </div>
    </div>
  );
}

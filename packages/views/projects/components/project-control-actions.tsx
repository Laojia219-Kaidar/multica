"use client";

import { useState } from "react";
import { Pause, Play, RotateCcw, Loader2 } from "lucide-react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api } from "@multica/core/api";
import type {
  ContinuePreview,
  Project,
  ProjectControlAction,
  ProjectControlReceipt,
} from "@multica/core/types";
import { projectKeys } from "@multica/core/projects/queries";
import { Button } from "@multica/ui/components/ui/button";

// ProjectControlActions renders owner/admin continue / pause_dispatch / resume
// with preview-first semantics and an immutable receipt toast.
export function ProjectControlActions({
  project,
  workspaceId,
}: {
  project: Project;
  workspaceId: string;
}) {
  const qc = useQueryClient();
  const [preview, setPreview] = useState<ContinuePreview | null>(null);
  const [confirming, setConfirming] = useState<ProjectControlAction | null>(null);

  const invalidate = () => {
    qc.invalidateQueries({ queryKey: projectKeys.list(workspaceId) });
    qc.invalidateQueries({ queryKey: projectKeys.detail(workspaceId, project.id) });
    qc.invalidateQueries({ queryKey: projectKeys.lifecycle(workspaceId) });
  };

  const run = useMutation({
    mutationFn: async (action: ProjectControlAction) =>
      api.projectLifecycleAction(project.id, action, {
        preview: false,
        idempotency_key: crypto.randomUUID(),
      }) as Promise<ProjectControlReceipt>,
    onSuccess: (receipt) => {
      invalidate();
      setPreview(null);
      setConfirming(null);
      if (receipt.blockers && receipt.blockers.length > 0) {
        toast.error(`已拒绝: ${receipt.blockers.join(", ")}`);
      } else if (receipt.replayed) {
        toast.success(`已幂等重放（${receipt.action}），无重复副作用`);
      } else {
        toast.success(`已执行 ${receipt.action}`, { description: `before=${receipt.before_status} → after=${receipt.after_status}` });
      }
    },
    onError: () => toast.error("操作失败"),
  });

  const previewContinue = async () => {
    const res = (await api.projectLifecycleAction(project.id, "continue", {
      preview: true,
    })) as { preview: ContinuePreview };
    setPreview(res.preview);
    setConfirming("continue");
  };

  return (
    <div className="space-y-1.5 pt-2">
      <div className="flex items-center gap-1.5">
        <Button
          size="sm"
          variant="outline"
          className="h-7 flex-1 gap-1 text-xs"
          onClick={previewContinue}
          disabled={run.isPending}
        >
          {run.isPending && confirming === "continue" ? <Loader2 className="h-3 w-3 animate-spin" /> : <Play className="h-3 w-3" />}
          继续
        </Button>
        <Button
          size="sm"
          variant="outline"
          className="h-7 flex-1 gap-1 text-xs"
          onClick={() => { setPreview(null); setConfirming("pause_dispatch"); }}
          disabled={run.isPending}
        >
          <Pause className="h-3 w-3" />
          暂停派发
        </Button>
        <Button
          size="sm"
          variant="outline"
          className="h-7 flex-1 gap-1 text-xs"
          onClick={() => { setPreview(null); setConfirming("resume"); }}
          disabled={run.isPending}
        >
          <RotateCcw className="h-3 w-3" />
          恢复
        </Button>
      </div>

      {preview && confirming === "continue" && (
        <div className="rounded-md border bg-accent/40 p-2 text-[11px]">
          {preview.blockers && preview.blockers.length > 0 ? (
            <div className="text-rose-700">已拒绝: {preview.blockers.join(", ")}</div>
          ) : (
            <div className="space-y-1">
              <div>目标议题: #{preview.target_issue_number}（{preview.target_issue_id.slice(0, 8)}…）</div>
              <div className="text-muted-foreground">将复用现有议题并创建/复用一条执行 Task，不会重复派发。</div>
            </div>
          )}
          <div className="mt-1.5 flex gap-1.5">
            <Button
              size="sm"
              className="h-6 flex-1 text-[11px]"
              disabled={run.isPending || (preview.blockers?.length ?? 0) > 0}
              onClick={() => run.mutate("continue")}
            >
              确认派发
            </Button>
            <Button size="sm" variant="ghost" className="h-6 text-[11px]" onClick={() => { setPreview(null); setConfirming(null); }}>
              取消
            </Button>
          </div>
        </div>
      )}

      {confirming === "pause_dispatch" && (
        <div className="rounded-md border bg-accent/40 p-2 text-[11px]">
          <div>停止「新派发」，不终止正在运行的任务（终止需单独执行 stop-current）。</div>
          <div className="mt-1.5 flex gap-1.5">
            <Button size="sm" className="h-6 flex-1 text-[11px]" disabled={run.isPending} onClick={() => run.mutate("pause_dispatch")}>
              确认暂停派发
            </Button>
            <Button size="sm" variant="ghost" className="h-6 text-[11px]" onClick={() => setConfirming(null)}>取消</Button>
          </div>
        </div>
      )}

      {confirming === "resume" && (
        <div className="rounded-md border bg-accent/40 p-2 text-[11px]">
          <div>恢复项目并派发 ready frontier（不复活已终结任务）。</div>
          <div className="mt-1.5 flex gap-1.5">
            <Button size="sm" className="h-6 flex-1 text-[11px]" disabled={run.isPending} onClick={() => run.mutate("resume")}>
              确认恢复
            </Button>
            <Button size="sm" variant="ghost" className="h-6 text-[11px]" onClick={() => setConfirming(null)}>取消</Button>
          </div>
        </div>
      )}
    </div>
  );
}

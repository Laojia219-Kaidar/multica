"use client";

import { useState } from "react";
import { Send, Square, ClipboardCheck, Loader2 } from "lucide-react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api } from "@multica/core/api";
import type {
  IssueDispatchPreview,
  IssueDispatchResult,
  IssueStopReceipt,
  IssueReviewReceipt,
} from "@multica/core/types";
import { issueKeys } from "@multica/core/issues/queries";
import { useCurrentMember } from "@multica/core/permissions";
import { Button } from "@multica/ui/components/ui/button";

// IssueDispatchControls renders the owner issue control plane (Lane A dispatch
// view): dispatch-preview -> dispatch, stop, and send-to-review. Each write
// surfaces its operation receipt as a toast.
export function IssueDispatchControls({
  issueId,
  issueStatus,
  workspaceId,
}: {
  issueId: string;
  issueStatus: string;
  workspaceId: string;
}) {
  const qc = useQueryClient();
  const { role, isLoading: memberLoading, isError: memberError } = useCurrentMember(workspaceId);
  const [preview, setPreview] = useState<IssueDispatchPreview | null>(null);

  const invalidate = () => {
    qc.invalidateQueries({ queryKey: ["issue-dispatch-preview", issueId] });
    qc.invalidateQueries({ queryKey: issueKeys.detail(workspaceId, issueId) });
    qc.invalidateQueries({ queryKey: issueKeys.list(workspaceId) });
  };

  const dispatch = useMutation({
    mutationFn: (): Promise<IssueDispatchResult> => api.dispatchIssue(issueId, { idempotency_key: crypto.randomUUID() }),
    onSuccess: (res: IssueDispatchResult) => {
      invalidate();
      setPreview(null);
      if (res.replayed || res.decision === "already_active") toast.success("已存在待处理任务（幂等），无重复派工");
      else if (res.decision === "would_enqueue" && res.task_ids?.[0]) toast.success("已派工", { description: `task=${res.task_ids[0]}` });
      else toast.info(`派工结果：${res.decision}`);
    },
    onError: () => toast.error("派工失败"),
  });

  const stop = useMutation({
    mutationFn: () => api.stopIssue(issueId),
    onSuccess: (res: { receipt: IssueStopReceipt }) => {
      invalidate();
      const r = res.receipt;
      toast.success(`已停止 ${r.cancelled_task_ids.length} 个任务`);
    },
    onError: () => toast.error("停止失败"),
  });

  const sendToReview = useMutation({
    mutationFn: () => api.sendIssueToReview(issueId),
    onSuccess: (res: { receipt: IssueReviewReceipt }) => {
      invalidate();
      const r = res.receipt;
      toast.success(`已送审 ${r.from_status} → ${r.to_status}`);
    },
    onError: () => toast.error("送审失败"),
  });

  const previewDispatch = async () => {
    const res = await api.previewIssueDispatch(issueId);
    setPreview(res);
  };

  const terminal = issueStatus === "done" || issueStatus === "cancelled";

  // Keep the affordance aligned with the backend's owner/admin gate. Hide
  // while membership is unresolved to avoid a member seeing a clickable
  // control during the query's loading window. All hooks above remain
  // unconditional so a later membership result cannot change hook order.
  if (memberLoading || memberError || (role !== "owner" && role !== "admin")) return null;

  return (
    <div className="space-y-1.5 pt-2">
      <div className="flex items-center gap-1.5">
        <Button
          size="sm"
          variant="outline"
          className="h-7 flex-1 gap-1 text-xs"
          disabled={terminal || dispatch.isPending}
          onClick={previewDispatch}
        >
          {dispatch.isPending ? <Loader2 className="size-3 animate-spin" /> : <Send className="size-3" />}
          派工预览
        </Button>
        <Button
          size="sm"
          variant="outline"
          className="h-7 flex-1 gap-1 text-xs"
          disabled={terminal || stop.isPending}
          onClick={() => stop.mutate()}
        >
          {stop.isPending ? <Loader2 className="size-3 animate-spin" /> : <Square className="size-3" />}
          停止
        </Button>
        <Button
          size="sm"
          variant="outline"
          className="h-7 flex-1 gap-1 text-xs"
          disabled={terminal || sendToReview.isPending}
          onClick={() => sendToReview.mutate()}
        >
          {sendToReview.isPending ? <Loader2 className="size-3 animate-spin" /> : <ClipboardCheck className="size-3" />}
          送审
        </Button>
      </div>

      {preview && (
        <div className="rounded-md border border-border bg-muted/40 px-2 py-1.5 text-xs">
          {preview.decision === "would_enqueue" || preview.decision === "already_active" ? (
            <>
              <div className="flex items-center justify-between gap-2">
                <span className="text-muted-foreground">
                  {preview.decision === "already_active" ? "已有待处理任务" : "可派工"}
                  {preview.assignee ? ` → ${preview.assignee.type} (${preview.assignee.id.slice(0, 8)}…)` : ""}
                </span>
                <Button size="sm" className="h-6 text-xs" disabled={dispatch.isPending} onClick={() => dispatch.mutate()}>
                  {dispatch.isPending ? <Loader2 className="size-3 animate-spin" /> : null}
                  确认派工
                </Button>
              </div>
            </>
          ) : (
            <span className="text-destructive">不可派工{preview.reason ? `：${preview.reason}` : ""}</span>
          )}
        </div>
      )}
    </div>
  );
}

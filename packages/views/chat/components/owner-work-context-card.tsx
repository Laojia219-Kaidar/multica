"use client";

/* eslint-disable i18next/no-literal-string -- bounded Owner workbench surface */

import { useRef, useState } from "react";
import type {
  CompanyOpsArtifactReviewCommand,
  CompanyOpsArtifactReviewReceipt,
  CompanyOpsAssignmentCommand,
  CompanyOpsAssignmentDispatchReceipt,
  CompanyOpsOwnerWorkContext,
} from "@multica/core/types";
import { Alert, AlertDescription } from "@multica/ui/components/ui/alert";
import { Button } from "@multica/ui/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@multica/ui/components/ui/card";
import { Textarea } from "@multica/ui/components/ui/textarea";

export interface ReadyOwnerWorkContext {
  state: "ready";
  data: CompanyOpsOwnerWorkContext;
}

export interface UnavailableOwnerWorkContext {
  state: "loading" | "invalid" | "conflict";
  reason: string;
}

export type OwnerWorkContext =
  | ReadyOwnerWorkContext
  | UnavailableOwnerWorkContext;

export type OwnerAssignmentCommand = Omit<
  CompanyOpsAssignmentCommand,
  "command_id"
>;
export type OwnerAssignmentWriter = (
  command: OwnerAssignmentCommand,
) => Promise<CompanyOpsAssignmentDispatchReceipt>;
export type OwnerAssignmentReceipt = CompanyOpsAssignmentDispatchReceipt;

export type OwnerArtifactReviewCommand = Omit<
  CompanyOpsArtifactReviewCommand,
  "review_id"
>;
export type OwnerArtifactReviewWriter = (
  command: OwnerArtifactReviewCommand,
) => Promise<CompanyOpsArtifactReviewReceipt>;

type WriterInput<T> = T | (new (...args: never[]) => unknown);

interface OwnerWorkContextCardProps {
  context: OwnerWorkContext;
  onConfirmAssignment: WriterInput<OwnerAssignmentWriter>;
  onReviewArtifact: WriterInput<OwnerArtifactReviewWriter>;
}

function ProvenanceRow({ label, value }: { label: string; value: string }) {
  return (
    <div className="grid min-w-0 gap-1 sm:grid-cols-[8rem_minmax(0,1fr)]">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="min-w-0 break-all font-mono text-xs">{value}</dd>
    </div>
  );
}

function errorMessage(error: unknown): string {
  if (error instanceof Error && error.message.trim()) return error.message;
  return "操作失败，系统没有返回可核对的回执。";
}

function executionStateLabel(state: string): string {
  switch (state) {
    case "awaiting_claim":
      return "等待员工领取";
    case "running":
      return "员工正在工作";
    case "completed":
      return "本次工作已完成";
    case "failed":
      return "本次工作失败";
    case "cancelled":
      return "本次工作已取消";
    default:
      return state;
  }
}

export function OwnerWorkContextCard({
  context,
  onConfirmAssignment,
  onReviewArtifact,
}: OwnerWorkContextCardProps) {
  const [handoffNote, setHandoffNote] = useState("");
  const [feedback, setFeedback] = useState("");
  const [isPending, setIsPending] = useState(false);
  const [writeError, setWriteError] = useState<string | null>(null);
  const [dispatchReceipt, setDispatchReceipt] =
    useState<CompanyOpsAssignmentDispatchReceipt | null>(null);
  const [reviewReceipt, setReviewReceipt] =
    useState<CompanyOpsArtifactReviewReceipt | null>(null);
  const pendingRef = useRef(false);
  const isReady = context.state === "ready";
  const resolved = isReady ? context.data : null;
  const outcome = resolved?.outcome;
  const artifact = outcome?.artifact;

  async function confirmAssignment() {
    if (!resolved || outcome || pendingRef.current || dispatchReceipt) return;
    if (typeof onConfirmAssignment !== "function") {
      setWriteError("派工能力尚未接通。");
      return;
    }
    pendingRef.current = true;
    setIsPending(true);
    setWriteError(null);
    try {
      const writer = onConfirmAssignment as OwnerAssignmentWriter;
      setDispatchReceipt(
        await writer({ ...resolved.request, handoff_note: handoffNote }),
      );
    } catch (error) {
      setWriteError(errorMessage(error));
    } finally {
      pendingRef.current = false;
      setIsPending(false);
    }
  }

  async function reviewArtifact(decision: "changes_requested" | "approved") {
    if (!resolved || !artifact || pendingRef.current || reviewReceipt) return;
    if (decision === "changes_requested" && !feedback.trim()) {
      setWriteError("请先写明需要员工修改的内容。");
      return;
    }
    if (typeof onReviewArtifact !== "function") {
      setWriteError("成果审查能力尚未接通。");
      return;
    }
    pendingRef.current = true;
    setIsPending(true);
    setWriteError(null);
    try {
      const writer = onReviewArtifact as OwnerArtifactReviewWriter;
      setReviewReceipt(
        await writer({
          ...resolved.request,
          candidate_id: artifact.id,
          decision,
          feedback: decision === "changes_requested" ? feedback.trim() : "",
        }),
      );
    } catch (error) {
      setWriteError(errorMessage(error));
    } finally {
      pendingRef.current = false;
      setIsPending(false);
    }
  }

  return (
    <Card size="sm" aria-label="Owner work context">
      <CardHeader>
        <CardTitle>工作单与成果</CardTitle>
        <CardDescription>
          在当前对话中派工、查看员工成果并决定返工或通过。
        </CardDescription>
      </CardHeader>

      <CardContent className="space-y-4">
        {resolved ? (
          <>
            <div className="grid gap-2 sm:grid-cols-3">
              <div>
                <p className="text-xs text-muted-foreground">数字员工</p>
                <p className="text-sm font-medium">{resolved.agent.name}</p>
              </div>
              <div>
                <p className="text-xs text-muted-foreground">本地工作项</p>
                <p className="text-sm font-medium">
                  {resolved.issue ? `#${resolved.issue.number}` : "尚未建立"}
                </p>
              </div>
              <div>
                <p className="text-xs text-muted-foreground">当前状态</p>
                <p className="text-sm font-medium">
                  {outcome
                    ? executionStateLabel(outcome.execution_state)
                    : "等待派工"}
                </p>
              </div>
            </div>

            <details className="rounded-md border p-3 text-sm">
              <summary className="cursor-pointer font-medium">技术依据</summary>
              <dl className="mt-3 space-y-2">
                <ProvenanceRow label="WorkOrder" value={resolved.work_order.source_ref} />
                <ProvenanceRow label="WorkOrder 版本" value={resolved.work_order.revision} />
                <ProvenanceRow label="员工身份" value={resolved.employee.employee_id} />
                <ProvenanceRow label="身份绑定" value={resolved.identity_binding.identity_binding_id} />
                <ProvenanceRow label="Agent" value={resolved.agent.id} />
                <ProvenanceRow label="对话" value={resolved.session.id} />
              </dl>
            </details>
          </>
        ) : context.state === "loading" ? (
          <Alert>
            <AlertDescription aria-live="polite">{context.reason}</AlertDescription>
          </Alert>
        ) : (
          <Alert variant="destructive">
            <AlertDescription>
              {context.state === "ready" ? "" : context.reason}
            </AlertDescription>
          </Alert>
        )}

        {resolved && !outcome && (
          <section className="space-y-2" aria-label="Assign work">
            <label htmlFor="owner-work-context-handoff" className="text-sm font-medium">
              工作要求
            </label>
            <Textarea
              id="owner-work-context-handoff"
              value={handoffNote}
              onChange={(event) => setHandoffNote(event.target.value)}
              disabled={isPending || dispatchReceipt !== null}
              placeholder="说明需要完成的工作、交付物和验收标准。"
            />
            <Button
              type="button"
              onClick={confirmAssignment}
              disabled={isPending || dispatchReceipt !== null || !handoffNote.trim()}
            >
              {isPending ? "正在派工…" : "派给这名员工"}
            </Button>
          </section>
        )}

        {writeError && (
          <Alert variant="destructive">
            <AlertDescription>{writeError}</AlertDescription>
          </Alert>
        )}

        {dispatchReceipt && (
          <Alert>
            <AlertDescription aria-live="polite">
              已创建 Run {dispatchReceipt.initial_task_id}，等待员工领取。
            </AlertDescription>
          </Alert>
        )}

        {outcome && (
          <section aria-label="Temporary artifact" className="space-y-3 rounded-md border p-3">
            <div>
              <h3 className="text-sm font-semibold">员工成果</h3>
              <p className="text-sm text-muted-foreground">
                Run {outcome.current_task_id} · {executionStateLabel(outcome.execution_state)}
              </p>
            </div>

            {!artifact ? (
              <p className="text-sm text-muted-foreground">
                员工完成 Run 后，临时成果会自动出现在这里。
              </p>
            ) : (
              <>
                <div className="flex flex-wrap items-center gap-2">
                  <a
                    className="inline-flex h-8 items-center rounded-md border px-3 text-sm font-medium hover:bg-accent"
                    href={artifact.durable_object_ref}
                    target="_blank"
                    rel="noreferrer"
                  >
                    打开临时成果 · 第 {artifact.revision} 版
                  </a>
                  <span className="text-xs text-muted-foreground">状态：{artifact.status}</span>
                </div>

                {artifact.status === "submitted" && !reviewReceipt && (
                  <div className="space-y-2">
                    <Textarea
                      aria-label="Rework feedback"
                      value={feedback}
                      onChange={(event) => setFeedback(event.target.value)}
                      placeholder="如果需要返工，请在这里写明具体修改意见。"
                      disabled={isPending}
                    />
                    <div className="flex flex-wrap gap-2">
                      <Button
                        type="button"
                        variant="outline"
                        disabled={isPending || !feedback.trim()}
                        onClick={() => reviewArtifact("changes_requested")}
                      >
                        {isPending ? "正在处理…" : "要求返工并创建下一次 Run"}
                      </Button>
                      <Button
                        type="button"
                        disabled={isPending}
                        onClick={() => reviewArtifact("approved")}
                      >
                        {isPending ? "正在处理…" : "确认通过"}
                      </Button>
                    </div>
                  </div>
                )}

                {artifact.status === "changes_requested" && (
                  <p className="text-sm text-muted-foreground">
                    已要求返工；下一次成功 Run 将自动形成第 {artifact.revision + 1} 版。
                  </p>
                )}
                {artifact.status === "approved" && (
                  <p className="text-sm text-muted-foreground">
                    临时成果已通过，等待晋升为 HiveCosm 正式成果。
                  </p>
                )}
                {artifact.formal_visible && artifact.formal_artifact_ref && (
                  <a className="text-sm underline" href={artifact.formal_artifact_ref}>
                    打开正式成果
                  </a>
                )}
              </>
            )}
          </section>
        )}

        {reviewReceipt && (
          <Alert>
            <AlertDescription aria-live="polite">
              {reviewReceipt.decision === "changes_requested"
                ? `返工任务已创建：${reviewReceipt.rework_task_id ?? "等待回读"}`
                : "成果已确认通过。"}
            </AlertDescription>
          </Alert>
        )}
      </CardContent>
    </Card>
  );
}

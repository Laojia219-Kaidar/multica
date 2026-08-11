"use client";

import { useMemo, useState } from "react";
import { MessageSquare, RefreshCw } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { NativeSelect, NativeSelectOption } from "@multica/ui/components/ui/native-select";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { Badge } from "@multica/ui/components/ui/badge";
import { useQuery } from "@tanstack/react-query";
import type { ChatSession, CompanyOpsOutcomeSummary } from "@multica/core/types";
import { chatSessionsOptions } from "@multica/core/chat/queries";
import { useWorkspacePaths } from "@multica/core/paths";
import { useT } from "../i18n";
import type { OutcomeActions } from "./outcome-actions";

export interface SessionGateProps {
  wsId: string;
  summary: CompanyOpsOutcomeSummary;
  sessionId: string;
  onSessionIdChange: (sessionId: string) => void;
  actions: OutcomeActions;
  onReread: () => void;
  rereading: boolean;
}

/**
 * The review session gate. Only the current owner's ACTIVE conversations whose
 * agent matches the outcome's local agent are offered; the user must pick one
 * explicitly before any review/promotion writer is reachable. Never guesses the
 * most recent session. When no qualified session exists, a "open / start a
 * conversation" action is offered instead of faking success.
 */
export function OutcomeSessionGate({
  wsId,
  summary,
  sessionId,
  onSessionIdChange,
  actions,
  onReread,
  rereading,
}: SessionGateProps) {
  const { t } = useT("outcomes");
  const wsPaths = useWorkspacePaths();
  const { data: sessions = [] } = useQuery({
    ...chatSessionsOptions(wsId),
    enabled: !!wsId,
  });

  const qualifiedSessions = useMemo<ChatSession[]>(
    () =>
      sessions.filter(
        (s) => s.status === "active" && s.agent_id === summary.execution_target.local_agent_id,
      ),
    [sessions, summary.execution_target.local_agent_id],
  );

  const selectedSession = useMemo(
    () => qualifiedSessions.find((s) => s.id === sessionId) ?? null,
    [qualifiedSessions, sessionId],
  );

  const gateOpen = !!selectedSession;

  const [feedback, setFeedback] = useState("");
  const [reviewFailed, setReviewFailed] = useState(false);
  const [promotionFailed, setPromotionFailed] = useState(false);

  const runReview = (decision: "changes_requested" | "approved") => {
    setReviewFailed(false);
    setPromotionFailed(false);
    const session = selectedSession;
    if (!session) return;
    void actions
      .onReviewArtifact({
        summary,
        sessionId: session.id,
        decision,
        feedback,
      })
      .then(() => {
        if (decision === "changes_requested") setFeedback("");
      })
      .catch(() => {
        setReviewFailed(true);
      });
  };

  const runPromote = () => {
    setPromotionFailed(false);
    setReviewFailed(false);
    const session = selectedSession;
    if (!session) return;
    void actions.onPromoteArtifact({ summary, sessionId: session.id }).catch(() => {
      setPromotionFailed(true);
    });
  };

  if (qualifiedSessions.length === 0) {
    return (
      <div className="rounded-md border bg-muted/30 p-3">
        <div className="flex items-center gap-2 text-sm font-medium">
          <MessageSquare className="size-4 text-muted-foreground" />
          <span>{t(($) => $.session.no_session_title)}</span>
        </div>
        <p className="mt-1 text-xs text-muted-foreground">
          {t(($) => $.session.no_session_description)}
        </p>
        <Button
          size="sm"
          variant="outline"
          className="mt-3 gap-1.5"
          render={
            <a href={`${wsPaths.chat()}?agent=${encodeURIComponent(summary.execution_target.local_agent_id)}`} />
          }
        >
          <MessageSquare className="size-3.5" />
          {t(($) => $.session.open_chat)}
        </Button>
      </div>
    );
  }

  return (
    <div className="space-y-3">
      <div className="rounded-md border bg-muted/30 p-3">
        <div className="flex items-center gap-2 text-sm font-medium">
          <MessageSquare className="size-4 text-muted-foreground" />
          <span>{t(($) => $.session.gate_title)}</span>
        </div>
        <p className="mt-1 text-xs text-muted-foreground">
          {t(($) => $.session.gate_hint)}
        </p>
        <NativeSelect
          value={sessionId}
          onChange={(e) => onSessionIdChange(e.target.value)}
          className="mt-3"
          aria-label={t(($) => $.session.select_placeholder)}
        >
          <NativeSelectOption value="">
            {t(($) => $.session.select_placeholder)}
          </NativeSelectOption>
          {qualifiedSessions.map((s) => (
            <NativeSelectOption key={s.id} value={s.id}>
              {s.title || s.id}
            </NativeSelectOption>
          ))}
        </NativeSelect>
      </div>

      {gateOpen ? (
        <div className="space-y-2 rounded-md border p-3">
          <div className="space-y-1.5">
            <label
              htmlFor="outcome-review-feedback"
              className="block text-xs font-medium text-muted-foreground"
            >
              {t(($) => $.actions.feedback_label)}
            </label>
            <Textarea
              id="outcome-review-feedback"
              value={feedback}
              onChange={(e) => {
                setFeedback(e.target.value);
                setReviewFailed(false);
              }}
              placeholder={t(($) => $.actions.feedback_placeholder)}
              rows={3}
            />
          </div>
          {(reviewFailed || promotionFailed) && (
            <p className="text-xs text-destructive" role="alert">
              {reviewFailed
                ? t(($) => $.actions.review_failed)
                : t(($) => $.actions.promotion_failed)}
            </p>
          )}
          <div className="flex flex-wrap gap-2">
            <Button
              size="sm"
              variant="outline"
              disabled={actions.reviewPending}
              onClick={() => runReview("changes_requested")}
            >
              {t(($) => $.actions.request_rework)}
            </Button>
            <Button
              size="sm"
              variant="default"
              disabled={actions.reviewPending}
              onClick={() => runReview("approved")}
            >
              {t(($) => $.actions.approve)}
            </Button>
            <Button
              size="sm"
              variant="secondary"
              disabled={actions.promotionPending}
              onClick={runPromote}
            >
              {t(($) => $.actions.promote)}
            </Button>
            <Button
              size="sm"
              variant="ghost"
              disabled={rereading}
              onClick={onReread}
              className="gap-1.5"
            >
              <RefreshCw className={rereading ? "size-3.5 animate-spin" : "size-3.5"} />
              {t(($) => $.actions.reread)}
            </Button>
          </div>
          <Badge variant="outline" className="text-[10px]">
            {selectedSession.title || selectedSession.id}
          </Badge>
        </div>
      ) : (
        <p className="text-xs text-muted-foreground">
          {t(($) => $.session.session_required)}
        </p>
      )}
    </div>
  );
}
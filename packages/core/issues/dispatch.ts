import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { issueKeys } from "./queries";
import type {
  IssueDispatchPreview,
  IssueDispatchReceipt,
  IssueStopReceipt,
  IssueReviewReceipt,
} from "../types";

/**
 * Owner issue control-plane hooks (Lane A dispatch view). Each write returns an
 * operation receipt; the dispatch preview is read-only and never mutates.
 */

export function useIssueDispatchPreview(issueId: string | null | undefined) {
  return useQuery({
    queryKey: ["issue-dispatch-preview", issueId ?? ""],
    queryFn: async () => {
      const res = await api.previewIssueDispatch(issueId as string);
      return res.preview;
    },
    enabled: !!issueId,
  });
}

function invalidateIssue(issueId: string) {
  return (qc: ReturnType<typeof useQueryClient>) => {
    qc.invalidateQueries({ queryKey: ["issue-dispatch-preview", issueId] });
    qc.invalidateQueries({ queryKey: issueKeys.detail("", issueId) });
  };
}

export function useIssueDispatch(issueId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (data?: { idempotency_key?: string; handoff_note?: string }) =>
      api.dispatchIssue(issueId, data),
    onSettled: () => invalidateIssue(issueId)(qc),
  });
}

export function useIssueStop(issueId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => api.stopIssue(issueId),
    onSettled: () => invalidateIssue(issueId)(qc),
  });
}

export function useIssueSendToReview(issueId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => api.sendIssueToReview(issueId),
    onSettled: () => invalidateIssue(issueId)(qc),
  });
}

export type {
  IssueDispatchPreview,
  IssueDispatchReceipt,
  IssueStopReceipt,
  IssueReviewReceipt,
};

"use client";

import { useRef } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import type {
  CompanyOpsArtifactPromotionCommand,
  CompanyOpsArtifactPromotionReceipt,
  CompanyOpsArtifactReviewCommand,
  CompanyOpsArtifactReviewReceipt,
  CompanyOpsOutcomeSummary,
  CompanyOpsWorkContextRequest,
} from "@multica/core/types";
import { outcomeKeys } from "./outcome-queries";

/**
 * Build the exact WorkOrder/Employee/Binding/ExecutionTarget selectors the
 * review and promotion writers consume from an outcome summary. The session
 * must be explicitly selected by the user — never guessed from recent activity.
 */
export function outcomeSelectors(
  summary: CompanyOpsOutcomeSummary,
  sessionId: string,
): CompanyOpsWorkContextRequest {
  return {
    work_order_source_ref: summary.work_order.source_ref,
    employee_id: summary.employee.id,
    identity_binding_id: summary.identity_binding.id,
    agent_id: summary.execution_target.local_agent_id,
    session_id: sessionId,
  };
}

/** The active candidate artifact, or null when the outcome has no artifact yet. */
export function outcomeCandidateId(
  summary: CompanyOpsOutcomeSummary,
): string | null {
  return summary.active_artifact?.id ?? null;
}

const CANONICAL_UUID_PATTERN =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
const SHA256_DIGEST_PATTERN = /^sha256:([0-9a-f]{64})$/;

/**
 * Resolve the current candidate's same-origin read-only object URL. The exact
 * workspace, candidate and digest are all bound into the materialized key;
 * anything else stays non-clickable instead of becoming an arbitrary link.
 */
export function outcomeCandidatePreviewRef(
  summary: CompanyOpsOutcomeSummary,
  wsId: string,
): string | null {
  const candidate = summary.active_artifact;
  if (
    !candidate ||
    !CANONICAL_UUID_PATTERN.test(wsId) ||
    !CANONICAL_UUID_PATTERN.test(candidate.id)
  ) {
    return null;
  }
  const digest = SHA256_DIGEST_PATTERN.exec(candidate.digest);
  if (!digest) return null;
  const expected = `/uploads/workspaces/${wsId}/artifact-candidates/${candidate.id}/${digest[1]}`;
  return candidate.durable_object_ref === expected ? expected : null;
}

/** The artifact is promotable when it already passed an explicit approval. */
export function isOutcomePromotable(
  summary: CompanyOpsOutcomeSummary,
): boolean {
  const status = summary.active_artifact?.status;
  return (
    status === "approved" ||
    status === "promotion_failed" ||
    status === "promotion_succeeded"
  );
}

/**
 * Formal status is only shown when all three hold at once: the active
 * artifact is in authority_readback_confirmed, is formally visible, and
 * carries a non-empty formal reference.
 */
export function isOutcomeFormal(
  summary: CompanyOpsOutcomeSummary,
): boolean {
  const artifact = summary.active_artifact;
  if (!artifact) return false;
  return (
    artifact.status === "authority_readback_confirmed" &&
    artifact.formal_visible === true &&
    Boolean(artifact.formal_artifact_ref)
  );
}

export interface OutcomeReviewInput {
  summary: CompanyOpsOutcomeSummary;
  sessionId: string;
  decision: "changes_requested" | "approved";
  feedback: string;
}

export interface OutcomePromoteInput {
  summary: CompanyOpsOutcomeSummary;
  sessionId: string;
}

export interface OutcomeActions {
  onReviewArtifact: (
    input: OutcomeReviewInput,
  ) => Promise<CompanyOpsArtifactReviewReceipt>;
  onPromoteArtifact: (
    input: OutcomePromoteInput,
  ) => Promise<CompanyOpsArtifactPromotionReceipt>;
  reviewPending: boolean;
  promotionPending: boolean;
}

/**
 * Idempotent review/promotion writers for a single outcome. The browser-side
 * stable UUID (review_id / promotion_id) is kept per exact command fingerprint
 * so a network retry of the same command reuses the same id, matching the
 * server-side idempotency key. On success (and on a failed promotion, so the
 * refreshed authority state can be re-read) the outcome detail cache is
 * invalidated.
 */
export function useOutcomeActions(wsId: string, commandId: string): OutcomeActions {
  const queryClient = useQueryClient();
  const reviewRef = useRef<{ fingerprint: string; reviewId: string } | null>(null);
  const promotionRef = useRef<{
    fingerprint: string;
    promotionId: string;
  } | null>(null);

  const invalidateDetail = async () => {
    await queryClient.invalidateQueries({
      queryKey: outcomeKeys.detail(wsId, commandId),
    });
  };

  const reviewMutation = useMutation({
    mutationFn: (command: CompanyOpsArtifactReviewCommand) =>
      api.reviewCompanyOpsArtifact(command),
    retry: false,
    onSuccess: async () => {
      await invalidateDetail();
    },
  });

  const promotionMutation = useMutation({
    mutationFn: (command: CompanyOpsArtifactPromotionCommand) =>
      api.promoteCompanyOpsArtifact(command),
    retry: false,
    onSuccess: async () => {
      await invalidateDetail();
    },
    onError: async () => {
      await invalidateDetail();
    },
  });

  const onReviewArtifact = async (
    input: OutcomeReviewInput,
  ): Promise<CompanyOpsArtifactReviewReceipt> => {
    const request = outcomeSelectors(input.summary, input.sessionId);
    const candidateId = outcomeCandidateId(input.summary);
    if (!candidateId) {
      throw new Error("Outcome has no active artifact candidate to review.");
    }
    const fingerprint = JSON.stringify([
      ...Object.values(request),
      candidateId,
      input.decision,
      input.feedback,
    ]);
    if (reviewRef.current?.fingerprint !== fingerprint) {
      reviewRef.current = {
        fingerprint,
        reviewId: globalThis.crypto.randomUUID(),
      };
    }
    const reviewId = reviewRef.current.reviewId;
    const receipt = await reviewMutation.mutateAsync({
      review_id: reviewId,
      ...request,
      candidate_id: candidateId,
      decision: input.decision,
      feedback: input.feedback,
    });
    if (
      receipt.review_id !== reviewId ||
      receipt.candidate_id !== candidateId ||
      receipt.decision !== input.decision
    ) {
      throw new Error("Artifact review receipt does not match the exact decision.");
    }
    return receipt;
  };

  const onPromoteArtifact = async (
    input: OutcomePromoteInput,
  ): Promise<CompanyOpsArtifactPromotionReceipt> => {
    const request = outcomeSelectors(input.summary, input.sessionId);
    const candidateId = outcomeCandidateId(input.summary);
    if (!candidateId) {
      throw new Error("Outcome has no active artifact candidate to promote.");
    }
    if (!isOutcomePromotable(input.summary)) {
      throw new Error("Artifact is not in a promotable state.");
    }
    const fingerprint = JSON.stringify([...Object.values(request), candidateId]);
    if (promotionRef.current?.fingerprint !== fingerprint) {
      promotionRef.current = {
        fingerprint,
        promotionId: globalThis.crypto.randomUUID(),
      };
    }
    const promotionId = promotionRef.current.promotionId;
    const receipt = await promotionMutation.mutateAsync({
      promotion_id: promotionId,
      ...request,
      candidate_id: candidateId,
    });
    if (
      receipt.promotion_id !== promotionId ||
      receipt.candidate_id !== candidateId
    ) {
      throw new Error("Artifact promotion receipt does not match the exact command.");
    }
    return receipt;
  };

  return {
    onReviewArtifact,
    onPromoteArtifact,
    reviewPending: reviewMutation.isPending,
    promotionPending: promotionMutation.isPending,
  };
}

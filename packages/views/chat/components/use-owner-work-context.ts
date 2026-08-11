"use client";

import { useRef } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError } from "@multica/core/api";
import { parseHiveCrewWorkContextUrl } from "@multica/core/paths";
import type {
  CompanyOpsAssignmentCommand,
  CompanyOpsAssignmentDispatchReceipt,
  CompanyOpsArtifactReviewCommand,
  CompanyOpsArtifactReviewReceipt,
  CompanyOpsArtifactPromotionCommand,
  CompanyOpsArtifactPromotionReceipt,
  CompanyOpsOwnerWorkContext,
  CompanyOpsWorkContextRequest,
} from "@multica/core/types";
import type {
  OwnerAssignmentCommand,
  OwnerAssignmentWriter,
  OwnerArtifactReviewWriter,
  OwnerArtifactPromotionWriter,
  OwnerWorkContext,
} from "./owner-work-context-card";

const WORK_CONTEXT_QUERY_KEYS = [
  "employee_id",
  "identity_binding_id",
  "agent_id",
  "work_order_source_ref",
  "session_id",
] as const;

export const ownerWorkContextKeys = {
  all: (wsId: string) => ["company-ops", wsId, "owner-work-context"] as const,
  detail: (wsId: string, request: CompanyOpsWorkContextRequest | null) =>
    [
      ...ownerWorkContextKeys.all(wsId),
      request?.work_order_source_ref ?? null,
      request?.employee_id ?? null,
      request?.identity_binding_id ?? null,
      request?.agent_id ?? null,
      request?.session_id ?? null,
    ] as const,
};

export interface OwnerWorkContextSession {
  id: string;
  agent_id: string;
}

export interface UseOwnerWorkContextOptions {
  wsId: string;
  pathname: string;
  searchParams: URLSearchParams;
  session: OwnerWorkContextSession | null;
  sessionsLoaded: boolean;
}

export interface OwnerWorkContextBinding {
  contextKey: string;
  context: OwnerWorkContext;
  onConfirmAssignment: OwnerAssignmentWriter;
  onReviewArtifact: OwnerArtifactReviewWriter;
  onPromoteArtifact: OwnerArtifactPromotionWriter;
}

type WorkContextPreflight =
  | { state: "absent" }
  | { state: "loading"; reason: string; contextKey: string }
  | { state: "invalid" | "conflict"; reason: string; contextKey: string }
  | {
      state: "valid";
      request: CompanyOpsWorkContextRequest;
      contextKey: string;
    };

const unavailableAssignmentWriter: OwnerAssignmentWriter = async () => {
  throw new Error("Owner assignment writer is unavailable.");
};

const unavailableArtifactReviewWriter: OwnerArtifactReviewWriter = async () => {
  throw new Error("Owner artifact review writer is unavailable.");
};

const unavailablePromotionWriter: OwnerArtifactPromotionWriter = async () => {
  throw new Error("Owner artifact promotion writer is unavailable.");
};

export function hasOwnerWorkContextParams(
  searchParams: URLSearchParams,
): boolean {
  return WORK_CONTEXT_QUERY_KEYS.some(
    (key) => searchParams.getAll(key).length > 0,
  );
}

function failClosed(
  contextKey: string,
  state: "invalid" | "conflict",
  reason: string,
): OwnerWorkContextBinding {
  return {
    contextKey,
    context: { state, reason },
    onConfirmAssignment: unavailableAssignmentWriter,
    onReviewArtifact: unavailableArtifactReviewWriter,
    onPromoteArtifact: unavailablePromotionWriter,
  };
}

function preflightWorkContext(
  pathname: string,
  searchParams: URLSearchParams,
  session: OwnerWorkContextSession | null,
  sessionsLoaded: boolean,
): WorkContextPreflight {
  if (!hasOwnerWorkContextParams(searchParams)) return { state: "absent" };

  const query = searchParams.toString();
  const url = query ? `${pathname}?${query}` : pathname;

  let parsed;
  try {
    parsed = parseHiveCrewWorkContextUrl(url);
  } catch (error) {
    const reason =
      error instanceof Error
        ? error.message
        : "Invalid HiveCrew work-context URL";
    return {
      state: reason.startsWith("Conflicting ") ? "conflict" : "invalid",
      reason,
      contextKey: url,
    };
  }

  const request: CompanyOpsWorkContextRequest = {
    work_order_source_ref: parsed.work_order_source_ref,
    employee_id: parsed.employee_id,
    identity_binding_id: parsed.identity_binding_id,
    agent_id: parsed.agent_id,
    session_id: parsed.session_id,
  };
  const contextKey = JSON.stringify(request);

  if (!session && !sessionsLoaded) {
    return {
      state: "loading",
      reason: `Resolving work-context session ${parsed.session_id}…`,
      contextKey,
    };
  }
  if (!session) {
    return {
      state: "invalid",
      reason: `Work-context session ${parsed.session_id} is unavailable.`,
      contextKey,
    };
  }
  if (session.id !== parsed.session_id) {
    return {
      state: "conflict",
      reason: `Session mismatch: URL session_id ${parsed.session_id} does not equal active session ${session.id}.`,
      contextKey,
    };
  }
  if (session.agent_id !== parsed.agent_id) {
    return {
      state: "conflict",
      reason: `Session Agent mismatch: URL agent_id ${parsed.agent_id} does not equal session agent_id ${session.agent_id}.`,
      contextKey,
    };
  }

  return { state: "valid", request, contextKey };
}

function resolvedContextConflict(
  resolved: CompanyOpsOwnerWorkContext,
  requested: CompanyOpsWorkContextRequest,
): string | null {
  for (const key of WORK_CONTEXT_QUERY_KEYS) {
    if (resolved.request[key] !== requested[key]) {
      return `Authority response ${key} does not match the exact URL selector.`;
    }
  }
  if (resolved.work_order.source_ref !== requested.work_order_source_ref) {
    return "Resolved WorkOrder does not match work_order_source_ref.";
  }
  if (resolved.employee.employee_id !== requested.employee_id) {
    return "Resolved Employee does not match employee_id.";
  }
  if (resolved.identity_binding.identity_binding_id !== requested.identity_binding_id) {
    return "Resolved IdentityBinding does not match identity_binding_id.";
  }
  if (resolved.agent.id !== requested.agent_id) {
    return "Resolved Agent does not match agent_id.";
  }
  if (
    resolved.session.id !== requested.session_id ||
    resolved.session.agent_id !== requested.agent_id
  ) {
    return "Resolved session does not match the exact session and Agent tuple.";
  }
  if (resolved.identity_binding.active !== true) {
    return "Resolved IdentityBinding is not active.";
  }
  if (
    resolved.identity_binding.employee_ref !==
    resolved.employee.authority.source_ref
  ) {
    return "IdentityBinding employee_ref does not match Employee authority.";
  }
  if (
    resolved.identity_binding.agent_ref !== resolved.agent.authority.source_ref
  ) {
    return "IdentityBinding agent_ref does not match Agent authority.";
  }
  if (
    resolved.work_order.freshness !== "current" ||
    resolved.employee.authority.freshness !== "current" ||
    resolved.identity_binding.authority.freshness !== "current" ||
    resolved.agent.authority.freshness !== "current"
  ) {
    return "Owner work context contains stale authority.";
  }
  if (
    (resolved.projection_state === "projected" && resolved.issue === null) ||
    (resolved.projection_state === "not_projected" && resolved.issue !== null)
  ) {
    return "Issue projection state conflicts with the resolved Issue.";
  }
  return null;
}

function assignmentFailureState(error: unknown): "invalid" | "conflict" {
  return error instanceof ApiError && error.status === 409
    ? "conflict"
    : "invalid";
}

function exactCommandFingerprint(command: OwnerAssignmentCommand): string {
  return JSON.stringify([
    command.work_order_source_ref,
    command.employee_id,
    command.identity_binding_id,
    command.agent_id,
    command.session_id,
    command.handoff_note,
  ]);
}

function assertExactAssignmentCommand(
	command: CompanyOpsWorkContextRequest,
  request: CompanyOpsWorkContextRequest,
): void {
  for (const key of WORK_CONTEXT_QUERY_KEYS) {
    if (command[key] !== request[key]) {
      throw new Error(`Assignment ${key} does not match the resolved context.`);
    }
  }
}

function assertExactDispatchReceipt(
  receipt: CompanyOpsAssignmentDispatchReceipt,
  commandId: string,
): void {
  if (receipt.command_id !== commandId) {
    throw new Error("Assignment receipt command_id does not match the command.");
  }
  if (receipt.execution_receipt.state !== "awaiting_claim") {
    throw new Error("Assignment receipt has an unsupported execution state.");
  }
  if (receipt.execution_receipt.task_id !== receipt.initial_task_id) {
    throw new Error(
      "Assignment receipt task_id does not match the initial_task_id.",
    );
  }
}

export function useOwnerWorkContext({
  wsId,
  pathname,
  searchParams,
  session,
  sessionsLoaded,
}: UseOwnerWorkContextOptions): OwnerWorkContextBinding | null {
  const preflight = preflightWorkContext(
    pathname,
    searchParams,
    session,
    sessionsLoaded,
  );
  const request = preflight.state === "valid" ? preflight.request : null;
  const queryClient = useQueryClient();
  const commandRef = useRef<{
    fingerprint: string;
    commandId: string;
  } | null>(null);
  const reviewRef = useRef<{
    fingerprint: string;
    reviewId: string;
  } | null>(null);
  const promotionRef = useRef<{
    fingerprint: string;
    promotionId: string;
  } | null>(null);

  const contextQuery = useQuery({
    queryKey: ownerWorkContextKeys.detail(wsId, request),
    queryFn: async () => {
      if (!request) throw new Error("Owner work-context selectors are invalid.");
      return api.getCompanyOpsWorkContext(request);
    },
    enabled: request !== null,
    retry: false,
  });
  const assignmentMutation = useMutation({
    mutationFn: (command: CompanyOpsAssignmentCommand) =>
      api.createCompanyOpsAssignment(command),
    retry: false,
    onSuccess: async (_receipt, command) => {
      await queryClient.invalidateQueries({
        queryKey: ownerWorkContextKeys.detail(wsId, command),
        exact: true,
      });
    },
  });
  const artifactReviewMutation = useMutation({
    mutationFn: (command: CompanyOpsArtifactReviewCommand) =>
      api.reviewCompanyOpsArtifact(command),
    retry: false,
    onSuccess: async (_receipt, command) => {
      await queryClient.invalidateQueries({
        queryKey: ownerWorkContextKeys.detail(wsId, command),
        exact: true,
      });
    },
  });
  const artifactPromotionMutation = useMutation({
    mutationFn: (command: CompanyOpsArtifactPromotionCommand) =>
      api.promoteCompanyOpsArtifact(command),
    retry: false,
    onSuccess: async (_receipt, command) => {
      await queryClient.invalidateQueries({
        queryKey: ownerWorkContextKeys.detail(wsId, command),
        exact: true,
      });
    },
    onError: async (_error, command) => {
      await queryClient.invalidateQueries({
        queryKey: ownerWorkContextKeys.detail(wsId, command),
        exact: true,
      });
    },
  });

  if (preflight.state !== "valid") {
    if (preflight.state === "absent") return null;
    if (preflight.state === "loading") {
      return {
        contextKey: `${wsId}:${preflight.contextKey}`,
        context: { state: "loading", reason: preflight.reason },
        onConfirmAssignment: unavailableAssignmentWriter,
        onReviewArtifact: unavailableArtifactReviewWriter,
        onPromoteArtifact: unavailablePromotionWriter,
      };
    }
    return failClosed(
      `${wsId}:${preflight.contextKey}`,
      preflight.state,
      preflight.reason,
    );
  }

  const exactRequest = preflight.request;
  const contextKey = `${wsId}:${preflight.contextKey}`;
  if (contextQuery.isPending) {
    return {
      contextKey,
      context: {
        state: "loading",
        reason: "Resolving authoritative Owner work context…",
      },
      onConfirmAssignment: unavailableAssignmentWriter,
      onReviewArtifact: unavailableArtifactReviewWriter,
      onPromoteArtifact: unavailablePromotionWriter,
    };
  }
  if (contextQuery.error) {
    return failClosed(
      contextKey,
      assignmentFailureState(contextQuery.error),
      contextQuery.error instanceof Error
        ? contextQuery.error.message
        : "Owner work-context authority is unavailable.",
    );
  }

  const resolved = contextQuery.data;
  if (!resolved) {
    return failClosed(
      contextKey,
      "invalid",
      "Owner work-context authority returned no resolved context.",
    );
  }
  const conflict = resolvedContextConflict(resolved, exactRequest);
  if (conflict) return failClosed(contextKey, "conflict", conflict);

  const onConfirmAssignment: OwnerAssignmentWriter = async (command) => {
    assertExactAssignmentCommand(command, exactRequest);
    const fingerprint = exactCommandFingerprint(command);
    if (commandRef.current?.fingerprint !== fingerprint) {
      commandRef.current = {
        fingerprint,
        commandId: globalThis.crypto.randomUUID(),
      };
    }
    const commandId = commandRef.current.commandId;
    const receipt = await assignmentMutation.mutateAsync({
      command_id: commandId,
      ...command,
    });
    assertExactDispatchReceipt(receipt, commandId);
    return receipt;
  };

  const onReviewArtifact: OwnerArtifactReviewWriter = async (command) => {
    assertExactAssignmentCommand(command, exactRequest);
    const activeCandidate = resolved.outcome?.artifact;
    if (!activeCandidate || activeCandidate.id !== command.candidate_id) {
      throw new Error("Artifact review candidate does not match the active outcome.");
    }
    const fingerprint = JSON.stringify([
      ...WORK_CONTEXT_QUERY_KEYS.map((key) => command[key]),
      command.candidate_id,
      command.decision,
      command.feedback,
    ]);
    if (reviewRef.current?.fingerprint !== fingerprint) {
      reviewRef.current = {
        fingerprint,
        reviewId: globalThis.crypto.randomUUID(),
      };
    }
    const reviewId = reviewRef.current.reviewId;
    const receipt: CompanyOpsArtifactReviewReceipt =
      await artifactReviewMutation.mutateAsync({
        review_id: reviewId,
        ...command,
      });
    if (
      receipt.review_id !== reviewId ||
      receipt.candidate_id !== command.candidate_id ||
      receipt.decision !== command.decision
    ) {
      throw new Error("Artifact review receipt does not match the exact decision.");
    }
    return receipt;
  };

  const onPromoteArtifact: OwnerArtifactPromotionWriter = async (command) => {
    assertExactAssignmentCommand(command, exactRequest);
    const activeCandidate = resolved.outcome?.artifact;
    if (!activeCandidate || activeCandidate.id !== command.candidate_id) {
      throw new Error("Artifact promotion candidate does not match the active outcome.");
    }
    if (
      activeCandidate.status !== "approved" &&
      activeCandidate.status !== "promotion_failed" &&
      activeCandidate.status !== "promotion_succeeded"
    ) {
      throw new Error("Artifact is not in a promotable state.");
    }
    const fingerprint = JSON.stringify([
      ...WORK_CONTEXT_QUERY_KEYS.map((key) => command[key]),
      command.candidate_id,
    ]);
    if (promotionRef.current?.fingerprint !== fingerprint) {
      promotionRef.current = {
        fingerprint,
        promotionId: globalThis.crypto.randomUUID(),
      };
    }
    const promotionId = promotionRef.current.promotionId;
    const receipt: CompanyOpsArtifactPromotionReceipt =
      await artifactPromotionMutation.mutateAsync({
        promotion_id: promotionId,
        ...command,
      });
    if (
      receipt.promotion_id !== promotionId ||
      receipt.candidate_id !== command.candidate_id
    ) {
      throw new Error("Artifact promotion receipt does not match the exact command.");
    }
    return receipt;
  };

  return {
    contextKey,
    context: { state: "ready", data: resolved },
    onConfirmAssignment,
    onReviewArtifact,
    onPromoteArtifact,
  };
}

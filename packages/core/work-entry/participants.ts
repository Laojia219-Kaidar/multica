// Pure, dependency-free helpers for the project participant / executor panel.
// Kept in packages/core so both the panel and the queries layer share one
// source of truth and the mapping is trivially unit-testable.

import type {
  WorkActorType,
  WorkforceBaseRuntimeRow,
  WorkParticipant,
} from "../types/work-entry";

export const WORK_ACTOR_TYPES: readonly WorkActorType[] = [
  "registered_employee",
  "external_agent",
  "human_operator",
  "automation_service",
  "observed_unclaimed_actor",
] as const;

/** zh-Hans labels for the closed five-value actor enumeration (VC-02). */
export const ACTOR_TYPE_LABELS: Record<WorkActorType, string> = {
  registered_employee: "注册员工",
  external_agent: "外部智能体",
  human_operator: "人工操作者",
  automation_service: "自动化服务",
  observed_unclaimed_actor: "观测未归属行动者",
};

/** Returns the zh-Hans label, falling back to the raw value for unknown input. */
export function actorTypeLabel(actorType: string): string {
  return ACTOR_TYPE_LABELS[actorType as WorkActorType] ?? actorType;
}

/**
 * Maps one workforce-base-runtime row (Employee → Agent → Runtime → Base) into
 * the participant read model. The workforce join is employee-centric: every
 * row is a `registered_employee`, `actor_id` == `employee_id` (DE-*), and the
 * dimensions not present in that join (carrier / base registry / session /
 * next_action) are left undefined so the UI can mark them "待后端部署".
 *
 * `base_machine_title` is the physical machine title, so it lands on `host_id`
 * (Host is the observed truth; Base is the governed registry, WORK-ACTOR-
 * CONTRACT §1) rather than `base_id`.
 */
export function participantFromWorkforceRow(
  row: WorkforceBaseRuntimeRow,
): WorkParticipant {
  return {
    actor_type: "registered_employee",
    actor_id: row.employee_id,
    employee_id: row.employee_id,
    runtime_id: row.runtime_id || undefined,
    model_ref: row.model || undefined,
    host_id: row.base_machine_title || undefined,
  };
}

/** True when a participant dimension is not yet available in the read model. */
export function isParticipantFieldPending(value: string | undefined): boolean {
  return value == null || value.trim() === "";
}

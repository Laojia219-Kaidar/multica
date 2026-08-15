import { describe, expect, it } from "vitest";
import {
  ACTOR_TYPE_LABELS,
  actorTypeLabel,
  isParticipantFieldPending,
  participantFromWorkforceRow,
  WORK_ACTOR_TYPES,
} from "./participants";

describe("actorTypeLabel", () => {
  it("maps every closed actor type to a zh-Hans label", () => {
    for (const actorType of WORK_ACTOR_TYPES) {
      expect(actorTypeLabel(actorType)).toBe(ACTOR_TYPE_LABELS[actorType]);
    }
  });

  it("falls back to the raw value for unknown actor types", () => {
    expect(actorTypeLabel("mystery_actor")).toBe("mystery_actor");
  });
});

describe("participantFromWorkforceRow", () => {
  it("projects an employee into a registered_employee participant", () => {
    const p = participantFromWorkforceRow({
      employee_id: "DE-ALICE",
      workforce_agent_id: "KT-1",
      hivecrew_agent_id: "agent-uuid",
      runtime_id: "runtime-uuid",
      base_machine_title: "Mac mini M5X",
      agent_status: "active",
      runtime_status: "online",
      model: "qwen3.6-27b",
    });

    expect(p.actor_type).toBe("registered_employee");
    expect(p.actor_id).toBe("DE-ALICE");
    expect(p.employee_id).toBe("DE-ALICE");
    expect(p.runtime_id).toBe("runtime-uuid");
    expect(p.model_ref).toBe("qwen3.6-27b");
    // base_machine_title is the physical host, not the governed base registry.
    expect(p.host_id).toBe("Mac mini M5X");
    expect(p.base_id).toBeUndefined();
    // Dimensions absent from the workforce join stay pending.
    expect(p.carrier_id).toBeUndefined();
    expect(p.session_id).toBeUndefined();
    expect(p.next_action).toBeUndefined();
  });

  it("drops empty optional fields from the join", () => {
    const p = participantFromWorkforceRow({
      employee_id: "DE-BOB",
      workforce_agent_id: "EXT-9",
    });

    expect(p.employee_id).toBe("DE-BOB");
    expect(p.runtime_id).toBeUndefined();
    expect(p.model_ref).toBeUndefined();
    expect(p.host_id).toBeUndefined();
  });
});

describe("isParticipantFieldPending", () => {
  it("treats missing and blank values as pending", () => {
    expect(isParticipantFieldPending(undefined)).toBe(true);
    expect(isParticipantFieldPending("")).toBe(true);
    expect(isParticipantFieldPending("   ")).toBe(true);
  });

  it("treats a concrete value as present", () => {
    expect(isParticipantFieldPending("runtime-uuid")).toBe(false);
  });
});

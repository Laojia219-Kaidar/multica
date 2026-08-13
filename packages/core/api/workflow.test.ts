import { describe, expect, it } from "vitest";
import {
  ControlReceiptSchema,
  WorkflowDefinitionSchema,
  WorkflowEventSchema,
  WorkflowInstanceSchema,
} from "./workflow";

const def = {
  id: "hivecrew.project-lifecycle",
  version: 1,
  risk: "standard",
  stages: [
    { name: "operate", sla_seconds: 604800 },
    { name: "review_repair" },
  ],
};

const inst = {
  id: "plc-1",
  definition_id: "hivecrew.project-lifecycle",
  definition_version: 1,
  context: { project_id: "PRJ-1" },
  stage_index: 1,
  status: "running",
};

describe("workflow wire contract", () => {
  it("accepts a definition and instance", () => {
    expect(WorkflowDefinitionSchema.parse(def).risk).toBe("standard");
    expect(WorkflowInstanceSchema.parse(inst).stage_index).toBe(1);
  });

  it("rejects unknown keys (strict wire)", () => {
    expect(() => WorkflowDefinitionSchema.parse({ ...def, extra: true })).toThrow();
  });

  it("rejects invalid risk tier", () => {
    expect(() => WorkflowDefinitionSchema.parse({ ...def, risk: "warp" })).toThrow();
  });

  it("rejects empty stages", () => {
    expect(() => WorkflowDefinitionSchema.parse({ ...def, stages: [] })).toThrow();
  });

  it("accepts an event and a receipt", () => {
    const ev = {
      sequence: 1,
      instance_id: "plc-1",
      kind: "workflow.stage_advanced",
      source_ref: "instance://plc-1",
      actor: "a1",
      occurred_at: "2026-08-13T12:00:00Z",
      observed_at: "2026-08-13T12:00:00Z",
      idempotency_key: "k1",
    };
    expect(WorkflowEventSchema.parse(ev).kind).toBe("workflow.stage_advanced");
    const receipt = {
      command: "advance",
      instance_id: "plc-1",
      idempotency_key: "k1",
      accepted: true,
      changed: true,
      reason: "",
    };
    expect(ControlReceiptSchema.parse(receipt).accepted).toBe(true);
  });
});

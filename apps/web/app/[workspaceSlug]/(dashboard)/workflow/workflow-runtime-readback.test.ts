import { describe, expect, it } from "vitest";
import type { PublishedWorkflowDefinitionVersion, WorkflowEvent, WorkflowInstance } from "@multica/core/api/workflow";
import { toWorkflowReceiptViews, toWorkflowRuntimeReadback } from "./workflow-runtime-readback";

const definition: PublishedWorkflowDefinitionVersion = {
  definition_id: "content.wechat-production-package.v1",
  workspace_id: "5b1521ee-e5d8-4b85-9491-27d77c7d1010",
  project_id: "2a13b313-9806-4d31-b99a-2b7a91f109b1",
  version: 1,
  risk: "standard",
  stages: [{ name: "选题" }, { name: "生产" }, { name: "审核" }],
  graph: {
    nodes: [
      { id: "topic", kind: "agent_task", name: "选题" },
      { id: "produce", kind: "agent_task", name: "生产" },
      { id: "review", kind: "approval", name: "审核" },
    ],
    edges: [
      { id: "topic-produce", from: "topic", to: "produce" },
      { id: "produce-review", from: "produce", to: "review" },
    ],
  },
  digest: "sha256:workflow-canary",
  created_at: "2026-08-15T00:00:00.000Z",
  published_at: "2026-08-15T00:00:00.000Z",
};

const instance: WorkflowInstance = {
  id: "workflow-instance-1",
  workspace_id: definition.workspace_id,
  definition_id: definition.definition_id,
  definition_version: definition.version,
  context: { project_id: definition.project_id },
  stage_index: 1,
  status: "running",
};

describe("workflow runtime readback", () => {
  it("projects only the persisted stage index onto the immutable graph", () => {
    expect(toWorkflowRuntimeReadback(instance, definition)).toMatchObject({
      instanceId: instance.id,
      status: "running",
      nodes: [
        { nodeId: "topic", status: "passed" },
        { nodeId: "produce", status: "running" },
        { nodeId: "review", status: "not_started" },
      ],
    });
  });

  it("does not fabricate a graph readback for a different immutable version", () => {
    expect(toWorkflowRuntimeReadback(instance, { ...definition, version: 2 })).toBeUndefined();
  });

  it("maps the durable event ledger to read-only receipts and preserves a rejection", () => {
    const events: WorkflowEvent[] = [
      {
        sequence: 1,
        instance_id: instance.id,
        kind: "workflow.started",
        source_ref: "workflow:start",
        actor: "system",
        occurred_at: "2026-08-15T00:00:01.000Z",
        observed_at: "2026-08-15T00:00:01.000Z",
        idempotency_key: "start-key",
      },
      {
        sequence: 2,
        instance_id: instance.id,
        kind: "workflow.advance_rejected",
        source_ref: "workflow:advance?reason=approval-required",
        actor: "system",
        occurred_at: "2026-08-15T00:00:02.000Z",
        observed_at: "2026-08-15T00:00:02.000Z",
        idempotency_key: "reject-key",
      },
    ];

    expect(toWorkflowReceiptViews(events)).toMatchObject([
      { status: "accepted", label: "启动工作流", idempotencyKey: "start-key" },
      { status: "rejected", label: "推进工作流阶段", idempotencyKey: "reject-key" },
    ]);
  });
});

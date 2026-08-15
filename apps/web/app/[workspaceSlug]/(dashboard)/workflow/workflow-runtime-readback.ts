import type { PublishedWorkflowDefinitionVersion, WorkflowEvent, WorkflowInstance } from "@multica/core/api/workflow";
import type { RuntimeNode, WorkflowNodeStatus, WorkflowReceiptView, WorkflowRuntime } from "@multica/core/workflow";

function currentNodeStatus(status: WorkflowInstance["status"]): WorkflowNodeStatus {
  switch (status) {
    case "completed": return "passed";
    case "paused": return "blocked";
    case "stopped": return "stopped";
    case "failed": return "failed";
    default: return "running";
  }
}

/**
 * Projects the existing persisted instance state onto the immutable graph.
 * This is deliberately read-only: it does not invent Task, Run, approval, or
 * Outcome state that the workflow kernel has not recorded.
 */
export function toWorkflowRuntimeReadback(
  instance: WorkflowInstance,
  definition: PublishedWorkflowDefinitionVersion | undefined,
): WorkflowRuntime | undefined {
  if (!definition || definition.definition_id !== instance.definition_id || definition.version !== instance.definition_version) return undefined;

  const current = Math.min(Math.max(instance.stage_index, 0), Math.max(definition.graph.nodes.length - 1, 0));
  const nodes: RuntimeNode[] = definition.graph.nodes.map((node, index) => ({
    nodeId: node.id,
    status: index < current ? "passed" : index === current ? currentNodeStatus(instance.status) : "not_started",
  }));

  return {
    instanceId: instance.id,
    definitionId: instance.definition_id,
    version: instance.definition_version,
    status: currentNodeStatus(instance.status),
    nodes,
  };
}

function receiptStatus(event: WorkflowEvent): WorkflowReceiptView["status"] {
  if (event.kind === "workflow.advance_rejected") return "rejected";
  if (event.kind === "workflow.started" || event.kind === "workflow.stage_advanced") return "accepted";
  return "observed";
}

function receiptLabel(event: WorkflowEvent): string {
  if (event.kind === "workflow.started") return "启动工作流";
  if (event.kind === "workflow.stage_advanced") return "推进工作流阶段";
  if (event.kind === "workflow.advance_rejected") return "推进工作流阶段";
  return event.kind;
}

/** Maps the durable event ledger to a display contract without new receipt storage. */
export function toWorkflowReceiptViews(events: WorkflowEvent[]): WorkflowReceiptView[] {
  return events.map((event) => ({
    id: `${event.instance_id}:${event.sequence}:${event.idempotency_key}`,
    instanceId: event.instance_id,
    kind: "event",
    status: receiptStatus(event),
    label: receiptLabel(event),
    sourceRef: event.source_ref,
    actor: event.actor,
    occurredAt: event.occurred_at,
    observedAt: event.observed_at,
    idempotencyKey: event.idempotency_key,
  }));
}

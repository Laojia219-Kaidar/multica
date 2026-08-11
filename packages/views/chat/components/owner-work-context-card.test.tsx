// @vitest-environment jsdom

import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {
  OwnerWorkContextCard,
  type OwnerArtifactReviewWriter,
  type OwnerAssignmentCommand,
  type OwnerAssignmentReceipt,
} from "./owner-work-context-card";

const AGENT_ID = "d34db33f-4ef7-4fe1-a32d-8f24c57b07b1";
const SESSION_ID = "01972f7e-7e8d-77ef-a13d-1b0ce3e9c001";
const CANDIDATE_ID = "77777777-7777-4777-8777-777777777777";
const REQUEST = {
  work_order_source_ref:
    "hive://hivecosm/delivery/project/PRJ-HIVECREW-P2/work-order/WO-P2-001",
  employee_id: "employee-01JOWNER",
  identity_binding_id: "binding-01JOWNER",
  agent_id: AGENT_ID,
  session_id: SESSION_ID,
};
const authority = (kind: string, sourceRef: string) => ({
  kind,
  source_ref: sourceRef,
  revision: `${kind}-revision-1`,
  content_digest: `${kind}-digest-1`,
  freshness: "current",
});
const READY_DATA = {
  schema_version: "hivecrew.owner-work-context.v1" as const,
  request: REQUEST,
  work_order: authority("WorkOrder", REQUEST.work_order_source_ref),
  employee: {
    employee_id: REQUEST.employee_id,
    authority: authority("Employee", `hivecosm://employees/${REQUEST.employee_id}`),
  },
  identity_binding: {
    identity_binding_id: REQUEST.identity_binding_id,
    employee_ref: `hivecosm://employees/${REQUEST.employee_id}`,
    agent_ref: `/api/agents/${AGENT_ID}`,
    active: true,
    authority: authority("IdentityBinding", `hivecosm://identity-bindings/${REQUEST.identity_binding_id}`),
  },
  agent: {
    id: AGENT_ID,
    name: "Atlas",
    status: "idle",
    runtime_mode: "local",
    model: "gpt-5.6",
    authority: authority("Agent", `/api/agents/${AGENT_ID}`),
  },
  session: { id: SESSION_ID, agent_id: AGENT_ID, status: "active" },
  issue: null,
  projection_state: "not_projected" as const,
  observed_at: "2026-08-11T10:00:00Z",
};
const DISPATCH_RECEIPT = {
  schema_version: "hivecrew.assignment-dispatch.v1" as const,
  command_id: "44444444-4444-4444-8444-444444444444",
  issue_id: "55555555-5555-4555-8555-555555555555",
  initial_task_id: "66666666-6666-4666-8666-666666666666",
  execution_receipt: {
    state: "awaiting_claim" as const,
    task_id: "66666666-6666-4666-8666-666666666666",
  },
};

function artifactContext() {
  return {
    state: "ready" as const,
    data: {
      ...READY_DATA,
      issue: {
        id: DISPATCH_RECEIPT.issue_id,
        number: 42,
        title: "Owner operating loop",
        status: "in_progress",
      },
      projection_state: "projected" as const,
      outcome: {
        command_id: DISPATCH_RECEIPT.command_id,
        issue_id: DISPATCH_RECEIPT.issue_id,
        initial_task_id: DISPATCH_RECEIPT.initial_task_id,
        current_task_id: DISPATCH_RECEIPT.initial_task_id,
        execution_state: "completed" as const,
        artifact: {
          id: CANDIDATE_ID,
          revision: 1,
          durable_object_ref: "/uploads/workspaces/ws/artifact.md",
          digest: "sha256:artifact",
          status: "submitted" as const,
          formal_visible: false,
        },
      },
    },
  };
}

function renderCard({
  context = { state: "ready" as const, data: READY_DATA },
  onConfirmAssignment = vi
    .fn<(command: OwnerAssignmentCommand) => Promise<OwnerAssignmentReceipt>>()
    .mockResolvedValue(DISPATCH_RECEIPT),
  onReviewArtifact = vi.fn<OwnerArtifactReviewWriter>(),
}: {
  context?:
    | { state: "ready"; data: typeof READY_DATA | ReturnType<typeof artifactContext>["data"] }
    | { state: "loading" | "invalid" | "conflict"; reason: string };
  onConfirmAssignment?: (
    command: OwnerAssignmentCommand,
  ) => Promise<OwnerAssignmentReceipt>;
  onReviewArtifact?: OwnerArtifactReviewWriter;
} = {}) {
  render(
    <OwnerWorkContextCard
      context={context}
      onConfirmAssignment={onConfirmAssignment}
      onReviewArtifact={onReviewArtifact}
    />,
  );
  return { onConfirmAssignment, onReviewArtifact };
}

describe("OwnerWorkContextCard", () => {
  it("presents the employee and operator state before technical provenance", () => {
    renderCard();
    expect(screen.getByText("Atlas")).toBeInTheDocument();
    expect(screen.getByText("等待派工")).toBeInTheDocument();
    expect(screen.getByText("技术依据")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "派给这名员工" })).toBeDisabled();
  });

  it("fails closed for an invalid context", () => {
    renderCard({ context: { state: "invalid", reason: "绑定关系无效" } });
    expect(screen.getByRole("alert")).toHaveTextContent("绑定关系无效");
    expect(screen.queryByRole("button", { name: "派给这名员工" })).not.toBeInTheDocument();
  });

  it("retains the work request when assignment fails", async () => {
    const user = userEvent.setup();
    const onConfirmAssignment = vi.fn().mockRejectedValue(new Error("派工服务不可用"));
    renderCard({ onConfirmAssignment });
    const handoff = screen.getByRole("textbox", { name: "工作要求" });
    await user.type(handoff, "完成真实纵向闭环并提交证据");
    await user.click(screen.getByRole("button", { name: "派给这名员工" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("派工服务不可用");
    expect(handoff).toHaveValue("完成真实纵向闭环并提交证据");
  });

  it("dispatches the exact target and shows the real Run id", async () => {
    const user = userEvent.setup();
    const onConfirmAssignment = vi.fn().mockResolvedValue(DISPATCH_RECEIPT);
    renderCard({ onConfirmAssignment });
    await user.type(screen.getByRole("textbox", { name: "工作要求" }), "完成工作单");
    await user.click(screen.getByRole("button", { name: "派给这名员工" }));
    await waitFor(() =>
      expect(onConfirmAssignment).toHaveBeenCalledWith({
        ...REQUEST,
        handoff_note: "完成工作单",
      }),
    );
    expect(await screen.findByText(new RegExp(DISPATCH_RECEIPT.initial_task_id))).toBeInTheDocument();
  });

  it("opens the temporary artifact and creates an exact rework Run", async () => {
    const user = userEvent.setup();
    const onReviewArtifact = vi.fn<OwnerArtifactReviewWriter>().mockResolvedValue({
      schema_version: "hivecrew.artifact-review.v1",
      review_id: "88888888-8888-4888-8888-888888888888",
      event_id: "99999999-9999-4999-8999-999999999999",
      sequence: 2,
      decision: "changes_requested",
      candidate_id: CANDIDATE_ID,
      rework_task_id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
    });
    renderCard({ context: artifactContext(), onReviewArtifact });
    expect(screen.getByRole("link", { name: "打开临时成果 · 第 1 版" })).toHaveAttribute(
      "href",
      "/uploads/workspaces/ws/artifact.md",
    );
    await user.type(screen.getByRole("textbox", { name: "Rework feedback" }), "补充浏览器验收证据");
    await user.click(screen.getByRole("button", { name: "要求返工并创建下一次 Run" }));
    await waitFor(() =>
      expect(onReviewArtifact).toHaveBeenCalledWith({
        ...REQUEST,
        candidate_id: CANDIDATE_ID,
        decision: "changes_requested",
        feedback: "补充浏览器验收证据",
      }),
    );
    expect(await screen.findByText(/aaaaaaaa-aaaa-4aaa/)).toBeInTheDocument();
  });

  it("approves the exact active candidate", async () => {
    const user = userEvent.setup();
    const onReviewArtifact = vi.fn<OwnerArtifactReviewWriter>().mockResolvedValue({
      schema_version: "hivecrew.artifact-review.v1",
      review_id: "88888888-8888-4888-8888-888888888888",
      event_id: "99999999-9999-4999-8999-999999999999",
      sequence: 2,
      decision: "approved",
      candidate_id: CANDIDATE_ID,
    });
    renderCard({ context: artifactContext(), onReviewArtifact });
    await user.click(screen.getByRole("button", { name: "确认通过" }));
    expect(await screen.findByText("成果已确认通过。")).toBeInTheDocument();
  });
});

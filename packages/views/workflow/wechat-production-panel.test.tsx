import { describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import {
  WechatProductionPanel,
  type WechatProductionAuthorityView,
  type WechatProductionView,
} from "./wechat-production-panel";
import type { WechatContentProductionRequest } from "@multica/core/workflow";

const PROJECT_ID = "PRJ-WECHAT-OPS";

const authority: WechatProductionAuthorityView = {
  work_order_source_ref: "hive://hivecosm/delivery/project/PRJ-WECHAT-OPS/work-order/WO-1",
  employee_id: "EMP-001",
  identity_binding_id: "IB-001",
  agent_id: "11111111-1111-4111-8111-111111111111",
  session_id: "22222222-2222-4222-8222-222222222222",
};

const pins = [
  {
    definition_id: "content.wechat-production-package",
    version: 1,
    digest: `sha256:${"a".repeat(64)}`,
  },
];

const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

function fillValidForm() {
  fireEvent.change(screen.getByLabelText("主题"), { target: { value: "新品发布稿" } });
  fireEvent.change(screen.getByLabelText("目标"), { target: { value: "向受众说明产品价值" } });
  fireEvent.change(screen.getByLabelText("受众"), { target: { value: "公众号订阅用户" } });
  fireEvent.change(screen.getByLabelText("语气"), { target: { value: "专业" } });
  fireEvent.change(screen.getByLabelText("资料引用"), { target: { value: "ref://material/1\nref://material/2" } });
  fireEvent.change(screen.getByLabelText("工作说明"), { target: { value: "请根据资料包撰写公众号文章。" } });
}

const awaitingProduction: WechatProductionView = {
  instance_id: "11111111-2222-3333-4444-555555555555",
  definition_id: "content.wechat-production-package",
  definition_version: 1,
  project_id: PROJECT_ID,
  status: "paused",
  current_node: "wechat-publication-package",
  approval_state: "awaiting",
  publication_state: "none",
  nodes: [
    { node: "research-material-package", order: 1, state: "completed", command_id: "c0ffee00-0000-4000-8000-000000000001", issue_id: "issue-1", task_id: "task-1", candidate_id: "cand-1" },
    { node: "article-draft", order: 2, state: "completed", command_id: "c0ffee00-0000-4000-8000-000000000002", issue_id: "issue-2", task_id: "task-2", candidate_id: "cand-2" },
    { node: "editorial-review-report", order: 3, state: "completed", command_id: "c0ffee00-0000-4000-8000-000000000003", issue_id: "issue-3", task_id: "task-3", candidate_id: "cand-3" },
    { node: "wechat-publication-package", order: 4, state: "pending" },
  ],
};

const terminalProduction: WechatProductionView = {
  ...awaitingProduction,
  instance_id: "99999999-8888-7777-6666-555555555555",
  status: "completed",
  approval_state: "approved",
  publication_state: "awaiting_publication",
  nodes: awaitingProduction.nodes.map((node) =>
    node.node === "wechat-publication-package"
      ? { ...node, state: "completed" as const, task_id: "task-4", candidate_id: "cand-4" }
      : node,
  ),
};

describe("WechatProductionPanel", () => {
  it("submits a validated frozen request through the integrator callback", () => {
    const onStart = vi.fn();
    render(<WechatProductionPanel projectId={PROJECT_ID} authority={authority} publishedPins={pins} productions={[]} onStart={onStart} />);
    fillValidForm();
    fireEvent.click(screen.getByTestId("wechat-start-submit"));

    expect(onStart).toHaveBeenCalledTimes(1);
    const request = onStart.mock.calls[0]?.[0] as WechatContentProductionRequest;
    expect(request.schema_version).toBe("hivecrew.wechat-content-production-request.v1");
    expect(request.channel).toBe("wechat");
    expect(request.project_id).toBe(PROJECT_ID);
    expect(request.authority.work_order_source_ref).toBe(authority.work_order_source_ref);
    expect(request.definition).toEqual(pins[0]);
    expect(request.brief.subject).toBe("新品发布稿");
    expect(request.brief.source_refs).toEqual(["ref://material/1", "ref://material/2"]);
    expect(request.brief.approval_policy).toBe("owner_approval");
    expect(UUID_RE.test(request.idempotency_key)).toBe(true);
    // The request must never carry caller-supplied execution/artifact proof.
    expect(JSON.stringify(request)).not.toMatch(/task_id|run_id|candidate_id|outcome_id|input_digest/);
  });

  it("fails closed on invalid input and never calls the integrator", () => {
    const onStart = vi.fn();
    render(<WechatProductionPanel projectId={PROJECT_ID} authority={authority} publishedPins={pins} productions={[]} onStart={onStart} />);
    // Subject left empty on purpose.
    fireEvent.change(screen.getByLabelText("目标"), { target: { value: "目标" } });
    fireEvent.click(screen.getByTestId("wechat-start-submit"));

    expect(onStart).not.toHaveBeenCalled();
    expect(screen.getByTestId("wechat-validation-issues")).toHaveTextContent("合同校验未通过");
    expect(screen.getByTestId("wechat-validation-issues")).toHaveTextContent("subject");
  });

  it("keeps one stable idempotency key across retries of the same draft", () => {
    const onStart = vi.fn();
    const { rerender } = render(
      <WechatProductionPanel projectId={PROJECT_ID} authority={authority} publishedPins={pins} productions={[]} onStart={onStart} startState="error" startError="网络超时" />,
    );
    fillValidForm();
    fireEvent.click(screen.getByTestId("wechat-start-submit"));
    fireEvent.click(screen.getByTestId("wechat-start-submit"));

    expect(onStart).toHaveBeenCalledTimes(2);
    const first = (onStart.mock.calls[0]?.[0] as WechatContentProductionRequest).idempotency_key;
    const second = (onStart.mock.calls[1]?.[0] as WechatContentProductionRequest).idempotency_key;
    expect(second).toBe(first);
    expect(screen.getByTestId("wechat-start-error")).toHaveTextContent("网络超时");
    rerender(<></>);
  });

  it("shows the start receipt and rotates the idempotency key for a new draft", () => {
    const onStart = vi.fn();
    render(
      <WechatProductionPanel
        projectId={PROJECT_ID}
        authority={authority}
        publishedPins={pins}
        productions={[]}
        onStart={onStart}
        startReceipt={{ instance_id: "inst-9", idempotency_key: "idem-9", changed: false }}
      />,
    );
    expect(screen.getByTestId("wechat-start-receipt")).toHaveTextContent("inst-9");
    expect(screen.getByTestId("wechat-start-receipt")).toHaveTextContent("幂等重放");
    const before = screen.getByTestId("wechat-idempotency-key").textContent;
    fireEvent.click(screen.getByRole("button", { name: "再发起新生产（新幂等键）" }));
    expect(screen.getByTestId("wechat-idempotency-key").textContent).not.toBe(before);
  });

  it("fails closed when the authority context is unresolved", () => {
    render(<WechatProductionPanel projectId={PROJECT_ID} authority={null} publishedPins={pins} productions={[]} onStart={vi.fn()} />);
    expect(screen.getByTestId("wechat-authority-unresolved")).toHaveTextContent("权限上下文未解析");
    expect(screen.queryByTestId("wechat-start-submit")).toBeNull();
  });

  it("fails closed when no published definition version can be pinned", () => {
    render(<WechatProductionPanel projectId={PROJECT_ID} authority={authority} publishedPins={[]} productions={[]} onStart={vi.fn()} />);
    expect(screen.getByTestId("wechat-no-published-pin")).toHaveTextContent("没有可锁定的已发布工作流版本");
    expect(screen.queryByTestId("wechat-start-submit")).toBeNull();
  });

  it("renders the four frozen nodes with read-only Task/Candidate lineage", () => {
    render(<WechatProductionPanel projectId={PROJECT_ID} authority={authority} publishedPins={pins} productions={[awaitingProduction]} />);
    const card = screen.getByTestId(`wechat-production-${awaitingProduction.instance_id}`);
    expect(card).toHaveTextContent("等待审批");
    for (const node of awaitingProduction.nodes) {
      const row = screen.getByTestId(`wechat-node-${awaitingProduction.instance_id}-${node.node}`);
      expect(row).toHaveTextContent(node.state === "completed" ? "已完成" : "待启动");
      if (node.task_id) expect(row).toHaveTextContent(`task ${node.task_id}`);
      if (node.candidate_id) expect(row).toHaveTextContent(`candidate ${node.candidate_id}`);
    }
  });

  it("exposes approve/reject controls only while the approval gate awaits a decision", () => {
    const onReview = vi.fn();
    render(<WechatProductionPanel projectId={PROJECT_ID} authority={authority} publishedPins={pins} productions={[awaitingProduction]} onReview={onReview} />);

    fireEvent.click(screen.getByRole("button", { name: "审批通过" }));
    expect(onReview).toHaveBeenCalledTimes(1);
    const approved = onReview.mock.calls[0]?.[0] as { instanceId: string; decision: string; reviewId: string };
    expect(approved.instanceId).toBe(awaitingProduction.instance_id);
    expect(approved.decision).toBe("approved");
    expect(UUID_RE.test(approved.reviewId)).toBe(true);

    fireEvent.click(screen.getByRole("button", { name: "退回修改" }));
    expect(onReview).toHaveBeenCalledTimes(2);
    expect((onReview.mock.calls[1]?.[0] as { decision: string }).decision).toBe("changes_requested");

    cleanup();
    render(
      <WechatProductionPanel
        projectId={PROJECT_ID}
        authority={authority}
        publishedPins={pins}
        productions={[{ ...awaitingProduction, status: "running", approval_state: "none" }]}
        onReview={onReview}
      />,
    );
    expect(screen.queryByRole("button", { name: "审批通过" })).toBeNull();
    expect(screen.queryByRole("button", { name: "退回修改" })).toBeNull();
  });

  it("shows the terminal package as awaiting publication and never as published", () => {
    render(
      <WechatProductionPanel
        projectId={PROJECT_ID}
        authority={authority}
        publishedPins={pins}
        productions={[terminalProduction]}
        outcomeHref={(id) => `/acme/outcomes?production=${id}`}
      />,
    );
    expect(screen.getByTestId(`wechat-publication-state-${terminalProduction.instance_id}`)).toHaveTextContent("待发布");
    expect(screen.queryByText("已发布", { exact: true })).toBeNull();
    expect(screen.getByTestId(`wechat-outcome-link-${terminalProduction.instance_id}`)).toHaveAttribute(
      "href",
      `/acme/outcomes?production=${terminalProduction.instance_id}`,
    );
    // The terminal production is not awaiting a gate decision: no controls.
    expect(screen.queryByRole("button", { name: "审批通过" })).toBeNull();
  });

  it("renders fail-closed node halts with the server-side reason", () => {
    const failed: WechatProductionView = {
      ...awaitingProduction,
      instance_id: "failed-1",
      status: "failed",
      approval_state: "none",
      nodes: awaitingProduction.nodes.map((node) =>
        node.node === "article-draft"
          ? { ...node, state: "failed" as const, failure: "receipt_missing", candidate_id: undefined }
          : node,
      ),
    };
    render(<WechatProductionPanel projectId={PROJECT_ID} authority={authority} publishedPins={pins} productions={[failed]} />);
    const row = screen.getByTestId("wechat-node-failed-1-article-draft");
    expect(row).toHaveTextContent("已失败");
    expect(row).toHaveTextContent("服务端执行回执缺失");
    expect(row).not.toHaveTextContent("candidate");
  });

  it("keeps loading and error states distinct from an empty production list", () => {
    render(<WechatProductionPanel projectId={PROJECT_ID} authority={authority} publishedPins={pins} productions={[]} productionsState="loading" />);
    expect(screen.getByText("正在回读生产状态…")).toBeDefined();

    cleanup();
    render(<WechatProductionPanel projectId={PROJECT_ID} authority={authority} publishedPins={pins} productions={[]} productionsState="error" productionsError="对账接口不可用" />);
    expect(screen.getByTestId("wechat-productions-error")).toHaveTextContent("对账接口不可用");
    expect(screen.queryByText(/没有进行中的公众号内容生产/)).toBeNull();
  });

  it("never renders pause/resume/retry/publish controls the backend does not implement", () => {
    render(<WechatProductionPanel projectId={PROJECT_ID} authority={authority} publishedPins={pins} productions={[awaitingProduction, terminalProduction]} onReview={vi.fn()} />);
    for (const name of [/暂停/, /恢复/, /重试任务/, /重新执行/, /发布到公众号/, /立即发布/]) {
      expect(screen.queryByRole("button", { name })).toBeNull();
    }
  });
});

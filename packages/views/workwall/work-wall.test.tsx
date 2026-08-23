import { describe, expect, it } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { WorkWall } from "./work-wall";
import type { EmployeeLiveActivityV1 } from "@multica/core/api/workwall";

function emp(over: Partial<EmployeeLiveActivityV1> = {}): EmployeeLiveActivityV1 {
  return {
    schema_version: "hivecrew.employee-live-activity.v1",
    workspace_id: "ws-1",
    employee_id: "emp-1",
    agent_id: "agt-1",
    display_name: "Emory",
    presence_state: "working",
    work_stage: "coding",
    recent_events: [],
    source_refs: ["agent://agt-1"],
    observed_at: "2026-08-13T12:00:00Z",
    freshness_state: "fresh",
    ...over,
  };
}

describe("WorkWall", () => {
  it("uses the shared semantic palette instead of the legacy green terminal theme", () => {
    render(<WorkWall employees={[emp()]} />);
    expect(screen.getByTestId("work-wall").querySelector('[class*="green"]')).toBeNull();
  });

  it("renders one owner card per employee with text (not colour-only) presence", () => {
    render(
      <WorkWall
        employees={[
          emp(),
          emp({
            agent_id: "agt-2",
            employee_id: "emp-2",
            display_name: "Coco",
            presence_state: "idle",
            work_stage: "none",
          }),
        ]}
      />,
    );
    expect(screen.getAllByTestId("owner-card").length).toBe(2);
    expect(screen.getByText("Emory")).toBeDefined();
    expect(screen.getByText("Coco")).toBeDefined();
    expect(screen.getAllByText(/工作中/).length).toBeGreaterThan(0);
    expect(screen.getAllByText(/空闲/).length).toBeGreaterThan(0);
  });

  it("expands in place on click without a second page", () => {
    render(
      <WorkWall
        employees={[
          emp({
            recent_events: [
              {
                event_id: "ev-1",
                kind: "run.started",
                safe_summary: "run started",
                occurred_at: "2026-08-13T12:00:00Z",
              },
            ],
          }),
        ]}
      />,
    );
    expect(screen.queryByTestId("owner-card-expanded")).toBeNull();
    fireEvent.click(screen.getByTestId("owner-card-header"));
    expect(screen.getByTestId("owner-card-expanded")).toBeDefined();
    expect(screen.getByText(/run started/)).toBeDefined();
  });

  it("filters by presence state", () => {
    render(
      <WorkWall
        employees={[
          emp({ display_name: "Emory" }),
          emp({
            agent_id: "agt-2",
            employee_id: "emp-2",
            display_name: "Coco",
            presence_state: "idle",
            work_stage: "none",
          }),
        ]}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "○ 空闲" }));
    expect(screen.queryByText("Emory")).toBeNull();
    expect(screen.getByText("Coco")).toBeDefined();
  });

  it("shows a status summary bar with counts", () => {
    render(
      <WorkWall
        employees={[
          emp(),
          emp({
            agent_id: "agt-2",
            employee_id: "emp-2",
            display_name: "Coco",
            presence_state: "idle",
            work_stage: "none",
          }),
          emp({
            agent_id: "agt-3",
            employee_id: "emp-3",
            display_name: "Drake",
            presence_state: "blocked",
            work_stage: "unknown",
            token_usage: 42,
          }),
        ]}
      />,
    );
    expect(screen.getByTestId("work-wall-status-bar")).toBeDefined();
    expect(screen.getByText("员工 3")).toBeDefined();
    expect(screen.getByText("工作中 1")).toBeDefined();
    expect(screen.getByText("等待/阻塞 1")).toBeDefined();
    expect(screen.getByText("Token 42")).toBeDefined();
  });

  it("filters by search text across employee_id, agent_id, blocked_reason, next_action", () => {
    render(
      <WorkWall
        employees={[
          emp({
            display_name: "Emory",
            project_title: "工作流与员工记忆系统",
            issue_title: "事件协议",
            employee_id: "emp-emory-001",
            agent_id: "agt-emory-001",
            blocked_reason: "等待设计评审",
            next_action: "合并 PR #42",
          }),
          emp({
            agent_id: "agt-2",
            employee_id: "emp-2",
            display_name: "Coco",
            project_title: "成果中心",
            presence_state: "idle",
            work_stage: "none",
          }),
        ]}
      />,
    );
    fireEvent.change(screen.getByTestId("work-wall-search"), { target: { value: "emory" } });
    expect(screen.queryByText("Coco")).toBeNull();
    expect(screen.getByText("Emory")).toBeDefined();

    fireEvent.change(screen.getByTestId("work-wall-search"), { target: { value: "emp-emory" } });
    expect(screen.queryByText("Coco")).toBeNull();
    expect(screen.getByText("Emory")).toBeDefined();

    fireEvent.change(screen.getByTestId("work-wall-search"), { target: { value: "设计评审" } });
    expect(screen.queryByText("Coco")).toBeNull();
    expect(screen.getByText("Emory")).toBeDefined();

    fireEvent.change(screen.getByTestId("work-wall-search"), { target: { value: "合并 PR" } });
    expect(screen.queryByText("Coco")).toBeNull();
    expect(screen.getByText("Emory")).toBeDefined();

    fireEvent.change(screen.getByTestId("work-wall-search"), { target: { value: "" } });
    expect(screen.getByText("Coco")).toBeDefined();
  });
});

describe("WorkWall Owner card identity and runtime", () => {
  it("shows employee_id and agent_id in the collapsed card header", () => {
    render(
      <WorkWall
        employees={[
          emp({
            employee_id: "emp-pixel-001",
            agent_id: "agt-pixel-001",
            display_name: "Pixel",
          }),
        ]}
      />,
    );
    expect(screen.getByText("emp-pixel-001")).toBeDefined();
  });

  it("shows model name and runtime carrier with clear labels on the collapsed card", () => {
    render(
      <WorkWall
        employees={[
          emp({
            model_name: "doubao-seed-2.1-turbo",
            runtime_provider: "volcengine",
          }),
        ]}
      />,
    );
    const runtime = screen.getByTestId("owner-card-runtime");
    expect(runtime.textContent).toContain("模型：doubao-seed-2.1-turbo");
    expect(runtime.textContent).toContain("运行载体：volcengine");
    expect(screen.getByTestId("owner-card-llm-provider").textContent).toContain(
      "模型提供方：未登记",
    );
  });

  it("shows runtime profile when profile_id is present", () => {
    render(
      <WorkWall
        employees={[
          emp({
            runtime_profile_id: "prof-123",
            runtime_profile_name: "前端开发工程师档案",
          }),
        ]}
      />,
    );
    const profile = screen.getByTestId("owner-card-profile");
    expect(profile.textContent).toContain("运行档案：前端开发工程师档案");
    expect(profile.textContent).toContain("prof-123");
  });

  it("does not show runtime profile when profile_id is absent", () => {
    render(<WorkWall employees={[emp({ runtime_profile_name: "孤儿档案名" })]} />);
    expect(screen.queryByTestId("owner-card-profile")).toBeNull();
  });

  it("shows freshness state with correct label", () => {
    render(<WorkWall employees={[emp({ freshness_state: "fresh" })]} />);
    const freshness = screen.getByTestId("owner-card-freshness");
    expect(freshness.textContent).toContain("新鲜度：新鲜");
    expect(freshness.className).toContain("text-muted-foreground");
    expect(freshness.className).not.toContain("text-success");
  });

  it("shows stale freshness with the shared warning tone", () => {
    render(<WorkWall employees={[emp({ freshness_state: "stale" })]} />);
    const freshness = screen.getByTestId("owner-card-freshness");
    expect(freshness.className).toContain("text-warning");
  });

  it("shows blocked reason with warning style", () => {
    render(
      <WorkWall
        employees={[emp({ blocked_reason: "等待 API 密钥审批" })]}
      />,
    );
    const blocked = screen.getByTestId("owner-card-blocked");
    expect(blocked.textContent).toContain("阻塞原因：等待 API 密钥审批");
    expect(blocked.className).toContain("text-warning");
  });

  it("shows next action", () => {
    render(
      <WorkWall
        employees={[emp({ next_action: "提交代码审查" })]}
      />,
    );
    const next = screen.getByTestId("owner-card-next");
    expect(next.textContent).toContain("下一动作：提交代码审查");
  });

  it("shows heartbeat age label", () => {
    const now = new Date();
    const thirtySecAgo = new Date(now.getTime() - 30_000).toISOString();
    render(<WorkWall employees={[emp({ last_heartbeat_at: thirtySecAgo })]} />);
    const hb = screen.getByTestId("owner-card-heartbeat");
    expect(hb.textContent).toMatch(/心跳：\d+ 秒前/);
  });
});

describe("WorkWall execution chain", () => {
  it("renders the full execution chain in the expanded card", () => {
    render(
      <WorkWall
        employees={[
          emp({
            issue_id: "11111111-1111-1111-1111-111111111111",
            issue_identifier: "HIV-797",
            issue_title: "[DEV] Work Wall complete execution-chain projection",
            project_id: "22222222-2222-2222-2222-222222222222",
            project_title: "HIVECREW 自我开发项目",
            task_id: "33333333-3333-3333-3333-333333333333",
            run_id: "44444444-4444-4444-4444-444444444444",
            runtime_profile_id: "55555555-5555-5555-5555-555555555555",
            runtime_profile_name: "glm-5.3 运行档案",
            execution_receipt_ref: "receipt://33333333-3333-3333-3333-333333333333",
            execution_receipt_status: "completed",
          }),
        ]}
      />,
    );
    expect(screen.queryByTestId("owner-card-chain")).toBeNull();
    fireEvent.click(screen.getByTestId("owner-card-header"));
    const chain = screen.getByTestId("owner-card-chain");
    expect(chain.textContent).toContain("HIV-797");
    expect(chain.textContent).toContain("[DEV] Work Wall complete execution-chain projection");
    expect(chain.textContent).toContain("11111111-1111-1111-1111-111111111111");
    expect(chain.textContent).toContain("HIVECREW 自我开发项目");
    expect(chain.textContent).toContain("33333333-3333-3333-3333-333333333333");
    expect(chain.textContent).toContain("44444444-4444-4444-4444-444444444444");
    expect(chain.textContent).toContain("glm-5.3 运行档案");
    expect(chain.textContent).toContain("receipt://33333333-3333-3333-3333-333333333333");
    expect(chain.textContent).toContain("已完成");
  });

  it("labels a direct task without fabricating a Run reference", () => {
    render(
      <WorkWall
        employees={[
          emp({
            issue_id: "11111111-1111-1111-1111-111111111111",
            issue_identifier: "HIV-797",
            issue_title: "[DEV] Work Wall complete execution-chain projection",
            task_id: "33333333-3333-3333-3333-333333333333",
          }),
        ]}
      />,
    );
    fireEvent.click(screen.getByTestId("owner-card-header"));
    const chain = screen.getByTestId("owner-card-chain");
    expect(chain.textContent).toContain("无独立 Run ID");
    expect(chain.textContent).not.toContain("44444444");
    expect(chain.textContent).not.toContain("Receipt");
  });

  it("renders no chain block when no evidence exists", () => {
    render(<WorkWall employees={[emp()]} />);
    fireEvent.click(screen.getByTestId("owner-card-header"));
    expect(screen.queryByTestId("owner-card-chain")).toBeNull();
  });

  it("renders execution runtime in the chain block when task runtime differs (HIV-940)", () => {
    render(
      <WorkWall
        employees={[
          emp({
            runtime_provider: "prime",
            task_id: "33333333-3333-3333-3333-333333333333",
            execution_runtime_id: "rt-task-orig",
            execution_runtime_carrier: "codex",
            execution_model_name: "o3",
            execution_profile_id: "profile-exec",
            execution_profile_name: "Codex 执行档案",
          }),
        ]}
      />,
    );
    fireEvent.click(screen.getByTestId("owner-card-header"));
    const execRT = screen.getByTestId("owner-card-exec-runtime");
    expect(execRT.textContent).toContain("执行运行时：codex");
    expect(execRT.textContent).toContain("rt-task-orig");
    expect(execRT.textContent).toContain("模型");
    expect(execRT.textContent).toContain("o3");
    expect(execRT.textContent).toContain("档案");
    expect(execRT.textContent).toContain("Codex 执行档案");
    expect(execRT.textContent).toContain("profile-exec");
  });

  it("does not render execution runtime when execution_runtime_id is absent (HIV-940)", () => {
    render(
      <WorkWall
        employees={[
          emp({
            task_id: "33333333-3333-3333-3333-333333333333",
            execution_runtime_carrier: "codex",
          }),
        ]}
      />,
    );
    fireEvent.click(screen.getByTestId("owner-card-header"));
    expect(screen.queryByTestId("owner-card-exec-runtime")).toBeNull();
  });

  it("shows the issue identifier on the collapsed card", () => {
    render(
      <WorkWall
        employees={[
          emp({
            issue_identifier: "HIV-797",
            issue_title: "[DEV] Work Wall complete execution-chain projection",
          }),
        ]}
      />,
    );
    expect(screen.getByText(/HIV-797 · /)).toBeDefined();
  });
});

describe("WorkWall expanded evidence panel", () => {
  it("shows identity evidence with employee_id and agent_id", () => {
    render(
      <WorkWall
        employees={[
          emp({
            employee_id: "emp-pixel",
            agent_id: "agt-pixel",
            department_name: "前端工程部",
            position_name: "前端开发工程师",
          }),
        ]}
      />,
    );
    fireEvent.click(screen.getByTestId("owner-card-header"));
    const evidence = screen.getByTestId("owner-card-evidence");
    expect(evidence.textContent).toContain("employee_id emp-pixel");
    expect(evidence.textContent).toContain("agent_id agt-pixel");
    expect(evidence.textContent).toContain("部门：前端工程部");
    expect(evidence.textContent).toContain("职位：前端开发工程师");
  });

  it("shows runtime and base model evidence", () => {
    render(
      <WorkWall
        employees={[
          emp({
            runtime_id: "rt-001",
            runtime_provider: "volcengine",
            model_name: "doubao-seed-2.1-turbo",
            base_id: "base-doubao",
            base_name: "doubao-seed",
            runtime_profile_id: "prof-001",
            runtime_profile_name: "高速前端档案",
          }),
        ]}
      />,
    );
    fireEvent.click(screen.getByTestId("owner-card-header"));
    const evidence = screen.getByTestId("owner-card-evidence");
    expect(evidence.textContent).toContain("运行载体：volcengine");
    expect(evidence.textContent).toContain("rt-001");
    expect(evidence.textContent).toContain("模型：doubao-seed-2.1-turbo");
    expect(evidence.textContent).toContain("基座：doubao-seed");
    expect(evidence.textContent).toContain("档案：高速前端档案");
  });

  it("shows task/run/receipt evidence only when linkage exists (exactly one unavailable indicator per card)", () => {
    render(<WorkWall employees={[emp()]} />);
    fireEvent.click(screen.getByTestId("owner-card-header"));
    const evidence = screen.getByTestId("owner-card-evidence");
    const card = screen.getByTestId("owner-card");
    expect(screen.getAllByTestId("owner-card-link-unavailable")).toHaveLength(1);
    expect(evidence.textContent).not.toContain("无关联 Task");
    expect(evidence.textContent).not.toContain("无执行回执");
    expect(card.textContent.match(/当前任务链接不可用/g)?.length ?? 0).toBe(1);
  });

  it("shows timeline evidence with all timestamps", () => {
    render(
      <WorkWall
        employees={[
          emp({
            queued_at: "2026-08-13T11:00:00Z",
            started_at: "2026-08-13T11:05:00Z",
            last_heartbeat_at: "2026-08-13T12:00:00Z",
            last_event_at: "2026-08-13T11:59:00Z",
            completed_at: null as unknown as string,
          }),
        ]}
      />,
    );
    fireEvent.click(screen.getByTestId("owner-card-header"));
    const evidence = screen.getByTestId("owner-card-evidence");
    expect(evidence.textContent).toContain("排队：2026-08-13T11:00:00Z");
    expect(evidence.textContent).toContain("开始：2026-08-13T11:05:00Z");
    expect(evidence.textContent).toContain("心跳：2026-08-13T12:00:00Z");
    expect(evidence.textContent).toContain("最近事件：2026-08-13T11:59:00Z");
    expect(evidence.textContent).toContain("观测：2026-08-13T12:00:00Z");
  });

  it("shows source refs", () => {
    render(
      <WorkWall
        employees={[emp({ source_refs: ["agent://agt-1", "run://run-1"] })]}
      />,
    );
    fireEvent.click(screen.getByTestId("owner-card-header"));
    const refs = screen.getByTestId("owner-card-source-refs");
    expect(refs.textContent).toContain("agent://agt-1");
    expect(refs.textContent).toContain("run://run-1");
  });
});

describe("WorkWall missing evidence regression", () => {
  it("does not fabricate a terminal-to-employee link from agent_hint", () => {
    render(
      <WorkWall
        employees={[
          emp({
            display_name: "Pixel",
            agent_id: "agt-pixel",
          }),
        ]}
      />,
    );
    expect(screen.queryByText(/Terminal 现场/)).toBeNull();
    expect(screen.queryByText(/agent_hint/)).toBeNull();
  });

  it("shows model name as 未计量, carrier as 无, and LLM provider as 未知 when absent", () => {
    render(
      <WorkWall
        employees={[emp({ model_name: undefined, runtime_provider: undefined, llm_provider: undefined })]}
      />,
    );
    const runtime = screen.getByTestId("owner-card-runtime");
    expect(runtime.textContent).toContain("模型：未计量");
    expect(runtime.textContent).toContain("运行载体：无");
    const llm = screen.getByTestId("owner-card-llm-provider");
    expect(llm.textContent).toContain("模型提供方：未登记");
  });

  it("does not show blocked or next sections when absent", () => {
    render(<WorkWall employees={[emp({ blocked_reason: undefined, next_action: undefined })]} />);
    expect(screen.queryByTestId("owner-card-blocked")).toBeNull();
    expect(screen.queryByTestId("owner-card-next")).toBeNull();
  });

  it("work_stage none hides the stage line", () => {
    render(<WorkWall employees={[emp({ work_stage: "none" })]} />);
    expect(screen.queryByText(/工作阶段：/)).toBeNull();
  });
});

describe("WorkWall unavailable linkage indicator (R3)", () => {
  it("shows exactly one unavailable indicator on a collapsed card with no issue/task/run", () => {
    render(<WorkWall employees={[emp()]} />);
    const indicators = screen.getAllByTestId("owner-card-link-unavailable");
    expect(indicators).toHaveLength(1);
    expect(indicators[0]?.textContent).toContain("当前任务链接不可用");
  });

  it("shows exactly one unavailable indicator when the card is expanded", () => {
    render(<WorkWall employees={[emp()]} />);
    fireEvent.click(screen.getByTestId("owner-card-header"));
    const card = screen.getByTestId("owner-card");
    const count = (card.textContent.match(/当前任务链接不可用/g) ?? []).length;
    expect(count).toBe(1);
    expect(screen.getAllByTestId("owner-card-link-unavailable")).toHaveLength(1);
  });

  it("hides the unavailable indicator when issue_id is present", () => {
    render(
      <WorkWall
        employees={[
          emp({
            issue_id: "issue-9001",
            issue_identifier: "HIV-9001",
            issue_title: "已链接议题",
          }),
        ]}
      />,
    );
    expect(screen.queryByTestId("owner-card-link-unavailable")).toBeNull();
  });

  it("hides the unavailable indicator when task_id is present", () => {
    render(
      <WorkWall
        employees={[emp({ task_id: "task-4242" })]}
      />,
    );
    expect(screen.queryByTestId("owner-card-link-unavailable")).toBeNull();
  });

  it("hides the unavailable indicator when only run_id is present (standalone run)", () => {
    render(
      <WorkWall
        employees={[emp({ run_id: "run-9999" })]}
      />,
    );
    expect(screen.queryByTestId("owner-card-link-unavailable")).toBeNull();
  });

  it("still shows a completed receipt when no current linkage exists", () => {
    render(
      <WorkWall
        employees={[
          emp({
            execution_receipt_ref: "receipt://finished-1",
            execution_receipt_status: "completed",
          }),
        ]}
      />,
    );
    expect(screen.getByTestId("owner-card-link-unavailable")).toBeDefined();
    fireEvent.click(screen.getByTestId("owner-card-header"));
    const evidence = screen.getByTestId("owner-card-evidence");
    expect(evidence.textContent).toContain("receipt://finished-1");
    expect(evidence.textContent).toContain("已完成");
  });

  it("renders standalone run_id explicitly in the evidence panel", () => {
    render(<WorkWall employees={[emp({ run_id: "run-standalone-7" })]} />);
    fireEvent.click(screen.getByTestId("owner-card-header"));
    const evidence = screen.getByTestId("owner-card-evidence");
    expect(evidence.textContent).toContain("run_id run-standalone-7");
  });
});

describe("WorkWall untrusted-extra-field regression (R3)", () => {
  it("never renders top-level secret sentinels including token", () => {
    const rogue = {
      ...emp(),
      token: "SENTINEL_TOKEN_sk_live_NEVER_RENDER",
      raw_prompt: "SENTINEL_PROMPT_never_render_prompt",
      env_vars: { api_key: "SENTINEL_ENV_never_render_env" },
      credential: "SENTINEL_CRED_never_render_credential",
      api_key: "SENTINEL_APIKEY_never_render_apikey",
      chain_of_thought: "SENTINEL_COT_never_render_cot",
    } as unknown as EmployeeLiveActivityV1;

    render(<WorkWall employees={[rogue]} />);
    const card = screen.getByTestId("owner-card").textContent ?? "";
    expect(card).not.toContain("SENTINEL_TOKEN_sk_live_NEVER_RENDER");
    expect(card).not.toContain("SENTINEL_PROMPT_never_render_prompt");
    expect(card).not.toContain("SENTINEL_ENV_never_render_env");
    expect(card).not.toContain("SENTINEL_CRED_never_render_credential");
    expect(card).not.toContain("SENTINEL_APIKEY_never_render_apikey");
    expect(card).not.toContain("SENTINEL_COT_never_render_cot");

    fireEvent.click(screen.getByTestId("owner-card-header"));
    const expanded = screen.getByTestId("owner-card-expanded").textContent ?? "";
    expect(expanded).not.toContain("SENTINEL_TOKEN_sk_live_NEVER_RENDER");
    expect(expanded).not.toContain("SENTINEL_PROMPT_never_render_prompt");
    expect(expanded).not.toContain("SENTINEL_ENV_never_render_env");
    expect(expanded).not.toContain("SENTINEL_CRED_never_render_credential");
    expect(expanded).not.toContain("SENTINEL_APIKEY_never_render_apikey");
    expect(expanded).not.toContain("SENTINEL_COT_never_render_cot");
  });
});

describe("WorkWall runtime carrier vs LLM provider semantic separation (HIV-911)", () => {
  it("displays the compatibility runtime_provider field as a runtime carrier", () => {
    render(
      <WorkWall
        employees={[
          emp({
            runtime_provider: "prime",
            model_name: "deepseek-v4",
          }),
        ]}
      />,
    );
    const runtime = screen.getByTestId("owner-card-runtime");
    expect(runtime.textContent).toContain("运行载体：prime");
    expect(runtime.textContent).not.toContain("提供商：prime");
    const llm = screen.getByTestId("owner-card-llm-provider");
    expect(llm.textContent).toContain("模型提供方：未登记");
  });

  it("displays llm_provider independently when an authoritative source exists", () => {
    render(
      <WorkWall
        employees={[
          emp({
            runtime_provider: "volcengine",
            llm_provider: "volcengine-ark",
            model_name: "doubao-seed-2.1-turbo",
          }),
        ]}
      />,
    );
    const runtime = screen.getByTestId("owner-card-runtime");
    expect(runtime.textContent).toContain("运行载体：volcengine");
    const llm = screen.getByTestId("owner-card-llm-provider");
    expect(llm.textContent).toContain("模型提供方：volcengine-ark");
  });

  it("shows the semantic separation in the expanded evidence panel", () => {
    render(
      <WorkWall
        employees={[
          emp({
            runtime_id: "rt-001",
            runtime_provider: "prime",
            llm_provider: "openai",
            model_name: "gpt-4.1",
          }),
        ]}
      />,
    );
    fireEvent.click(screen.getByTestId("owner-card-header"));
    const evidence = screen.getByTestId("owner-card-evidence");
    expect(evidence.textContent).toContain("运行载体：prime");
    expect(evidence.textContent).toContain("模型提供方：openai");
    expect(evidence.textContent).toContain("模型：gpt-4.1");
  });
});

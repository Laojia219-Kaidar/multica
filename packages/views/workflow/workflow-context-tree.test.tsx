import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { WorkflowContextTree } from "./workflow-context-tree";

const programs = [{ id: "brand", name: "蜂巢创科品牌运营", projectIds: ["wechat"] }];
const projects = [
  { id: "wechat", programId: "brand", formalProjectId: "PRJ-WECHAT", name: "微信公众号运营", platform: "公众号" },
  { id: "novel", programId: "", programClassification: "unassigned" as const, formalProjectId: "PRJ-NOVEL", name: "微信读书小说", platform: "微信读书" },
];

describe("WorkflowContextTree", () => {
  it("selects L3 and L4 operating objects and can hide and restore the pane", () => {
    const onSelect = vi.fn();
    const { rerender } = render(<WorkflowContextTree programs={programs} projects={projects} onSelect={onSelect} onToggleCollapsed={() => rerender(<WorkflowContextTree programs={programs} projects={projects} onSelect={onSelect} collapsed onToggleCollapsed={() => undefined} />)} />);
    fireEvent.click(screen.getByTestId("workflow-program-brand"));
    expect(onSelect).toHaveBeenCalledWith({ kind: "program", id: "brand" });
    fireEvent.click(screen.getByTestId("workflow-project-wechat"));
    expect(onSelect).toHaveBeenCalledWith({ kind: "project", id: "wechat" });
    fireEvent.click(screen.getByRole("button", { name: "隐藏运营项目树" }));
    expect(screen.getByTestId("workflow-context-tree-collapsed")).toBeDefined();
  });

  it("keeps formal projects without an L3 assignment in an explicit unclassified group", () => {
    const onSelect = vi.fn();
    render(<WorkflowContextTree programs={programs} projects={projects} onSelect={onSelect} />);
    expect(screen.getByTestId("workflow-unassigned-projects")).toHaveTextContent("未归类正式项目");
    fireEvent.click(screen.getByTestId("workflow-unassigned-project-novel"));
    expect(onSelect).toHaveBeenCalledWith({ kind: "project", id: "novel" });
    expect(screen.queryByTestId("workflow-program-novel")).toBeNull();
  });

  it("emits the new subject form payload and shows loading state", () => {
    const onCreateProgram = vi.fn();
    render(<WorkflowContextTree programs={programs} projects={projects} onSelect={() => undefined} onCreateProgram={onCreateProgram} programCreationState="ready" />);
    fireEvent.click(screen.getByText("新建运营科目"));
    fireEvent.change(screen.getByLabelText("科目名称"), { target: { value: "内容运营" } });
    fireEvent.change(screen.getByLabelText("描述（可选）"), { target: { value: "多平台内容生产" } });
    fireEvent.click(screen.getByRole("button", { name: "创建运营科目" }));
    expect(onCreateProgram).toHaveBeenCalledWith({ name: "内容运营", description: "多平台内容生产" });
    expect(screen.getByLabelText("科目名称")).not.toBeDisabled();
  });

  it("does not invent the create action when the persistence callback is absent", () => {
    render(<WorkflowContextTree programs={programs} projects={projects} onSelect={() => undefined} />);
    expect(screen.queryByText("新建运营科目")).toBeNull();
  });
});

import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { WorkflowContextTree } from "./workflow-context-tree";

const programs = [{ id: "brand", name: "蜂巢创科品牌运营", projectIds: ["wechat"] }];
const projects = [{ id: "wechat", programId: "brand", formalProjectId: "PRJ-WECHAT", name: "微信公众号运营", platform: "公众号" }];

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
});

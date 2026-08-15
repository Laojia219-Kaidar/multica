import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { WorkflowProgramSettings } from "./workflow-program-settings";

const program = {
  id: "brand",
  name: "蜂巢创科品牌运营",
  description: "多平台运营",
  projectIds: ["wechat"],
};

const projects = [
  { id: "wechat", programId: "brand", formalProjectId: "PRJ-WECHAT", name: "微信公众号运营" },
  { id: "novel", programId: "", programClassification: "unassigned" as const, formalProjectId: "PRJ-NOVEL", name: "微信读书小说" },
];

describe("WorkflowProgramSettings", () => {
  it("emits an update intent with the edited name and description", () => {
    const onUpdateProgram = vi.fn();
    render(<WorkflowProgramSettings program={program} projects={projects} onUpdateProgram={onUpdateProgram} />);
    fireEvent.change(screen.getByLabelText("科目名称"), { target: { value: "内容品牌运营" } });
    fireEvent.change(screen.getByLabelText("描述（可选）"), { target: { value: "公众号、小说与短内容" } });
    fireEvent.click(screen.getByRole("button", { name: "保存科目信息" }));
    expect(onUpdateProgram).toHaveBeenCalledWith("brand", { name: "内容品牌运营", description: "公众号、小说与短内容" });
  });

  it("requires an explicit confirmation and preserves formal projects on delete", () => {
    const onDeleteProgram = vi.fn();
    render(<WorkflowProgramSettings program={program} projects={projects} onDeleteProgram={onDeleteProgram} />);
    expect(screen.getByText(/正式 L4 Project、工作流、成果和文件都会保留/)).toBeDefined();
    fireEvent.click(screen.getByRole("button", { name: "删除运营科目" }));
    expect(screen.getByTestId("workflow-program-delete-confirmation")).toHaveTextContent("正式 L4 Project 不会被删除");
    expect(onDeleteProgram).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "确认删除 L3 科目" }));
    expect(onDeleteProgram).toHaveBeenCalledWith("brand");
  });

  it("surfaces update and deletion errors and disables controls during loading", () => {
    render(<WorkflowProgramSettings
      program={program}
      projects={projects}
      onUpdateProgram={() => undefined}
      programUpdateState="loading"
      programUpdateError="版本冲突"
      onDeleteProgram={() => undefined}
      programDeletionState="error"
      programDeletionError="科目仍在执行中"
    />);
    expect(screen.getByRole("button", { name: "正在保存…" })).toBeDisabled();
    expect(screen.getAllByRole("alert")[0]).toHaveTextContent("版本冲突");
    fireEvent.click(screen.getByRole("button", { name: "删除运营科目" }));
    expect(screen.getAllByRole("alert").some((alert) => alert.textContent?.includes("科目仍在执行中"))).toBe(true);
  });
});

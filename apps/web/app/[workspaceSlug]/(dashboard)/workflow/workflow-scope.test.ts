import { describe, expect, it } from "vitest";
import { scopeWorkflowOperations } from "./workflow-scope";

describe("scopeWorkflowOperations", () => {
  const projects = [
    { id: "l4-wechat", formalProjectId: "project-wechat", programId: "brand", name: "微信公众号运营" },
    { id: "l4-weibo", formalProjectId: "project-weibo", programId: "brand", name: "微博运营" },
  ];
  const definitions = [
    { definition_id: "content.wechat", project_id: "project-wechat" },
    { definition_id: "content.weibo", project_id: "project-weibo" },
    { definition_id: "legacy.unbound", project_id: "" },
  ];
  const instances = [
    { id: "run-wechat", context: { project_id: "project-wechat" } },
    { id: "run-weibo", context: { project_id: "project-weibo" } },
    { id: "run-unbound", context: {} },
  ];

  it("shows only the selected L4 Project's instances and definitions", () => {
    const result = scopeWorkflowOperations({ kind: "project", id: "l4-wechat" }, projects, definitions, instances);

    expect(result.definitions.map((definition) => definition.definition_id)).toEqual(["content.wechat"]);
    expect(result.instances.map((instance) => instance.id)).toEqual(["run-wechat"]);
  });

  it("aggregates only projects belonging to the selected L3 Program", () => {
    const result = scopeWorkflowOperations({ kind: "program", id: "brand" }, projects, definitions, instances);

    expect(result.definitions.map((definition) => definition.definition_id)).toEqual(["content.wechat", "content.weibo"]);
    expect(result.instances.map((instance) => instance.id)).toEqual(["run-wechat", "run-weibo"]);
  });

  it("returns no operational records until a formal Project or Program is selected", () => {
    const result = scopeWorkflowOperations(undefined, projects, definitions, instances);

    expect(result.definitions).toEqual([]);
    expect(result.instances).toEqual([]);
  });
});

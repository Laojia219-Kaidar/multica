"use client";

import { useMemo } from "react";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { useQuery } from "@tanstack/react-query";
import { useCurrentWorkspace } from "@multica/core/paths";
import { projectListOptions } from "@multica/core/projects/queries";
import { workflowInstanceListOptions } from "@multica/core/workflow";
import type { OperatingProgram, OperatingProject } from "@multica/core/workflow";
import type { Project } from "@multica/core/types";
import {
  WorkflowOperationsPage,
  type WorkflowContextSelection,
  type WorkflowOperationsSection,
} from "@multica/views/workflow";

const SECTIONS: WorkflowOperationsSection[] = [
  "overview",
  "plan",
  "workflow",
  "instances",
  "artifacts",
  "review",
  "settings",
];

function isSection(value: string | null): value is WorkflowOperationsSection {
  return value !== null && SECTIONS.includes(value as WorkflowOperationsSection);
}

function projectStatus(status: Project["status"]): OperatingProject["status"] {
  if (status === "in_progress") return "active";
  if (status === "paused") return "paused";
  return "archived";
}

/**
 * There is not yet an authority-managed OperatingProgram registry. This is an
 * explicit UI projection of the formal Project list, not a new registry: all
 * L4 selections remain the native Project ID and this one holding program is
 * visibly marked as awaiting operating-program registration.
 */
function deriveOperatingProjection(workspaceId: string, projects: Project[]) {
  const programId = `formal-projects-awaiting-program-registration:${workspaceId}`;
  const operatingProjects: OperatingProject[] = projects.map((project) => ({
    id: project.id,
    formalProjectId: project.id,
    programId,
    name: project.title,
    platform: "正式项目",
    status: projectStatus(project.status),
  }));
  const programs: OperatingProgram[] = operatingProjects.length === 0
    ? []
    : [{
      id: programId,
      name: "运营科目待建档",
      description: "来自正式 Project 的临时只读投影；请先在现有项目权威中完成运营科目登记。",
      projectIds: operatingProjects.map((project) => project.id),
    }];
  return { programs, projects: operatingProjects };
}

export default function Page() {
  const workspace = useCurrentWorkspace();
  const router = useRouter();
  const pathname = usePathname();
  const searchParams = useSearchParams();
  const projectsQuery = useQuery({
    ...projectListOptions(workspace?.id ?? "workflow-workspace-unresolved"),
    enabled: Boolean(workspace?.id),
  });
  const instancesQuery = useQuery({
    ...workflowInstanceListOptions(workspace?.id ?? "workflow-workspace-unresolved"),
    enabled: Boolean(workspace?.id),
  });

  const projection = useMemo(
    () => deriveOperatingProjection(workspace?.id ?? "unresolved", projectsQuery.data ?? []),
    [projectsQuery.data, workspace?.id],
  );
  const requestedProjectId = searchParams.get("project");
  const requestedProgramId = searchParams.get("program");
  const selectedProject = projection.projects.find((project) => project.id === requestedProjectId);
  const selection: WorkflowContextSelection | undefined = selectedProject
    ? { kind: "project", id: selectedProject.id }
    : projection.programs.some((program) => program.id === requestedProgramId)
      ? { kind: "program", id: requestedProgramId! }
      : undefined;
  const section = isSection(searchParams.get("section")) ? searchParams.get("section") : "overview";

  const updateSearch = (updates: Record<string, string | null>) => {
    const next = new URLSearchParams(searchParams.toString());
    for (const [key, value] of Object.entries(updates)) {
      if (value) next.set(key, value);
      else next.delete(key);
    }
    const suffix = next.toString();
    router.replace(suffix ? `${pathname}?${suffix}` : pathname, { scroll: false });
  };

  const selectContext = (next: WorkflowContextSelection) => {
    if (next.kind === "project") {
      const project = projection.projects.find((item) => item.id === next.id);
      updateSearch({ project: next.id, program: project?.programId ?? null });
      return;
    }
    updateSearch({ program: next.id, project: null });
  };

  if (!workspace) {
    return <WorkspaceSourceState title="工作区来源不可用" detail="正在读取当前工作区；不会用名称或本地缓存猜测工作流所属范围。" />;
  }
  if (projectsQuery.isLoading || instancesQuery.isLoading) {
    return <WorkspaceSourceState title="正在读取正式项目与工作流实例" detail="项目列表和实例列表来自各自的权威读模型。" />;
  }
  if (projectsQuery.isError || instancesQuery.isError) {
    return <WorkspaceSourceState title="来源暂不可用" detail="工作流页面没有把读取失败伪装成空项目或零实例；请恢复来源后重试。" />;
  }
  if (requestedProjectId && !selectedProject) {
    return <WorkspaceSourceState title="项目未在当前工作区被观察到" detail="深链中的 project ID 不属于当前正式项目列表；系统没有自动替换为第一条项目。" />;
  }

  return (
    <WorkflowOperationsPage
      programs={projection.programs}
      projects={projection.projects}
      instances={instancesQuery.data ?? []}
      selection={selection}
      section={section}
      onSelectContext={selectContext}
      onSelectSection={(next) => updateSearch({ section: next })}
    />
  );
}

function WorkspaceSourceState({ title, detail }: { title: string; detail: string }) {
  return (
    <section className="rounded-xl border border-dashed bg-card p-6" data-testid="workflow-source-state">
      <h1 className="text-sm font-semibold">{title}</h1>
      <p className="mt-2 text-xs text-muted-foreground">{detail}</p>
    </section>
  );
}

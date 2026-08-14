"use client";

import { useMemo } from "react";
import type { OperatingProgram, OperatingProject } from "@multica/core/workflow";
import type { WorkflowProgramMutationState } from "./workflow-context-tree";

export type WorkflowProgramProjectMutation = "assign" | "unassign";

export interface WorkflowProgramSettingsProps {
  program: OperatingProgram;
  projects: OperatingProject[];
  onAssignProject?: (programId: string, projectId: string) => void | Promise<void>;
  onUnassignProject?: (programId: string, projectId: string) => void | Promise<void>;
  projectMutationState?: (input: { programId: string; projectId: string; mutation: WorkflowProgramProjectMutation }) => WorkflowProgramMutationState;
  projectMutationError?: (input: { programId: string; projectId: string; mutation: WorkflowProgramProjectMutation }) => string | undefined;
}

function isProjectAssigned(program: OperatingProgram, project: OperatingProject): boolean {
  return !isUnassignedProject(project) && (project.programId === program.id || program.projectIds.includes(project.id));
}

function isUnassignedProject(project: OperatingProject): boolean {
  return project.programClassification === "unassigned" || project.programId === "";
}

export function WorkflowProgramSettings({
  program,
  projects,
  onAssignProject,
  onUnassignProject,
  projectMutationState,
  projectMutationError,
}: WorkflowProgramSettingsProps) {
  const assigned = useMemo(() => projects.filter((project) => isProjectAssigned(program, project)), [program, projects]);
  const unassigned = useMemo(
    () => projects.filter((project) => isUnassignedProject(project) && !program.projectIds.includes(project.id)),
    [program.projectIds, projects],
  );
  const assignedElsewhere = useMemo(
    () => projects.filter((project) => !isUnassignedProject(project) && project.programId !== program.id && !program.projectIds.includes(project.id)),
    [program.id, program.projectIds, projects],
  );

  return (
    <section className="space-y-4" data-testid="workflow-program-settings">
      <div className="rounded-lg border bg-card p-5">
        <h2 className="text-sm font-semibold">运营科目设置</h2>
        <p className="mt-1 text-xs text-muted-foreground">{program.name}</p>
        {program.description ? <p className="mt-2 text-xs text-muted-foreground">{program.description}</p> : null}
        <p className="mt-3 text-[11px] text-muted-foreground">这里只管理 L3 科目与正式 L4 Project 的归属关系；Project、成果和工作流定义仍由各自正式系统负责。</p>
      </div>

      <ProjectAssignmentGroup
        title="已归入此科目的正式项目"
        description="这些项目会出现在当前科目树下。"
        projects={assigned}
        programId={program.id}
        mutation="unassign"
        actionLabel="移出科目"
        emptyLabel="当前科目尚未归入正式项目"
        onAction={onUnassignProject}
        projectMutationState={projectMutationState}
        projectMutationError={projectMutationError}
      />

      <ProjectAssignmentGroup
        title="未归类正式项目"
        description="归入后，该 Project 才会出现在此 L3 科目下面。"
        projects={unassigned}
        programId={program.id}
        mutation="assign"
        actionLabel="归入此科目"
        emptyLabel="没有未归类正式项目"
        onAction={onAssignProject}
        projectMutationState={projectMutationState}
        projectMutationError={projectMutationError}
        testId="workflow-program-unassigned-projects"
      />

      {assignedElsewhere.length > 0 ? (
        <section className="rounded-lg border border-dashed p-4" data-testid="workflow-program-assigned-elsewhere">
          <h3 className="text-xs font-semibold">已归属于其他科目的项目</h3>
          <p className="mt-1 text-[11px] text-muted-foreground">为保持一个 Project 只有一个 L3 归属，这些项目不能直接重复归入当前科目。</p>
          <ul className="mt-2 space-y-1 text-xs text-muted-foreground">
            {assignedElsewhere.map((project) => <li key={project.id}>{project.name}</li>)}
          </ul>
        </section>
      ) : null}
    </section>
  );
}

function ProjectAssignmentGroup({
  title,
  description,
  projects,
  programId,
  mutation,
  actionLabel,
  emptyLabel,
  onAction,
  projectMutationState,
  projectMutationError,
  testId,
}: {
  title: string;
  description: string;
  projects: OperatingProject[];
  programId: string;
  mutation: WorkflowProgramProjectMutation;
  actionLabel: string;
  emptyLabel: string;
  onAction?: (programId: string, projectId: string) => void | Promise<void>;
  projectMutationState?: WorkflowProgramSettingsProps["projectMutationState"];
  projectMutationError?: WorkflowProgramSettingsProps["projectMutationError"];
  testId?: string;
}) {
  return (
    <section className="rounded-lg border bg-card p-4" data-testid={testId ?? `workflow-program-${mutation}-projects`}>
      <h3 className="text-xs font-semibold">{title}</h3>
      <p className="mt-1 text-[11px] text-muted-foreground">{description}</p>
      {projects.length === 0 ? <p className="mt-3 text-xs text-muted-foreground">{emptyLabel}</p> : (
        <ul className="mt-3 space-y-2">
          {projects.map((project) => {
            const state = projectMutationState?.({ programId, projectId: project.id, mutation }) ?? (onAction ? "ready" : "disabled");
            const error = projectMutationError?.({ programId, projectId: project.id, mutation });
            const disabled = !onAction || state === "loading" || state === "disabled";
            return (
              <li key={project.id} className="flex flex-wrap items-center gap-2 rounded border px-3 py-2 text-xs">
                <span className="min-w-0 flex-1 truncate">{project.name}{project.platform ? <span className="ml-2 text-[10px] text-muted-foreground">{project.platform}</span> : null}</span>
                <button type="button" aria-label={`${actionLabel}：${project.name}`} className="rounded border px-2 py-1 text-[11px] hover:bg-accent disabled:cursor-not-allowed disabled:opacity-60" disabled={disabled} onClick={() => onAction?.(programId, project.id)}>
                  {state === "loading" ? "处理中…" : actionLabel}
                </button>
                {error ? <p role="alert" className="basis-full text-[11px] text-destructive">操作失败：{error}</p> : null}
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}

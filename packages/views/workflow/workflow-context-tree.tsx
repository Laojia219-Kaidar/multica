"use client";

import { ChevronDown, ChevronRight, FolderKanban, PanelLeftClose, PanelLeftOpen } from "lucide-react";
import { useMemo, useState } from "react";
import type { OperatingProgram, OperatingProject } from "@multica/core/workflow";

export type WorkflowContextSelection =
  | { kind: "program"; id: string }
  | { kind: "project"; id: string };

export interface WorkflowContextTreeProps {
  programs: OperatingProgram[];
  projects: OperatingProject[];
  selected?: WorkflowContextSelection;
  onSelect: (selection: WorkflowContextSelection) => void;
  collapsed?: boolean;
  onToggleCollapsed?: () => void;
}

export function WorkflowContextTree({
  programs,
  projects,
  selected,
  onSelect,
  collapsed = false,
  onToggleCollapsed,
}: WorkflowContextTreeProps) {
  const [expanded, setExpanded] = useState<Record<string, boolean>>(() =>
    Object.fromEntries(programs.map((program) => [program.id, true])),
  );
  const projectsByProgram = useMemo(() => {
    const result = new Map<string, OperatingProject[]>();
    for (const project of projects) result.set(project.programId, [...(result.get(project.programId) ?? []), project]);
    return result;
  }, [projects]);

  if (collapsed) {
    return (
      <aside className="flex w-10 shrink-0 flex-col items-center border-r bg-muted/20 py-3" data-testid="workflow-context-tree-collapsed">
        <button type="button" aria-label="展开运营项目树" title="展开运营项目树" onClick={onToggleCollapsed} className="rounded p-1.5 hover:bg-accent">
          <PanelLeftOpen className="h-4 w-4" />
        </button>
      </aside>
    );
  }

  return (
    <aside className="flex w-64 min-w-52 max-w-80 shrink-0 flex-col border-r bg-muted/20" data-testid="workflow-context-tree">
      <div className="flex items-center justify-between border-b px-3 py-2">
        <div>
          <p className="text-xs font-semibold">运营项目</p>
          <p className="text-[11px] text-muted-foreground">L3 科目 · L4 项目</p>
        </div>
        <button type="button" aria-label="隐藏运营项目树" title="隐藏运营项目树" onClick={onToggleCollapsed} className="rounded p-1.5 hover:bg-accent">
          <PanelLeftClose className="h-4 w-4" />
        </button>
      </div>
      <nav className="min-h-0 flex-1 overflow-y-auto p-2" aria-label="运营项目上下文">
        {programs.length === 0 ? <p className="p-2 text-xs text-muted-foreground">暂无运营科目</p> : null}
        {programs.map((program) => {
          const isExpanded = expanded[program.id] !== false;
          const programSelected = selected?.kind === "program" && selected.id === program.id;
          return (
            <div key={program.id} className="mb-1">
              <div className="flex items-center gap-1">
                <button
                  type="button"
                  aria-label={`${isExpanded ? "折叠" : "展开"}${program.name}`}
                  className="rounded p-1 hover:bg-accent"
                  onClick={() => setExpanded((current) => ({ ...current, [program.id]: !isExpanded }))}
                >
                  {isExpanded ? <ChevronDown className="h-3.5 w-3.5" /> : <ChevronRight className="h-3.5 w-3.5" />}
                </button>
                <button
                  type="button"
                  data-testid={`workflow-program-${program.id}`}
                  aria-current={programSelected ? "page" : undefined}
                  onClick={() => onSelect({ kind: "program", id: program.id })}
                  className={`flex min-w-0 flex-1 items-center gap-2 rounded px-2 py-1.5 text-left text-xs font-medium hover:bg-accent ${programSelected ? "bg-accent" : ""}`}
                >
                  <FolderKanban className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                  <span className="truncate">{program.name}</span>
                </button>
              </div>
              {isExpanded ? (
                <div className="ml-5 border-l pl-2">
                  {(projectsByProgram.get(program.id) ?? []).map((project) => {
                    const projectSelected = selected?.kind === "project" && selected.id === project.id;
                    return (
                      <button
                        type="button"
                        key={project.id}
                        data-testid={`workflow-project-${project.id}`}
                        aria-current={projectSelected ? "page" : undefined}
                        onClick={() => onSelect({ kind: "project", id: project.id })}
                        className={`mb-0.5 flex w-full items-center justify-between rounded px-2 py-1.5 text-left text-xs hover:bg-accent ${projectSelected ? "bg-accent" : ""}`}
                      >
                        <span className="truncate">{project.name}</span>
                        {project.platform ? <span className="ml-2 shrink-0 text-[10px] text-muted-foreground">{project.platform}</span> : null}
                      </button>
                    );
                  })}
                </div>
              ) : null}
            </div>
          );
        })}
      </nav>
    </aside>
  );
}

"use client";

import { ChevronDown, ChevronRight, FolderKanban, PanelLeftClose, PanelLeftOpen } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
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

const CONTEXT_TREE_WIDTH_KEY = "hivecrew.workflow.context-tree.width.v1";
const DEFAULT_CONTEXT_TREE_WIDTH = 256;
const MIN_CONTEXT_TREE_WIDTH = 208;
const MAX_CONTEXT_TREE_WIDTH = 360;

function clampWidth(value: number) {
  return Math.min(MAX_CONTEXT_TREE_WIDTH, Math.max(MIN_CONTEXT_TREE_WIDTH, value));
}

export function WorkflowContextTree({
  programs,
  projects,
  selected,
  onSelect,
  collapsed = false,
  onToggleCollapsed,
}: WorkflowContextTreeProps) {
  const [width, setWidth] = useState(DEFAULT_CONTEXT_TREE_WIDTH);
  const resizeCleanup = useRef<(() => void) | null>(null);
  const [expanded, setExpanded] = useState<Record<string, boolean>>(() =>
    Object.fromEntries(programs.map((program) => [program.id, true])),
  );
  const projectsByProgram = useMemo(() => {
    const result = new Map<string, OperatingProject[]>();
    for (const project of projects) result.set(project.programId, [...(result.get(project.programId) ?? []), project]);
    return result;
  }, [projects]);

  useEffect(() => {
    try {
      const stored = Number(window.localStorage.getItem(CONTEXT_TREE_WIDTH_KEY));
      if (Number.isFinite(stored)) setWidth(clampWidth(stored));
    } catch {
      // Layout preference is optional and must never block project context.
    }
  }, []);

  useEffect(() => () => resizeCleanup.current?.(), []);

  const setAndPersistWidth = useCallback((next: number) => {
    const bounded = clampWidth(next);
    setWidth(bounded);
    try {
      window.localStorage.setItem(CONTEXT_TREE_WIDTH_KEY, String(bounded));
    } catch {
      // Private-mode or quota errors leave the current in-memory layout usable.
    }
  }, []);

  const startResize = useCallback((event: React.PointerEvent<HTMLButtonElement>) => {
    event.preventDefault();
    const originX = event.clientX;
    const originWidth = width;
    resizeCleanup.current?.();
    const onMove = (move: PointerEvent) => setAndPersistWidth(originWidth + move.clientX - originX);
    const onUp = () => {
      window.removeEventListener("pointermove", onMove);
      window.removeEventListener("pointerup", onUp);
      resizeCleanup.current = null;
    };
    resizeCleanup.current = onUp;
    window.addEventListener("pointermove", onMove);
    window.addEventListener("pointerup", onUp, { once: true });
  }, [setAndPersistWidth, width]);

  const onResizeKeyDown = useCallback((event: React.KeyboardEvent<HTMLButtonElement>) => {
    if (event.key === "ArrowLeft") {
      event.preventDefault();
      setAndPersistWidth(width - 16);
    } else if (event.key === "ArrowRight") {
      event.preventDefault();
      setAndPersistWidth(width + 16);
    } else if (event.key === "Home") {
      event.preventDefault();
      setAndPersistWidth(MIN_CONTEXT_TREE_WIDTH);
    } else if (event.key === "End") {
      event.preventDefault();
      setAndPersistWidth(MAX_CONTEXT_TREE_WIDTH);
    }
  }, [setAndPersistWidth, width]);

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
    <aside className="relative flex min-w-0 shrink-0 flex-col border-r bg-muted/20" style={{ width }} data-testid="workflow-context-tree">
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
      <button
        type="button"
        role="separator"
        aria-label="调整运营项目树宽度"
        aria-orientation="vertical"
        aria-valuemin={MIN_CONTEXT_TREE_WIDTH}
        aria-valuemax={MAX_CONTEXT_TREE_WIDTH}
        aria-valuenow={width}
        onPointerDown={startResize}
        onKeyDown={onResizeKeyDown}
        className="absolute -right-1 top-0 z-10 h-full w-2 cursor-col-resize touch-none focus:bg-primary/30 focus:outline-none"
      />
    </aside>
  );
}

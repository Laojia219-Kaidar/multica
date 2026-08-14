"use client";

import { useEffect, useMemo, useState } from "react";
import { ChevronRight, LayoutDashboard, PackageCheck, PlayCircle, Settings2, Workflow } from "lucide-react";
import type { ArtifactSummary, OperatingProgram, OperatingProject, WorkflowDefinitionDraft, WorkflowRuntime } from "@multica/core/workflow";
import type { WorkflowDefinition, WorkflowInstance } from "@multica/core/api/workflow";
import { WorkflowContextTree, type WorkflowContextSelection } from "./workflow-context-tree";
import { WorkflowDesigner } from "./workflow-designer";
import { WorkflowRuntimeGraph } from "./workflow-runtime-graph";
import { WorkflowWorkbench } from "./workflow-workbench";

export type WorkflowOperationsSection = "overview" | "plan" | "workflow" | "instances" | "artifacts" | "review" | "settings";

export interface WorkflowOperationsPageProps {
  programs: OperatingProgram[];
  projects: OperatingProject[];
  definitionDrafts?: WorkflowDefinitionDraft[];
  runtime?: WorkflowRuntime;
  artifacts?: ArtifactSummary[];
  instances?: WorkflowInstance[];
  definitions?: WorkflowDefinition[];
  selection?: WorkflowContextSelection;
  section?: WorkflowOperationsSection;
  initialSelection?: WorkflowContextSelection;
  initialSection?: WorkflowOperationsSection;
  onSelectContext?: (selection: WorkflowContextSelection) => void;
  onSelectSection?: (section: WorkflowOperationsSection) => void;
  onChangeDefinition?: (definition: WorkflowDefinitionDraft) => void;
  onCreateDefinition?: (project: OperatingProject) => void;
  onPublishDefinition?: (definition: WorkflowDefinitionDraft) => void;
  publishReceipt?: { definitionId: string; version: number; changed: boolean } | null;
  publishError?: string | null;
}

const CONTEXT_TREE_COLLAPSED_KEY = "hivecrew.workflow.context-tree.collapsed.v1";

const sectionLabels: Array<{ id: WorkflowOperationsSection; label: string; icon: typeof LayoutDashboard }> = [
  { id: "overview", label: "项目总览", icon: LayoutDashboard },
  { id: "plan", label: "生产计划", icon: PlayCircle },
  { id: "workflow", label: "工作流", icon: Workflow },
  { id: "instances", label: "运行实例", icon: PlayCircle },
  { id: "artifacts", label: "项目成果", icon: PackageCheck },
  { id: "review", label: "数据复盘", icon: PackageCheck },
  { id: "settings", label: "项目设置", icon: Settings2 },
];

function EmptyProjectState() {
  return <div className="flex min-h-[360px] items-center justify-center rounded-lg border border-dashed p-6 text-center"><div><p className="text-sm font-medium">请选择一个运营科目或项目</p><p className="mt-1 text-xs text-muted-foreground">L3 是运营科目，L4 是可独立运营项目；流程动作只存在于工作流图中。</p></div></div>;
}

export function WorkflowOperationsPage({
  programs,
  projects,
  definitionDrafts = [],
  runtime,
  artifacts = [],
  instances = [],
  definitions = [],
  selection: controlledSelection,
  section: controlledSection,
  initialSelection,
  initialSection = "overview",
  onSelectContext,
  onSelectSection,
  onChangeDefinition,
  onCreateDefinition,
  onPublishDefinition,
  publishReceipt,
  publishError,
}: WorkflowOperationsPageProps) {
  const [uncontrolledSelection, setUncontrolledSelection] = useState<WorkflowContextSelection | undefined>(initialSelection);
  const [uncontrolledSection, setUncontrolledSection] = useState<WorkflowOperationsSection>(initialSection);
  const [treeCollapsed, setTreeCollapsed] = useState(false);

  useEffect(() => {
    try {
      setTreeCollapsed(window.localStorage.getItem(CONTEXT_TREE_COLLAPSED_KEY) === "true");
    } catch {
      // Layout preference is intentionally best-effort.
    }
  }, []);
  const selection = controlledSelection ?? uncontrolledSelection;
  const section = controlledSection ?? uncontrolledSection;
  const project = selection?.kind === "project" ? projects.find((item) => item.id === selection.id) : undefined;
  const program = selection?.kind === "program" ? programs.find((item) => item.id === selection.id) : project ? programs.find((item) => item.id === project.programId) : undefined;
  const selectedDraft = useMemo(() => definitionDrafts.find((draft) => draft.projectId === project?.id), [definitionDrafts, project?.id]);

  const selectContext = (next: WorkflowContextSelection) => {
    if (!controlledSelection) setUncontrolledSelection(next);
    onSelectContext?.(next);
  };

  const selectSection = (next: WorkflowOperationsSection) => {
    if (!controlledSection) setUncontrolledSection(next);
    onSelectSection?.(next);
  };

  return (
    <div className="flex min-h-[620px] flex-col overflow-hidden rounded-xl border bg-background" data-testid="workflow-operations-page">
      <header className="flex flex-wrap items-center gap-2 border-b px-4 py-3">
        <Workflow className="h-4 w-4 text-muted-foreground" />
        <div className="min-w-0"><h1 className="text-sm font-semibold">工作流生产运营</h1><p className="truncate text-xs text-muted-foreground">{program?.name ?? "选择运营项目开始"}{project ? ` / ${project.name}` : ""}</p></div>
        {project ? <span className="ml-auto rounded-full border px-2 py-1 text-[11px] text-muted-foreground">Project · {project.formalProjectId}</span> : null}
      </header>
      <div className="flex min-h-0 flex-1">
        <WorkflowContextTree programs={programs} projects={projects} selected={selection} onSelect={selectContext} collapsed={treeCollapsed} onToggleCollapsed={() => setTreeCollapsed((value) => {
          const next = !value;
          try {
            window.localStorage.setItem(CONTEXT_TREE_COLLAPSED_KEY, String(next));
          } catch {
            // The current view remains usable when local storage is unavailable.
          }
          return next;
        })} />
        <main className="min-w-0 flex-1 overflow-y-auto p-4" data-testid="workflow-main-workbench">
          <div className="mb-4 flex flex-wrap items-center gap-1 border-b pb-2" role="tablist" aria-label="项目工作区">
            {sectionLabels.map(({ id, label, icon: Icon }) => <button type="button" role="tab" aria-selected={section === id} key={id} onClick={() => selectSection(id)} className={`inline-flex items-center gap-1 rounded px-2 py-1.5 text-xs hover:bg-accent ${section === id ? "bg-accent font-medium" : "text-muted-foreground"}`}><Icon className="h-3.5 w-3.5" />{label}</button>)}
          </div>
          {!selection ? <EmptyProjectState /> : section === "workflow" && selectedDraft ? <><WorkflowDesigner definition={selectedDraft} onChange={onChangeDefinition} onPublish={onPublishDefinition} />{publishReceipt ? <p className="mt-3 rounded border border-emerald-500/30 bg-emerald-500/5 p-2 text-xs text-emerald-700">已持久化候选版本：{publishReceipt.definitionId} v{publishReceipt.version}{publishReceipt.changed ? "" : "（幂等重放）"}</p> : null}{publishError ? <p className="mt-3 rounded border border-destructive/40 bg-destructive/5 p-2 text-xs text-destructive">发布未完成：{publishError}。草稿仍保留在当前页面。</p> : null}</> : section === "workflow" && project && onCreateDefinition ? <div className="rounded-lg border border-dashed p-6 text-center"><p className="text-xs text-muted-foreground">该项目尚无已发布工作流版本。</p><button type="button" onClick={() => onCreateDefinition(project)} className="mt-3 rounded border px-3 py-1.5 text-xs hover:bg-accent">新建候选工作流草稿</button></div> : section === "workflow" ? <div className="rounded-lg border border-dashed p-6 text-center text-xs text-muted-foreground">该项目暂无工作流草稿</div> : section === "instances" ? <WorkflowWorkbench instances={instances} definitions={definitions} /> : section === "artifacts" ? <ArtifactList artifacts={artifacts} /> : <ProjectSection section={section} project={project} program={program} />}
          {runtime && section === "instances" ? <div className="mt-4"><WorkflowRuntimeGraph graph={selectedDraft?.graph ?? { nodes: [], edges: [] }} runtime={runtime} /></div> : null}
        </main>
        {runtime && section !== "instances" ? <aside className="hidden w-72 shrink-0 border-l p-3 xl:block" data-testid="workflow-inspector"><WorkflowRuntimeGraph graph={selectedDraft?.graph ?? { nodes: [], edges: [] }} runtime={runtime} /></aside> : null}
      </div>
    </div>
  );
}

function ProjectSection({ section, project, program }: { section: WorkflowOperationsSection; project?: OperatingProject; program?: OperatingProgram }) {
  const label = sectionLabels.find((item) => item.id === section)?.label ?? section;
  return <section className="rounded-lg border bg-card p-5"><div className="flex items-center gap-2"><ChevronRight className="h-4 w-4 text-muted-foreground" /><h2 className="text-sm font-semibold">{label}</h2></div><p className="mt-2 text-xs text-muted-foreground">{project ? `${program?.name ?? "运营科目"} / ${project.name}` : "请选择 L4 项目"}。该视图将由正式 API 数据驱动。</p></section>;
}

function ArtifactList({ artifacts }: { artifacts: ArtifactSummary[] }) {
  return <section data-testid="workflow-artifact-list" className="rounded-lg border bg-card p-4"><h2 className="text-sm font-semibold">项目成果</h2>{artifacts.length === 0 ? <p className="mt-3 text-xs text-muted-foreground">暂无成果；工作流节点产出会进入正式 Outcome Center。</p> : <div className="mt-3 space-y-2">{artifacts.map((artifact) => <div key={`${artifact.id}:${artifact.version}`} className="rounded border p-3"><div className="flex items-center justify-between gap-2 text-xs"><span className="font-medium">{artifact.title}</span><span className="text-muted-foreground">v{artifact.version} · {artifact.status}</span></div><div className="mt-1 text-[11px] text-muted-foreground">{artifact.id} · {artifact.locationCount ?? 0} 个物理位置</div></div>)}</div>}</section>;
}

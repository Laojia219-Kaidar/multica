"use client";

import { useEffect, useMemo, useState } from "react";
import { ChevronRight, LayoutDashboard, PackageCheck, PlayCircle, Settings2, Workflow } from "lucide-react";
import type { ArtifactSummary, OperatingProgram, OperatingProject, WorkflowDefinitionDraft, WorkflowRuntime } from "@multica/core/workflow";
import type { WorkflowDefinition, WorkflowInstance } from "@multica/core/api/workflow";
import type { CompanyOpsArtifactReplicaLocation, CompanyOpsOutcomeSummary } from "@multica/core/types";
import { WorkflowContextTree, type WorkflowContextSelection, type WorkflowProgramMutationState } from "./workflow-context-tree";
import { WorkflowDesigner } from "./workflow-designer";
import { WorkflowRuntimeGraph } from "./workflow-runtime-graph";
import { WorkflowWorkbench, type WorkflowWorkbenchProps } from "./workflow-workbench";
import { WorkflowProgramSettings, type WorkflowProgramSettingsProps } from "./workflow-program-settings";
import { WechatProductionPanel, type WechatProductionPanelProps } from "./wechat-production-panel";

export type WorkflowOperationsSection = "overview" | "plan" | "workflow" | "instances" | "artifacts" | "review" | "settings";

export interface WorkflowOperationsPageProps {
  programs: OperatingProgram[];
  projects: OperatingProject[];
  definitionDrafts?: WorkflowDefinitionDraft[];
  runtime?: WorkflowRuntime;
  runtimeGraph?: WorkflowDefinitionDraft["graph"];
  artifacts?: ArtifactSummary[];
  outcomes?: CompanyOpsOutcomeSummary[];
  outcomesLoading?: boolean;
  outcomesError?: boolean;
  outcomeHref?: (outcomeId: string) => string;
  artifactLocationsByOutcome?: Record<string, ArtifactLocationsState>;
  instances?: WorkflowInstance[];
  definitions?: WorkflowDefinition[];
  /** Read-only API adapter forwarded to the instances workbench. */
  workbench?: Omit<WorkflowWorkbenchProps, "instances" | "definitions">;
  selectedDefinitionId?: string;
  selection?: WorkflowContextSelection;
  section?: WorkflowOperationsSection;
  initialSelection?: WorkflowContextSelection;
  initialSection?: WorkflowOperationsSection;
  onSelectContext?: (selection: WorkflowContextSelection) => void;
  onSelectSection?: (section: WorkflowOperationsSection) => void;
  onSelectDefinition?: (definitionId: string) => void;
  onChangeDefinition?: (definition: WorkflowDefinitionDraft) => void;
  onCreateDefinition?: (project: OperatingProject) => void;
  onPublishDefinition?: (definition: WorkflowDefinitionDraft) => void;
  publishReceipt?: { definitionId: string; version: number; changed: boolean } | null;
  publishError?: string | null;
  /**
   * Optional WeChat content production operations surface (WO-20), rendered
   * on the L4 project's 生产计划 section. The integrator supplies the
   * server-resolved authority context, published definition pins, and the
   * server-side production read models; omitting it keeps the placeholder.
   */
  wechatProduction?: Omit<WechatProductionPanelProps, "projectId" | "projectName"> | null;
  /** Pure view callbacks for L3 subject and L4 Project classification. */
  programManagement?: {
    onCreateProgram?: (input: { name: string; description?: string }) => void | Promise<void>;
    programCreationState?: WorkflowProgramMutationState;
    programCreationError?: string;
    onUpdateProgram?: WorkflowProgramSettingsProps["onUpdateProgram"];
    programUpdateState?: WorkflowProgramSettingsProps["programUpdateState"];
    programUpdateError?: WorkflowProgramSettingsProps["programUpdateError"];
    onDeleteProgram?: WorkflowProgramSettingsProps["onDeleteProgram"];
    programDeletionState?: WorkflowProgramSettingsProps["programDeletionState"];
    programDeletionError?: WorkflowProgramSettingsProps["programDeletionError"];
    onAssignProject?: WorkflowProgramSettingsProps["onAssignProject"];
    onUnassignProject?: WorkflowProgramSettingsProps["onUnassignProject"];
    projectMutationState?: WorkflowProgramSettingsProps["projectMutationState"];
    projectMutationError?: WorkflowProgramSettingsProps["projectMutationError"];
  };
}

export interface ArtifactLocationsState {
  items: CompanyOpsArtifactReplicaLocation[];
  loading?: boolean;
  error?: boolean;
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
  runtimeGraph,
  artifacts = [],
  outcomes = [],
  outcomesLoading = false,
  outcomesError = false,
  outcomeHref,
  artifactLocationsByOutcome,
  instances = [],
  definitions = [],
  workbench,
  selectedDefinitionId,
  selection: controlledSelection,
  section: controlledSection,
  initialSelection,
  initialSection = "overview",
  onSelectContext,
  onSelectSection,
  onSelectDefinition,
  onChangeDefinition,
  onCreateDefinition,
  onPublishDefinition,
  publishReceipt,
  publishError,
  wechatProduction,
  programManagement,
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
  const program = selection?.kind === "program" ? programs.find((item) => item.id === selection.id) : project?.programId ? programs.find((item) => item.id === project.programId) : undefined;
  const projectDrafts = useMemo(() => definitionDrafts.filter((draft) => draft.projectId === project?.id), [definitionDrafts, project?.id]);
  const selectedDraft = useMemo(
    () => projectDrafts.find((draft) => draft.id === selectedDefinitionId) ?? projectDrafts[0],
    [projectDrafts, selectedDefinitionId],
  );

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
        <WorkflowContextTree programs={programs} projects={projects} selected={selection} onSelect={selectContext} onCreateProgram={programManagement?.onCreateProgram} programCreationState={programManagement?.programCreationState} programCreationError={programManagement?.programCreationError} collapsed={treeCollapsed} onToggleCollapsed={() => setTreeCollapsed((value) => {
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
          {!selection ? <EmptyProjectState /> : selection.kind === "program" && section === "settings" && program ? <WorkflowProgramSettings program={program} projects={projects} onUpdateProgram={programManagement?.onUpdateProgram} programUpdateState={programManagement?.programUpdateState} programUpdateError={programManagement?.programUpdateError} onDeleteProgram={programManagement?.onDeleteProgram} programDeletionState={programManagement?.programDeletionState} programDeletionError={programManagement?.programDeletionError} onAssignProject={programManagement?.onAssignProject} onUnassignProject={programManagement?.onUnassignProject} projectMutationState={programManagement?.projectMutationState} projectMutationError={programManagement?.projectMutationError} /> : section === "workflow" && project ? <WorkflowDefinitionsPanel project={project} drafts={projectDrafts} selectedDraft={selectedDraft} onSelectDefinition={onSelectDefinition} onCreateDefinition={onCreateDefinition} onChangeDefinition={onChangeDefinition} onPublishDefinition={onPublishDefinition} publishReceipt={publishReceipt} publishError={publishError} /> : section === "instances" ? <WorkflowWorkbench instances={instances} definitions={definitions} {...workbench} /> : section === "artifacts" && !project ? <NoProjectArtifactState /> : section === "artifacts" ? <ArtifactList artifacts={artifacts} outcomes={outcomes} loading={outcomesLoading} sourceError={outcomesError} outcomeHref={outcomeHref} artifactLocationsByOutcome={artifactLocationsByOutcome} /> : section === "plan" && project && wechatProduction ? <WechatProductionPanel projectId={project.formalProjectId} projectName={project.name} {...wechatProduction} /> : <ProjectSection section={section} project={project} program={program} />}
          {runtime && section === "instances" ? <div className="mt-4"><WorkflowRuntimeGraph graph={runtimeGraph ?? selectedDraft?.graph ?? { nodes: [], edges: [] }} runtime={runtime} /></div> : null}
        </main>
        {runtime && section !== "instances" ? <aside className="hidden w-72 shrink-0 border-l p-3 xl:block" data-testid="workflow-inspector"><WorkflowRuntimeGraph graph={runtimeGraph ?? selectedDraft?.graph ?? { nodes: [], edges: [] }} runtime={runtime} /></aside> : null}
      </div>
    </div>
  );
}

function WorkflowDefinitionsPanel({
  project,
  drafts,
  selectedDraft,
  onSelectDefinition,
  onCreateDefinition,
  onChangeDefinition,
  onPublishDefinition,
  publishReceipt,
  publishError,
}: {
  project: OperatingProject;
  drafts: WorkflowDefinitionDraft[];
  selectedDraft?: WorkflowDefinitionDraft;
  onSelectDefinition?: (definitionId: string) => void;
  onCreateDefinition?: (project: OperatingProject) => void;
  onChangeDefinition?: (definition: WorkflowDefinitionDraft) => void;
  onPublishDefinition?: (definition: WorkflowDefinitionDraft) => void;
  publishReceipt?: WorkflowOperationsPageProps["publishReceipt"];
  publishError?: string | null;
}) {
  if (!selectedDraft) {
    return <div className="rounded-lg border border-dashed p-6 text-center"><p className="text-xs text-muted-foreground">该项目尚无工作流。每个项目可并行维护多条独立的候选或已发布流程。</p>{onCreateDefinition ? <button type="button" onClick={() => onCreateDefinition(project)} className="mt-3 rounded border px-3 py-1.5 text-xs hover:bg-accent">新建候选工作流草稿</button> : null}</div>;
  }
  return <>
    <div className="mb-3 flex flex-wrap items-center gap-2 rounded border bg-muted/20 p-2">
      <label className="text-xs text-muted-foreground" htmlFor="workflow-definition-select">项目工作流</label>
      <select id="workflow-definition-select" aria-label="选择项目工作流" value={selectedDraft.id} onChange={(event) => onSelectDefinition?.(event.target.value)} className="min-w-0 flex-1 rounded border bg-background px-2 py-1 text-xs">
        {drafts.map((draft) => <option key={draft.id} value={draft.id}>{draft.name} · v{draft.version}</option>)}
      </select>
      {onCreateDefinition ? <button type="button" onClick={() => onCreateDefinition(project)} className="rounded border px-2 py-1 text-xs hover:bg-accent">新增工作流</button> : null}
    </div>
    <WorkflowDesigner definition={selectedDraft} onChange={onChangeDefinition} onPublish={onPublishDefinition} />
    {publishReceipt ? <p className="mt-3 rounded border border-emerald-500/30 bg-emerald-500/5 p-2 text-xs text-emerald-700">已持久化候选版本：{publishReceipt.definitionId} v{publishReceipt.version}{publishReceipt.changed ? "" : "（幂等重放）"}</p> : null}
    {publishError ? <p className="mt-3 rounded border border-destructive/40 bg-destructive/5 p-2 text-xs text-destructive">发布未完成：{publishError}。草稿仍保留在当前页面。</p> : null}
  </>;
}

function ProjectSection({ section, project, program }: { section: WorkflowOperationsSection; project?: OperatingProject; program?: OperatingProgram }) {
  const label = sectionLabels.find((item) => item.id === section)?.label ?? section;
  return <section className="rounded-lg border bg-card p-5"><div className="flex items-center gap-2"><ChevronRight className="h-4 w-4 text-muted-foreground" /><h2 className="text-sm font-semibold">{label}</h2></div><p className="mt-2 text-xs text-muted-foreground">{project ? `${program?.name ?? "运营科目"} / ${project.name}` : "请选择 L4 项目"}。该视图将由正式 API 数据驱动。</p></section>;
}

function NoProjectArtifactState() {
  return <section data-testid="workflow-artifact-list" className="rounded-lg border border-dashed bg-card p-5"><h2 className="text-sm font-semibold">项目成果</h2><p className="mt-2 text-xs text-muted-foreground">先选择 L4 项目，尚未查询成果</p><p className="mt-1 text-[11px] text-muted-foreground">L3 运营科目不直接查询成果；成果归属于正式 Project，并由 Outcome Center 提供。</p></section>;
}

function ArtifactList({ artifacts, outcomes, loading, sourceError, outcomeHref, artifactLocationsByOutcome }: { artifacts: ArtifactSummary[]; outcomes: CompanyOpsOutcomeSummary[]; loading: boolean; sourceError: boolean; outcomeHref?: (outcomeId: string) => string; artifactLocationsByOutcome?: Record<string, ArtifactLocationsState> }) {
  return <section data-testid="workflow-artifact-list" className="rounded-lg border bg-card p-4"><h2 className="text-sm font-semibold">项目成果</h2><p className="mt-1 text-xs text-muted-foreground">读取既有正式 Outcome Center；本页面不维护第二套成果或审核状态。存储位置仅是副本观察台账。</p>{sourceError ? <p data-testid="workflow-artifact-source-error" className="mt-3 rounded border border-destructive/40 p-3 text-xs text-destructive">成果中心来源暂不可用；不会把读取失败显示为零成果。</p> : loading ? <p className="mt-3 text-xs text-muted-foreground">正在读取正式成果中心…</p> : outcomes.length > 0 ? <div className="mt-3 space-y-2">{outcomes.map((outcome) => { const locations = artifactLocationsByOutcome?.[outcome.id]; return <div key={outcome.id} className="rounded border p-3"><div className="flex items-center justify-between gap-2 text-xs"><span className="font-medium">Outcome · {outcome.id}</span><span className="text-muted-foreground">{outcome.execution_state}</span></div><div className="mt-1 text-[11px] text-muted-foreground">{outcome.current_agent_display.name} · 版本 {outcome.version_count}{outcome.active_artifact ? ` · ${outcome.active_artifact.status}${outcome.active_artifact.formal_visible ? "（正式可见）" : "（候选）"}` : " · 尚未形成活动成果"}</div>{locations?.error ? <p data-testid={`workflow-artifact-location-error-${outcome.id}`} className="mt-2 text-[11px] text-destructive">存储位置来源暂不可用；不会把读取失败显示为零位置。</p> : locations?.loading ? <p className="mt-2 text-[11px] text-muted-foreground">正在读取存储位置…</p> : locations ? <p data-testid={`workflow-artifact-location-summary-${outcome.id}`} className="mt-2 text-[11px] text-muted-foreground">已登记 {locations.items.length} 个存储位置{locations.items.length > 0 ? `：${locations.items.map((item) => `${item.location_class} / ${item.storage_id}`).join("、")}` : "（当前无位置台账记录）"}</p> : null}{outcomeHref ? <a className="mt-2 inline-block text-xs underline" href={outcomeHref(outcome.id)}>在成果中心查看、审核与晋级</a> : null}</div>; })}</div> : artifacts.length === 0 ? <p className="mt-3 text-xs text-muted-foreground">当前项目没有被 Outcome Center 观察到成果；工作流节点产出会进入该正式中心。</p> : <div className="mt-3 space-y-2">{artifacts.map((artifact) => <div key={`${artifact.id}:${artifact.version}`} className="rounded border p-3"><div className="flex items-center justify-between gap-2 text-xs"><span className="font-medium">{artifact.title}</span><span className="text-muted-foreground">v{artifact.version} · {artifact.status}</span></div><div className="mt-1 text-[11px] text-muted-foreground">{artifact.id} · {artifact.locationCount ?? 0} 个物理位置</div></div>)}</div>}</section>;
}

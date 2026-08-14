"use client";

import { useMemo, useState } from "react";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import type {
  PublishedWorkflowDefinitionVersion,
  PublishedWorkflowGraph,
  WorkflowDefinition,
} from "@multica/core/api/workflow";
import { useCurrentWorkspace, useWorkspacePaths } from "@multica/core/paths";
import { projectListOptions } from "@multica/core/projects/queries";
import {
  workflowDefinitionListOptions,
  workflowInstanceEventListOptions,
  workflowInstanceListOptions,
  workflowKeys,
  workflowOperatingProgramListOptions,
} from "@multica/core/workflow";
import type {
  OperatingProgram,
  OperatingProject,
  WorkflowAgentBinding,
  WorkflowDefinitionDraft,
  WorkflowGraph,
} from "@multica/core/workflow";
import type { Project } from "@multica/core/types";
import {
  WorkflowOperationsPage,
  type WorkflowContextSelection,
  type WorkflowOperationsSection,
} from "@multica/views/workflow";
import { outcomeArtifactLocationsOptions, outcomesListOptions } from "@multica/views/outcomes";
import { scopeWorkflowOperations } from "./workflow-scope";
import { toWorkflowReceiptViews, toWorkflowRuntimeReadback } from "./workflow-runtime-readback";

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
 * Combines the two authoritative reads without manufacturing either layer:
 * native Project remains L4 authority; the workflow registry contributes only
 * the optional L3 membership relation. A stale relation to a no-longer-visible
 * Project is intentionally not rendered as a replacement Project.
 */
function toOperatingProjection(
  programs: Array<{ id: string; name: string; description: string; project_ids: string[] }>,
  projects: Project[],
) {
  const programIdByProjectId = new Map<string, string>();
  for (const program of programs) {
    for (const projectId of program.project_ids) programIdByProjectId.set(projectId, program.id);
  }
  const operatingProjects: OperatingProject[] = projects.map((project) => ({
    id: project.id,
    formalProjectId: project.id,
    programId: programIdByProjectId.get(project.id) ?? "",
    programClassification: programIdByProjectId.has(project.id) ? "assigned" : "unassigned",
    name: project.title,
    platform: "正式项目",
    status: projectStatus(project.status),
  }));
  const operatingPrograms: OperatingProgram[] = programs.map((program) => ({
    id: program.id,
    name: program.name,
    description: program.description || undefined,
    projectIds: program.project_ids,
  }));
  return { programs: operatingPrograms, projects: operatingProjects };
}

function toFrontendBinding(binding: PublishedWorkflowDefinitionVersion["graph"]["nodes"][number]["agent_binding"]): WorkflowAgentBinding | undefined {
  if (!binding) return undefined;
  switch (binding.mode) {
    case "fixed_employee": return { mode: "fixed_employee", employeeId: binding.employee_id ?? "" };
    case "role_pool": return { mode: "role_pool", role: binding.role ?? "", capability: binding.capability };
    case "project_default": return { mode: "project_default" };
    case "human": return { mode: "human" };
    case "capability_pool": return { mode: "role_pool", role: "能力池", capability: binding.capabilities?.join(", ") };
  }
}

function toDraft(version: PublishedWorkflowDefinitionVersion): WorkflowDefinitionDraft {
  return {
    id: version.definition_id,
    name: version.definition_id,
    version: version.version,
    projectId: version.project_id || undefined,
    graph: {
      nodes: version.graph.nodes.map((node) => ({
        id: node.id,
        type: node.kind,
        position: node.position ?? { x: 80, y: 80 },
        data: { label: node.name, binding: toFrontendBinding(node.agent_binding), risk: version.risk, evidenceRequired: true },
      })),
      edges: version.graph.edges.map((edge) => ({ id: edge.id, source: edge.from, target: edge.to, condition: edge.when })),
    },
  };
}

function toPublishedGraph(graph: WorkflowGraph): PublishedWorkflowGraph {
  return {
    nodes: graph.nodes.map((node) => ({
      id: node.id,
      kind: node.type,
      name: node.data.label,
      position: node.position,
      agent_binding: toPublishedBinding(node.type, node.data.binding),
    })),
    edges: graph.edges.map((edge) => ({ id: edge.id, from: edge.source, to: edge.target, when: edge.condition })),
  };
}

function toPublishedBinding(kind: WorkflowGraph["nodes"][number]["type"], binding?: WorkflowAgentBinding): PublishedWorkflowGraph["nodes"][number]["agent_binding"] {
  if (!binding || (kind !== "agent_task" && kind !== "human_task")) return undefined;
  switch (binding.mode) {
    case "fixed_employee": return { mode: "fixed_employee", employee_id: binding.employeeId };
    case "role_pool": return { mode: "role_pool", role: binding.role, capability: binding.capability };
    case "project_default": return { mode: "project_default" };
    case "human": return { mode: "human" };
  }
}

function linearGraphStageProjection(version: PublishedWorkflowDefinitionVersion): WorkflowDefinition["stages"] {
  // This is a read-only display adapter for the same linear V1 subset the
  // server compiler accepts. It neither persists a second stage definition nor
  // pretends a branching/conditional graph is executable.
  const nodes = new Map(version.graph.nodes.map((node) => [node.id, node]));
  const incoming = new Map<string, number>();
  const outgoing = new Map<string, string>();
  for (const node of version.graph.nodes) incoming.set(node.id, 0);
  for (const edge of version.graph.edges) {
    if (edge.when || !nodes.has(edge.from) || !nodes.has(edge.to) || outgoing.has(edge.from)) return [];
    incoming.set(edge.to, (incoming.get(edge.to) ?? 0) + 1);
    if ((incoming.get(edge.to) ?? 0) > 1) return [];
    outgoing.set(edge.from, edge.to);
  }
  const roots = [...nodes.keys()].filter((nodeId) => (incoming.get(nodeId) ?? 0) === 0);
  if (roots.length !== 1) return [];
  const result: WorkflowDefinition["stages"] = [];
  const seen = new Set<string>();
  let nodeId: string | undefined = roots[0];
  while (nodeId && !seen.has(nodeId)) {
    const node = nodes.get(nodeId);
    if (!node) return [];
    seen.add(nodeId);
    result.push({ name: node.name });
    nodeId = outgoing.get(nodeId);
  }
  return seen.size === nodes.size ? result : [];
}

function toLegacyDefinition(version: PublishedWorkflowDefinitionVersion): WorkflowDefinition {
  return {
    id: version.definition_id,
    version: version.version,
    risk: version.risk,
    stages: linearGraphStageProjection(version).length > 0 ? linearGraphStageProjection(version) : version.stages.map((stage) => ({
      name: stage.name,
      sla_seconds: stage.sla_ns ? Math.floor(stage.sla_ns / 1_000_000_000) : undefined,
    })),
  };
}

function candidateDraftForProject(project: OperatingProject, id: string): WorkflowDefinitionDraft {
  return {
    id,
    name: `${project.name} · 候选工作流`,
    version: 1,
    projectId: project.id,
    graph: { nodes: [], edges: [] },
  };
}

function newIdempotencyKey() {
  return globalThis.crypto?.randomUUID?.() ?? `workflow-${Date.now()}-${Math.random().toString(16).slice(2)}`;
}

export default function Page() {
  const workspace = useCurrentWorkspace();
  const workspacePaths = useWorkspacePaths();
  const router = useRouter();
  const pathname = usePathname();
  const searchParams = useSearchParams();
  const queryClient = useQueryClient();
  const [localDrafts, setLocalDrafts] = useState<WorkflowDefinitionDraft[]>([]);
  const [publishReceipt, setPublishReceipt] = useState<{ definitionId: string; version: number; changed: boolean } | null>(null);
  const [publishError, setPublishError] = useState<string | null>(null);
  const projectsQuery = useQuery({
    ...projectListOptions(workspace?.id ?? "workflow-workspace-unresolved"),
    enabled: Boolean(workspace?.id),
  });
  const instancesQuery = useQuery({
    ...workflowInstanceListOptions(workspace?.id ?? "workflow-workspace-unresolved"),
    enabled: Boolean(workspace?.id),
  });
  const definitionsQuery = useQuery({
    ...workflowDefinitionListOptions(workspace?.id ?? "workflow-workspace-unresolved"),
    enabled: Boolean(workspace?.id),
  });
  const operatingProgramsQuery = useQuery({
    ...workflowOperatingProgramListOptions(workspace?.id ?? "workflow-workspace-unresolved"),
    enabled: Boolean(workspace?.id),
  });

  const projection = useMemo(
    () => toOperatingProjection(operatingProgramsQuery.data ?? [], projectsQuery.data ?? []),
    [operatingProgramsQuery.data, projectsQuery.data],
  );
  const publishedDrafts = useMemo(() => (definitionsQuery.data ?? []).map(toDraft), [definitionsQuery.data]);
  const definitionDrafts = useMemo(() => [
    ...publishedDrafts.filter((draft) => !localDrafts.some((local) => local.id === draft.id)),
    ...localDrafts,
  ], [localDrafts, publishedDrafts]);
  const requestedProjectId = searchParams.get("project");
  const requestedProgramId = searchParams.get("program");
  const selectedProject = projection.projects.find((project) => project.id === requestedProjectId);
  const selection: WorkflowContextSelection | undefined = selectedProject
    ? { kind: "project", id: selectedProject.id }
    : projection.programs.some((program) => program.id === requestedProgramId)
      ? { kind: "program", id: requestedProgramId! }
      : undefined;
  const requestedSection = searchParams.get("section");
  const section: WorkflowOperationsSection = isSection(requestedSection) ? requestedSection : "overview";
  const selectedDefinitionId = searchParams.get("workflow") ?? undefined;
  const scopedOperations = useMemo(
    () => scopeWorkflowOperations(selection, projection.projects, definitionsQuery.data ?? [], instancesQuery.data ?? []),
    [definitionsQuery.data, instancesQuery.data, projection.projects, selection],
  );
  const instanceEventQueries = useQueries({
    queries: scopedOperations.instances.map((instance) => ({
      ...workflowInstanceEventListOptions(workspace?.id ?? "workflow-workspace-unresolved", instance.id),
      enabled: Boolean(workspace?.id && selectedProject),
    })),
  });
  const durableReceipts = useMemo(
    () => instanceEventQueries.flatMap((query) => toWorkflowReceiptViews(query.data ?? [])),
    [instanceEventQueries],
  );
  const runtimes = useMemo(
    () => scopedOperations.instances.flatMap((instance) => {
      const definition = scopedOperations.definitions.find((item) => item.definition_id === instance.definition_id && item.version === instance.definition_version);
      const runtime = toWorkflowRuntimeReadback(instance, definition);
      return runtime ? [runtime] : [];
    }),
    [scopedOperations.definitions, scopedOperations.instances],
  );
  const requestedInstanceId = searchParams.get("instance");
  const selectedInstance = requestedInstanceId
    ? scopedOperations.instances.find((instance) => instance.id === requestedInstanceId)
    : scopedOperations.instances[0];
  const selectedRuntime = selectedInstance ? runtimes.find((runtime) => runtime.instanceId === selectedInstance.id) : undefined;
  const selectedRuntimeDefinition = selectedRuntime
    ? definitionDrafts.find((definition) => definition.id === selectedRuntime.definitionId && definition.version === selectedRuntime.version)
    : undefined;
  const outcomesQuery = useQuery({
    ...outcomesListOptions(workspace?.id ?? "workflow-workspace-unresolved", {
      project_id: selectedProject?.formalProjectId,
      limit: 30,
    }),
    enabled: Boolean(workspace?.id && selectedProject),
  });
  const artifactLocationQueries = useQueries({
    queries: (outcomesQuery.data?.items ?? []).map((outcome) => outcomeArtifactLocationsOptions(
      workspace?.id ?? "workflow-workspace-unresolved",
      outcome.id,
    )),
  });
  const artifactLocationsByOutcome = useMemo(() => Object.fromEntries(
    (outcomesQuery.data?.items ?? []).map((outcome, index) => {
      const locationQuery = artifactLocationQueries[index];
      return [outcome.id, {
        items: locationQuery?.data?.items ?? [],
        loading: locationQuery?.isLoading,
        error: locationQuery?.isError,
      }];
    }),
  ), [artifactLocationQueries, outcomesQuery.data?.items]);

  const createOperatingProgramMutation = useMutation({
    mutationFn: async (input: { name: string; description?: string }) => api.createWorkflowOperatingProgram({
      ...input,
      idempotency_key: newIdempotencyKey(),
    }),
    onSuccess: async () => {
      if (workspace?.id) await queryClient.invalidateQueries({ queryKey: workflowKeys.operatingPrograms(workspace.id) });
    },
  });
  const updateOperatingProgramMutation = useMutation({
    mutationFn: async (input: { programId: string; name: string; description?: string }) => api.updateWorkflowOperatingProgram(input.programId, {
      name: input.name,
      description: input.description,
    }),
    onSuccess: async () => {
      if (workspace?.id) await queryClient.invalidateQueries({ queryKey: workflowKeys.operatingPrograms(workspace.id) });
    },
  });
  const deleteOperatingProgramMutation = useMutation({
    mutationFn: async (programId: string) => api.deleteWorkflowOperatingProgram(programId),
    onSuccess: async () => {
      if (workspace?.id) await queryClient.invalidateQueries({ queryKey: workflowKeys.operatingPrograms(workspace.id) });
    },
  });
  const assignOperatingProgramProjectMutation = useMutation({
    mutationFn: async (input: { programId: string; projectId: string }) => api.assignWorkflowOperatingProgramProject(input.programId, input.projectId),
    onSuccess: async () => {
      if (workspace?.id) await queryClient.invalidateQueries({ queryKey: workflowKeys.operatingPrograms(workspace.id) });
    },
  });
  const unassignOperatingProgramProjectMutation = useMutation({
    mutationFn: async (input: { programId: string; projectId: string }) => api.unassignWorkflowOperatingProgramProject(input.programId, input.projectId),
    onSuccess: async () => {
      if (workspace?.id) await queryClient.invalidateQueries({ queryKey: workflowKeys.operatingPrograms(workspace.id) });
    },
  });

  const startPublishedGraphMutation = useMutation({
    mutationFn: async (definition: WorkflowDefinition) => {
      if (!selectedProject) throw new Error("请先选择 L4 正式 Project 后再启动工作流");
      const version = scopedOperations.definitions.find((item) => item.definition_id === definition.id && item.version === definition.version);
      if (!version) throw new Error("已发布工作流版本未在当前 Project 范围内回读");
      return api.startPublishedWorkflowGraphInstance(version.definition_id, version.version, {
        context: { project_id: selectedProject.formalProjectId },
        idempotency_key: newIdempotencyKey(),
      });
    },
    onSuccess: (instance) => {
      if (workspace?.id) {
        void queryClient.invalidateQueries({ queryKey: workflowKeys.instances(workspace.id) });
        void queryClient.invalidateQueries({ queryKey: workflowKeys.instanceEvents(workspace.id, instance.id) });
      }
    },
  });

  const publishMutation = useMutation({
    mutationFn: async (draft: WorkflowDefinitionDraft) => api.publishWorkflowDefinitionVersion(draft.id, {
      project_id: draft.projectId,
      risk: "standard",
      // The immutable graph is the source. The server compiles only the
      // guarded linear V1 subset at start time; branching remains fail-closed.
      stages: [],
      graph: toPublishedGraph(draft.graph),
      idempotency_key: newIdempotencyKey(),
    }),
    onSuccess: (result, draft) => {
      setPublishError(null);
      setPublishReceipt({ definitionId: result.version.definition_id, version: result.version.version, changed: result.receipt.changed });
      setLocalDrafts((current) => current.filter((item) => item.id !== draft.id));
      if (workspace?.id) void queryClient.invalidateQueries({ queryKey: workflowKeys.definitions(workspace.id) });
    },
    onError: (error) => setPublishError(error instanceof Error ? error.message : "候选版本未被服务端接受"),
  });

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
      updateSearch({ project: next.id, program: project?.programId ?? null, workflow: null });
      return;
    }
    updateSearch({ program: next.id, project: null, workflow: null });
  };

  if (!workspace) {
    return <WorkspaceSourceState title="工作区来源不可用" detail="正在读取当前工作区；不会用名称或本地缓存猜测工作流所属范围。" />;
  }
  if (projectsQuery.isLoading || instancesQuery.isLoading || definitionsQuery.isLoading || operatingProgramsQuery.isLoading) {
    return <WorkspaceSourceState title="正在读取正式项目、运营科目与工作流记录" detail="L4 项目、L3 运营科目、工作流实例和已发布版本来自各自的权威读模型。" />;
  }
  if (projectsQuery.isError || instancesQuery.isError || definitionsQuery.isError || operatingProgramsQuery.isError) {
    return <WorkspaceSourceState title="来源暂不可用" detail="工作流页面没有把读取失败伪装成空项目、零实例或临时运营科目；请恢复来源后重试。" />;
  }
  if (requestedProjectId && !selectedProject) {
    return <WorkspaceSourceState title="项目未在当前工作区被观察到" detail="深链中的 project ID 不属于当前正式项目列表；系统没有自动替换为第一条项目。" />;
  }
  if (requestedInstanceId && !selectedInstance) {
    return <WorkspaceSourceState title="运行实例未在当前项目范围被观察到" detail="深链中的 instance ID 不属于当前 L4 项目的已持久化工作流实例；系统没有替换为另一条实例。" />;
  }

  return (
    <WorkflowOperationsPage
      programs={projection.programs}
      projects={projection.projects}
      definitionDrafts={definitionDrafts}
      runtime={selectedRuntime}
      runtimeGraph={selectedRuntimeDefinition?.graph}
      definitions={scopedOperations.definitions.map(toLegacyDefinition)}
      instances={scopedOperations.instances}
      outcomes={outcomesQuery.data?.items ?? []}
      outcomesLoading={outcomesQuery.isLoading}
      outcomesError={outcomesQuery.isError}
      outcomeHref={(outcomeId) => `${workspacePaths.outcomes()}?outcome=${encodeURIComponent(outcomeId)}`}
      artifactLocationsByOutcome={artifactLocationsByOutcome}
      selection={selection}
      section={section}
      selectedDefinitionId={selectedDefinitionId}
      onSelectContext={selectContext}
      onSelectSection={(next) => updateSearch({ section: next })}
      onSelectDefinition={(definitionId) => updateSearch({ workflow: definitionId, section: "workflow" })}
      onCreateDefinition={(project) => {
        const id = `candidate.workflow.${project.formalProjectId}.${newIdempotencyKey()}`;
        setPublishReceipt(null);
        setPublishError(null);
        setLocalDrafts((current) => [...current, candidateDraftForProject(project, id)]);
        updateSearch({ workflow: id, section: "workflow" });
      }}
      onChangeDefinition={(next) => {
        setPublishReceipt(null);
        setPublishError(null);
        setLocalDrafts((current) => [...current.filter((draft) => draft.id !== next.id), next]);
      }}
      onPublishDefinition={(draft) => publishMutation.mutate(draft)}
      publishReceipt={publishReceipt}
      publishError={publishError}
      programManagement={{
        onCreateProgram: async (input) => {
          const result = await createOperatingProgramMutation.mutateAsync(input);
          updateSearch({ program: result.program.id, project: null, workflow: null, section: "settings" });
        },
        programCreationState: createOperatingProgramMutation.isPending ? "loading" : createOperatingProgramMutation.isError ? "error" : "ready",
        programCreationError: createOperatingProgramMutation.error instanceof Error ? createOperatingProgramMutation.error.message : undefined,
        onUpdateProgram: async (programId, input) => {
          await updateOperatingProgramMutation.mutateAsync({ programId, ...input });
        },
        programUpdateState: updateOperatingProgramMutation.isPending ? "loading" : updateOperatingProgramMutation.isError ? "error" : "ready",
        programUpdateError: updateOperatingProgramMutation.error instanceof Error ? updateOperatingProgramMutation.error.message : undefined,
        onDeleteProgram: async (programId) => {
          await deleteOperatingProgramMutation.mutateAsync(programId);
          updateSearch({ program: null, project: null, workflow: null, section: "overview" });
        },
        programDeletionState: deleteOperatingProgramMutation.isPending ? "loading" : deleteOperatingProgramMutation.isError ? "error" : "ready",
        programDeletionError: deleteOperatingProgramMutation.error instanceof Error ? deleteOperatingProgramMutation.error.message : undefined,
        onAssignProject: async (programId, projectId) => {
          await assignOperatingProgramProjectMutation.mutateAsync({ programId, projectId });
        },
        onUnassignProject: async (programId, projectId) => {
          await unassignOperatingProgramProjectMutation.mutateAsync({ programId, projectId });
        },
        projectMutationState: ({ programId, projectId, mutation }) => {
          const active = mutation === "assign" ? assignOperatingProgramProjectMutation : unassignOperatingProgramProjectMutation;
          return active.isPending && active.variables?.programId === programId && active.variables.projectId === projectId
            ? "loading"
            : "ready";
        },
        projectMutationError: ({ programId, projectId, mutation }) => {
          const active = mutation === "assign" ? assignOperatingProgramProjectMutation : unassignOperatingProgramProjectMutation;
          return active.isError && active.variables?.programId === programId && active.variables.projectId === projectId && active.error instanceof Error
            ? active.error.message
            : undefined;
        },
      }}
      workbench={{
        runtimes,
        runtimesState: "ready",
        selectedInstanceId: selectedInstance?.id,
        onSelectInstance: (instanceId) => updateSearch({ instance: instanceId, section: "instances" }),
        receipts: durableReceipts,
        receiptsState: instanceEventQueries.some((query) => query.isError) ? "error" : instanceEventQueries.some((query) => query.isLoading) ? "loading" : "ready",
        receiptsError: instanceEventQueries.find((query) => query.isError)?.error instanceof Error ? instanceEventQueries.find((query) => query.isError)?.error.message : undefined,
        onRetryReceipts: () => {
          void Promise.all(instanceEventQueries.map((query) => query.refetch()));
        },
        onCreateInstance: selectedProject ? async (definition) => {
          await startPublishedGraphMutation.mutateAsync(definition);
        } : undefined,
        instanceCreationState: startPublishedGraphMutation.isPending ? "loading" : startPublishedGraphMutation.isError ? "error" : selectedProject ? "ready" : "unavailable",
        instanceCreationError: startPublishedGraphMutation.error instanceof Error ? startPublishedGraphMutation.error.message : undefined,
      }}
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

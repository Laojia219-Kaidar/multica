export type WorkflowScopeSelection =
  | { kind: "project"; id: string }
  | { kind: "program"; id: string }
  | undefined;

type ScopeProject = {
  id: string;
  formalProjectId: string;
  programId: string;
};

type ScopedDefinition = {
  project_id?: string;
};

type ScopedInstance = {
  context: {
    project_id?: string;
  };
};

/**
 * Scopes workspace-wide workflow projections to the selected formal Project
 * or its explicit OperatingProgram projection. Definitions and instances with
 * no formal Project reference are intentionally invisible in this operations
 * surface rather than being guessed into a project.
 */
export function scopeWorkflowOperations<TDefinition extends ScopedDefinition, TInstance extends ScopedInstance>(
  selection: WorkflowScopeSelection,
  projects: ScopeProject[],
  definitions: TDefinition[],
  instances: TInstance[],
): { definitions: TDefinition[]; instances: TInstance[] } {
  const projectIds = selection?.kind === "project"
    ? projects.filter((project) => project.id === selection.id).map((project) => project.formalProjectId)
    : selection?.kind === "program"
      ? projects.filter((project) => project.programId === selection.id).map((project) => project.formalProjectId)
      : [];
  const permitted = new Set(projectIds);
  return {
    definitions: definitions.filter((definition) => permitted.has(definition.project_id ?? "")),
    instances: instances.filter((instance) => permitted.has(instance.context.project_id ?? "")),
  };
}

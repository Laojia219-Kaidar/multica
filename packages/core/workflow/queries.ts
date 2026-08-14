import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

// Workflow instances are HiveCrew-owned orchestration records. The workspace
// ID stays in the key so switching workspaces cannot reuse a prior workspace's
// read projection.
export const workflowKeys = {
  all: (workspaceId: string) => ["workflow", workspaceId] as const,
  instances: (workspaceId: string) => [...workflowKeys.all(workspaceId), "instances"] as const,
  definitions: (workspaceId: string) => [...workflowKeys.all(workspaceId), "definitions"] as const,
};

export function workflowInstanceListOptions(workspaceId: string) {
  return queryOptions({
    queryKey: workflowKeys.instances(workspaceId),
    queryFn: () => api.listWorkflowInstances(),
    refetchInterval: 5_000,
    placeholderData: (previous) => previous,
  });
}

export function workflowDefinitionListOptions(workspaceId: string) {
  return queryOptions({
    queryKey: workflowKeys.definitions(workspaceId),
    queryFn: () => api.listPublishedWorkflowDefinitionVersions(),
    refetchInterval: 10_000,
    placeholderData: (previous) => previous,
  });
}

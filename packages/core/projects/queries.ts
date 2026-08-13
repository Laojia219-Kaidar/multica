import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";
import type { ProjectPipelineResponse } from "./pipeline-types";

export const projectKeys = {
  all: (wsId: string) => ["projects", wsId] as const,
  list: (wsId: string) => [...projectKeys.all(wsId), "list"] as const,
  detail: (wsId: string, id: string) =>
    [...projectKeys.all(wsId), "detail", id] as const,
  /** HIV-367 (P0-E): pipeline projection prefix — WS-reconnect invalidates here. */
  pipelineAll: (wsId: string) => [...projectKeys.all(wsId), "pipeline"] as const,
  pipeline: (wsId: string, projectId: string) =>
    [...projectKeys.pipelineAll(wsId), projectId] as const,
};

export function projectListOptions(wsId: string) {
  return queryOptions({
    queryKey: projectKeys.list(wsId),
    queryFn: () => api.listProjects(),
    select: (data) => data.projects,
  });
}

export function projectDetailOptions(wsId: string, id: string) {
  return queryOptions({
    queryKey: projectKeys.detail(wsId, id),
    queryFn: () => api.getProject(id),
  });
}

/**
 * HIV-367 (P0-E): pipeline projection for a single project. Polls every 5s
 * (contract §8 fallback); the WS invalidation hook also invalidates on task:*
 * events so a busy board converges instantly. Key includes workspace + project.
 */
export function projectPipelineOptions(wsId: string, projectId: string) {
  return queryOptions<ProjectPipelineResponse>({
    queryKey: projectKeys.pipeline(wsId, projectId),
    queryFn: () => api.getProjectPipeline(projectId),
    refetchInterval: 5_000,
    placeholderData: (prev) => prev,
  });
}

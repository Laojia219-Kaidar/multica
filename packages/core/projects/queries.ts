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
 * HIV-367 (P0-E): pipeline projection for a single project. Per acceptance §8
 * ("WebSocket OR 5 second refresh"), this query polls every 5 seconds; the WS
 * invalidation hook (useRealtimeSync) ALSO invalidates on `task:*` events so
 * a quiet board still converges to truth within 5 s and a busy one updates
 * instantly. The query key includes workspace + project so workspaces never
 * collide and so cache-coordinated cross-issue invalidation reaches it.
 */
export function projectPipelineOptions(wsId: string, projectId: string) {
  return queryOptions<ProjectPipelineResponse>({
    queryKey: projectKeys.pipeline(wsId, projectId),
    queryFn: () => api.getProjectPipeline(projectId),
    // 5s polling — contract §8 fallback for surfaces without live WS delivery.
    refetchInterval: 5_000,
    // Keep the previous projection while refetching so column totals don't
    // flash empty during the 5s window.
    placeholderData: (prev) => prev,
  });
}

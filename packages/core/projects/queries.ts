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
  lifecycle: (wsId: string) => [...projectKeys.all(wsId), "lifecycle"] as const,
  lifecycleDetail: (wsId: string, id: string) =>
    [...projectKeys.all(wsId), "lifecycle", "detail", id] as const,
  workConserving: (wsId: string, id: string) =>
    [...projectKeys.detail(wsId, id), "work-conserving"] as const,
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

export function projectLifecycleListOptions(wsId: string) {
  return queryOptions({
    queryKey: projectKeys.lifecycle(wsId),
    queryFn: () => api.listProjectLifecycle(),
    select: (data) => data.projects,
  });
}

export function projectLifecycleDetailOptions(wsId: string, id: string) {
  return queryOptions({
    queryKey: projectKeys.lifecycleDetail(wsId, id),
    queryFn: () => api.getProjectLifecycle(id),
  });
}

/**
 * HIV-941 (P1): read-only work-conserving projection for a single project.
 *
 * The scheduler can settle outcomes (automatic assignment, retry lease
 * expiry, queue reordering) without emitting a Task lifecycle event or a
 * WebSocket message observed by this client. A bounded 15-second fallback
 * poll guarantees those silent outcomes still converge in the UI without
 * forcing a reload. Immediate invalidation is provided separately by the
 * Task lifecycle WebSocket hook; this poll never dispatches, drains, or
 * mutates anything — the underlying query function is GET-only.
 *
 * Key includes workspace + project so two projects never share cache, and
 * the interval is scoped to this projection only (it does not touch the
 * 5s pipeline poll or any other query).
 */
export function projectWorkConservingOptions(wsId: string, id: string) {
  return queryOptions({
    queryKey: projectKeys.workConserving(wsId, id),
    queryFn: () => api.getProjectWorkConservingProjection(id),
    refetchInterval: 15_000,
  });
}

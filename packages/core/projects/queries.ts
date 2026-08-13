import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const projectKeys = {
  all: (wsId: string) => ["projects", wsId] as const,
  list: (wsId: string) => [...projectKeys.all(wsId), "list"] as const,
  detail: (wsId: string, id: string) =>
    [...projectKeys.all(wsId), "detail", id] as const,
  lifecycle: (wsId: string) => [...projectKeys.all(wsId), "lifecycle"] as const,
  lifecycleDetail: (wsId: string, id: string) =>
    [...projectKeys.all(wsId), "lifecycle", "detail", id] as const,
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

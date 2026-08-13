import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const baseKeys = {
  all: (wsId: string) => ["runtimes", wsId, "bases"] as const,
  list: (wsId: string) => [...baseKeys.all(wsId), "list"] as const,
};

export function baseListOptions(wsId: string) {
  return queryOptions({
    queryKey: baseKeys.list(wsId),
    queryFn: () => api.listRuntimeBases(),
    staleTime: 15 * 1000,
  });
}

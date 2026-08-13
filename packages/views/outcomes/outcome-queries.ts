import { queryOptions } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import type { CompanyOpsOutcomeListParams } from "@multica/core/types";

export const outcomeKeys = {
  all: (wsId: string) => ["company-ops", wsId, "outcomes"] as const,
  list: (wsId: string, params: CompanyOpsOutcomeListParams) =>
    [
      ...outcomeKeys.all(wsId),
      "list",
      params.q ?? "",
      params.status ?? "",
      params.limit ?? "",
      params.offset ?? "",
      params.cursor ?? "",
    ] as const,
  detail: (wsId: string, commandId: string) =>
    [...outcomeKeys.all(wsId), "detail", commandId] as const,
};

export function outcomesListOptions(
  wsId: string,
  params: CompanyOpsOutcomeListParams = {},
) {
  return queryOptions({
    queryKey: outcomeKeys.list(wsId, params),
    queryFn: () => api.listCompanyOpsOutcomes(params),
    enabled: !!wsId,
    staleTime: 30_000,
  });
}

export function outcomeDetailOptions(wsId: string, commandId: string) {
  return queryOptions({
    queryKey: outcomeKeys.detail(wsId, commandId),
    queryFn: () => api.getCompanyOpsOutcome(commandId),
    enabled: !!wsId && !!commandId,
    retry: false,
    staleTime: 30_000,
  });
}
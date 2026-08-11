import { queryOptions } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import type { CompanyOpsRosterParams } from "@multica/core/types";

export const organizationKeys = {
  all: (wsId: string) => ["company-ops", wsId, "organization"] as const,
  tree: (wsId: string) => [...organizationKeys.all(wsId), "tree"] as const,
  roster: (wsId: string, params: CompanyOpsRosterParams) =>
    [
      ...organizationKeys.all(wsId),
      "roster",
      params.q ?? "",
      params.status ?? "",
      params.limit ?? "",
      params.offset ?? "",
    ] as const,
  dossier: (wsId: string, employeeId: string) =>
    [...organizationKeys.all(wsId), "employees", "detail", employeeId] as const,
};

export function organizationTreeOptions(wsId: string) {
  return queryOptions({
    queryKey: organizationKeys.tree(wsId),
    queryFn: () => api.getCompanyOpsOrganization(),
    enabled: !!wsId,
    staleTime: 30_000,
  });
}

export function rosterListOptions(
  wsId: string,
  params: CompanyOpsRosterParams = {},
) {
  return queryOptions({
    queryKey: organizationKeys.roster(wsId, params),
    queryFn: () => api.listCompanyOpsEmployees(params),
    enabled: !!wsId,
    staleTime: 30_000,
  });
}

export function employeeDossierOptions(wsId: string, employeeId: string) {
  return queryOptions({
    queryKey: organizationKeys.dossier(wsId, employeeId),
    queryFn: () => api.getCompanyOpsEmployee(employeeId),
    enabled: !!wsId && !!employeeId,
    retry: false,
    staleTime: 30_000,
  });
}
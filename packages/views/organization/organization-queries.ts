import { queryOptions } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import type { CompanyOpsRosterParams } from "@multica/core/types";

/**
 * Organization/employees queries are bound to the exact workspace slug the
 * request is sent with (`X-Workspace-Slug` on the wire). The query key embeds
 * BOTH the workspace id and the slug so the cache fingerprint can never
 * diverge from the request fingerprint (a slug-identity race would otherwise
 * cache workspace B's response under workspace A's key).
 *
 * The authority projection is Cache-Control: no-store and stale must fail
 * closed, so staleTime is 0 — every mount/focus refetches and the cache never
 * serves a stale projection as fresh.
 */
export const organizationKeys = {
  all: (wsId: string, slug: string) =>
    ["company-ops", wsId, slug, "organization"] as const,
  tree: (wsId: string, slug: string) =>
    [...organizationKeys.all(wsId, slug), "tree"] as const,
  roster: (wsId: string, slug: string, params: CompanyOpsRosterParams) =>
    [
      ...organizationKeys.all(wsId, slug),
      "roster",
      params.q ?? "",
      params.status ?? "",
      params.limit ?? "",
      params.offset ?? "",
    ] as const,
  dossier: (wsId: string, slug: string, employeeId: string) =>
    [...organizationKeys.all(wsId, slug), "employees", "detail", employeeId] as const,
};

export function organizationTreeOptions(wsId: string, slug: string) {
  return queryOptions({
    queryKey: organizationKeys.tree(wsId, slug),
    queryFn: () => api.getCompanyOpsOrganization(slug),
    enabled: !!wsId && !!slug,
    staleTime: 0,
  });
}

export function rosterListOptions(
  wsId: string,
  slug: string,
  params: CompanyOpsRosterParams = {},
) {
  return queryOptions({
    queryKey: organizationKeys.roster(wsId, slug, params),
    queryFn: () => api.listCompanyOpsEmployees(slug, params),
    enabled: !!wsId && !!slug,
    staleTime: 0,
  });
}

export function employeeDossierOptions(
  wsId: string,
  slug: string,
  employeeId: string,
) {
  return queryOptions({
    queryKey: organizationKeys.dossier(wsId, slug, employeeId),
    queryFn: () => api.getCompanyOpsEmployee(slug, employeeId),
    enabled: !!wsId && !!slug && !!employeeId,
    retry: false,
    staleTime: 0,
  });
}
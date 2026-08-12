import { describe, expect, it, vi } from "vitest";
import { api } from "@multica/core/api";
import {
  employeeDossierOptions,
  organizationKeys,
  organizationTreeOptions,
  rosterListOptions,
} from "./organization-queries";

vi.mock("@multica/core/api", () => ({
  api: {
    getCompanyOpsOrganization: vi.fn(),
    listCompanyOpsEmployees: vi.fn(),
    getCompanyOpsEmployee: vi.fn(),
  },
}));

// Frontend↔Consumer contract: the query key must embed the exact workspace
// slug the request is bound to (X-Workspace-Slug), so a Workspace B response
// can never be cached under Workspace A's key; and, because the authority wire
// is no-store/stale-fail-closed, staleTime must be 0 so the cache never serves
// a stale projection as fresh.
describe("organization query contract", () => {
  it("keys embed wsId AND the request-bound slug", () => {
    expect(organizationKeys.tree("ws-1", "acme")).toEqual([
      "company-ops",
      "ws-1",
      "acme",
      "organization",
      "tree",
    ]);
    expect(organizationKeys.roster("ws-1", "acme", { q: "x", status: "available" })).toEqual(
      expect.arrayContaining(["company-ops", "ws-1", "acme", "organization", "roster", "x", "available"]),
    );
    expect(organizationKeys.dossier("ws-1", "acme", "DE-CEO-001")).toEqual([
      "company-ops",
      "ws-1",
      "acme",
      "organization",
      "employees",
      "detail",
      "DE-CEO-001",
    ]);
    // Same employee under a different workspace slug must be a different key.
    expect(organizationKeys.dossier("ws-1", "acme", "DE-CEO-001")).not.toEqual(
      organizationKeys.dossier("ws-1", "other", "DE-CEO-001"),
    );
  });

  it("forwards the slug to the workspace-bound API calls", () => {
    const opts = organizationTreeOptions("ws-1", "acme");
    (opts.queryFn as () => unknown)();
    expect(api.getCompanyOpsOrganization).toHaveBeenCalledWith("acme");

    (rosterListOptions("ws-1", "acme", { q: "coco" }).queryFn as () => unknown)();
    expect(api.listCompanyOpsEmployees).toHaveBeenCalledWith("acme", {
      q: "coco",
    });

    (employeeDossierOptions("ws-1", "acme", "DE-CEO-001").queryFn as () => unknown)();
    expect(api.getCompanyOpsEmployee).toHaveBeenCalledWith("acme", "DE-CEO-001");
  });

  it("never marks the projection as stale-able (no-store semantics)", () => {
    expect(organizationTreeOptions("ws-1", "acme").staleTime).toBe(0);
    expect(rosterListOptions("ws-1", "acme").staleTime).toBe(0);
    expect(employeeDossierOptions("ws-1", "acme", "DE-CEO-001").staleTime).toBe(0);
  });

  it("disables the dossier query until both workspace and employee id exist", () => {
    expect(employeeDossierOptions("", "acme", "DE-CEO-001").enabled).toBe(false);
    expect(employeeDossierOptions("ws-1", "", "DE-CEO-001").enabled).toBe(false);
    expect(employeeDossierOptions("ws-1", "acme", "").enabled).toBe(false);
    expect(employeeDossierOptions("ws-1", "acme", "DE-CEO-001").enabled).toBe(true);
  });
});

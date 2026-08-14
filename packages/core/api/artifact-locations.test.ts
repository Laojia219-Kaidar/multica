import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient } from "./client";

afterEach(() => vi.unstubAllGlobals());

describe("CompanyOps artifact replica locations", () => {
  it("reads the workspace-scoped location observation contract", async () => {
    const outcomeID = "a4d7525e-98ba-4aa2-8dc4-bf49c6bf5ed9";
    const response = {
      schema_version: "hivecrew.artifact-replica-locations.v1",
      workspace_id: "11111111-1111-4111-8111-111111111111",
      outcome_id: outcomeID,
      items: [],
    };
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify(response), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(new ApiClient("https://api.example.test").listCompanyOpsArtifactReplicaLocations(outcomeID)).resolves.toEqual(response);
    expect(fetchMock).toHaveBeenCalledWith(`https://api.example.test/api/company-ops/outcomes/${outcomeID}/artifact-locations`, expect.anything());
  });

  it("fails closed when the ledger response is not canonical", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ schema_version: "hivecrew.artifact-replica-locations.v0", items: [] }), { status: 200 })));
    await expect(new ApiClient("https://api.example.test").listCompanyOpsArtifactReplicaLocations("a4d7525e-98ba-4aa2-8dc4-bf49c6bf5ed9")).rejects.toThrow("Invalid CompanyOps artifact replica locations response.");
  });
});

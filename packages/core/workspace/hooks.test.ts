import { describe, expect, it } from "vitest";
import { buildActorNameResolver } from "./hooks";

describe("buildActorNameResolver", () => {
  it("uses HiveCrew for system-authored activity", () => {
    const resolveName = buildActorNameResolver({
      members: [],
      agents: [],
      squads: [],
    });

    expect(resolveName("system", "system")).toBe("HiveCrew");
  });
});

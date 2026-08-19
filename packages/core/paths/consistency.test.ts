import { describe, it, expect } from "vitest";
import { paths, isGlobalPath } from "./paths";
import { RESERVED_SLUGS } from "./reserved-slugs";

// C4 — link-handler's WORKSPACE_ROUTE_SEGMENTS must match paths.workspace's
// parameterless method names. We can't import WORKSPACE_ROUTE_SEGMENTS here
// because link-handler is in packages/views (no inverse import allowed), so
// we hardcode the expected list and assert paths.workspace produces the same
// keys. If you change either, BOTH need to be updated — the test catches drift.
describe("paths.workspace() shape", () => {
  it("exposes the expected parameterless workspace route methods", () => {
    const ws = paths.workspace("__probe__");
    const parameterlessRoutes = Object.entries(ws)
      .filter(([, fn]) => typeof fn === "function" && fn.length === 0)
      .map(([key]) => key);

    expect(new Set(parameterlessRoutes)).toEqual(
      new Set([
        "root",
        "command",
        "usage",
        "issues",
        "projects",
        "workflow",
        "outcomes",
        "organization",
        "autopilots",
        "agents",
        "newAgent",
        "workrooms",
        "employees",
        "datasets",
        "chat",
        "squads",
        "bases",
        "inbox",
        "myIssues",
        "runtimes",
        "runtimeBases",
        "workWall",
        "skills",
        "settings",
      ]),
    );
  });

  it("each parameterless route emits /{slug}/{segment}", () => {
    const ws = paths.workspace("acme");
    // Check that none of the parameterless paths embed a leaked literal and
    // that each method emits its explicitly registered workspace subpath.
    const expectedSegments: Array<[string, string]> = [
      ["command", "command"],
      ["usage", "usage"],
      ["issues", "issues"],
      ["projects", "projects"],
      ["workflow", "workflow"],
      ["outcomes", "outcomes"],
      ["organization", "organization"],
      ["autopilots", "autopilots"],
      ["agents", "agents"],
      ["newAgent", "agents/new"],
      ["workrooms", "workrooms"],
      ["employees", "employees"],
      ["datasets", "datasets"],
      ["chat", "chat"],
      ["squads", "squads"],
      ["bases", "bases"],
      ["inbox", "inbox"],
      ["myIssues", "my-issues"],
      ["runtimes", "runtimes"],
      ["runtimeBases", "runtimes/bases"],
      ["workWall", "work-wall"],
      ["skills", "skills"],
      ["settings", "settings"],
    ];
    const wsAsAny = ws as unknown as Record<string, () => string>;
    for (const [method, segment] of expectedSegments) {
      const fn = wsAsAny[method];
      expect(typeof fn).toBe("function");
      expect(fn!()).toBe(`/acme/${segment}`);
    }
  });
});

// C5 — invariants between the global/reserved lists.
describe("global path / reserved slug consistency", () => {
  // If a path is "global" (never workspace-scoped), the slug name underlying it
  // must be reserved — otherwise a user could create a workspace with that slug
  // and shadow the global route's URL space.
  //
  // GLOBAL_PREFIXES from paths.ts is private — we re-derive the list from
  // probing isGlobalPath. Order matters: keep this list in sync with paths.ts.
  const globalPrefixes = [
    "/login",
    "/logout",
    "/signup",
    "/workspaces/",
    "/invite/",
    "/auth/",
  ];

  it("isGlobalPath agrees with the canonical global prefix list", () => {
    for (const prefix of globalPrefixes) {
      expect(isGlobalPath(prefix)).toBe(true);
    }
    expect(isGlobalPath("/acme/issues")).toBe(false);
    expect(isGlobalPath("/")).toBe(false);
  });

  it("every global prefix's first path segment is a reserved slug", () => {
    for (const prefix of globalPrefixes) {
      const firstSegment = prefix.split("/").filter(Boolean)[0];
      if (!firstSegment) continue;
      expect(
        RESERVED_SLUGS.has(firstSegment),
        `'${firstSegment}' is a global path prefix but not a reserved slug — ` +
          `a workspace could be created with this slug and shadow the global route`,
      ).toBe(true);
    }
  });
});

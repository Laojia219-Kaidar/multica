import { describe, expect, it } from "vitest";
import type { Agent, Squad, SquadMember } from "@multica/core/types";
import type { SquadDirectoryEntry } from "@multica/core/workspace/queries";
import type { AgentListRow } from "./agents-page";
import { buildAgentCardGroups } from "./agent-card-directory";

const BASE_AGENT: Agent = {
  id: "agent-base",
  workspace_id: "workspace-1",
  runtime_id: "runtime-1",
  name: "Base employee",
  description: "",
  instructions: "",
  avatar_url: null,
  runtime_mode: "cloud",
  runtime_config: {},
  custom_args: [],
  visibility: "workspace",
  permission_mode: "private",
  invocation_targets: [],
  status: "idle",
  max_concurrent_tasks: 1,
  model: "test-model",
  owner_id: "owner-1",
  skills: [],
  created_at: "2026-08-01T00:00:00Z",
  updated_at: "2026-08-01T00:00:00Z",
  archived_at: null,
  archived_by: null,
};

function row(id: string, name: string): AgentListRow {
  return {
    agent: { ...BASE_AGENT, id, name },
    runtime: null,
    presence: null,
    activity: null,
    runCount: 0,
    lastActiveDays: null,
    owner: null,
    isOwnedByMe: true,
    canManage: true,
  };
}

function squad(id: string, name: string): Squad {
  return {
    id,
    workspace_id: "workspace-1",
    name,
    description: "",
    instructions: "",
    avatar_url: null,
    leader_id: "leader-1",
    creator_id: "owner-1",
    created_at: "2026-08-01T00:00:00Z",
    updated_at: "2026-08-01T00:00:00Z",
    archived_at: null,
    archived_by: null,
  };
}

function member(
  squadId: string,
  agentId: string,
  role = "Full-stack engineer",
): SquadMember {
  return {
    id: `${squadId}-${agentId}-${role}`,
    squad_id: squadId,
    member_type: "agent",
    member_id: agentId,
    role,
    created_at: "2026-08-01T00:00:00Z",
  };
}

describe("buildAgentCardGroups", () => {
  it("groups employees by exact Agent UUID and keeps the operational role", () => {
    const alpha = row("agent-alpha", "Alpha");
    const beta = row("agent-beta", "Beta");
    const directory: SquadDirectoryEntry[] = [
      {
        squad: squad("engineering", "Engineering"),
        members: [member("engineering", alpha.agent.id, "Backend engineer")],
      },
      {
        squad: squad("operations", "Operations"),
        members: [member("operations", beta.agent.id, "Operations engineer")],
      },
    ];

    const groups = buildAgentCardGroups([alpha, beta], [alpha, beta], directory);

    expect(groups.map((group) => group.squad?.name)).toEqual([
      "Engineering",
      "Operations",
    ]);
    expect(groups[0]?.members[0]?.row.agent.id).toBe("agent-alpha");
    expect(groups[0]?.members[0]?.role).toBe("Backend engineer");
  });

  it("shows an employee once in the conflict group when two departments claim the same UUID", () => {
    const alpha = row("agent-alpha", "Alpha");
    const directory: SquadDirectoryEntry[] = [
      {
        squad: squad("engineering", "Engineering"),
        members: [member("engineering", alpha.agent.id)],
      },
      {
        squad: squad("operations", "Operations"),
        members: [member("operations", alpha.agent.id)],
      },
    ];

    const groups = buildAgentCardGroups([alpha], [alpha], directory);

    expect(groups).toHaveLength(1);
    expect(groups[0]?.kind).toBe("conflict");
    expect(groups[0]?.members.map((entry) => entry.row.agent.id)).toEqual([
      "agent-alpha",
    ]);
  });

  it("does not mistake duplicate membership rows in one department for a conflict", () => {
    const alpha = row("agent-alpha", "Alpha");
    const directory: SquadDirectoryEntry[] = [
      {
        squad: squad("engineering", "Engineering"),
        members: [
          member("engineering", alpha.agent.id, "Engineer"),
          member("engineering", alpha.agent.id, "Engineer"),
        ],
      },
    ];

    const groups = buildAgentCardGroups([alpha], [alpha], directory);

    expect(groups).toHaveLength(1);
    expect(groups[0]?.kind).toBe("department");
    expect(groups[0]?.members).toHaveLength(1);
  });

  it("keeps employees without a membership visible in the pending group", () => {
    const alpha = row("agent-alpha", "Alpha");

    const groups = buildAgentCardGroups([alpha], [alpha], []);

    expect(groups).toHaveLength(1);
    expect(groups[0]?.kind).toBe("unassigned");
    expect(groups[0]?.members[0]?.row.agent.id).toBe("agent-alpha");
  });

  it("uses all scoped rows for department totals while filters narrow visible cards", () => {
    const alpha = row("agent-alpha", "Alpha");
    const beta = row("agent-beta", "Beta");
    const directory: SquadDirectoryEntry[] = [
      {
        squad: squad("engineering", "Engineering"),
        members: [
          member("engineering", alpha.agent.id),
          member("engineering", beta.agent.id),
        ],
      },
    ];

    const groups = buildAgentCardGroups([alpha], [alpha, beta], directory);

    expect(groups[0]?.members).toHaveLength(1);
    expect(groups[0]?.totalCount).toBe(2);
  });
});

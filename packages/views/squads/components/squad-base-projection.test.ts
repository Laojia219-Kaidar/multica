import { describe, expect, it } from "vitest";
import type {
  Agent,
  AgentRuntime,
  SquadMember,
  SquadMemberStatus,
} from "@multica/core/types";
import {
  buildSquadBaseProjection,
  observedBaseLabel,
} from "./squad-base-projection";

const baseAgent: Agent = {
  id: "agent-1",
  workspace_id: "ws-1",
  runtime_id: "runtime-1",
  name: "Ada",
  description: "",
  instructions: "",
  avatar_url: null,
  runtime_mode: "local",
  runtime_config: {},
  custom_args: [],
  visibility: "workspace",
  permission_mode: "public_to",
  invocation_targets: [{ target_type: "workspace", target_id: null }],
  status: "idle",
  max_concurrent_tasks: 1,
  model: "glm-5",
  owner_id: "user-1",
  skills: [],
  created_at: "2026-08-13T00:00:00Z",
  updated_at: "2026-08-13T00:00:00Z",
  archived_at: null,
  archived_by: null,
};

const baseRuntime: AgentRuntime = {
  id: "runtime-1",
  workspace_id: "ws-1",
  daemon_id: "daemon-mac-mini",
  name: "Codex (HiveCosm Mac mini)",
  custom_name: "Codex 主控",
  runtime_mode: "local",
  provider: "codex",
  launch_header: "",
  status: "online",
  device_info: "HiveCosm Mac mini · macOS (arm64)",
  metadata: {},
  owner_id: "user-1",
  visibility: "private",
  profile_id: null,
  last_seen_at: "2026-08-13T00:00:00Z",
  created_at: "2026-08-13T00:00:00Z",
  updated_at: "2026-08-13T00:00:00Z",
};

function member(id: string, agentId: string): SquadMember {
  return {
    id,
    squad_id: "squad-1",
    member_type: "agent",
    member_id: agentId,
    role: "engineer",
    created_at: "2026-08-13T00:00:00Z",
  };
}

function status(
  agentId: string,
  value: SquadMemberStatus["status"],
): SquadMemberStatus {
  return {
    member_type: "agent",
    member_id: agentId,
    status: value,
    active_issues: [],
    last_active_at: null,
  };
}

describe("observedBaseLabel", () => {
  it("uses only the daemon-published machine segment", () => {
    expect(observedBaseLabel(baseRuntime)).toBe("HiveCosm Mac mini");
    expect(observedBaseLabel({ ...baseRuntime, device_info: "" })).toBeNull();
  });
});

describe("buildSquadBaseProjection", () => {
  it("groups agents by their current daemon without turning human members into runtimes", () => {
    const agent2 = {
      ...baseAgent,
      id: "agent-2",
      name: "Grace",
      runtime_id: "runtime-2",
    };
    const runtime2 = {
      ...baseRuntime,
      id: "runtime-2",
      name: "Qwen Code (HiveCosm Mac mini)",
      custom_name: null,
      provider: "qwen",
    };
    const humanMember: SquadMember = {
      id: "member-human",
      squad_id: "squad-1",
      member_type: "member",
      member_id: "user-2",
      role: "owner",
      created_at: "2026-08-13T00:00:00Z",
    };

    const result = buildSquadBaseProjection({
      members: [member("member-1", "agent-1"), member("member-2", "agent-2"), humanMember],
      agents: [baseAgent, agent2],
      runtimes: [baseRuntime, runtime2],
      memberStatusById: new Map([
        ["agent-1", status("agent-1", "working")],
        ["agent-2", status("agent-2", "idle")],
      ]),
    });

    expect(result.agents).toHaveLength(2);
    expect(result.groups).toHaveLength(1);
    expect(result.groups[0]?.label).toBe("HiveCosm Mac mini");
    expect(result.groups[0]?.agents.map((entry) => entry.agent.name)).toEqual([
      "Ada",
      "Grace",
    ]);
    expect(result.workingCount).toBe(1);
    expect(result.attentionCount).toBe(0);
  });

  it("fails closed when a runtime is absent or offline", () => {
    const missingAgent = {
      ...baseAgent,
      id: "agent-missing",
      name: "Missing",
      runtime_id: "runtime-missing",
    };
    const offlineAgent = {
      ...baseAgent,
      id: "agent-offline",
      name: "Offline",
      runtime_id: "runtime-offline",
    };
    const offlineRuntime = {
      ...baseRuntime,
      id: "runtime-offline",
      daemon_id: "daemon-laptop",
      status: "offline" as const,
      device_info: "HiveCrew MBP M5X · macOS (arm64)",
    };

    const result = buildSquadBaseProjection({
      members: [
        member("member-missing", "agent-missing"),
        member("member-offline", "agent-offline"),
      ],
      agents: [missingAgent, offlineAgent],
      runtimes: [offlineRuntime],
      memberStatusById: new Map(),
    });

    expect(result.groups).toHaveLength(2);
    expect(result.attentionCount).toBe(2);
    expect(result.agents.every((entry) => entry.needsAttention)).toBe(true);
    expect(result.agents.find((entry) => entry.agent.id === "agent-missing")?.baseLabel).toBeNull();
  });

  it("uses the safe shared runtime display label instead of the raw daemon name", () => {
    const result = buildSquadBaseProjection({
      members: [member("member-1", "agent-1")],
      agents: [baseAgent],
      runtimes: [baseRuntime],
      memberStatusById: new Map(),
    });

    expect(result.agents[0]?.runtimeLabel).toBe("Codex 主控 (Codex)");
  });
});

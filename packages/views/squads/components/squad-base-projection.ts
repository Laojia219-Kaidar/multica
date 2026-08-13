import { runtimeDisplayLabel } from "@multica/core/runtimes";
import type {
  Agent,
  AgentRuntime,
  SquadMember,
  SquadMemberStatus,
} from "@multica/core/types";

export interface SquadBaseAgentProjection {
  member: SquadMember;
  agent: Agent;
  runtime: AgentRuntime | null;
  runtimeLabel: string | null;
  baseKey: string;
  baseLabel: string | null;
  status: SquadMemberStatus | null;
  needsAttention: boolean;
}

export interface SquadBaseGroup {
  key: string;
  label: string | null;
  agents: SquadBaseAgentProjection[];
  onlineCount: number;
  attentionCount: number;
}

export interface SquadBaseProjection {
  agents: SquadBaseAgentProjection[];
  groups: SquadBaseGroup[];
  workingCount: number;
  attentionCount: number;
}

/**
 * Extracts the machine label that the runtime daemon already publishes in
 * device_info ("machine title · operating system"). This is an observed
 * execution location, not a company-owned home-base assignment.
 */
export function observedBaseLabel(runtime: AgentRuntime): string | null {
  const machine = runtime.device_info.split(" · ")[0]?.trim();
  return machine || null;
}

export function buildSquadBaseProjection({
  members,
  agents,
  runtimes,
  memberStatusById,
}: {
  members: SquadMember[];
  agents: Agent[];
  runtimes: AgentRuntime[];
  memberStatusById: Map<string, SquadMemberStatus>;
}): SquadBaseProjection {
  const agentById = new Map(agents.map((agent) => [agent.id, agent]));
  const runtimeById = new Map(runtimes.map((runtime) => [runtime.id, runtime]));

  const projectedAgents = members.flatMap<SquadBaseAgentProjection>((member) => {
    if (member.member_type !== "agent") return [];
    const agent = agentById.get(member.member_id);
    if (!agent) return [];

    const runtime = runtimeById.get(agent.runtime_id) ?? null;
    const baseLabel = runtime ? observedBaseLabel(runtime) : null;
    const status = memberStatusById.get(member.member_id) ?? null;
    const needsAttention =
      runtime === null ||
      runtime.status !== "online" ||
      status?.status === "offline" ||
      status?.status === "unstable";

    return [
      {
        member,
        agent,
        runtime,
        runtimeLabel: runtime ? runtimeDisplayLabel(runtime) : null,
        baseKey: runtime?.daemon_id ?? runtime?.id ?? "unavailable",
        baseLabel,
        status,
        needsAttention,
      },
    ];
  });

  const grouped = new Map<string, SquadBaseGroup>();
  for (const projection of projectedAgents) {
    const existing = grouped.get(projection.baseKey);
    if (existing) {
      existing.agents.push(projection);
      if (projection.runtime?.status === "online") existing.onlineCount += 1;
      if (projection.needsAttention) existing.attentionCount += 1;
      continue;
    }
    grouped.set(projection.baseKey, {
      key: projection.baseKey,
      label: projection.baseLabel,
      agents: [projection],
      onlineCount: projection.runtime?.status === "online" ? 1 : 0,
      attentionCount: projection.needsAttention ? 1 : 0,
    });
  }

  const groups = [...grouped.values()]
    .map((group) => ({
      ...group,
      agents: [...group.agents].sort((a, b) =>
        a.agent.name.localeCompare(b.agent.name),
      ),
    }))
    .sort((a, b) => {
      if (a.label === null && b.label !== null) return 1;
      if (a.label !== null && b.label === null) return -1;
      return (a.label ?? "").localeCompare(b.label ?? "");
    });

  return {
    agents: projectedAgents,
    groups,
    workingCount: projectedAgents.filter(
      (projection) => projection.status?.status === "working",
    ).length,
    attentionCount: projectedAgents.filter(
      (projection) => projection.needsAttention,
    ).length,
  };
}

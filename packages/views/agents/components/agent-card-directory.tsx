"use client";

import { AlertTriangle, Building2, UsersRound } from "lucide-react";
import type { Squad } from "@multica/core/types";
import type { SquadDirectoryEntry } from "@multica/core/workspace/queries";
import { runtimeDisplayLabel } from "@multica/core/runtimes";
import { useWorkspacePaths } from "@multica/core/paths";
import { useRowLink } from "../../navigation";
import { ActorAvatar } from "../../common/actor-avatar";
import { useT } from "../../i18n";
import { availabilityConfig } from "../presence";
import { AgentRowActions } from "./agent-row-actions";
import type { AgentListRow } from "./agents-page";

export type AgentCardGroupKind = "department" | "unassigned" | "conflict";

export interface AgentCardMember {
  row: AgentListRow;
  role: string | null;
}

export interface AgentCardGroup {
  key: string;
  kind: AgentCardGroupKind;
  squad: Squad | null;
  members: AgentCardMember[];
  totalCount: number;
}

interface MembershipProjection {
  squadIds: Set<string>;
  roles: Map<string, string>;
}

/**
 * Projects Agents into their exact Squad membership without inventing a
 * second organization model. Agent UUID is the only join key. Duplicate
 * rows inside one Squad are collapsed; multiple distinct Squad memberships
 * are surfaced as a conflict and the Agent is rendered once.
 */
export function buildAgentCardGroups(
  visibleRows: AgentListRow[],
  allRows: AgentListRow[],
  directory: SquadDirectoryEntry[],
): AgentCardGroup[] {
  const membershipByAgent = new Map<string, MembershipProjection>();

  for (const { squad, members } of directory) {
    for (const member of members) {
      if (member.member_type !== "agent") continue;
      const projection = membershipByAgent.get(member.member_id) ?? {
        squadIds: new Set<string>(),
        roles: new Map<string, string>(),
      };
      projection.squadIds.add(squad.id);
      if (member.role.trim()) projection.roles.set(squad.id, member.role.trim());
      membershipByAgent.set(member.member_id, projection);
    }
  }

  const classify = (row: AgentListRow) => {
    const projection = membershipByAgent.get(row.agent.id);
    const squadIds = projection ? [...projection.squadIds] : [];
    if (squadIds.length === 0) {
      return { key: "unassigned", kind: "unassigned" as const, role: null };
    }
    if (squadIds.length > 1) {
      return { key: "conflict", kind: "conflict" as const, role: null };
    }
    const squadId = squadIds[0]!;
    return {
      key: squadId,
      kind: "department" as const,
      role: projection?.roles.get(squadId) ?? null,
    };
  };

  const totals = new Map<string, number>();
  for (const row of allRows) {
    const { key } = classify(row);
    totals.set(key, (totals.get(key) ?? 0) + 1);
  }

  const visible = new Map<string, AgentCardMember[]>();
  for (const row of visibleRows) {
    const { key, role } = classify(row);
    const members = visible.get(key) ?? [];
    members.push({ row, role });
    visible.set(key, members);
  }

  const groups: AgentCardGroup[] = [];
  for (const { squad } of directory) {
    const members = visible.get(squad.id);
    if (!members?.length) continue;
    groups.push({
      key: squad.id,
      kind: "department",
      squad,
      members,
      totalCount: totals.get(squad.id) ?? members.length,
    });
  }

  for (const kind of ["conflict", "unassigned"] as const) {
    const members = visible.get(kind);
    if (!members?.length) continue;
    groups.push({
      key: kind,
      kind,
      squad: null,
      members,
      totalCount: totals.get(kind) ?? members.length,
    });
  }

  return groups.filter((group) => group.members.length > 0);
}

function EmployeeCard({
  member,
  onDuplicate,
}: {
  member: AgentCardMember;
  onDuplicate: (agent: AgentListRow["agent"]) => void;
}) {
  const { t } = useT("agents");
  const paths = useWorkspacePaths();
  const rowLink = useRowLink();
  const { row, role } = member;
  const { agent, presence, runtime } = row;
  const visual = presence ? availabilityConfig[presence.availability] : null;

  return (
    <article
      className="group/row relative flex min-h-52 cursor-pointer flex-col rounded-xl border bg-card p-4 shadow-xs transition-colors hover:border-primary/35 hover:bg-accent/20"
      data-agent-card={agent.id}
      {...rowLink(paths.agentDetail(agent.id))}
    >
      <div className="flex items-start gap-3">
        <ActorAvatar
          actorType="agent"
          actorId={agent.id}
          size="2xl"
          profileLink={false}
          className={agent.archived_at ? "grayscale" : undefined}
        />
        <div className="min-w-0 flex-1 pt-0.5">
          <h3 className="truncate text-sm font-semibold">{agent.name}</h3>
          <p className="mt-1 truncate text-xs text-muted-foreground">
            {role || agent.description || t(($) => $.directory.role_unassigned)}
          </p>
        </div>
        <span
          className="-mr-1 -mt-1 flex shrink-0"
          onClick={(event) => event.stopPropagation()}
          onAuxClick={(event) => event.stopPropagation()}
        >
          <AgentRowActions
            agent={agent}
            presence={presence}
            canManage={row.canManage}
            onDuplicate={onDuplicate}
          />
        </span>
      </div>

      <div className="mt-4 grid grid-cols-2 gap-x-3 gap-y-3 text-xs">
        <div className="min-w-0">
          <p className="text-muted-foreground">{t(($) => $.columns.status)}</p>
          <div className="mt-1 flex items-center gap-1.5">
            {visual ? (
              <span className={`size-1.5 shrink-0 rounded-full ${visual.dotClass}`} />
            ) : null}
            <span className="truncate font-medium">
              {agent.archived_at
                ? t(($) => $.availability.archived)
                : presence
                  ? t(($) => $.availability[presence.availability])
                  : "—"}
            </span>
          </div>
        </div>
        <div className="min-w-0">
          <p className="text-muted-foreground">{t(($) => $.directory.model)}</p>
          <p className="mt-1 truncate font-medium">
            {agent.model || t(($) => $.directory.no_model)}
          </p>
        </div>
        <div className="col-span-2 min-w-0">
          <p className="text-muted-foreground">{t(($) => $.directory.runtime)}</p>
          <p className="mt-1 truncate font-medium">
            {runtime
              ? runtimeDisplayLabel(runtime)
              : t(($) => $.directory.no_runtime)}
          </p>
        </div>
      </div>

      <div className="mt-auto border-t pt-3 text-xs text-muted-foreground">
        {t(($) => $.directory.runs, { count: row.runCount })}
      </div>
    </article>
  );
}

export function AgentCardDirectory({
  visibleRows,
  allRows,
  directory,
  onDuplicate,
}: {
  visibleRows: AgentListRow[];
  allRows: AgentListRow[];
  directory: SquadDirectoryEntry[];
  onDuplicate: (agent: AgentListRow["agent"]) => void;
}) {
  const { t } = useT("agents");
  const groups = buildAgentCardGroups(visibleRows, allRows, directory);
  const allAgentsById = new Map(allRows.map((row) => [row.agent.id, row.agent]));

  return (
    <div className="space-y-8 px-5 pb-8 pt-3">
      {groups.map((group) => {
        const leader = group.squad
          ? allAgentsById.get(group.squad.leader_id)
          : null;
        const Icon = group.kind === "department" ? Building2 : group.kind === "conflict" ? AlertTriangle : UsersRound;
        const title = group.squad?.name ??
          (group.kind === "conflict"
            ? t(($) => $.directory.conflict)
            : t(($) => $.directory.unassigned));
        const description = group.squad?.description ||
          (group.kind === "conflict"
            ? t(($) => $.directory.conflict_description)
            : group.kind === "unassigned"
              ? t(($) => $.directory.unassigned_description)
              : null);

        return (
          <section key={group.key} aria-labelledby={`agent-group-${group.key}`}>
            <div className="mb-3 flex flex-wrap items-end justify-between gap-2 border-b pb-3">
              <div className="min-w-0">
                <div className="flex items-center gap-2">
                  <Icon className="size-4 shrink-0 text-muted-foreground" />
                  <h2 id={`agent-group-${group.key}`} className="truncate text-base font-semibold">
                    {title}
                  </h2>
                  <span className="rounded-full bg-muted px-2 py-0.5 text-xs tabular-nums text-muted-foreground">
                    {group.members.length === group.totalCount
                      ? t(($) => $.directory.employees, { count: group.totalCount })
                      : t(($) => $.directory.visible_of_total, {
                          visible: group.members.length,
                          total: group.totalCount,
                        })}
                  </span>
                </div>
                {description ? (
                  <p className="mt-1 max-w-3xl truncate text-xs text-muted-foreground">
                    {description}
                  </p>
                ) : null}
              </div>
              {leader ? (
                <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
                  <span>{t(($) => $.directory.leader)}</span>
                  <ActorAvatar
                    actorType="agent"
                    actorId={leader.id}
                    size="sm"
                    profileLink={false}
                  />
                  <span className="font-medium text-foreground">{leader.name}</span>
                </div>
              ) : null}
            </div>

            <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4">
              {group.members.map((member) => (
                <EmployeeCard
                  key={member.row.agent.id}
                  member={member}
                  onDuplicate={onDuplicate}
                />
              ))}
            </div>
          </section>
        );
      })}
    </div>
  );
}

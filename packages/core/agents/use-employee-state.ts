"use client";

import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { agentListOptions } from "../workspace/queries";
import { runtimeListOptions } from "../runtimes/queries";
import { agentTaskSnapshotOptions } from "./queries";
import {
  buildEmployeeStateMap,
  deriveEmployeeStateExplanation,
  type EmployeeStateExplanation,
} from "./derive-employee-state";

// Same 30s tick as use-agent-presence.ts: availability buckets decay with
// wall-clock time (recently_lost → offline at 5 min), so the explanation
// must re-derive without waiting for new server data.
const EMPLOYEE_STATE_TICK_MS = 30_000;

function useEmployeeStateTick(): number {
  const [tick, setTick] = useState(0);
  useEffect(() => {
    const id = setInterval(() => setTick((t) => t + 1), EMPLOYEE_STATE_TICK_MS);
    return () => clearInterval(id);
  }, []);
  return tick;
}

/**
 * Workspace-wide employee status explanations keyed by `agent.id` — the
 * batch entry point for list surfaces. Reads the same three queries as
 * useWorkspacePresenceMap (react-query dedupes the subscriptions); rows
 * just `Map.get(id)`.
 */
export function useWorkspaceEmployeeStateMap(wsId: string | undefined): {
  byAgent: Map<string, EmployeeStateExplanation>;
  loading: boolean;
} {
  const { data: agents, isPending: agentsPending, isError: agentsErr } = useQuery({
    ...agentListOptions(wsId ?? ""),
    enabled: !!wsId,
  });
  const { data: runtimes, isPending: runtimesPending, isError: runtimesErr } = useQuery({
    ...runtimeListOptions(wsId ?? ""),
    enabled: !!wsId,
  });
  const { data: snapshot, isPending: snapshotPending, isError: snapshotErr } = useQuery({
    ...agentTaskSnapshotOptions(wsId ?? ""),
    enabled: !!wsId,
  });
  const tick = useEmployeeStateTick();

  const byAgent = useMemo(() => {
    // Errored queries degrade to empty — a snapshot 404 must not blank the
    // whole surface (same rule as useWorkspacePresenceMap).
    const safeAgents = agents ?? (agentsErr ? [] : null);
    const safeRuntimes = runtimes ?? (runtimesErr ? [] : null);
    const safeSnapshot = snapshot ?? (snapshotErr ? [] : null);
    if (!safeAgents || !safeRuntimes || !safeSnapshot) {
      return new Map<string, EmployeeStateExplanation>();
    }
    return buildEmployeeStateMap({
      agents: safeAgents,
      runtimes: safeRuntimes,
      snapshot: safeSnapshot,
      now: Date.now(),
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [agents, runtimes, snapshot, agentsErr, runtimesErr, snapshotErr, tick]);

  return {
    byAgent,
    loading:
      (agentsPending && !agentsErr) ||
      (runtimesPending && !runtimesErr) ||
      (snapshotPending && !snapshotErr),
  };
}

// Workspace queued backlog for the single-agent path: derived from the same
// snapshot, so an idle employee still gets the honest "backlog waiting"
// explanation on detail surfaces.
function countWorkspaceBacklog(snapshot: readonly { status: string }[]): number {
  let count = 0;
  for (const t of snapshot) {
    if (
      t.status === "queued" ||
      t.status === "dispatched" ||
      t.status === "waiting_local_directory"
    ) {
      count += 1;
    }
  }
  return count;
}

/**
 * Single-agent explanation for detail surfaces. Returns "loading" only
 * until the underlying queries settle (success OR error); a missing runtime
 * is a real state (unavailable), not loading.
 */
export function useEmployeeStateExplanation(
  wsId: string | undefined,
  agentId: string | undefined,
): EmployeeStateExplanation | "loading" {
  const { data: agents, isError: agentsErr } = useQuery({
    ...agentListOptions(wsId ?? ""),
    enabled: !!wsId,
  });
  const { data: runtimes, isError: runtimesErr } = useQuery({
    ...runtimeListOptions(wsId ?? ""),
    enabled: !!wsId,
  });
  const { data: snapshot, isError: snapshotErr } = useQuery({
    ...agentTaskSnapshotOptions(wsId ?? ""),
    enabled: !!wsId,
  });
  const tick = useEmployeeStateTick();

  return useMemo<EmployeeStateExplanation | "loading">(() => {
    if (!wsId || !agentId) return "loading";
    const safeAgents = agents ?? (agentsErr ? [] : null);
    const safeRuntimes = runtimes ?? (runtimesErr ? [] : null);
    const safeSnapshot = snapshot ?? (snapshotErr ? [] : null);
    if (!safeAgents || !safeRuntimes || !safeSnapshot) return "loading";

    const agent = safeAgents.find((a) => a.id === agentId);
    if (!agent) return "loading";
    const runtime = safeRuntimes.find((r) => r.id === agent.runtime_id) ?? null;
    const tasks = safeSnapshot.filter((t) => t.agent_id === agentId);
    return deriveEmployeeStateExplanation({
      agent,
      runtime,
      tasks,
      now: Date.now(),
      workspaceBacklogCount: countWorkspaceBacklog(safeSnapshot),
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [wsId, agentId, agents, runtimes, snapshot, agentsErr, runtimesErr, snapshotErr, tick]);
}

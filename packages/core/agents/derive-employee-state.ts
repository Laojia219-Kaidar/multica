// Employee status explanation: folds the two presence dimensions
// (availability × workload, see derive-presence.ts) plus runtime health into
// the operator-facing four-state vocabulary — working / idle / waiting /
// unavailable — each with the exact reason, the current task/run on record,
// capacity and runtime-health staleness, and the next recovery action.
//
// No status truth is invented client-side: every field is derived from
// server facts (agent row, runtime row + last_seen_at, workspace task
// snapshot) through the same derivations the presence dot already uses.
// `reason` / `nextAction` are stable codes; human copy lives in the views
// layer (same pattern as failure-reason.ts → task-failure.ts), so a code
// the copy map doesn't know degrades to the raw code, never a crash.

import { deriveRuntimeHealth } from "../runtimes/derive-health";
import type { RuntimeHealth } from "../runtimes/types";
import type { Agent, AgentRuntime, AgentTask } from "../types";
import { deriveAgentPresenceDetail } from "./derive-presence";
import type { AgentAvailability, Workload } from "./types";

export type EmployeeStatus = "working" | "idle" | "waiting" | "unavailable";

export type EmployeeStateReason =
  // Lifecycle — archived wins over every other signal (same precedence as
  // deriveAgentPresenceDetail).
  | "agent_archived"
  // Runtime-reachability reasons (status = unavailable).
  | "runtime_missing" // no runtime row at all
  | "runtime_recently_lost" // offline < 5 min — likely transient
  | "runtime_offline" // offline beyond the grace window
  | "runtime_about_to_gc" // offline > 6 days — sweeper rescue window
  // Workload reasons.
  | "running_tasks" // ≥1 task running
  | "waiting_local_directory" // parked on a busy local_directory path lock
  | "dispatched_awaiting_start" // daemon acked, run not started yet
  | "awaiting_dispatch" // queued, not yet claimed
  | "idle_ready" // nothing on the plate
  | "idle_backlog_waiting"; // free capacity while the workspace queue holds work

export type EmployeeNextAction =
  | "none" // healthy — nothing to do
  | "unarchive" // restore the agent from archive
  | "restore_runtime" // check / restart the runtime daemon
  | "monitor_runtime" // recently lost — usually recovers within minutes
  | "await_path_lock" // resolves when the occupying run releases the path
  | "await_daemon_start" // dispatched — daemon will start the run
  | "await_dispatch" // queued — dispatcher will pick it up
  | "assign_work"; // idle with free capacity and workspace backlog

// The current task/run on record for this employee: the earliest-started
// running task when working, otherwise the earliest-created queued task
// when waiting. null when unavailable (a server-side "running" row on an
// unreachable runtime is stale, not current) or idle.
export interface EmployeeCurrentTask {
  id: string;
  issueId: string;
  status: AgentTask["status"];
  createdAt: string;
  dispatchedAt: string | null;
  startedAt: string | null;
}

export interface EmployeeStateExplanation {
  status: EmployeeStatus;
  reason: EmployeeStateReason;
  nextAction: EmployeeNextAction;
  // The two underlying presence dimensions, kept for consumers that already
  // speak that vocabulary (dots, filters).
  availability: AgentAvailability;
  workload: Workload;
  runningCount: number;
  queuedCount: number;
  // agent.max_concurrent_tasks when it is a positive finite number, else
  // null = quota unknown. Consumers must render null as "unknown"
  // fail-closed — never guess a number (same rule as capability flags).
  capacity: number | null;
  currentTask: EmployeeCurrentTask | null;
  runtimeHealth: RuntimeHealth | "missing";
  runtimeLastSeenAt: string | null;
  // Wall-clock millis since the runtime's last_seen_at; null when no
  // timestamp exists. Threaded through so the UI can show staleness
  // without re-parsing.
  runtimeStalenessMs: number | null;
  // Queued tasks across the whole workspace snapshot — the backlog an idle
  // employee could pick up.
  workspaceBacklogCount: number;
}

interface DeriveEmployeeStateInput {
  agent: Agent;
  runtime: AgentRuntime | null;
  // Tasks for THIS agent only (pre-filtered by the caller, same contract
  // as deriveAgentPresenceDetail).
  tasks: readonly AgentTask[];
  now: number;
  // Workspace-wide queued backlog; defaults to 0 (unknown = not claimed).
  workspaceBacklogCount?: number;
}

function isQueuedStatus(status: AgentTask["status"]): boolean {
  return (
    status === "queued" ||
    status === "dispatched" ||
    status === "waiting_local_directory"
  );
}

function toCurrentTask(task: AgentTask): EmployeeCurrentTask {
  return {
    id: task.id,
    issueId: task.issue_id,
    status: task.status,
    createdAt: task.created_at,
    dispatchedAt: task.dispatched_at,
    startedAt: task.started_at,
  };
}

// Earliest-started running task; a null started_at sorts last within the
// running set (claimed but not yet reporting a start).
function pickRunningTask(tasks: readonly AgentTask[]): AgentTask | null {
  let best: AgentTask | null = null;
  for (const t of tasks) {
    if (t.status !== "running") continue;
    if (!best) {
      best = t;
      continue;
    }
    const tStart = t.started_at ? Date.parse(t.started_at) : Number.POSITIVE_INFINITY;
    const bestStart = best.started_at
      ? Date.parse(best.started_at)
      : Number.POSITIVE_INFINITY;
    if (tStart < bestStart) best = t;
  }
  return best;
}

function pickQueuedTask(tasks: readonly AgentTask[]): AgentTask | null {
  let best: AgentTask | null = null;
  for (const t of tasks) {
    if (!isQueuedStatus(t.status)) continue;
    if (!best || Date.parse(t.created_at) < Date.parse(best.created_at)) {
      best = t;
    }
  }
  return best;
}

export function deriveEmployeeStateExplanation(
  input: DeriveEmployeeStateInput,
): EmployeeStateExplanation {
  const { agent, runtime, tasks, now } = input;
  const presence = deriveAgentPresenceDetail({ agent, runtime, tasks, now });
  const backlog = input.workspaceBacklogCount ?? 0;
  const capacity =
    Number.isFinite(agent.max_concurrent_tasks) && agent.max_concurrent_tasks > 0
      ? agent.max_concurrent_tasks
      : null;
  const runtimeHealth: RuntimeHealth | "missing" = runtime
    ? deriveRuntimeHealth(runtime, now)
    : "missing";
  const runtimeLastSeenAt = runtime?.last_seen_at ?? null;
  const parsedLastSeen = runtimeLastSeenAt
    ? Date.parse(runtimeLastSeenAt)
    : Number.NaN;
  const runtimeStalenessMs = Number.isFinite(parsedLastSeen)
    ? Math.max(0, now - parsedLastSeen)
    : null;

  const base = {
    availability: presence.availability,
    workload: presence.workload,
    runningCount: presence.runningCount,
    queuedCount: presence.queuedCount,
    capacity,
    runtimeHealth,
    runtimeLastSeenAt,
    runtimeStalenessMs,
    workspaceBacklogCount: backlog,
  };

  if (agent.archived_at) {
    return {
      ...base,
      status: "unavailable",
      reason: "agent_archived",
      nextAction: "unarchive",
      currentTask: null,
    };
  }

  // Availability precedes workload: an employee whose runtime is unreachable
  // cannot take work, so the honest status is unavailable even when stale
  // task rows claim otherwise. currentTask is nulled — a "running" row on a
  // dead runtime is a stuck record, not current work.
  if (presence.availability === "offline" || presence.availability === "unstable") {
    const reason: EmployeeStateReason = !runtime
      ? "runtime_missing"
      : runtimeHealth === "recently_lost"
        ? "runtime_recently_lost"
        : runtimeHealth === "about_to_gc"
          ? "runtime_about_to_gc"
          : "runtime_offline";
    return {
      ...base,
      status: "unavailable",
      reason,
      nextAction:
        runtimeHealth === "recently_lost" ? "monitor_runtime" : "restore_runtime",
      currentTask: null,
    };
  }

  if (presence.workload === "working") {
    const running = pickRunningTask(tasks);
    return {
      ...base,
      status: "working",
      reason: "running_tasks",
      nextAction: "none",
      currentTask: running ? toCurrentTask(running) : null,
    };
  }

  if (presence.workload === "queued") {
    const queued = pickQueuedTask(tasks);
    const reason: EmployeeStateReason = tasks.some(
      (t) => t.status === "waiting_local_directory",
    )
      ? "waiting_local_directory"
      : tasks.some((t) => t.status === "dispatched")
        ? "dispatched_awaiting_start"
        : "awaiting_dispatch";
    const nextAction: EmployeeNextAction =
      reason === "waiting_local_directory"
        ? "await_path_lock"
        : reason === "dispatched_awaiting_start"
          ? "await_daemon_start"
          : "await_dispatch";
    return {
      ...base,
      status: "waiting",
      reason,
      nextAction,
      currentTask: queued ? toCurrentTask(queued) : null,
    };
  }

  // Idle: healthy and free. The backlog distinction tells the operator
  // whether this employee could be taking work right now.
  const hasBacklog = backlog > 0;
  return {
    ...base,
    status: "idle",
    reason: hasBacklog ? "idle_backlog_waiting" : "idle_ready",
    nextAction: hasBacklog ? "assign_work" : "none",
    currentTask: null,
  };
}

// Workspace-level batch builder mirroring buildPresenceMap: one pass over
// agents + snapshot, O(1) per-agent derivation, plus the workspace queued
// backlog total that feeds the idle-with-backlog explanation.
export function buildEmployeeStateMap(args: {
  agents: readonly Agent[];
  runtimes: readonly AgentRuntime[];
  // Straight from getAgentTaskSnapshot() — active tasks plus each agent's
  // most recent terminal task; terminal rows are ignored by the workload
  // derivation.
  snapshot: readonly AgentTask[];
  now: number;
}): Map<string, EmployeeStateExplanation> {
  const out = new Map<string, EmployeeStateExplanation>();
  const runtimesById = new Map<string, AgentRuntime>();
  for (const r of args.runtimes) runtimesById.set(r.id, r);

  const tasksByAgent = new Map<string, AgentTask[]>();
  let workspaceBacklogCount = 0;
  for (const t of args.snapshot) {
    const list = tasksByAgent.get(t.agent_id);
    if (list) list.push(t);
    else tasksByAgent.set(t.agent_id, [t]);
    if (isQueuedStatus(t.status)) workspaceBacklogCount += 1;
  }

  for (const agent of args.agents) {
    const runtime = runtimesById.get(agent.runtime_id) ?? null;
    const tasks = tasksByAgent.get(agent.id) ?? [];
    out.set(
      agent.id,
      deriveEmployeeStateExplanation({
        agent,
        runtime,
        tasks,
        now: args.now,
        workspaceBacklogCount,
      }),
    );
  }
  return out;
}

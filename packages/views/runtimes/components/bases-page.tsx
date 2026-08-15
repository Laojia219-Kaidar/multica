"use client";

import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  AlertTriangle,
  ArrowRight,
  ChevronRight,
  Cpu,
  Network,
  Server,
  Users,
} from "lucide-react";
import { toast } from "sonner";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { api } from "@multica/core/api";
import { baseKeys, baseListOptions } from "@multica/core/runtimes";
import { runtimeListOptions } from "@multica/core/runtimes/queries";
import { agentListOptions } from "@multica/core/workspace/queries";
import type { Agent, AgentRuntime } from "@multica/core/types";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import {
  CollectionPageHeader,
  CollectionPageState,
} from "../../layout/collection-page";
import { AppLink } from "../../navigation";

/**
 * BasesPage — Lane C observed-execution base overview.
 *
 * A base is a physical execution machine derived read-only from runtime
 * `device_info`. This page shows machine / daemon / runtime / resident
 * employees / running load, and lets an owner migrate the agents of a faulted
 * runtime onto a healthy runtime — agent identity never changes, so the
 * Employee -> Agent binding survives the move.
 */
export function BasesPage() {
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const qc = useQueryClient();
  const { data: bases = [], isLoading } = useQuery(baseListOptions(wsId));
  const { data: runtimes = [] } = useQuery(runtimeListOptions(wsId));
  const { data: agents = [] } = useQuery(agentListOptions(wsId));
  const [expanded, setExpanded] = useState<string | null>(null);
  const [targets, setTargets] = useState<Record<string, string>>({});

  const agentsByRuntime = useMemo(() => {
    const map = new Map<string, Agent[]>();
    for (const agent of agents) {
      const list = map.get(agent.runtime_id) ?? [];
      list.push(agent);
      map.set(agent.runtime_id, list);
    }
    return map;
  }, [agents]);

  const runtimeById = useMemo(() => {
    const map = new Map<string, AgentRuntime>();
    for (const runtime of runtimes) {
      map.set(runtime.id, runtime);
    }
    return map;
  }, [runtimes]);

  const onlineTargets = useMemo(
    () => runtimes.filter((runtime) => runtime.status === "online"),
    [runtimes],
  );

  const migrate = useMutation({
    mutationFn: ({
      sourceId,
      targetId,
    }: {
      sourceId: string;
      targetId: string;
    }) => api.migrateRuntimeAgents(sourceId, targetId),
    onSuccess: (result) => {
      void qc.invalidateQueries({ queryKey: baseKeys.all(wsId) });
      void qc.invalidateQueries({ queryKey: runtimeListOptions(wsId).queryKey });
      void qc.invalidateQueries({ queryKey: agentListOptions(wsId).queryKey });
      setTargets((current) => ({ ...current, [result.source_runtime_id]: "" }));
      toast.success(
        `Migrated ${result.agents_migrated} agent(s) and ${result.tasks_migrated} task(s)`,
      );
    },
    onError: () => toast.error("Migration failed"),
  });

  if (isLoading) {
    return (
      <div className="flex min-h-0 flex-1 flex-col">
        <CollectionPageHeader icon={Server} title="Bases" />
        <div className="space-y-3 p-5">
          {Array.from({ length: 3 }).map((_, index) => (
            <Skeleton key={index} className="h-24 w-full rounded-lg" />
          ))}
        </div>
      </div>
    );
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <CollectionPageHeader
        icon={Server}
        title="Bases"
        description="Observed execution bases grouped by physical machine"
        actions={
          <AppLink href={paths.runtimes()} className="text-sm text-muted-foreground hover:text-foreground">
            Back to runtimes
          </AppLink>
        }
      />

      {bases.length === 0 ? (
        <CollectionPageState
          icon={Network}
          title="No bases observed"
          description="Register a runtime from a machine to populate its execution base."
        />
      ) : (
        <div className="min-h-0 flex-1 overflow-y-auto">
          <div className="grid grid-cols-1 gap-4 p-5 sm:grid-cols-2 xl:grid-cols-3">
            {bases.map((base) => (
              <BaseCard
                key={base.machine_title}
                base={base}
                agentsByRuntime={agentsByRuntime}
                runtimeById={runtimeById}
                onlineTargets={onlineTargets}
                targets={targets}
                expanded={expanded === base.machine_title}
                onToggle={() =>
                  setExpanded((current) =>
                    current === base.machine_title ? null : base.machine_title,
                  )
                }
                onTargetChange={(sourceId, targetId) =>
                  setTargets((current) => ({ ...current, [sourceId]: targetId }))
                }
                onMigrate={(sourceId) => {
                  const targetId = targets[sourceId];
                  if (!targetId) {
                    toast.error("Choose a healthy target runtime first");
                    return;
                  }
                  migrate.mutate({ sourceId, targetId });
                }}
                migrating={
                  migrate.isPending
                    ? Array.from(
                        migrate.variables
                          ? [migrate.variables.sourceId]
                          : [],
                      )
                    : []
                }
              />
            ))}
          </div>
        </div>
      )}
    </div>
  );
}

function BaseCard({
  base,
  agentsByRuntime,
  runtimeById,
  onlineTargets,
  targets,
  expanded,
  onToggle,
  onTargetChange,
  onMigrate,
  migrating,
}: {
  base: import("@multica/core/types").BaseOverview;
  agentsByRuntime: Map<string, Agent[]>;
  runtimeById: Map<string, AgentRuntime>;
  onlineTargets: AgentRuntime[];
  targets: Record<string, string>;
  expanded: boolean;
  onToggle: () => void;
  onTargetChange: (sourceId: string, targetId: string) => void;
  onMigrate: (sourceId: string) => void;
  migrating: string[];
}) {
  const employees = base.runtimes.reduce(
    (total, runtime) => total + (agentsByRuntime.get(runtime.runtime_id)?.length ?? 0),
    0,
  );

  return (
    <div className="rounded-lg border bg-card shadow-sm">
      <button
        type="button"
        onClick={onToggle}
        className="flex w-full items-center gap-2 p-4 text-left"
        aria-expanded={expanded}
      >
        <Server className="size-5 shrink-0 text-muted-foreground" />
        <div className="min-w-0 flex-1">
          <h3 className="truncate text-base font-semibold">{base.machine_title}</h3>
          <p className="mt-0.5 truncate text-xs text-muted-foreground">
            {base.daemon_count} daemon{base.daemon_count === 1 ? "" : "s"} ·{" "}
            {base.runtime_online}/{base.runtime_registered} runtimes online
          </p>
        </div>
        <ChevronRight
          className={`size-4 shrink-0 text-muted-foreground transition-transform ${
            expanded ? "rotate-90" : ""
          }`}
        />
      </button>

      <div className="grid grid-cols-2 gap-2 px-4 pb-3 text-sm">
        <Metric
          icon={Cpu}
          label="Employees"
          value={String(employees)}
        />
        <Metric
          icon={Network}
          label="Load (running)"
          value={String(base.load_running)}
        />
      </div>

      {expanded ? (
        <div className="border-t">
          <ul className="divide-y">
            {base.runtimes.map((runtime) => {
              const residentAgents = agentsByRuntime.get(runtime.runtime_id) ?? [];
              const faultedRuntime = runtime.status !== "online";
              const fullRuntime = runtimeById.get(runtime.runtime_id);
              return (
                <li key={runtime.runtime_id} className="px-4 py-3">
                  <div className="flex items-center gap-2">
                    <span
                      className={`size-2 shrink-0 rounded-full ${
                        faultedRuntime ? "bg-red-500" : "bg-emerald-500"
                      }`}
                    />
                    <span className="min-w-0 flex-1 truncate text-sm font-medium">
                      {runtime.runtime_name}
                    </span>
                    <span className="text-xs text-muted-foreground">
                      {runtime.status}
                    </span>
                  </div>
                  {residentAgents.length > 0 ? (
                    <p className="mt-1 text-xs text-muted-foreground">
                      {residentAgents.map((agent) => agent.name).join(", ")}
                    </p>
                  ) : null}
                  {faultedRuntime ? (
                    <div className="mt-2 flex items-center gap-2">
                      <AlertTriangle className="size-4 shrink-0 text-amber-500" />
                      <select
                        value={targets[runtime.runtime_id] ?? ""}
                        onChange={(event) =>
                          onTargetChange(runtime.runtime_id, event.target.value)
                        }
                        className="h-8 min-w-0 flex-1 rounded-md border bg-background px-2 text-xs"
                        aria-label={`Migrate ${runtime.runtime_name} to`}
                      >
                        <option value="" disabled>
                          Migrate to healthy runtime…
                        </option>
                        {onlineTargets
                          .filter((target) => target.id !== runtime.runtime_id)
                          .map((target) => (
                            <option key={target.id} value={target.id}>
                              {fullRuntime && target.runtime_mode !== fullRuntime.runtime_mode
                                ? `⚠ `
                                : ""}
                              {target.name}
                            </option>
                          ))}
                      </select>
                      <button
                        type="button"
                        onClick={() => onMigrate(runtime.runtime_id)}
                        disabled={
                          migrating.includes(runtime.runtime_id) ||
                          !targets[runtime.runtime_id]
                        }
                        className="flex h-8 shrink-0 items-center gap-1 rounded-md border border-amber-300 px-2 text-xs text-amber-700 transition-colors hover:bg-amber-50 disabled:opacity-50"
                      >
                        <ArrowRight className="size-3.5" />
                        Migrate
                      </button>
                    </div>
                  ) : null}
                </li>
              );
            })}
          </ul>
        </div>
      ) : null}
    </div>
  );
}

function Metric({
  icon: Icon,
  label,
  value,
}: {
  icon: typeof Users;
  label: string;
  value: string;
}) {
  return (
    <div className="flex items-center gap-2 rounded-md bg-muted/50 px-2 py-1.5">
      <Icon className="size-3.5 shrink-0 text-muted-foreground" />
      <span className="min-w-0">
        <span className="block truncate text-[10px] uppercase tracking-wide text-muted-foreground">
          {label}
        </span>
        <span className="block text-sm font-medium">{value}</span>
      </span>
    </div>
  );
}

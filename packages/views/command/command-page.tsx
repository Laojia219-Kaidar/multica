"use client";

import { useQuery } from "@tanstack/react-query";
import { ArrowRight, Bot, FolderKanban, ListTodo, Monitor, ShieldCheck } from "lucide-react";
import { useWorkspaceId } from "@multica/core/hooks";
import {
  commandIssueMetricsOptions,
  type CommandIssueMetrics,
} from "@multica/core/issues/queries";
import { projectListOptions } from "@multica/core/projects/queries";
import { runtimeListOptions } from "@multica/core/runtimes/queries";
import { useWorkspacePaths } from "@multica/core/paths";
import type { Agent, AgentRuntime, Project } from "@multica/core/types";
import { agentListOptions } from "@multica/core/workspace/queries";
import { AppLink } from "../navigation";
import { PageHeader } from "../layout/page-header";
import { useT } from "../i18n";

type CommandProject = Pick<Project, "status">;
type CommandAgent = Pick<Agent, "status">;
type CommandRuntime = Pick<AgentRuntime, "status">;

export interface CommandMetrics {
  openWork: number;
  inReview: number;
  activeProjects: number;
  onlineRuntimes: number;
  workingAgents: number;
}

export function buildCommandMetrics(
  issueMetrics: CommandIssueMetrics,
  projects: readonly CommandProject[],
  agents: readonly CommandAgent[],
  runtimes: readonly CommandRuntime[],
): CommandMetrics {
  return {
    openWork: issueMetrics.openWork,
    inReview: issueMetrics.inReview,
    activeProjects: projects.filter((project) => project.status === "in_progress").length,
    onlineRuntimes: runtimes.filter((runtime) => runtime.status === "online").length,
    workingAgents: agents.filter((agent) => agent.status === "working").length,
  };
}

const EMPTY_ISSUE_METRICS: CommandIssueMetrics = { openWork: 0, inReview: 0 };
const EMPTY_PROJECTS: Project[] = [];
const EMPTY_AGENTS: Agent[] = [];
const EMPTY_RUNTIMES: AgentRuntime[] = [];

export function CommandPage() {
  const { t } = useT("layout");
  const wsId = useWorkspaceId();
  const wsPaths = useWorkspacePaths();
  const issuesQuery = useQuery(commandIssueMetricsOptions(wsId));
  const projectsQuery = useQuery(projectListOptions(wsId));
  const agentsQuery = useQuery(agentListOptions(wsId));
  const runtimesQuery = useQuery(runtimeListOptions(wsId));

  const metrics = buildCommandMetrics(
    issuesQuery.data ?? EMPTY_ISSUE_METRICS,
    projectsQuery.data ?? EMPTY_PROJECTS,
    agentsQuery.data ?? EMPTY_AGENTS,
    runtimesQuery.data ?? EMPTY_RUNTIMES,
  );
  const isLoading = [issuesQuery, projectsQuery, agentsQuery, runtimesQuery].some((query) => query.isLoading);
  const isDegraded = [issuesQuery, projectsQuery, agentsQuery, runtimesQuery].some((query) => query.isError);
  const cards = [
    { label: t(($) => $.command.open_work), value: metrics.openWork, href: wsPaths.issues(), icon: ListTodo },
    { label: t(($) => $.command.in_review), value: metrics.inReview, href: wsPaths.issues(), icon: ShieldCheck },
    { label: t(($) => $.command.active_projects), value: metrics.activeProjects, href: wsPaths.projects(), icon: FolderKanban },
    { label: t(($) => $.command.online_runtimes), value: metrics.onlineRuntimes, href: wsPaths.runtimes(), icon: Monitor },
    { label: t(($) => $.command.working_agents), value: metrics.workingAgents, href: wsPaths.agents(), icon: Bot },
  ];

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PageHeader>
        <h1 className="text-sm font-semibold">{t(($) => $.command.title)}</h1>
      </PageHeader>
      <main className="flex-1 overflow-auto p-6">
        <div className="mx-auto max-w-6xl space-y-6">
          <div>
            <h2 className="text-2xl font-semibold tracking-tight">{t(($) => $.command.title)}</h2>
            <p className="mt-1 text-sm text-muted-foreground">{t(($) => $.command.subtitle)}</p>
          </div>
          {isLoading && <p className="text-sm text-muted-foreground">{t(($) => $.command.loading)}</p>}
          {isDegraded && (
            <p className="rounded-lg border border-destructive/30 bg-destructive/5 px-4 py-3 text-sm text-destructive">
              {t(($) => $.command.degraded)}
            </p>
          )}
          <section className="grid gap-4 sm:grid-cols-2 xl:grid-cols-5">
            {cards.map((card) => {
              const Icon = card.icon;
              return (
                <AppLink
                  key={card.label}
                  href={card.href}
                  className="group rounded-xl border bg-card p-4 transition-colors hover:bg-accent/50"
                >
                  <div className="flex items-center justify-between text-muted-foreground">
                    <Icon className="size-4" />
                    <ArrowRight className="size-4 transition-transform group-hover:translate-x-0.5" />
                  </div>
                  <p className="mt-6 text-3xl font-semibold tabular-nums">{card.value}</p>
                  <div className="mt-1 flex items-center justify-between gap-2 text-sm">
                    <span>{card.label}</span>
                    <span className="text-xs text-muted-foreground">{t(($) => $.command.open_surface)}</span>
                  </div>
                </AppLink>
              );
            })}
          </section>
        </div>
      </main>
    </div>
  );
}

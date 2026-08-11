"use client";

import { Bot, Building2, ExternalLink, Fingerprint, FolderKanban, ShieldCheck } from "lucide-react";
import { Badge } from "@multica/ui/components/ui/badge";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { buttonVariants } from "@multica/ui/components/ui/button";
import { useWorkspacePaths } from "@multica/core/paths";
import type { CompanyOpsEmployeeDossier } from "@multica/core/types";
import { useT } from "../i18n";
import { isBindingOperable } from "./binding-state";
import { BindingBadge } from "./binding-badge";
import { BindingStateExplanation } from "./source-gap";

function MetaRow({
  icon,
  label,
  children,
}: {
  icon: React.ReactNode;
  label: string;
  children: React.ReactNode;
}) {
  return (
    <div className="flex items-start gap-2.5">
      <span className="mt-0.5 shrink-0 text-muted-foreground">{icon}</span>
      <div className="min-w-0 flex-1">
        <div className="text-xs text-muted-foreground">{label}</div>
        <div className="mt-0.5 min-w-0 truncate text-sm">{children}</div>
      </div>
    </div>
  );
}

export interface EmployeeDossierProps {
  dossier: CompanyOpsEmployeeDossier;
}

export function EmployeeDossier({ dossier }: EmployeeDossierProps) {
  const { t } = useT("organization");
  const wsPaths = useWorkspacePaths();

  const operable = isBindingOperable(dossier.binding_state);
  const agentUuid = dossier.hivecrew_agent_id;

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex-1 min-h-0 overflow-y-auto">
        <div className="max-w-2xl space-y-5 p-4">
          {/* Header */}
          <div>
            <div className="flex flex-wrap items-center gap-2">
              <Fingerprint className="size-5 text-muted-foreground" />
              <h1 className="min-w-0 flex-1 truncate text-base font-semibold">
                {dossier.display_name ?? dossier.employee_id}
              </h1>
              <BindingBadge state={dossier.binding_state} />
            </div>
            <p className="mt-2 truncate text-xs text-muted-foreground">
              {dossier.employee_id}
            </p>
          </div>

          {/* Identity */}
          <div className="grid grid-cols-1 gap-3 rounded-md border p-3 sm:grid-cols-2">
            <MetaRow icon={<Fingerprint className="size-4" />} label={t(($) => $.dossier.employee_id_label)}>
              {dossier.employee_id}
            </MetaRow>
            <MetaRow icon={<Building2 className="size-4" />} label={t(($) => $.dossier.department_label)}>
              {dossier.department_ref ?? "—"}
            </MetaRow>
            <MetaRow icon={<FolderKanban className="size-4" />} label={t(($) => $.dossier.position_label)}>
              {dossier.position_ref ?? "—"}
            </MetaRow>
            <MetaRow icon={<Bot className="size-4" />} label={t(($) => $.dossier.workforce_agent_label)}>
              {dossier.workforce_agent_id ?? "—"}
            </MetaRow>
          </div>

          {/* Fail-closed explanation for non-operable states */}
          <BindingStateExplanation state={dossier.binding_state} />

          {/* Execution identity — only when the exact binding is available */}
          <div className="rounded-md border p-3">
            <div className="flex items-center gap-2 text-sm font-medium">
              <ShieldCheck className="size-4 text-muted-foreground" />
              <span>{t(($) => $.dossier.execution_identity_label)}</span>
            </div>
            {operable && agentUuid && dossier.local_agent ? (
              <div className="mt-3 space-y-2">
                <div className="flex items-center gap-2.5 rounded-md bg-muted/40 px-3 py-2.5">
                  <Bot className="size-4 shrink-0 text-muted-foreground" />
                  <div className="min-w-0 flex-1">
                    <div className="truncate text-sm font-medium">
                      {dossier.local_agent.name}
                    </div>
                    <div className="truncate text-xs text-muted-foreground">
                      {dossier.local_agent.id} · {dossier.local_agent.runtime_mode}
                      {dossier.local_agent.model ? ` · ${dossier.local_agent.model}` : ""}
                    </div>
                  </div>
                  <Badge variant="outline" className="text-[10px]">
                    {dossier.local_agent.status}
                  </Badge>
                </div>
                <div className="flex flex-wrap items-center gap-2">
                  <a
                    href={wsPaths.agentDetail(agentUuid)}
                    className={buttonVariants({ size: "sm", variant: "outline", className: "gap-1.5" })}
                  >
                    <ExternalLink className="size-3.5" />
                    {t(($) => $.dossier.open_agent_settings)}
                  </a>
                  <a
                    href={`${wsPaths.chat()}?agent=${encodeURIComponent(agentUuid)}`}
                    className={buttonVariants({ size: "sm", variant: "default", className: "gap-1.5" })}
                  >
                    {t(($) => $.dossier.open_chat_to_assign)}
                  </a>
                </div>
              </div>
            ) : (
              <p className="mt-2 text-xs text-muted-foreground">
                {t(($) => $.dossier.execution_identity_none)}
              </p>
            )}
          </div>

          {/* Bindings — all, active and inactive */}
          <section>
            <div className="flex items-center gap-2 text-sm font-medium">
              <Fingerprint className="size-4 text-muted-foreground" />
              <span>{t(($) => $.dossier.bindings_label)}</span>
            </div>
            {dossier.bindings.length === 0 ? (
              <p className="mt-2 text-xs text-muted-foreground">
                {t(($) => $.dossier.bindings_empty)}
              </p>
            ) : (
              <ul className="mt-2 space-y-1.5">
                {dossier.bindings.map((binding) => (
                  <li
                    key={binding.identity_binding_id}
                    className="rounded-md border px-2.5 py-2 text-xs"
                  >
                    <div className="flex items-center gap-2">
                      <Badge
                        variant={binding.active ? "default" : "secondary"}
                        className="text-[10px]"
                      >
                        {binding.active
                          ? t(($) => $.dossier.binding_active)
                          : t(($) => $.dossier.binding_inactive)}
                      </Badge>
                      <span className="min-w-0 flex-1 truncate tabular-nums text-muted-foreground">
                        {binding.identity_binding_id}
                      </span>
                    </div>
                    <div className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1">
                      <span className="truncate text-muted-foreground">
                        {binding.workforce_agent_id}
                      </span>
                      <span className="truncate text-muted-foreground">
                        {binding.agent_ref}
                      </span>
                    </div>
                    {!binding.active && (
                      <p className="mt-1 text-[10px] text-muted-foreground">
                        {t(($) => $.dossier.binding_inactive_note)}
                      </p>
                    )}
                  </li>
                ))}
              </ul>
            )}
          </section>

          {/* Authority basis */}
          <section className="rounded-md border p-3">
            <div className="flex items-center gap-2 text-sm font-medium">
              <ShieldCheck className="size-4 text-muted-foreground" />
              <span>{t(($) => $.dossier.authority_basis)}</span>
            </div>
            <dl className="mt-2 space-y-1 text-xs">
              <div className="flex items-start gap-2">
                <dt className="w-24 shrink-0 text-muted-foreground">
                  {t(($) => $.dossier.source_ref)}
                </dt>
                <dd className="min-w-0 flex-1 truncate tabular-nums">
                  {dossier.authority.source_ref}
                </dd>
              </div>
              <div className="flex items-start gap-2">
                <dt className="w-24 shrink-0 text-muted-foreground">
                  {t(($) => $.dossier.revision)}
                </dt>
                <dd className="min-w-0 flex-1 truncate tabular-nums">
                  {dossier.authority.revision}
                </dd>
              </div>
              <div className="flex items-start gap-2">
                <dt className="w-24 shrink-0 text-muted-foreground">
                  {t(($) => $.dossier.digest)}
                </dt>
                <dd className="min-w-0 flex-1 truncate tabular-nums">
                  {dossier.authority.content_digest}
                </dd>
              </div>
              <div className="flex items-start gap-2">
                <dt className="w-24 shrink-0 text-muted-foreground">
                  {t(($) => $.dossier.freshness)}
                </dt>
                <dd className="min-w-0 flex-1 truncate">
                  {dossier.authority.freshness}
                </dd>
              </div>
            </dl>
          </section>
        </div>
      </div>
    </div>
  );
}

export function EmployeeDossierSkeleton() {
  const { t } = useT("organization");
  return (
    <div className="p-4">
      <Skeleton className="h-6 w-48" />
      <Skeleton className="mt-3 h-4 w-32" />
      <div className="mt-4 grid grid-cols-1 gap-3 sm:grid-cols-2">
        <Skeleton className="h-16 w-full" />
        <Skeleton className="h-16 w-full" />
      </div>
      <Skeleton className="mt-4 h-24 w-full" />
      <p className="mt-4 text-xs text-muted-foreground">{t(($) => $.page.loading)}</p>
    </div>
  );
}
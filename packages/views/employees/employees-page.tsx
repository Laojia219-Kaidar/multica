"use client";

import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Factory, Plus } from "lucide-react";
import { toast } from "sonner";
import { useWorkspaceId } from "@multica/core/hooks";
import { api } from "@multica/core/api";
import { workforceBaseRuntimeOptions } from "@multica/core/work-entry";
import { WORKFORCE_BASE_RUNTIME_SCHEMA_VERSION } from "@multica/core/types";
import { CollectionPageHeader } from "../layout/collection-page";
import { useT } from "../i18n";

type Emp = { id: string; name: string; position?: string; department?: string; agent_id?: string; status: string };

/**
 * Base authority verdict for the employee column. A resolved verdict carries
 * the exact-`employee_id` join of the workforce-base-runtime read model; a
 * gap verdict means the source is unavailable or ambiguous and every card
 * must fail closed with the same generic gap copy.
 */
type BaseAuthority =
  | { state: "pending" }
  | { state: "gap" }
  | { state: "resolved"; baseByEmployee: ReadonlyMap<string, string> };

/**
 * Validates the complete workforce-base-runtime response shape BEFORE any row
 * access and only then builds the exact-`employee_id` join. Any structural
 * doubt fails the whole read model closed: non-array `items`, a null /
 * wrong-type / unknown row, a row whose join keys or Base evidence are
 * missing or whitespace-only, or duplicate / conflicting matches for one
 * Employee all poison the claim of "exactly one complete unambiguous match",
 * so no card may keep showing a verified Base. Joining happens on the trimmed
 * `employee_id` by strict equality only — never on name, position,
 * department, terminal or truncated-id heuristics.
 */
function evaluateBaseAuthority(raw: unknown): BaseAuthority {
  if (raw === null || typeof raw !== "object" || Array.isArray(raw)) return { state: "gap" };
  const response = raw as Record<string, unknown>;
  if (response.schema_version !== WORKFORCE_BASE_RUNTIME_SCHEMA_VERSION) return { state: "gap" };
  const items: unknown = response.items;
  if (!Array.isArray(items)) return { state: "gap" };
  const baseByEmployee = new Map<string, string>();
  for (const row of items) {
    if (row === null || typeof row !== "object" || Array.isArray(row)) return { state: "gap" };
    const record = row as Record<string, unknown>;
    const { employee_id, workforce_agent_id, base_machine_title } = record;
    if (
      typeof employee_id !== "string" ||
      typeof workforce_agent_id !== "string" ||
      typeof base_machine_title !== "string"
    ) {
      return { state: "gap" };
    }
    const employeeId = employee_id.trim();
    const agentId = workforce_agent_id.trim();
    const baseTitle = base_machine_title.trim();
    if (employeeId === "" || agentId === "" || baseTitle === "") return { state: "gap" };
    if (baseByEmployee.has(employeeId)) return { state: "gap" };
    baseByEmployee.set(employeeId, baseTitle);
  }
  return { state: "resolved", baseByEmployee };
}

function BaseAuthorityLine({ authority, employeeId }: { authority: BaseAuthority; employeeId: string }) {
  const { t } = useT("organization");
  if (authority.state === "resolved") {
    const baseTitle = authority.baseByEmployee.get(employeeId);
    if (baseTitle) {
      return (
        <div className="mt-1 flex items-center gap-1.5 text-xs">
          <span className="inline-block size-1.5 shrink-0 rounded-full bg-emerald-500" />
          <span className="text-muted-foreground">{t(($) => $.employees_page.base_label)}</span>
          <span className="truncate font-medium">{baseTitle}</span>
        </div>
      );
    }
    return (
      <div className="mt-1 flex items-center gap-1.5 text-xs text-muted-foreground">
        <span className="inline-block size-1.5 shrink-0 rounded-full bg-muted-foreground/50" />
        <span>{t(($) => $.employees_page.base_not_verified)}</span>
      </div>
    );
  }
  if (authority.state === "gap") {
    return (
      <div className="mt-1 flex items-center gap-1.5 text-xs text-amber-600">
        <span className="inline-block size-1.5 shrink-0 rounded-full bg-amber-500" />
        <span>{t(($) => $.employees_page.base_authority_gap)}</span>
      </div>
    );
  }
  return (
    <div className="mt-1 flex items-center gap-1.5 text-xs text-muted-foreground">
      <span className="inline-block size-1.5 shrink-0 rounded-full bg-muted-foreground/50" />
      <span>{t(($) => $.employees_page.base_checking)}</span>
    </div>
  );
}

export function EmployeesPage() {
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const { t } = useT("organization");
  const { data: employees = [], isLoading } = useQuery({ queryKey: ["employees", wsId], queryFn: () => api.listEmployees() });
  const baseQuery = useQuery(workforceBaseRuntimeOptions(wsId));
  const [name, setName] = useState(""); const [position, setPosition] = useState(""); const [department, setDepartment] = useState("");

  // Fail closed on ANY query error, including a refetch error after a
  // successful load: TanStack Query keeps the stale data in that case, so the
  // verdict must be derived from the error state first — a stale verified
  // Base must never survive a source failure.
  const baseAuthority = useMemo<BaseAuthority>(() => {
    if (baseQuery.isError) return { state: "gap" };
    if (baseQuery.data === undefined) return { state: "pending" };
    return evaluateBaseAuthority(baseQuery.data);
  }, [baseQuery.isError, baseQuery.data]);

  const create = useMutation({
    mutationFn: (d: { name: string; position?: string; department?: string }) => api.createEmployee(d),
    onSuccess: () => { void qc.invalidateQueries({ queryKey: ["employees", wsId] }); setName(""); setPosition(""); setDepartment(""); toast.success(t(($) => $.employees_page.create_success)); },
    onError: () => toast.error(t(($) => $.employees_page.create_failed)),
  });
  const bind = useMutation({
    mutationFn: (d: { id: string; agent_id: string; status: string }) => api.updateEmployeeBinding(d.id, { agent_id: d.agent_id, status: d.status }),
    onSuccess: () => { void qc.invalidateQueries({ queryKey: ["employees", wsId] }); toast.success(t(($) => $.employees_page.bind_success)); },
    onError: () => toast.error(t(($) => $.employees_page.bind_failed)),
  });
  return (
    <div className="flex h-full flex-col">
      <CollectionPageHeader icon={Factory} title={t(($) => $.employees_page.header_title)} description={t(($) => $.employees_page.header_description)} />
      <div className="grid grid-cols-1 gap-4 p-4 lg:grid-cols-3">
        <div className="rounded-lg border bg-card p-4 shadow-sm">
          <h3 className="text-sm font-semibold">{t(($) => $.employees_page.create_title)}</h3>
          <input value={name} onChange={(e)=>setName(e.target.value)} placeholder={t(($) => $.employees_page.name_placeholder)} className="mt-2 w-full rounded-md border px-2 py-1.5 text-sm" />
          <input value={position} onChange={(e)=>setPosition(e.target.value)} placeholder={t(($) => $.employees_page.position_placeholder)} className="mt-2 w-full rounded-md border px-2 py-1.5 text-sm" />
          <input value={department} onChange={(e)=>setDepartment(e.target.value)} placeholder={t(($) => $.employees_page.department_placeholder)} className="mt-2 w-full rounded-md border px-2 py-1.5 text-sm" />
          <button disabled={!name.trim() || create.isPending} onClick={()=>create.mutate({ name: name.trim(), position: position.trim() || undefined, department: department.trim() || undefined })} className="mt-3 flex items-center gap-1 rounded-md bg-primary px-3 py-1.5 text-sm text-primary-foreground disabled:opacity-50"><Plus className="size-4" />{t(($) => $.employees_page.create_action)}</button>
        </div>
        <div className="lg:col-span-2 rounded-lg border bg-card p-4 shadow-sm">
          <h3 className="text-sm font-semibold">{t(($) => $.employees_page.list_title)}</h3>
          {isLoading ? <p className="mt-2 text-sm text-muted-foreground">{t(($) => $.employees_page.loading)}</p> : employees.length === 0 ? <p className="mt-2 text-sm text-muted-foreground">{t(($) => $.employees_page.empty)}</p> : (
            <ul className="mt-2 space-y-2">
              {employees.map((e: Emp) => (
                <li key={e.id} className="rounded-md border p-3 text-sm">
                  <div className="flex items-center gap-2">
                    <div className="min-w-0 flex-1">
                      <div className="font-medium">{e.name}{e.position ? `｜${e.position}` : ""}</div>
                      <div className="text-xs text-muted-foreground">
                        {[
                          `${t(($) => $.employees_page.id_label)} ${e.id.slice(0, 8)}`,
                          e.department || t(($) => $.employees_page.no_department),
                          e.status,
                          e.agent_id ? `${t(($) => $.employees_page.agent_label)} ${e.agent_id.slice(0, 8)}` : null,
                        ].filter(Boolean).join(" · ")}
                      </div>
                      <BaseAuthorityLine authority={baseAuthority} employeeId={e.id} />
                    </div>
                    <select value={e.status} onChange={(ev)=>bind.mutate({ id: e.id, agent_id: e.agent_id || "", status: ev.target.value })} className="h-8 rounded-md border bg-background px-2 text-xs">
                      <option value="draft">{t(($) => $.employees_page.status_draft)}</option>
                      <option value="onboarding">{t(($) => $.employees_page.status_onboarding)}</option>
                      <option value="canary">{t(($) => $.employees_page.status_canary)}</option>
                      <option value="active">{t(($) => $.employees_page.status_active)}</option>
                      <option value="retired">{t(($) => $.employees_page.status_retired)}</option>
                    </select>
                  </div>
                </li>
              ))}
            </ul>
          )}
        </div>
      </div>
    </div>
  );
}

"use client";
import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Factory, Plus } from "lucide-react";
import { toast } from "sonner";
import { useWorkspaceId } from "@multica/core/hooks";
import { api } from "@multica/core/api";
import { CollectionPageHeader } from "../layout/collection-page";

type Emp = { id: string; name: string; position?: string; department?: string; agent_id?: string; status: string };

export function EmployeesPage() {
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const { data: employees = [], isLoading } = useQuery({ queryKey: ["employees", wsId], queryFn: () => api.listEmployees() });
  const [name, setName] = useState(""); const [position, setPosition] = useState(""); const [department, setDepartment] = useState("");
  const create = useMutation({
    mutationFn: (d: { name: string; position?: string; department?: string }) => api.createEmployee(d),
    onSuccess: () => { void qc.invalidateQueries({ queryKey: ["employees", wsId] }); setName(""); setPosition(""); setDepartment(""); toast.success("员工身份已创建"); },
    onError: () => toast.error("创建失败"),
  });
  const bind = useMutation({
    mutationFn: (d: { id: string; agent_id: string; status: string }) => api.updateEmployeeBinding(d.id, { agent_id: d.agent_id, status: d.status }),
    onSuccess: () => { void qc.invalidateQueries({ queryKey: ["employees", wsId] }); toast.success("已上岗"); },
    onError: () => toast.error("上岗失败"),
  });
  return (
    <div className="flex h-full flex-col">
      <CollectionPageHeader icon={Factory} title="数字员工工厂" description="Employee 身份（岗位需求→身份→绑定→Canary→上岗）。本地执行投影；公司正式身份真源=外部 HiveCosm 权威。" />
      <div className="grid grid-cols-1 gap-4 p-4 lg:grid-cols-3">
        <div className="rounded-lg border bg-card p-4 shadow-sm">
          <h3 className="text-sm font-semibold">新员工身份（岗位需求）</h3>
          <input value={name} onChange={(e)=>setName(e.target.value)} placeholder="姓名" className="mt-2 w-full rounded-md border px-2 py-1.5 text-sm" />
          <input value={position} onChange={(e)=>setPosition(e.target.value)} placeholder="岗位（如 全栈工程师）" className="mt-2 w-full rounded-md border px-2 py-1.5 text-sm" />
          <input value={department} onChange={(e)=>setDepartment(e.target.value)} placeholder="部门（如 工程基地）" className="mt-2 w-full rounded-md border px-2 py-1.5 text-sm" />
          <button disabled={!name.trim() || create.isPending} onClick={()=>create.mutate({ name: name.trim(), position: position.trim() || undefined, department: department.trim() || undefined })} className="mt-3 flex items-center gap-1 rounded-md bg-primary px-3 py-1.5 text-sm text-primary-foreground disabled:opacity-50"><Plus className="size-4" />创建</button>
        </div>
        <div className="lg:col-span-2 rounded-lg border bg-card p-4 shadow-sm">
          <h3 className="text-sm font-semibold">员工列表</h3>
          {isLoading ? <p className="mt-2 text-sm text-muted-foreground">加载中…</p> : employees.length === 0 ? <p className="mt-2 text-sm text-muted-foreground">暂无员工</p> : (
            <ul className="mt-2 space-y-2">
              {employees.map((e: Emp) => (
                <li key={e.id} className="flex items-center gap-2 rounded-md border p-3 text-sm">
                  <div className="min-w-0 flex-1">
                    <div className="font-medium">{e.name}{e.position ? `｜${e.position}` : ""}</div>
                    <div className="text-xs text-muted-foreground">id {e.id.slice(0,8)} · {e.department || "未设部门"} · {e.status}{e.agent_id ? ` · agent ${e.agent_id.slice(0,8)}` : ""}</div>
                  </div>
                  <select value={e.status} onChange={(ev)=>bind.mutate({ id: e.id, agent_id: e.agent_id || "", status: ev.target.value })} className="h-8 rounded-md border bg-background px-2 text-xs">
                    <option value="draft">draft</option><option value="onboarding">onboarding</option><option value="canary">canary</option><option value="active">active</option><option value="retired">retired</option>
                  </select>
                </li>
              ))}
            </ul>
          )}
        </div>
      </div>
    </div>
  );
}

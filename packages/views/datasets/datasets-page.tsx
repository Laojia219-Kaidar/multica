"use client";
import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronDown, ChevronRight, Database, Plus, ShieldCheck } from "lucide-react";
import { toast } from "sonner";
import { useWorkspaceId } from "@multica/core/hooks";
import { api } from "@multica/core/api";
import { CollectionPageHeader } from "../layout/collection-page";

type Ds = { id: string; name: string; domain: string; product_type: string; version: number; authorized_agent_ids: string[] };
type Employee = { id: string; name: string; agent_id?: string; status: string };

const DOMAINS = ["公司治理","项目成果","产品代码","客户市场","合同财务法务","个人受限"];
const PRODUCTS: { value: string; label: string }[] = [
  { value: "rag_kb", label: "RAG 知识库" },
  { value: "training_pack", label: "员工训练包" },
  { value: "finetune", label: "模型微调 Dataset" },
  { value: "eval", label: "独立评测 Dataset" },
];
const PRODUCT_LABEL: Record<string, string> = Object.fromEntries(PRODUCTS.map((p) => [p.value, p.label]));

export function DatasetsPage() {
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const { data: datasets = [], isLoading } = useQuery({ queryKey: ["datasets", wsId], queryFn: () => api.listDatasets() });
  const { data: employees = [] } = useQuery({ queryKey: ["employees", wsId, "datasets"], queryFn: () => api.listEmployees() });
  const [name, setName] = useState(""); const [domain, setDomain] = useState("项目成果"); const [product, setProduct] = useState("rag_kb");
  const [authOpenId, setAuthOpenId] = useState<string | null>(null);
  const [renameId, setRenameId] = useState<string | null>(null);
  const [renameValue, setRenameValue] = useState("");
  const create = useMutation({
    mutationFn: (d: { name: string; domain: string; product_type: string }) => api.createDataset(d),
    onSuccess: () => { void qc.invalidateQueries({ queryKey: ["datasets", wsId] }); setName(""); toast.success("数据集已创建"); },
    onError: () => toast.error("创建失败"),
  });
  const update = useMutation({
    mutationFn: (v: { id: string; patch: { name?: string; version?: number; authorized_agent_ids?: string[] } }) => api.updateDataset(v.id, v.patch),
    onSuccess: () => { void qc.invalidateQueries({ queryKey: ["datasets", wsId] }); toast.success("数据集已更新"); },
    onError: () => toast.error("更新失败"),
  });
  const remove = useMutation({
    mutationFn: (id: string) => api.deleteDataset(id),
    onSuccess: () => { void qc.invalidateQueries({ queryKey: ["datasets", wsId] }); toast.success("数据集已删除"); },
    onError: () => toast.error("删除失败"),
  });

  // 员工授权投影：数据集授权的是执行身份（Agent id）；员工通过其绑定的 Agent
  // 被投影为可授权对象。未绑定 Agent 的员工不可授权，未投影到员工的 Agent id
  // 原样列出，保持诚实显示。
  const toggleEmployee = (d: Ds, employee: Employee) => {
    if (!employee.agent_id) return;
    const set = new Set(d.authorized_agent_ids);
    if (set.has(employee.agent_id)) { set.delete(employee.agent_id); } else { set.add(employee.agent_id); }
    update.mutate({ id: d.id, patch: { authorized_agent_ids: [...set] } });
  };

  const projectedIds = (d: Ds) => {
    const known = new Set(employees.map((e) => e.agent_id).filter(Boolean) as string[]);
    return d.authorized_agent_ids.filter((id) => !known.has(id));
  };

  return (
    <div className="flex h-full flex-col">
      <CollectionPageHeader icon={Database} title="数据与知识" description="原始资料→版本化 Dataset→员工授权。本地执行投影；知识权威=World Library（source_available_runtime_unavailable）。" />
      <div className="grid grid-cols-1 gap-4 p-4 lg:grid-cols-3">
        <div className="rounded-lg border bg-card p-4 shadow-sm">
          <h3 className="text-sm font-semibold">新数据集</h3>
          <input value={name} onChange={(e)=>setName(e.target.value)} placeholder="数据集名" className="mt-2 w-full rounded-md border px-2 py-1.5 text-sm" />
          <select value={domain} onChange={(e)=>setDomain(e.target.value)} className="mt-2 w-full rounded-md border bg-background px-2 py-1.5 text-sm">
            {DOMAINS.map((d)=>(<option key={d} value={d}>{d}</option>))}
          </select>
          <select value={product} onChange={(e)=>setProduct(e.target.value)} className="mt-2 w-full rounded-md border bg-background px-2 py-1.5 text-sm">
            {PRODUCTS.map((p)=>(<option key={p.value} value={p.value}>{p.label}</option>))}
          </select>
          <button disabled={!name.trim() || create.isPending} onClick={()=>create.mutate({ name: name.trim(), domain, product_type: product })} className="mt-3 flex items-center gap-1 rounded-md bg-primary px-3 py-1.5 text-sm text-primary-foreground disabled:opacity-50"><Plus className="size-4" />创建</button>
        </div>
        <div className="lg:col-span-2 rounded-lg border bg-card p-4 shadow-sm">
          <h3 className="text-sm font-semibold">数据集列表</h3>
          {isLoading ? <p className="mt-2 text-sm text-muted-foreground">加载中…</p> : datasets.length === 0 ? (
            <p className="mt-2 text-sm text-muted-foreground">暂无数据集。权威知识库 World Library 运行时尚未接通（source_available_runtime_unavailable）；此处仅展示本地执行投影。</p>
          ) : (
            <ul className="mt-2 space-y-2">
              {datasets.map((d: Ds) => {
                return (
                  <li key={d.id} className="rounded-md border p-3 text-sm">
                    {renameId === d.id ? (
                      <div className="flex items-center gap-2">
                        <input value={renameValue} onChange={(e)=>setRenameValue(e.target.value)} aria-label="数据集名称" className="w-48 rounded-md border px-2 py-1 text-sm" />
                        <button disabled={!renameValue.trim() || update.isPending} onClick={()=>{ update.mutate({ id: d.id, patch: { name: renameValue.trim() } }, { onSuccess: () => setRenameId(null) }); }} className="rounded-md bg-primary px-2 py-1 text-xs text-primary-foreground disabled:opacity-50">保存</button>
                        <button onClick={()=>setRenameId(null)} className="rounded-md border px-2 py-1 text-xs">取消</button>
                      </div>
                    ) : (
                      <div className="font-medium">{d.name} <span className="text-xs text-muted-foreground">v{d.version}</span></div>
                    )}
                    <div className="mt-1 text-xs text-muted-foreground">域 {d.domain} · {PRODUCT_LABEL[d.product_type] ?? d.product_type} · id {d.id.slice(0,8)} · 授权 {d.authorized_agent_ids.length} 员工</div>
                    <div className="mt-2 flex flex-wrap items-center gap-2">
                      <button onClick={()=>setAuthOpenId(authOpenId === d.id ? null : d.id)} className="flex items-center gap-1 rounded-md border px-2 py-1 text-xs">
                        {authOpenId === d.id ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}
                        <ShieldCheck className="size-3.5" />授权员工
                      </button>
                      <button onClick={()=>{ setRenameId(d.id); setRenameValue(d.name); }} className="rounded-md border px-2 py-1 text-xs">重命名</button>
                      <button disabled={update.isPending} onClick={()=>update.mutate({ id: d.id, patch: { version: d.version + 1 } })} className="rounded-md border px-2 py-1 text-xs disabled:opacity-50">升版 v{d.version + 1}</button>
                      <button disabled={remove.isPending} onClick={()=>{ if (!confirm(`删除数据集「${d.name}」？此操作不可撤销。`)) return; remove.mutate(d.id); }} className="rounded-md border border-destructive/40 px-2 py-1 text-xs text-destructive disabled:opacity-50">删除</button>
                    </div>
                    {authOpenId === d.id && (
                      <div className="mt-2 rounded-md border bg-muted/40 p-2">
                        <p className="text-xs text-muted-foreground">授权以执行身份（Agent）记录；勾选员工即授权其绑定的 Agent 访问本数据集。</p>
                        {employees.length === 0 ? (
                          <p className="mt-1 text-xs text-muted-foreground">暂无员工可授权</p>
                        ) : (
                          <ul className="mt-1 space-y-1">
                            {employees.map((e) => (
                              <li key={e.id} className="flex items-center gap-2 text-xs">
                                <input type="checkbox" checked={!!e.agent_id && d.authorized_agent_ids.includes(e.agent_id)} disabled={!e.agent_id || update.isPending} onChange={()=>toggleEmployee(d, e)} aria-label={`授权员工 ${e.name}`} />
                                <span>{e.name}</span>
                                {!e.agent_id && <span className="text-muted-foreground">未绑定 Agent</span>}
                                {e.agent_id && e.status !== "active" && <span className="text-muted-foreground">（{e.status}）</span>}
                              </li>
                            ))}
                          </ul>
                        )}
                        {projectedIds(d).length > 0 && (
                          <p className="mt-1 text-xs text-muted-foreground">未投影到员工的 Agent：{projectedIds(d).map((id) => id.slice(0,8)).join("、")}</p>
                        )}
                      </div>
                    )}
                  </li>
                );
              })}
            </ul>
          )}
        </div>
      </div>
    </div>
  );
}

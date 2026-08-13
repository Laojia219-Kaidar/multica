"use client";
import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Database, Plus } from "lucide-react";
import { toast } from "sonner";
import { useWorkspaceId } from "@multica/core/hooks";
import { api } from "@multica/core/api";
import { CollectionPageHeader } from "../layout/collection-page";

type Ds = { id: string; name: string; domain: string; version: number; authorized_agent_ids: string[] };

const DOMAINS = ["公司治理","项目成果","产品代码","客户市场","合同财务法务","个人受限"];

export function DatasetsPage() {
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const { data: datasets = [], isLoading } = useQuery({ queryKey: ["datasets", wsId], queryFn: () => api.listDatasets() });
  const [name, setName] = useState(""); const [domain, setDomain] = useState("项目成果");
  const create = useMutation({
    mutationFn: (d: { name: string; domain: string }) => api.createDataset(d),
    onSuccess: () => { void qc.invalidateQueries({ queryKey: ["datasets", wsId] }); setName(""); toast.success("数据集已创建"); },
    onError: () => toast.error("创建失败"),
  });
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
          <button disabled={!name.trim() || create.isPending} onClick={()=>create.mutate({ name: name.trim(), domain })} className="mt-3 flex items-center gap-1 rounded-md bg-primary px-3 py-1.5 text-sm text-primary-foreground disabled:opacity-50"><Plus className="size-4" />创建</button>
        </div>
        <div className="lg:col-span-2 rounded-lg border bg-card p-4 shadow-sm">
          <h3 className="text-sm font-semibold">数据集列表</h3>
          {isLoading ? <p className="mt-2 text-sm text-muted-foreground">加载中…</p> : datasets.length === 0 ? <p className="mt-2 text-sm text-muted-foreground">暂无数据集</p> : (
            <ul className="mt-2 space-y-2">
              {datasets.map((d: Ds) => (
                <li key={d.id} className="rounded-md border p-3 text-sm">
                  <div className="font-medium">{d.name} <span className="text-xs text-muted-foreground">v{d.version}</span></div>
                  <div className="mt-1 text-xs text-muted-foreground">域 {d.domain} · id {d.id.slice(0,8)} · 授权 {d.authorized_agent_ids.length} 员工</div>
                </li>
              ))}
            </ul>
          )}
        </div>
      </div>
    </div>
  );
}

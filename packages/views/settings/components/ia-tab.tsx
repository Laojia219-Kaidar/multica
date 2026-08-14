"use client";
import { useQuery } from "@tanstack/react-query";
import { Network } from "lucide-react";
import { api } from "@multica/core/api";
import { SettingsSection } from "./settings-layout";

type Entry = { domain: string; domain_key: string; objects: string[]; canonical_writer: string };

export function IaTab() {
  const { data, isLoading } = useQuery({ queryKey: ["object-ownership"], queryFn: () => api.getObjectOwnership() });
  const entries: Entry[] = data?.domains ?? [];
  return (
    <SettingsSection title="信息架构 · 11 能力域对象归属矩阵" description="单一真源：WO-IA-02。每个对象有且仅有一个 canonical writer；跨栏目引用走 readback，不复制真源。">
      {isLoading ? <p className="text-sm text-muted-foreground">加载中…</p> : (
        <div className="overflow-hidden rounded-md border">
          <table className="w-full text-sm">
            <thead className="bg-surface-muted text-left text-xs text-muted-foreground">
              <tr>
                <th className="px-3 py-2 font-medium">L1 能力域</th>
                <th className="px-3 py-2 font-medium">归属对象</th>
                <th className="px-3 py-2 font-medium">canonical writer</th>
              </tr>
            </thead>
            <tbody>
              {entries.map((e) => (
                <tr key={e.domain_key} className="border-t border-surface-border">
                  <td className="px-3 py-2 font-medium">{e.domain}</td>
                  <td className="px-3 py-2 text-muted-foreground">{e.objects.join(" · ")}</td>
                  <td className="px-3 py-2 text-muted-foreground">{e.canonical_writer}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <p className="mt-2 flex items-center gap-1 text-xs text-muted-foreground"><Network className="size-3" />运行时回读：GET /api/ia/object-ownership（{data?.count ?? 0} 个域）</p>
    </SettingsSection>
  );
}

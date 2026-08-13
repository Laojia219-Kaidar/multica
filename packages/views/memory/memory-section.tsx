"use client";

import type { MemoryCandidate } from "@multica/core/api/memory";

const STATUS_LABEL: Record<string, string> = {
  pending: "候选",
  validated: "已校验",
  rejected: "已拒绝",
  promoted: "已验证",
  revoked: "已撤销",
};

const KIND_LABEL: Record<string, string> = {
  episodic: "经历",
  experience: "经验",
};

export interface MemorySectionProps {
  candidates: MemoryCandidate[];
}

function MemoryRow({ c }: { c: MemoryCandidate }) {
  return (
    <div className="rounded-md border border-zinc-800 px-3 py-2 text-sm" data-testid="memory-row">
      <div className="flex items-center gap-2">
        <span className="font-semibold">{KIND_LABEL[c.kind] ?? c.kind}</span>
        <span className="text-xs text-zinc-400">{STATUS_LABEL[c.status] ?? c.status}</span>
        <span className="ml-auto text-xs text-zinc-500">证据 {c.evidence.length}</span>
      </div>
      <div className="mt-1 truncate text-zinc-300">{c.content}</div>
    </div>
  );
}

export function MemorySection({ candidates }: MemorySectionProps) {
  const promoted = candidates.filter((c) => c.status === "promoted");
  const pending = candidates.filter((c) => c.status === "pending" || c.status === "validated");
  const revoked = candidates.filter((c) => c.status === "revoked");

  return (
    <div className="flex flex-col gap-4" data-testid="memory-section">
      <section>
        <h3 className="mb-2 text-sm font-semibold">已验证经验 ({promoted.length})</h3>
        <div className="flex flex-col gap-2">
          {promoted.length === 0 ? (
            <p className="text-xs text-zinc-500">暂无已验证经验</p>
          ) : (
            promoted.map((c) => <MemoryRow key={c.id} c={c} />)
          )}
        </div>
      </section>
      <section>
        <h3 className="mb-2 text-sm font-semibold">经验候选 ({pending.length})</h3>
        <div className="flex flex-col gap-2">
          {pending.length === 0 ? (
            <p className="text-xs text-zinc-500">暂无候选</p>
          ) : (
            pending.map((c) => <MemoryRow key={c.id} c={c} />)
          )}
        </div>
      </section>
      <section>
        <h3 className="mb-2 text-sm font-semibold">纠错与废弃 ({revoked.length})</h3>
        <div className="flex flex-col gap-2">
          {revoked.length === 0 ? (
            <p className="text-xs text-zinc-500">无撤销记录</p>
          ) : (
            revoked.map((c) => <MemoryRow key={c.id} c={c} />)
          )}
        </div>
      </section>
    </div>
  );
}

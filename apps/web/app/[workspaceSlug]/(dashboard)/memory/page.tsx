"use client";

import { useQuery } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { MemorySection } from "@multica/views/memory";

export default function Page() {
  const { data: promoted = [] } = useQuery({
    queryKey: ["memory", "promoted", "employee_memory"],
    queryFn: () => api.listPromotedMemories("employee_memory"),
    refetchInterval: 15000,
  });
  const { data: candidates = [] } = useQuery({
    queryKey: ["memory", "candidates", "all"],
    queryFn: () => api.listMemoryCandidates(),
    refetchInterval: 15000,
  });
  const promotedIds = new Set(promoted.map((c) => c.id));
  const merged = [...promoted, ...candidates.filter((c) => !promotedIds.has(c.id))];

  return (
    <div className="p-6">
      <div className="mb-4">
        <h1 className="text-lg font-semibold">员工记忆</h1>
        <p className="text-sm text-zinc-500">已验证经验（promotion 回执）与经验候选（候选层只读）</p>
      </div>
      <MemorySection candidates={merged} />
    </div>
  );
}

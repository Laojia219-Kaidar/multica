"use client";

import { useQuery } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { MemorySection } from "@multica/views/memory";

export default function Page() {
  const { data = [] } = useQuery({
    queryKey: ["memory", "promoted", "employee_memory"],
    queryFn: () => api.listPromotedMemories("employee_memory"),
    refetchInterval: 15000,
  });

  return (
    <div className="p-6">
      <div className="mb-4">
        <h1 className="text-lg font-semibold">员工记忆</h1>
        <p className="text-sm text-zinc-500">已验证经验（promotion 回执，候选层只读）</p>
      </div>
      <MemorySection candidates={data} />
    </div>
  );
}

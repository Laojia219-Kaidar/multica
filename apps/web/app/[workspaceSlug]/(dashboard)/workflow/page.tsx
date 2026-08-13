"use client";

import { useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { WorkflowWorkbench } from "@multica/views/workflow";
import type { WorkflowDefinition, WorkflowInstance } from "@multica/core/api/workflow";

const PROJECT_LIFECYCLE: WorkflowDefinition = {
  id: "hivecrew.project-lifecycle",
  version: 1,
  risk: "standard",
  stages: [
    { name: "operate", sla_seconds: 7 * 24 * 3600 },
    { name: "review_repair", sla_seconds: 48 * 3600 },
    { name: "closure_pending", sla_seconds: 24 * 3600 },
    { name: "close" },
  ],
};

export default function Page() {
  const [projectId, setProjectId] = useState("");
  const [instances, setInstances] = useState<WorkflowInstance[]>([]);

  const start = useMutation({
    mutationFn: () =>
      api.startWorkflowInstance({ context: { project_id: projectId || undefined } }),
    onSuccess: (inst) => setInstances((xs) => [...xs, inst]),
  });

  return (
    <div className="p-6">
      <div className="mb-4">
        <h1 className="text-lg font-semibold">工作流内核</h1>
        <p className="text-sm text-zinc-500">
          项目生命周期（复用 W2 HIV-553）：operate → review_repair → closure_pending → close
        </p>
      </div>

      <div className="mb-4 flex gap-2">
        <input
          className="rounded-md border border-zinc-700 bg-zinc-900 px-3 py-2 text-sm"
          placeholder="project_id（可选）"
          value={projectId}
          onChange={(e) => setProjectId(e.target.value)}
        />
        <button
          className="rounded-md bg-zinc-100 px-3 py-2 text-sm font-semibold text-zinc-900"
          onClick={() => start.mutate()}
          disabled={start.isPending}
        >
          {start.isPending ? "启动中…" : "启动实例"}
        </button>
      </div>

      {start.error ? (
        <p className="mb-3 text-sm text-red-400">{(start.error as Error).message}</p>
      ) : null}

      <WorkflowWorkbench instances={instances} definitions={[PROJECT_LIFECYCLE]} />
    </div>
  );
}

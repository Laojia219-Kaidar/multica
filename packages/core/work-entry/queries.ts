import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";
import { participantFromWorkforceRow } from "./participants";
import type { WorkInboxItem, ProjectParticipantsData } from "../types/work-entry";

export const workKeys = {
  all: (wsId: string) => ["work", wsId] as const,
  inbox: (wsId: string) => [...workKeys.all(wsId), "inbox"] as const,
  workforceBaseRuntime: (wsId: string) =>
    [...workKeys.all(wsId), "workforce-base-runtime"] as const,
  participants: (wsId: string, projectId: string) =>
    [...workKeys.all(wsId), "participants", projectId] as const,
};

/** 未登记工作收件箱 — GET /api/work/reconcile (read-only). */
export function workInboxOptions(wsId: string) {
  return queryOptions({
    queryKey: workKeys.inbox(wsId),
    queryFn: (): Promise<WorkInboxItem[]> => api.reconcileWorkInbox(),
  });
}

/** 现读模型：员工 → 智能体 → 运行时 → 基地（工作区级）。 */
export function workforceBaseRuntimeOptions(wsId: string) {
  return queryOptions({
    queryKey: workKeys.workforceBaseRuntime(wsId),
    queryFn: () => api.listWorkforceBaseRuntime(),
  });
}

/**
 * 项目参与者 / 执行者读模型（VC-04）。
 *
 * 数据源策略：优先复用现有 companyops workforce_base_runtime 读模型（已部署），
 * 映射为 registered_employee 参与者；项目级聚合端点（external_agent /
 * carrier / host / session / next_action）待后端部署后接通，届时切换到
 * api.listProjectParticipants(projectId)。
 */
export function projectParticipantsOptions(wsId: string, projectId: string) {
  return queryOptions({
    queryKey: workKeys.participants(wsId, projectId),
    queryFn: async (): Promise<ProjectParticipantsData> => {
      const workforce = await api.listWorkforceBaseRuntime();
      return {
        source: "workforce_base_runtime",
        pending_project_scope: true,
        participants: workforce.items.map(participantFromWorkforceRow),
      };
    },
  });
}

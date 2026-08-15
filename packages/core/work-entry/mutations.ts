import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { workKeys } from "./queries";
import { useWorkspaceId } from "../hooks";
import type {
  WorkAttachRequest,
  WorkIgnoreRequest,
} from "../types/work-entry";

/** attach：把未归属动作挂到既有 project/issue（幂等，409 表示重复挂载）。 */
export function useAttachWorkInbox() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (data: WorkAttachRequest) => api.attachWorkInbox(data),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: workKeys.inbox(wsId) });
    },
  });
}

/** ignore：忽略未归属动作。 */
export function useIgnoreWorkInbox() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (data: WorkIgnoreRequest) => api.ignoreWorkInbox(data),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: workKeys.inbox(wsId) });
    },
  });
}

"use client";

import { useQuery } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { WorkWall } from "./work-wall";
import { TerminalLiveSection } from "./terminal-live";

export function WorkWallPage() {
  const wsId = useWorkspaceId();
  const { data = [] } = useQuery({
    queryKey: ["work-wall", wsId, "snapshot"],
    queryFn: () => api.workWallSnapshot(),
    // v1 "实时" via one workspace-wide snapshot poll every 5s (NOT per-employee).
    // SSE/WS event stream (Last-Event-ID + snapshot compensation) is the
    // planned upgrade once the integrator wires the realtime hub.
    refetchInterval: 5000,
  });
  const { data: panes = [] } = useQuery({
    queryKey: ["work-wall", wsId, "terminal-presence"],
    queryFn: () => api.listTerminalPresence(),
    refetchInterval: 10000,
  });
  return (
    <>
      <WorkWall employees={data} />
      <TerminalLiveSection panes={panes} />
    </>
  );
}

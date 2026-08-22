"use client";

import { useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import {
  subscribeWorkWallStream,
  type EmployeeLiveActivityV1,
  type WorkWallStreamState,
} from "@multica/core/api/workwall";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspaceSlug } from "@multica/core/paths";
import { WorkWall } from "./work-wall";
import { TerminalLiveSection } from "./terminal-live";

export function WorkWallPage() {
  const wsId = useWorkspaceId();
  const workspaceSlug = useWorkspaceSlug();
  const queryClient = useQueryClient();
  const [streamState, setStreamState] = useState<WorkWallStreamState>("connecting");
  const { data = [] } = useQuery({
    queryKey: ["work-wall", wsId, "snapshot"],
    queryFn: () => api.workWallSnapshot(),
    // Keep one workspace-wide poll as initial load and fallback. A healthy SSE
    // stream disables polling; EventSource owns reconnect/backoff.
    refetchInterval: streamState === "open" ? false : 5000,
  });
  const { data: panes = [] } = useQuery({
    queryKey: ["work-wall", wsId, "terminal-presence"],
    queryFn: () => api.listTerminalPresence(),
    refetchInterval: 10000,
  });

  useEffect(() => {
    if (!workspaceSlug) {
      setStreamState("error");
      return;
    }
    return subscribeWorkWallStream(workspaceSlug, {
        onSnapshot: (snapshot) => {
          queryClient.setQueryData<EmployeeLiveActivityV1[]>(
            ["work-wall", wsId, "snapshot"],
            snapshot,
          );
        },
        onStateChange: setStreamState,
        // A malformed frame or transport error restores the 5s snapshot poll.
        // The EventSource remains responsible for reconnecting.
        onError: () => setStreamState("error"),
      });
  }, [queryClient, workspaceSlug, wsId]);

  return (
    <>
      <WorkWall employees={data} />
      <TerminalLiveSection panes={panes} />
    </>
  );
}

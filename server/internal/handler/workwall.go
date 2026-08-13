package handler

import (
	"net/http"

	"github.com/multica-ai/multica/server/internal/liveactivity"
	"github.com/multica-ai/multica/server/internal/workwall"
)

// GetWorkWallSnapshot returns the workspace "工作现场" (work wall) snapshot:
// one sanitized EmployeeLiveActivityV1 per accessible agent, derived read-only
// from agent / agent_runtime / agent_task_queue.
//
// Route wiring (one line in server/cmd/server/router.go) is intentionally left
// to the mainline integrator: router.go is a shared file across W1/W2/W3 lanes.
func (h *Handler) GetWorkWallSnapshot(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}

	svc := workwall.NewService(h.Queries)
	snapshot, err := svc.Snapshot(r.Context(), parseUUID(workspaceID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to assemble work wall snapshot")
		return
	}

	// Same access discipline as the Agents list: only agents the caller may see.
	actorType, actorID := h.resolveActor(r, requestUserID(r), workspaceID)
	allowed, ok := h.accessibleAgentIDs(r.Context(), workspaceID, actorType, actorID, member.Role)
	if !ok {
		writeError(w, http.StatusInternalServerError, "failed to resolve agent access")
		return
	}

	resp := make([]liveactivity.EmployeeLiveActivityV1, 0, len(snapshot))
	for _, d := range snapshot {
		if _, visible := allowed[d.AgentID]; !visible {
			continue
		}
		resp = append(resp, d)
	}

	writeJSON(w, http.StatusOK, resp)
}

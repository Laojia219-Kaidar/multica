package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/multica-ai/multica/server/internal/workwall"
)

// workWallStreamInterval is the server-side poll cadence for the workspace-wide
// SSE stream. One stream per workspace (NOT per employee).
const workWallStreamInterval = 5 * time.Second

// GetWorkWallStream is the workspace-level SSE stream for the "工作现场"
// (work wall). It emits a snapshot event every interval. Client reconnect is
// inherent (each connection starts a fresh loop); Last-Event-ID + delta
// compensation are left as a future enhancement over this v1 baseline.
//
// Route wiring (one line in router.go) is left to the mainline integrator.
func (h *Handler) GetWorkWallStream(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}
	_ = member

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	svc := workwall.NewService(h.Queries)
	ticker := time.NewTicker(workWallStreamInterval)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			snapshot, err := svc.Snapshot(r.Context(), parseUUID(workspaceID))
			if err != nil {
				fmt.Fprintf(w, "event: error\ndata: %q\n\n", "failed to assemble snapshot")
				flusher.Flush()
				continue
			}
			// Access filter (same discipline as the snapshot endpoint).
			actorType, actorID := h.resolveActor(r, requestUserID(r), workspaceID)
			allowed, ok := h.accessibleAgentIDs(r.Context(), workspaceID, actorType, actorID, member.Role)
			if !ok {
				fmt.Fprintf(w, "event: error\ndata: %q\n\n", "failed to resolve agent access")
				flusher.Flush()
				continue
			}
			filtered := make([]json.RawMessage, 0, len(snapshot))
			for _, d := range snapshot {
				if _, visible := allowed[d.AgentID]; !visible {
					continue
				}
				b, err := json.Marshal(d)
				if err != nil {
					continue
				}
				filtered = append(filtered, b)
			}
			payload, err := json.Marshal(filtered)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", payload)
			flusher.Flush()
		}
	}
}

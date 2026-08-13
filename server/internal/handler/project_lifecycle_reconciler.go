package handler

import (
	"net/http"

	"github.com/multica-ai/multica/server/internal/service"
)

// ListProjectLifecycleReconciler returns the read-only self-operation diagnosis
// (the four VC-12 broken-chain detectors). It never writes.
//
//	GET /api/projects/reconciler
func (h *Handler) ListProjectLifecycleReconciler(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	reconciler := service.NewProjectLifecycleReconciler(h.Queries)
	findings, err := reconciler.Diagnose(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to run lifecycle reconciler")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"findings": findings, "total": len(findings)})
}

package handler

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/service"
)

// ListProjectLifecycle returns the derived project-health portfolio for the
// current workspace. It is a READ-ONLY projection over the existing
// project/issue/task truth and never mutates project.status.
//
//	GET /api/projects/lifecycle
func (h *Handler) ListProjectLifecycle(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}

	projector := service.NewProjectLifecycleProjector(h.Queries)
	snaps, err := projector.ListPortfolio(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to build project lifecycle portfolio")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"projects": snaps, "total": len(snaps)})
}

// GetProjectLifecycle returns the derived lifecycle snapshot for one project.
//
//	GET /api/projects/{id}/lifecycle
func (h *Handler) GetProjectLifecycle(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	workspaceID := h.resolveWorkspaceID(r)
	idUUID, ok := parseUUIDOrBadRequest(w, id, "project id")
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}

	projector := service.NewProjectLifecycleProjector(h.Queries)
	snap, err := projector.GetSnapshot(r.Context(), wsUUID, idUUID)
	if err != nil {
		if errors.Is(err, service.ErrProjectLifecycleNotFound) {
			writeError(w, http.StatusNotFound, "project lifecycle snapshot not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to build project lifecycle snapshot")
		return
	}

	writeJSON(w, http.StatusOK, snap)
}

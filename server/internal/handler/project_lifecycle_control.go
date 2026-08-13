package handler

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// projectLifecycleActionRequest is the body for the Slice 2 control endpoints.
// preview=true returns the planned effect with zero writes.
type projectLifecycleActionRequest struct {
	Preview        bool   `json:"preview"`
	IdempotencyKey string `json:"idempotency_key"`
}

// ProjectLifecycleAction handles continue / pause_dispatch / resume for a
// project (owner/admin only, preview-first).
//
//	POST /api/projects/{id}/lifecycle/actions/{action}
func (h *Handler) ProjectLifecycleAction(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	action := chi.URLParam(r, "action")
	workspaceID := h.resolveWorkspaceID(r)

	idUUID, ok := parseUUIDOrBadRequest(w, id, "project id")
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	project, err := h.Queries.GetProjectInWorkspace(r.Context(), db.GetProjectInWorkspaceParams{ID: idUUID, WorkspaceID: wsUUID})
	if err != nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	// owner/admin RBAC.
	if _, ok := h.requireWorkspaceRole(w, r, util.UUIDToString(project.WorkspaceID), "project not found", "owner", "admin"); !ok {
		return
	}

	var req projectLifecycleActionRequest
	if r.Body != nil {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeError(w, http.StatusBadRequest, "failed to read request body")
			return
		}
		if len(body) > 0 {
			if err := json.Unmarshal(body, &req); err != nil {
				writeError(w, http.StatusBadRequest, "invalid request body")
				return
			}
		}
	}

	ctrl := service.NewProjectLifecycleControlService(h.Queries, h.TaskService)

	switch service.ControlAction(action) {
	case service.ActionContinue:
		if req.Preview {
			p, err := ctrl.PreviewContinue(r.Context(), wsUUID, idUUID)
			if err != nil {
				writeError(w, http.StatusNotFound, "project not found")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"preview": p})
			return
		}
		receipt, err := ctrl.Continue(r.Context(), wsUUID, idUUID, req.IdempotencyKey)
		if err != nil {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		writeJSON(w, http.StatusOK, receipt)

	case service.ActionPauseDispatch:
		if req.Preview {
			receipt, err := ctrl.PreviewPause(r.Context(), wsUUID, idUUID)
			if err != nil {
				writeError(w, http.StatusNotFound, "project not found")
				return
			}
			writeJSON(w, http.StatusOK, receipt)
			return
		}
		receipt, err := ctrl.PauseDispatch(r.Context(), wsUUID, idUUID, req.IdempotencyKey)
		if err != nil {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		writeJSON(w, http.StatusOK, receipt)

	case service.ActionResume:
		if req.Preview {
			receipt, err := ctrl.PreviewResume(r.Context(), wsUUID, idUUID)
			if err != nil {
				writeError(w, http.StatusNotFound, "project not found")
				return
			}
			writeJSON(w, http.StatusOK, receipt)
			return
		}
		receipt, err := ctrl.Resume(r.Context(), wsUUID, idUUID, req.IdempotencyKey)
		if err != nil {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		writeJSON(w, http.StatusOK, receipt)

	case service.ActionClose:
		if req.Preview {
			pkg, err := ctrl.PreviewClose(r.Context(), wsUUID, idUUID)
			if err != nil {
				writeError(w, http.StatusNotFound, "project not found")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"preview": pkg})
			return
		}
		receipt, err := ctrl.Close(r.Context(), wsUUID, idUUID, req.IdempotencyKey)
		if err != nil {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		writeJSON(w, http.StatusOK, receipt)

	default:
		writeError(w, http.StatusBadRequest, "unknown action; valid values: continue, pause_dispatch, resume, close")
	}
}

// ProjectClosurePackage generates a candidate project closure package (read-only,
// never auto-closes).
//
//	POST /api/projects/{id}/closure-package
func (h *Handler) ProjectClosurePackage(w http.ResponseWriter, r *http.Request) {
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
	project, err := h.Queries.GetProjectInWorkspace(r.Context(), db.GetProjectInWorkspaceParams{ID: idUUID, WorkspaceID: wsUUID})
	if err != nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if _, ok := h.requireWorkspaceRole(w, r, util.UUIDToString(project.WorkspaceID), "project not found", "owner", "admin"); !ok {
		return
	}
	var req projectLifecycleActionRequest
	if r.Body != nil {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeError(w, http.StatusBadRequest, "failed to read request body")
			return
		}
		if len(body) > 0 {
			if err := json.Unmarshal(body, &req); err != nil {
				writeError(w, http.StatusBadRequest, "invalid request body")
				return
			}
		}
	}
	ctrl := service.NewProjectLifecycleControlService(h.Queries, h.TaskService)
	pkg, err := ctrl.GenerateClosurePackage(r.Context(), wsUUID, idUUID, req.IdempotencyKey)
	if err != nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	writeJSON(w, http.StatusOK, pkg)
}

package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// project_autostart.go — bounded Project start/continue control (HIV-405).
//
// Two endpoints under /api/projects/{id}:
//   POST /start-preview — read-only dependency-ready wave preview
//   POST /start         — idempotent batch dispatch over the ready wave
//
// Both reuse the existing OwnerDispatchService for per-issue dispatch —
// no second scheduler, no auto-deploy. Owner/admin only.

// ProjectStartPreviewRequest is the (empty) body of POST /start-preview.
type ProjectStartPreviewRequest struct{}

// ProjectStartRequest is the body of POST /start.
type ProjectStartRequest struct {
	IdempotencyKey string   `json:"idempotency_key"`
	IssueIDs       []string `json:"issue_ids,omitempty"`
}

// ProjectStartPreview runs the read-only dependency-ready wave preview.
func (h *Handler) ProjectStartPreview(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")

	projectID := chi.URLParam(r, "id")
	workspaceID := h.resolveWorkspaceID(r)
	projectUUID, ok := parseUUIDOrBadRequest(w, projectID, "project id")
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}

	_, ok = requireUserID(w, r)
	if !ok {
		return
	}
	if _, roleOK := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin"); !roleOK {
		return
	}

	if h.ProjectAutoStartService == nil {
		writeError(w, http.StatusServiceUnavailable, "project start service unavailable")
		return
	}

	// Reject unknown fields.
	var req ProjectStartPreviewRequest
	if r.ContentLength != 0 {
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}
		if dec.More() {
			writeError(w, http.StatusBadRequest, "trailing content after JSON object")
			return
		}
	}

	// Verify project exists in workspace.
	_, err := h.Queries.GetProjectInWorkspace(r.Context(), db.GetProjectInWorkspaceParams{
		ID: projectUUID, WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	result, err := h.ProjectAutoStartService.Preview(r.Context(), wsUUID, projectUUID)
	if err != nil {
		slog.Warn("project start-preview failed", "project_id", projectID, "error", err)
		writeError(w, http.StatusInternalServerError, "start preview failed")
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// ProjectStart runs the idempotent batch dispatch over the ready wave.
func (h *Handler) ProjectStart(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")

	projectID := chi.URLParam(r, "id")
	workspaceID := h.resolveWorkspaceID(r)
	projectUUID, ok := parseUUIDOrBadRequest(w, projectID, "project id")
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}

	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	if _, roleOK := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin"); !roleOK {
		return
	}

	if h.ProjectAutoStartService == nil {
		writeError(w, http.StatusServiceUnavailable, "project start service unavailable")
		return
	}

	// Read the full body for digest computation.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	var req ProjectStartRequest
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if dec.More() {
		writeError(w, http.StatusBadRequest, "trailing content after JSON object")
		return
	}

	// Idempotency key is required.
	idemKey := req.IdempotencyKey
	if idemKey == "" {
		idemKey = r.Header.Get("Idempotency-Key")
	}
	if idemKey == "" {
		writeError(w, http.StatusBadRequest, "idempotency_key is required")
		return
	}
	if len(idemKey) > 256 {
		writeError(w, http.StatusBadRequest, "idempotency_key too long (max 256)")
		return
	}

	// Verify project exists in workspace.
	_, err = h.Queries.GetProjectInWorkspace(r.Context(), db.GetProjectInWorkspaceParams{
		ID: projectUUID, WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	// Parse selected issue IDs.
	var selectedIDs []pgtype.UUID
	for _, idStr := range req.IssueIDs {
		u, parseErr := util.ParseUUID(idStr)
		if parseErr != nil {
			writeError(w, http.StatusBadRequest, "invalid issue_id: "+idStr)
			return
		}
		selectedIDs = append(selectedIDs, u)
	}

	digest := service.ComputeDigest(body)
	userUUID, _ := util.ParseUUID(userID)

	result, err := h.ProjectAutoStartService.Start(r.Context(), service.ProjectAutoStartParams{
		WorkspaceID:    wsUUID,
		ProjectID:      projectUUID,
		IdempotencyKey: idemKey,
		RequestDigest:  digest,
		ActorUserID:    userUUID,
		SelectedIssueIDs: selectedIDs,
	})
	if err != nil {
		slog.Warn("project start failed", "project_id", projectID, "error", err)
		writeError(w, http.StatusInternalServerError, "project start failed")
		return
	}

	writeJSON(w, http.StatusAccepted, result)
}

package handler

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
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

// DispatchProjectLifecycleReconcileAction turns one reconciler finding into a
// traceable Issue (backlog) so the management team can act on it from the page
// — the "handle" half of VC-12 self-operation.
//
//	POST /api/projects/reconciler/dispatch
func (h *Handler) DispatchProjectLifecycleReconcileAction(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	if _, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin"); !ok {
		return
	}
	var req struct {
		ProjectID  string `json:"project_id"`
		Kind       string `json:"kind"`
		Summary    string `json:"summary"`
		NextAction string `json:"next_action"`
	}
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
	if h.IssueService == nil || req.ProjectID == "" || req.Kind == "" {
		writeError(w, http.StatusBadRequest, "project_id and kind are required")
		return
	}
	projectID := util.MustParseUUID(req.ProjectID)
	title := "[自愈] " + req.Kind + " · " + req.Summary
	if len(title) > 200 {
		title = title[:200]
	}
	res, err := h.IssueService.Create(r.Context(), service.IssueCreateParams{
		WorkspaceID:    wsUUID,
		Title:          title,
		Description:    pgtypeText(req.NextAction),
		Status:         "backlog",
		Priority:       "medium",
		CreatorType:    "member",
		CreatorID:      util.MustParseUUID(userID),
		ProjectID:      projectID,
		AllowDuplicate: true,
	}, service.IssueCreateOpts{})
	if err != nil {
		slog.Error("reconcile dispatch failed", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create reconcile action")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"issue_id": util.UUIDToString(res.Issue.ID)})
}

func pgtypeText(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }

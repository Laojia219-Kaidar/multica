package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// OwnerDispatchService is wired by router.go. When nil the two endpoints
// return 503 so a misconfigured server fails closed instead of panicking.

// DispatchPreviewRequest is the (empty) body of POST /dispatch-preview.
// All context comes from the URL (issue id) and the workspace header.
// The struct exists so a future body extension stays backward-compatible
// and unknown-field rejection has a typed target.
type DispatchPreviewRequest struct{}

// DispatchRequest is the body of POST /dispatch.
type DispatchRequest struct {
	IdempotencyKey    string `json:"idempotency_key"`
	ExpectedStatus    string `json:"expected_status"`
	ExpectedUpdatedAt string `json:"expected_updated_at"`
}

// DispatchPreview runs the read-only dispatch preview. Owner/admin only.
func (h *Handler) DispatchPreview(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, id)
	if !ok {
		return
	}

	workspaceID := h.resolveWorkspaceID(r)
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	if _, roleOK := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin"); !roleOK {
		return
	}

	if h.OwnerDispatchService == nil {
		writeError(w, http.StatusServiceUnavailable, "dispatch service unavailable")
		return
	}

	// Reject unknown fields.
	var req DispatchPreviewRequest
	if r.ContentLength != 0 {
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}
		// Reject trailing JSON tokens.
		if dec.More() {
			writeError(w, http.StatusBadRequest, "trailing content after JSON object")
			return
		}
	}

	// Load active tasks.
	activeTasks, _ := h.Queries.ListActiveTasksByIssue(r.Context(), issue.ID)

	// Build the permission probe.
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	originatorUserID := h.invokeOriginatorFromRequest(r, actorType, actorID)
	canInvoke := func(agent db.Agent) bool {
		return h.canInvokeAgent(r.Context(), agent, actorType, actorID, originatorUserID, workspaceID)
	}

	result, err := h.OwnerDispatchService.Preview(r.Context(), service.DispatchPreviewParams{
		Issue:          issue,
		ActiveTasks:    activeTasks,
		CanInvokeAgent: canInvoke,
	})
	if err != nil {
		slog.Warn("dispatch-preview failed", "issue_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "dispatch preview failed")
		return
	}

	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, result)
}

// Dispatch runs the idempotent dispatch write. Owner/admin only.
func (h *Handler) Dispatch(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, id)
	if !ok {
		return
	}

	workspaceID := h.resolveWorkspaceID(r)
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	if _, roleOK := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin"); !roleOK {
		return
	}

	if h.OwnerDispatchService == nil {
		writeError(w, http.StatusServiceUnavailable, "dispatch service unavailable")
		return
	}

	// Read the full body for digest computation, then decode with strict mode.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	var req DispatchRequest
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

	// Idempotency-Key is required.
	idemKey := req.IdempotencyKey
	if idemKey == "" {
		// Also accept the standard HTTP header as fallback.
		idemKey = r.Header.Get("Idempotency-Key")
	}
	if idemKey == "" {
		writeError(w, http.StatusBadRequest, "idempotency_key is required")
		return
	}
	// Cap key length to prevent abuse.
	if len(idemKey) > 256 {
		writeError(w, http.StatusBadRequest, "idempotency_key too long (max 256)")
		return
	}

	digest := service.ComputeDigest(body)

	// Load active tasks.
	activeTasks, _ := h.Queries.ListActiveTasksByIssue(r.Context(), issue.ID)

	userUUID, _ := util.ParseUUID(userID)
	wsUUID, _ := util.ParseUUID(workspaceID)

	result, err := h.OwnerDispatchService.Dispatch(r.Context(), service.DispatchParams{
		Issue:             issue,
		WorkspaceID:       wsUUID,
		IdempotencyKey:    idemKey,
		RequestDigest:     digest,
		ExpectedStatus:    req.ExpectedStatus,
		ExpectedUpdatedAt: req.ExpectedUpdatedAt,
		ActiveTasks:       activeTasks,
		ActorUserID:       userUUID,
	})
	if err != nil {
		if errors.Is(err, service.ErrIdempotencyConflict) {
			w.Header().Set("Cache-Control", "private, no-store")
			writeJSON(w, http.StatusConflict, result)
			return
		}
		if errors.Is(err, service.ErrExpectedStateMismatch) {
			w.Header().Set("Cache-Control", "private, no-store")
			writeJSON(w, http.StatusPreconditionFailed, result)
			return
		}
		slog.Warn("dispatch failed", "issue_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "dispatch failed")
		return
	}

	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusAccepted, result)
}

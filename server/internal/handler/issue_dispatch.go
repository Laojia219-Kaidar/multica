package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

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
	// HandoffNote is a compatibility field used by the pre-Gate frontend. It
	// maps to the canonical task handoff note; no client-selected agent or
	// workspace is accepted from this legacy shape.
	HandoffNote string `json:"handoff_note"`
}

func rejectTrailingJSON(dec *json.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("trailing content after JSON object")
		}
		return fmt.Errorf("trailing content after JSON object: %w", err)
	}
	return nil
}

func legacyDispatchPreview(issue db.Issue, result *service.PreviewResult) IssueDispatchPreview {
	targetAgentID := ""
	assigneeType := issue.AssigneeType.String
	if result.Assignee != nil {
		targetAgentID = result.Assignee.ID
		assigneeType = result.Assignee.Type
	}
	dispatchable := result.Decision == service.DecisionWouldEnqueue || result.Decision == service.DecisionAlreadyActive
	return IssueDispatchPreview{
		Dispatchable:     dispatchable,
		AlreadyPending:   result.Decision == service.DecisionAlreadyActive,
		TargetAgentID:    targetAgentID,
		AssigneeType:     assigneeType,
		Reason:           string(result.Reason),
		HandoffSupported: dispatchable,
	}
}

func legacyDispatchReceipt(issue db.Issue, workspaceID, actorType, actorID, idemKey string, result *service.DispatchResult) IssueDispatchReceipt {
	receipt := IssueDispatchReceipt{
		Operation:      "dispatch",
		IssueID:        util.UUIDToString(issue.ID),
		WorkspaceID:    workspaceID,
		AlreadyPending: result.Decision == service.DecisionAlreadyActive || result.Replayed,
		TargetAgentID:  result.TargetAgentID,
		AssigneeType:   result.AssigneeType,
		IdempotencyKey: idemKey,
		PerformedAt:    time.Now().UTC().Format(time.RFC3339Nano),
		ActorType:      actorType,
		ActorID:        actorID,
	}
	if len(result.TaskIDs) > 0 {
		taskID := result.TaskIDs[0]
		receipt.TaskID = &taskID
	}
	return receipt
}

func dispatchPreviewResponse(issue db.Issue, result *service.PreviewResult) map[string]any {
	response := map[string]any{
		"decision":         result.Decision,
		"reason":           result.Reason,
		"issue_status":     result.IssueStatus,
		"issue_updated_at": result.IssueUpdatedAt,
		"active_tasks":     result.ActiveTasks,
		// Compatibility wrapper for the pre-Gate frontend image.
		"issue_id": util.UUIDToString(issue.ID),
		"preview":  legacyDispatchPreview(issue, result),
	}
	if result.Assignee != nil {
		response["assignee"] = result.Assignee
	}
	return response
}

func dispatchResultResponse(issue db.Issue, workspaceID, actorType, actorID, idemKey string, result *service.DispatchResult) map[string]any {
	response := map[string]any{
		"decision": result.Decision,
		"reason":   result.Reason,
		"replayed": result.Replayed,
		// Compatibility wrapper for the pre-Gate frontend image.
		"receipt": legacyDispatchReceipt(issue, workspaceID, actorType, actorID, idemKey, result),
	}
	if result.TaskIDs != nil {
		response["task_ids"] = result.TaskIDs
	}
	return response
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
		if err := rejectTrailingJSON(dec); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	// Load active tasks.
	activeTasks, err := h.Queries.ListActiveTasksByIssue(r.Context(), issue.ID)
	if err != nil {
		slog.Warn("dispatch-preview active task lookup failed", "issue_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "dispatch preview unavailable")
		return
	}

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
	writeJSON(w, http.StatusOK, dispatchPreviewResponse(issue, result))
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
	if err := rejectTrailingJSON(dec); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
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

	digest := service.ComputeIssueDispatchDigest(issue.ID, body)

	// Load active tasks.
	activeTasks, err := h.Queries.ListActiveTasksByIssue(r.Context(), issue.ID)
	if err != nil {
		slog.Warn("dispatch active task lookup failed", "issue_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "dispatch unavailable")
		return
	}

	userUUID, _ := util.ParseUUID(userID)
	wsUUID, _ := util.ParseUUID(workspaceID)
	actorType, actorID := h.resolveActor(r, userID, workspaceID)

	result, err := h.OwnerDispatchService.Dispatch(r.Context(), service.DispatchParams{
		Issue:             issue,
		WorkspaceID:       wsUUID,
		IdempotencyKey:    idemKey,
		RequestDigest:     digest,
		ExpectedStatus:    req.ExpectedStatus,
		ExpectedUpdatedAt: req.ExpectedUpdatedAt,
		ActiveTasks:       activeTasks,
		ActorUserID:       userUUID,
		HandoffNote:       req.HandoffNote,
	})
	if err != nil {
		if errors.Is(err, service.ErrIdempotencyConflict) {
			w.Header().Set("Cache-Control", "private, no-store")
			writeJSON(w, http.StatusConflict, dispatchResultResponse(issue, workspaceID, actorType, actorID, idemKey, result))
			return
		}
		if errors.Is(err, service.ErrExpectedStateMismatch) {
			w.Header().Set("Cache-Control", "private, no-store")
			writeJSON(w, http.StatusPreconditionFailed, dispatchResultResponse(issue, workspaceID, actorType, actorID, idemKey, result))
			return
		}
		slog.Warn("dispatch failed", "issue_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "dispatch failed")
		return
	}

	w.Header().Set("Cache-Control", "private, no-store")
	status := http.StatusConflict
	switch result.Decision {
	case service.DecisionWouldEnqueue:
		status = http.StatusAccepted
		if result.Replayed {
			status = http.StatusOK
		}
	case service.DecisionAlreadyActive:
		status = http.StatusOK
	case service.DecisionBlocked, service.DecisionNeedsAssignment:
		status = http.StatusConflict
	}
	writeJSON(w, status, dispatchResultResponse(issue, workspaceID, actorType, actorID, idemKey, result))
}

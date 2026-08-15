package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/multica-ai/multica/server/internal/continuousdispatch"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
)

const (
	continuousDispatchCommandSchema = "hivecrew.continuous-dispatch-command/v1"
	maxContinuousDispatchBodySize   = 16 << 10
	maxContinuousDispatchHandoff    = 8 << 10
)

type continuousDispatchCommandRequest struct {
	HandoffNote string `json:"handoff_note"`
}

type continuousDispatchCommandResponse struct {
	SchemaVersion string                   `json:"schema_version"`
	State         continuousdispatch.State `json:"state"`
	TaskID        string                   `json:"task_id"`
	Identity      map[string]string        `json:"dispatch_identity"`
	Route         map[string]string        `json:"route"`
}

// DispatchProjectNextAction is an explicit Owner/Admin control command. The
// request never accepts employee, Agent, Runtime, model, account, stage,
// revision or generation fields; all routing truth is recomputed server-side.
func (h *Handler) DispatchProjectNextAction(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	for key, items := range r.URL.Query() {
		if key != "workspace_id" || len(items) != 1 {
			writeContinuousDispatchShadowError(w, http.StatusBadRequest, "invalid_request", "query parameters must be known and singular")
			return
		}
	}
	if h.ContinuousDispatchTrigger == nil {
		writeContinuousDispatchShadowError(w, http.StatusServiceUnavailable, "writer_unavailable", "continuous dispatch writer is unavailable")
		return
	}
	workspaceID := h.resolveWorkspaceID(r)
	member, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin")
	if !ok {
		return
	}
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	projectUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "project id")
	if !ok {
		return
	}
	issueUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "issueId"), "issue id")
	if !ok {
		return
	}

	request, err := decodeContinuousDispatchCommand(r)
	if err != nil {
		writeContinuousDispatchShadowError(w, http.StatusBadRequest, "invalid_request", "continuous dispatch request is invalid")
		return
	}
	result, err := h.ContinuousDispatchTrigger.DispatchIssue(
		r.Context(), workspaceUUID, projectUUID, issueUUID, member.UserID, request.HandoffNote,
	)
	if err != nil {
		writeContinuousDispatchCommandError(w, err)
		return
	}
	selected := result.Action.Selected
	if selected == nil {
		writeContinuousDispatchShadowError(w, http.StatusServiceUnavailable, "source_gap", "continuous dispatch result is incomplete")
		return
	}
	receipt := result.Receipt
	writeJSON(w, http.StatusOK, continuousDispatchCommandResponse{
		SchemaVersion: continuousDispatchCommandSchema,
		State:         result.Action.State,
		TaskID:        util.UUIDToString(receipt.TaskID),
		Identity: map[string]string{
			"workspace_id": receipt.Identity.WorkspaceID, "issue_id": receipt.Identity.IssueID,
			"stage": receipt.Identity.Stage, "candidate_revision": receipt.Identity.CandidateRevision,
			"generation": receipt.Identity.Generation,
		},
		Route: map[string]string{
			"employee_ref": receipt.EmployeeRef, "agent_id": util.UUIDToString(receipt.LocalAgentID),
			"runtime_id": util.UUIDToString(receipt.RuntimeID), "model": receipt.Model, "account_ref": receipt.AccountRef,
		},
	})
}

func decodeContinuousDispatchCommand(r *http.Request) (continuousDispatchCommandRequest, error) {
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxContinuousDispatchBodySize+1))
	decoder.DisallowUnknownFields()
	var request continuousDispatchCommandRequest
	if err := decoder.Decode(&request); err != nil {
		return continuousDispatchCommandRequest{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return continuousDispatchCommandRequest{}, errors.New("multiple JSON values")
		}
		return continuousDispatchCommandRequest{}, err
	}
	if len([]byte(request.HandoffNote)) > maxContinuousDispatchHandoff || strings.ContainsRune(request.HandoffNote, '\x00') {
		return continuousDispatchCommandRequest{}, errors.New("handoff_note is invalid")
	}
	return request, nil
}

func writeContinuousDispatchCommandError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrContinuousDispatchIssueAbsent):
		writeContinuousDispatchShadowError(w, http.StatusNotFound, "not_found", "issue was not found in the project")
	case errors.Is(err, service.ErrContinuousDispatchNotReady),
		errors.Is(err, service.ErrContinuousDispatchIssueNotReady):
		writeContinuousDispatchShadowError(w, http.StatusConflict, "not_ready", "issue has no executable next action")
	case errors.Is(err, service.ErrContinuousDispatchConflict),
		errors.Is(err, service.ErrContinuousDispatchReceiptConflict),
		errors.Is(err, service.ErrContinuousDispatchIssueDrift),
		errors.Is(err, service.ErrContinuousDispatchRouteDrift):
		writeContinuousDispatchShadowError(w, http.StatusConflict, "dispatch_conflict", "continuous dispatch truth changed; recompute and retry")
	case errors.Is(err, service.ErrContinuousDispatchProjectAbsent):
		writeContinuousDispatchShadowError(w, http.StatusNotFound, "not_found", "project was not found")
	case errors.Is(err, service.ErrContinuousDispatchSourceGap):
		writeContinuousDispatchShadowError(w, http.StatusServiceUnavailable, "source_gap", "continuous dispatch source is temporarily unavailable")
	default:
		writeContinuousDispatchShadowError(w, http.StatusServiceUnavailable, "dispatch_unavailable", "continuous dispatch is temporarily unavailable")
	}
}

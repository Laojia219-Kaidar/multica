package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/multica-ai/multica/server/internal/service"
)

const (
	maxWorkConservingDrainBodySize = 1024
	maxWorkConservingDrainBatch    = 20
)

type workConservingDrainRequest struct {
	BatchSize *int `json:"batch_size,omitempty"`
}

// DrainProjectNextActions is an explicit Owner/Admin command over the current
// server-validated work-conserving projection. The browser controls only the
// bounded batch size; workspace, project, actor and every route selector are
// derived or recomputed server-side.
func (h *Handler) DrainProjectNextActions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	for key, values := range r.URL.Query() {
		if key != "workspace_id" || len(values) != 1 {
			writeContinuousDispatchShadowError(w, http.StatusBadRequest, "invalid_request", "query parameters must be known and singular")
			return
		}
	}

	request, err := decodeWorkConservingDrainRequest(r)
	if err != nil {
		writeContinuousDispatchShadowError(w, http.StatusBadRequest, "invalid_request", "work-conserving drain request is invalid")
		return
	}
	batchSize := 1
	if request.BatchSize != nil {
		batchSize = *request.BatchSize
	}
	if batchSize < 1 || batchSize > maxWorkConservingDrainBatch {
		writeContinuousDispatchShadowError(w, http.StatusBadRequest, "invalid_request", "batch_size must be between 1 and 20")
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

	if h.WorkConservingDrain == nil {
		writeContinuousDispatchShadowError(w, http.StatusServiceUnavailable, "source_gap", "work-conserving drain is unavailable")
		return
	}
	result, err := h.WorkConservingDrain.Drain(r.Context(), service.WorkConservingDrainRequest{
		WorkspaceID: workspaceUUID,
		ProjectID:   projectUUID,
		ActorUserID: member.UserID,
		BatchSize:   batchSize,
	})
	if result.State == service.WorkConservingDrainStateSourceGap {
		writeJSON(w, http.StatusServiceUnavailable, sanitizeWorkConservingDrainResult(result))
		return
	}
	if err != nil {
		writeContinuousDispatchShadowError(w, http.StatusServiceUnavailable, "source_gap", "work-conserving drain source is temporarily unavailable")
		return
	}

	writeJSON(w, http.StatusOK, sanitizeWorkConservingDrainResult(result))
}

func decodeWorkConservingDrainRequest(r *http.Request) (workConservingDrainRequest, error) {
	if r == nil || r.Body == nil {
		return workConservingDrainRequest{}, errors.New("request body is required")
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWorkConservingDrainBodySize+1))
	if err != nil {
		return workConservingDrainRequest{}, err
	}
	if len(body) == 0 || len(body) > maxWorkConservingDrainBodySize {
		return workConservingDrainRequest{}, errors.New("request body size is invalid")
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var request workConservingDrainRequest
	if err := decoder.Decode(&request); err != nil {
		return workConservingDrainRequest{}, err
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) < 2 || trimmed[0] != '{' {
		return workConservingDrainRequest{}, errors.New("request body must be a JSON object")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &fields); err != nil {
		return workConservingDrainRequest{}, err
	}
	if raw, present := fields["batch_size"]; present && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return workConservingDrainRequest{}, errors.New("batch_size must be an integer")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return workConservingDrainRequest{}, errors.New("multiple JSON values")
		}
		return workConservingDrainRequest{}, err
	}
	return request, nil
}

// sanitizeWorkConservingDrainResult preserves every per-Issue outcome and
// receipt while replacing service error strings with stable public reasons.
// Structured blocked-backlog reason codes are already public and remain
// visible to the Owner.
func sanitizeWorkConservingDrainResult(result service.WorkConservingDrainResult) service.WorkConservingDrainResult {
	result.Results = append([]service.WorkConservingDrainIssueResult(nil), result.Results...)
	for i := range result.Results {
		row := &result.Results[i]
		switch row.Outcome {
		case service.WorkConservingDrainAlreadyTerminal:
			row.Reason = "issue is already terminal"
		case service.WorkConservingDrainBlocked:
			if !row.NotAttempted {
				row.Reason = "issue has no executable next action"
			}
		case service.WorkConservingDrainConflict:
			row.Reason = "continuous dispatch truth changed"
		case service.WorkConservingDrainSourceGap:
			row.Reason = "continuous dispatch source is unavailable"
		}
	}
	return result
}

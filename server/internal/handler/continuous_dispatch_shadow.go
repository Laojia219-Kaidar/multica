package handler

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/multica-ai/multica/server/internal/service"
)

var allowedContinuousDispatchShadowQueryParams = map[string]bool{
	"workspace_id": true,
	"limit":        true,
	"offset":       true,
	"projection":   true,
}

func (h *Handler) GetProjectNextActions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	values := r.URL.Query()
	projectionRequested := false
	for key, items := range values {
		if !allowedContinuousDispatchShadowQueryParams[key] || len(items) != 1 {
			writeContinuousDispatchShadowError(w, http.StatusBadRequest, "invalid_request", "query parameters must be known and singular")
			return
		}
		if key == "projection" {
			if items[0] != "work_conserving" {
				writeContinuousDispatchShadowError(w, http.StatusBadRequest, "invalid_request", "projection must be work_conserving")
				return
			}
			projectionRequested = true
		}
	}
	limit := 50
	if items, ok := values["limit"]; ok {
		parsed, valid := parseCanonicalDecimal(items[0], false)
		if !valid || parsed > 200 {
			writeContinuousDispatchShadowError(w, http.StatusBadRequest, "invalid_request", "limit must be a canonical integer from 1 to 200")
			return
		}
		limit = parsed
	}
	offset := 0
	if items, ok := values["offset"]; ok {
		parsed, valid := parseCanonicalDecimal(items[0], true)
		if !valid {
			writeContinuousDispatchShadowError(w, http.StatusBadRequest, "invalid_request", "offset must be a canonical non-negative integer")
			return
		}
		offset = parsed
	}
	if h.ContinuousDispatchShadow == nil {
		writeContinuousDispatchShadowError(w, http.StatusServiceUnavailable, "source_gap", "continuous dispatch shadow service is unavailable")
		return
	}
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	projectUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "project id")
	if !ok {
		return
	}
	result, err := h.ContinuousDispatchShadow.InspectProject(r.Context(), workspaceUUID, projectUUID, limit, offset)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrContinuousDispatchProjectAbsent):
			writeContinuousDispatchShadowError(w, http.StatusNotFound, "not_found", "project was not found")
		case errors.Is(err, service.ErrContinuousDispatchSourceGap):
			writeContinuousDispatchShadowError(w, http.StatusServiceUnavailable, "source_gap", "continuous dispatch source is temporarily unavailable")
		default:
			writeContinuousDispatchShadowError(w, http.StatusServiceUnavailable, "source_gap", "continuous dispatch source is temporarily unavailable")
		}
		return
	}
	if projectionRequested {
		projection := service.NewWorkConservingSourceGapProjection(limit, offset)
		if h.WorkConservingProjection != nil {
			candidate, providerErr := h.WorkConservingProjection.ProjectWorkConserving(r.Context(), service.WorkConservingProjectionRequest{
				WorkspaceID: workspaceUUID,
				ProjectID:   projectUUID,
				Limit:       limit,
				Offset:      offset,
			})
			if providerErr == nil && service.ValidateWorkConservingProjectionAt(candidate, service.WorkConservingProjectionRequest{
				WorkspaceID: workspaceUUID,
				ProjectID:   projectUUID,
				Limit:       limit,
				Offset:      offset,
			}, time.Now().UTC()) == nil {
				// Read-only is enforced by the service boundary, not delegated to a
				// provider or a browser-controlled response field.
				candidate.NoWrite = true
				candidate.Blocked = candidate.State == service.WorkConservingProjectionBlocked
				projection = candidate
			}
		}
		result.WorkConserving = &projection
	}
	writeJSON(w, http.StatusOK, result)
}

func writeContinuousDispatchShadowError(w http.ResponseWriter, status int, reason, message string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	writeJSON(w, status, map[string]string{"error": message, "reason_code": reason})
}

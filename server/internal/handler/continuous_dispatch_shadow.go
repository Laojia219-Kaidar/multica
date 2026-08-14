package handler

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/multica-ai/multica/server/internal/service"
)

var allowedContinuousDispatchShadowQueryParams = map[string]bool{
	"workspace_id": true,
	"limit":        true,
	"offset":       true,
}

func (h *Handler) GetProjectNextActions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	values := r.URL.Query()
	for key, items := range values {
		if !allowedContinuousDispatchShadowQueryParams[key] || len(items) != 1 {
			writeContinuousDispatchShadowError(w, http.StatusBadRequest, "invalid_request", "query parameters must be known and singular")
			return
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
	writeJSON(w, http.StatusOK, result)
}

func writeContinuousDispatchShadowError(w http.ResponseWriter, status int, reason, message string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	writeJSON(w, status, map[string]string{"error": message, "reason_code": reason})
}

package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
)

const maxReviewDispatchBodySize = 1024

var allowedReviewDispatchQueryParams = map[string]bool{
	"workspace_id": true,
	"limit":        true,
	"offset":       true,
}

// GetProjectReviewDispatchPreview returns a read-only, owner-controlled page
// of the existing in_review frontier. It neither creates Tasks nor wakes a
// runtime; dispatch requires the separate explicit POST command.
func (h *Handler) GetProjectReviewDispatchPreview(w http.ResponseWriter, r *http.Request) {
	workspaceID, projectID, limit, offset, ok := h.reviewDispatchRequestScope(w, r, 25)
	if !ok {
		return
	}
	if h.ReviewDispatch == nil {
		writeContinuousDispatchShadowError(w, http.StatusServiceUnavailable, "source_gap", "review dispatch is unavailable")
		return
	}
	if _, ok := h.requireWorkspaceRole(w, r, util.UUIDToString(workspaceID), "workspace not found", "owner", "admin"); !ok {
		return
	}
	result, err := h.ReviewDispatch.PreviewProject(r.Context(), workspaceID, projectID, limit, offset)
	if err != nil {
		writeReviewDispatchError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	writeJSON(w, http.StatusOK, result)
}

// DispatchProjectReviewBatch is an explicit Owner/Admin command. It defaults
// to one Issue and caps each request at 25. The request body must be empty or
// `{}`: a browser cannot nominate reviewer, Agent, Runtime, model, account,
// generation, source Task, or handoff text.
func (h *Handler) DispatchProjectReviewBatch(w http.ResponseWriter, r *http.Request) {
	workspaceID, projectID, limit, offset, ok := h.reviewDispatchRequestScope(w, r, 1)
	if !ok {
		return
	}
	if h.ReviewDispatch == nil {
		writeContinuousDispatchShadowError(w, http.StatusServiceUnavailable, "writer_unavailable", "review dispatch writer is unavailable")
		return
	}
	if err := decodeEmptyReviewDispatchBody(r); err != nil {
		writeContinuousDispatchShadowError(w, http.StatusBadRequest, "invalid_request", "review dispatch request must not select a route")
		return
	}
	member, ok := h.requireWorkspaceRole(w, r, util.UUIDToString(workspaceID), "workspace not found", "owner", "admin")
	if !ok {
		return
	}
	result, err := h.ReviewDispatch.DispatchProject(r.Context(), workspaceID, projectID, member.UserID, limit, offset)
	if err != nil {
		writeReviewDispatchError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) reviewDispatchRequestScope(w http.ResponseWriter, r *http.Request, defaultLimit int) (workspaceID, projectID pgtype.UUID, limit, offset int, ok bool) {
	values := r.URL.Query()
	for key, items := range values {
		if !allowedReviewDispatchQueryParams[key] || len(items) != 1 {
			writeContinuousDispatchShadowError(w, http.StatusBadRequest, "invalid_request", "query parameters must be known and singular")
			return pgtype.UUID{}, pgtype.UUID{}, 0, 0, false
		}
	}
	limit = defaultLimit
	if items, present := values["limit"]; present {
		parsed, valid := parseCanonicalDecimal(items[0], false)
		if !valid || parsed > 25 {
			writeContinuousDispatchShadowError(w, http.StatusBadRequest, "invalid_request", "limit must be a canonical integer from 1 to 25")
			return pgtype.UUID{}, pgtype.UUID{}, 0, 0, false
		}
		limit = parsed
	}
	offset = 0
	if items, present := values["offset"]; present {
		parsed, valid := parseCanonicalDecimal(items[0], true)
		if !valid {
			writeContinuousDispatchShadowError(w, http.StatusBadRequest, "invalid_request", "offset must be a canonical non-negative integer")
			return pgtype.UUID{}, pgtype.UUID{}, 0, 0, false
		}
		offset = parsed
	}
	workspaceRaw := h.resolveWorkspaceID(r)
	workspaceID, ok = parseUUIDOrBadRequest(w, workspaceRaw, "workspace id")
	if !ok {
		return pgtype.UUID{}, pgtype.UUID{}, 0, 0, false
	}
	projectID, ok = parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "project id")
	if !ok {
		return pgtype.UUID{}, pgtype.UUID{}, 0, 0, false
	}
	return workspaceID, projectID, limit, offset, true
}

func decodeEmptyReviewDispatchBody(r *http.Request) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxReviewDispatchBodySize+1))
	decoder.DisallowUnknownFields()
	var body struct{}
	if err := decoder.Decode(&body); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func writeReviewDispatchError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrContinuousDispatchProjectAbsent), errors.Is(err, service.ErrContinuousDispatchIssueAbsent):
		writeContinuousDispatchShadowError(w, http.StatusNotFound, "not_found", "review dispatch source was not found")
	case errors.Is(err, service.ErrContinuousDispatchIssueDrift), errors.Is(err, service.ErrContinuousDispatchRouteDrift), errors.Is(err, service.ErrContinuousDispatchConflict):
		writeContinuousDispatchShadowError(w, http.StatusConflict, "dispatch_conflict", "review dispatch truth changed; recompute and retry")
	case errors.Is(err, service.ErrContinuousDispatchNotReady), errors.Is(err, service.ErrContinuousDispatchIssueNotReady):
		writeContinuousDispatchShadowError(w, http.StatusConflict, "not_ready", "review issue has no executable next action")
	case errors.Is(err, service.ErrContinuousDispatchSourceGap):
		writeContinuousDispatchShadowError(w, http.StatusServiceUnavailable, "source_gap", "review dispatch source is temporarily unavailable")
	default:
		writeContinuousDispatchShadowError(w, http.StatusServiceUnavailable, "dispatch_unavailable", "review dispatch is temporarily unavailable")
	}
}

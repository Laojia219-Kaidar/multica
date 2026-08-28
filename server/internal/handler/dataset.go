package handler

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// DatasetResponse is the local projection of a versioned Dataset/Knowledge
// asset. The canonical knowledge authority is the World Library (noah-ark-4);
// this is the local execution projection (source_available_runtime_unavailable).
type DatasetResponse struct {
	ID                 string   `json:"id"`
	Name               string   `json:"name"`
	Domain             string   `json:"domain"`
	ProductType        string   `json:"product_type"`
	Version            int32    `json:"version"`
	AuthorizedAgentIds []string `json:"authorized_agent_ids"`
}

func datasetToResponse(id pgtype.UUID, name, domain, productType string, version int32, authorizedAgentIds []pgtype.UUID) DatasetResponse {
	ids := make([]string, 0, len(authorizedAgentIds))
	for _, a := range authorizedAgentIds { ids = append(ids, uuidToString(a)) }
	return DatasetResponse{ID: uuidToString(id), Name: name, Domain: domain, ProductType: productType, Version: version, AuthorizedAgentIds: ids}
}

type createDatasetRequest struct {
	Name        string `json:"name"`
	Domain      string `json:"domain"`
	ProductType string `json:"product_type"`
	Version     int32  `json:"version"`
}

// CreateDataset creates a versioned Dataset (default version 1).
func (h *Handler) CreateDataset(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	var req createDatasetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body"); return
	}
	if req.Name == "" { writeError(w, http.StatusBadRequest, "name is required"); return }
	if req.Domain == "" { writeError(w, http.StatusBadRequest, "domain is required"); return }
	if req.ProductType == "" { req.ProductType = "rag_kb" }
	if req.Version <= 0 { req.Version = 1 }
	d, err := h.Queries.CreateDataset(r.Context(), db.CreateDatasetParams{
		WorkspaceID: parseUUID(workspaceID), Name: req.Name, Domain: req.Domain, ProductType: req.ProductType, Version: req.Version,
		AuthorizedAgentIds: []pgtype.UUID{},
	})
	if err != nil { writeError(w, http.StatusInternalServerError, "failed to create dataset"); return }
	writeJSON(w, http.StatusCreated, datasetToResponse(d.ID, d.Name, d.Domain, d.ProductType, d.Version, d.AuthorizedAgentIds))
}

// ListDatasets lists Datasets in the workspace.
func (h *Handler) ListDatasets(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	rows, err := h.Queries.ListDatasets(r.Context(), parseUUID(workspaceID))
	if err != nil { writeError(w, http.StatusInternalServerError, "failed to list datasets"); return }
	resp := make([]DatasetResponse, 0, len(rows))
	for _, d := range rows { resp = append(resp, datasetToResponse(d.ID, d.Name, d.Domain, d.ProductType, d.Version, d.AuthorizedAgentIds)) }
	writeJSON(w, http.StatusOK, resp)
}

// GetDataset returns one Dataset with the workspace tenant guard.
func (h *Handler) GetDataset(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "dataset id")
	if !ok { return }
	d, err := h.Queries.GetDataset(r.Context(), db.GetDatasetParams{
		ID: id, WorkspaceID: parseUUID(h.resolveWorkspaceID(r)),
	})
	if err != nil { writeError(w, http.StatusNotFound, "dataset not found"); return }
	writeJSON(w, http.StatusOK, datasetToResponse(d.ID, d.Name, d.Domain, d.ProductType, d.Version, d.AuthorizedAgentIds))
}

// updateDatasetRequest is a partial update. Pointer fields distinguish an
// absent field (value preserved) from an explicit zero value; a present
// authorized_agent_ids slice replaces the set — including an empty slice,
// which clears it.
type updateDatasetRequest struct {
	Name               *string  `json:"name"`
	Domain             *string  `json:"domain"`
	ProductType        *string  `json:"product_type"`
	Version            *int32   `json:"version"`
	AuthorizedAgentIds []string `json:"authorized_agent_ids"`
}

// buildDatasetUpdateParams maps a partial update request onto sqlc params and
// validates the provided fields. It is pure so the request contract stays
// unit-testable without a database.
func buildDatasetUpdateParams(id, workspaceID pgtype.UUID, req updateDatasetRequest) (db.UpdateDatasetParams, error) {
	params := db.UpdateDatasetParams{ID: id, WorkspaceID: workspaceID}
	if req.Name != nil {
		if *req.Name == "" { return params, errDatasetInvalidField("name") }
		params.Name = pgtype.Text{String: *req.Name, Valid: true}
	}
	if req.Domain != nil {
		if *req.Domain == "" { return params, errDatasetInvalidField("domain") }
		params.Domain = pgtype.Text{String: *req.Domain, Valid: true}
	}
	if req.ProductType != nil {
		if *req.ProductType == "" { return params, errDatasetInvalidField("product_type") }
		params.ProductType = pgtype.Text{String: *req.ProductType, Valid: true}
	}
	if req.Version != nil {
		if *req.Version <= 0 { return params, errDatasetInvalidField("version") }
		params.Version = pgtype.Int4{Int32: *req.Version, Valid: true}
	}
	if req.AuthorizedAgentIds != nil {
		ids := make([]pgtype.UUID, 0, len(req.AuthorizedAgentIds))
		for _, s := range req.AuthorizedAgentIds {
			u, err := parseUUIDLoose(s)
			if err != nil { return params, errDatasetInvalidField("authorized_agent_ids") }
			ids = append(ids, u)
		}
		params.AuthorizedAgentIds = ids
	}
	return params, nil
}

type datasetFieldError struct{ field string }

func (e datasetFieldError) Error() string { return "invalid " + e.field }

func errDatasetInvalidField(field string) error { return datasetFieldError{field: field} }

// UpdateDataset partially updates a Dataset: rename, domain, product type,
// version bump, or employee authorization projection (authorized agent ids).
func (h *Handler) UpdateDataset(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "dataset id")
	if !ok { return }
	var req updateDatasetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body"); return
	}
	params, err := buildDatasetUpdateParams(id, parseUUID(h.resolveWorkspaceID(r)), req)
	if err != nil { writeError(w, http.StatusBadRequest, err.Error()); return }
	d, err := h.Queries.UpdateDataset(r.Context(), params)
	if err != nil { writeError(w, http.StatusNotFound, "dataset not found"); return }
	writeJSON(w, http.StatusOK, datasetToResponse(d.ID, d.Name, d.Domain, d.ProductType, d.Version, d.AuthorizedAgentIds))
}

// DeleteDataset removes a Dataset with the workspace tenant guard.
func (h *Handler) DeleteDataset(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "dataset id")
	if !ok { return }
	rows, err := h.Queries.DeleteDataset(r.Context(), db.DeleteDatasetParams{
		ID: id, WorkspaceID: parseUUID(h.resolveWorkspaceID(r)),
	})
	if err != nil { writeError(w, http.StatusInternalServerError, "failed to delete dataset"); return }
	if rows == 0 { writeError(w, http.StatusNotFound, "dataset not found"); return }
	w.WriteHeader(http.StatusNoContent)
}

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

type updateDatasetAuthorizationRequest struct {
	AuthorizedAgentIds []string `json:"authorized_agent_ids"`
}

// UpdateDatasetAuthorization authorizes specific agents to use a Dataset.
func (h *Handler) UpdateDatasetAuthorization(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req updateDatasetAuthorizationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body"); return
	}
	ids := make([]pgtype.UUID, 0, len(req.AuthorizedAgentIds))
	for _, s := range req.AuthorizedAgentIds { ids = append(ids, parseUUID(s)) }
	d, err := h.Queries.UpdateDatasetAuthorization(r.Context(), db.UpdateDatasetAuthorizationParams{
		ID: parseUUID(id), AuthorizedAgentIds: ids,
	})
	if err != nil { writeError(w, http.StatusInternalServerError, "failed to authorize dataset"); return }
	writeJSON(w, http.StatusOK, datasetToResponse(d.ID, d.Name, d.Domain, d.ProductType, d.Version, d.AuthorizedAgentIds))
}

package handler

import (
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/go-chi/chi/v5"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// EmployeeResponse is the HiveCrew execution projection of an Employee
// identity. The canonical company identity is the external HiveCosm
// authority (companyops); this table is the local execution binding.
type EmployeeResponse struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Position   string `json:"position,omitempty"`
	Department string `json:"department,omitempty"`
	AgentID    string `json:"agent_id,omitempty"`
	Status     string `json:"status"`
}

func employeeToResponse(e db.Employee) EmployeeResponse {
	out := EmployeeResponse{ID: uuidToString(e.ID), Name: e.Name, Status: e.Status}
	if e.Position.Valid { out.Position = e.Position.String }
	if e.Department.Valid { out.Department = e.Department.String }
	if e.AgentID.Valid { out.AgentID = uuidToString(e.AgentID) }
	return out
}

type createEmployeeRequest struct {
	Name       string `json:"name"`
	Position   string `json:"position"`
	Department string `json:"department"`
	AgentID    string `json:"agent_id"`
}

// CreateEmployee creates an Employee identity (status draft).
func (h *Handler) CreateEmployee(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	var req createEmployeeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body"); return
	}
	if req.Name == "" { writeError(w, http.StatusBadRequest, "name is required"); return }
	params := db.CreateEmployeeParams{WorkspaceID: parseUUID(workspaceID), Name: req.Name, Status: "draft"}
	if req.Position != "" { params.Position = pgtype.Text{String: req.Position, Valid: true} }
	if req.Department != "" { params.Department = pgtype.Text{String: req.Department, Valid: true} }
	if req.AgentID != "" { params.AgentID = parseUUID(req.AgentID) }
	e, err := h.Queries.CreateEmployee(r.Context(), params)
	if err != nil { writeError(w, http.StatusInternalServerError, "failed to create employee"); return }
	writeJSON(w, http.StatusCreated, employeeToResponse(e))
}

// ListEmployees lists Employee identities in the workspace.
func (h *Handler) ListEmployees(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	rows, err := h.Queries.ListEmployees(r.Context(), parseUUID(workspaceID))
	if err != nil { writeError(w, http.StatusInternalServerError, "failed to list employees"); return }
	resp := make([]EmployeeResponse, 0, len(rows))
	for _, e := range rows { resp = append(resp, employeeToResponse(e)) }
	writeJSON(w, http.StatusOK, resp)
}

type updateEmployeeBindingRequest struct {
	AgentID string `json:"agent_id"`
	Status  string `json:"status"`
}

// UpdateEmployeeBinding binds an Agent + sets status (onboarding/canary/active/retired).
func (h *Handler) UpdateEmployeeBinding(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req updateEmployeeBindingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body"); return
	}
	if req.Status == "" { req.Status = "active" }
	params := db.UpdateEmployeeBindingParams{ID: parseUUID(id), Status: req.Status}
	if req.AgentID != "" { params.AgentID = parseUUID(req.AgentID) }
	e, err := h.Queries.UpdateEmployeeBinding(r.Context(), params)
	if err != nil { writeError(w, http.StatusInternalServerError, "failed to update employee"); return }
	writeJSON(w, http.StatusOK, employeeToResponse(e))
}

package handler

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// WorkroomResponse is the wire shape of a QM Workroom. A Workroom is a
// collaboration context bound to a Project/Issue/WorkOrder — it is NOT a
// second Project/Task truth source (WO-QM-01).
type WorkroomResponse struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ProjectID   string `json:"project_id,omitempty"`
	IssueID     string `json:"issue_id,omitempty"`
	WorkOrderID string `json:"work_order_id,omitempty"`
	CreatedBy   string `json:"created_by"`
}

func workroomToResponse(w db.Workroom) WorkroomResponse {
	out := WorkroomResponse{
		ID:        uuidToString(w.ID),
		Name:      w.Name,
		CreatedBy: uuidToString(w.CreatedBy),
	}
	if w.ProjectID.Valid {
		out.ProjectID = uuidToString(w.ProjectID)
	}
	if w.IssueID.Valid {
		out.IssueID = uuidToString(w.IssueID)
	}
	if w.WorkOrderID.Valid {
		out.WorkOrderID = w.WorkOrderID.String
	}
	return out
}

type createWorkroomRequest struct {
	Name        string `json:"name"`
	ProjectID   string `json:"project_id,omitempty"`
	IssueID     string `json:"issue_id,omitempty"`
	WorkOrderID string `json:"work_order_id,omitempty"`
}

// CreateWorkroom creates a QM Workroom bound to an optional Project/Issue/WorkOrder.
func (h *Handler) CreateWorkroom(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req createWorkroomRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	params := db.CreateWorkroomParams{
		WorkspaceID: parseUUID(workspaceID),
		Name:        req.Name,
		CreatedBy:   parseUUID(userID),
	}
	if req.ProjectID != "" {
		params.ProjectID = parseUUID(req.ProjectID)
	}
	if req.IssueID != "" {
		params.IssueID = parseUUID(req.IssueID)
	}
	if req.WorkOrderID != "" {
		params.WorkOrderID = pgtype.Text{String: req.WorkOrderID, Valid: true}
	}
	wr, err := h.Queries.CreateWorkroom(r.Context(), params)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create workroom")
		return
	}
	writeJSON(w, http.StatusCreated, workroomToResponse(wr))
}

// ListWorkrooms lists QM Workrooms in the workspace.
func (h *Handler) ListWorkrooms(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	rows, err := h.Queries.ListWorkrooms(r.Context(), parseUUID(workspaceID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list workrooms")
		return
	}
	resp := make([]WorkroomResponse, 0, len(rows))
	for _, wr := range rows {
		resp = append(resp, workroomToResponse(wr))
	}
	writeJSON(w, http.StatusOK, resp)
}

// GetWorkroom returns one QM Workroom by id.
func (h *Handler) GetWorkroom(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	wr, err := h.Queries.GetWorkroom(r.Context(), parseUUID(id))
	if err != nil {
		writeError(w, http.StatusNotFound, "workroom not found")
		return
	}
	writeJSON(w, http.StatusOK, workroomToResponse(wr))
}

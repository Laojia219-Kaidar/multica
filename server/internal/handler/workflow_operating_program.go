package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/workflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type workflowOperatingProgramResponse struct {
	ID          string   `json:"id"`
	WorkspaceID string   `json:"workspace_id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	ProjectIDs  []string `json:"project_ids"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
}

type workflowOperatingProgramReceipt struct {
	Changed  bool `json:"changed"`
	Accepted bool `json:"accepted"`
	Replayed bool `json:"replayed,omitempty"`
}

type workflowOperatingProgramCommandResponse struct {
	Program workflowOperatingProgramResponse `json:"program"`
	Receipt workflowOperatingProgramReceipt  `json:"receipt"`
}

type workflowOperatingProgramProjectCommandResponse struct {
	ProgramID string                          `json:"program_id"`
	ProjectID string                          `json:"project_id"`
	Receipt   workflowOperatingProgramReceipt `json:"receipt"`
}

type createWorkflowOperatingProgramRequest struct {
	Name           string `json:"name"`
	Description    string `json:"description"`
	IdempotencyKey string `json:"idempotency_key"`
}

type updateWorkflowOperatingProgramRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type workflowOperatingProgramProjectRequest struct {
	ProjectID string `json:"project_id"`
}

func workflowOperatingProgramResponseFromModel(program workflow.OperatingProgram) workflowOperatingProgramResponse {
	return workflowOperatingProgramResponse{
		ID: program.ID, WorkspaceID: program.WorkspaceID, Name: program.Name,
		Description: program.Description, ProjectIDs: program.ProjectIDs,
		CreatedAt: program.CreatedAt.UTC().Format("2006-01-02T15:04:05.999Z07:00"),
		UpdatedAt: program.UpdatedAt.UTC().Format("2006-01-02T15:04:05.999Z07:00"),
	}
}

func (h *Handler) workflowOperatingProgramRepository() *workflow.OperatingProgramRepository {
	return workflow.NewOperatingProgramRepository(h.Queries)
}

func (h *Handler) workflowOperatingProgramWorkspace(w http.ResponseWriter, r *http.Request) (string, bool) {
	workspaceID := h.resolveWorkspaceID(r)
	if workspaceID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return "", false
	}
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return "", false
	}
	if _, err := canonicalWorkflowOperatingProgramUUID(workspaceID); err != nil {
		writeError(w, http.StatusBadRequest, "workspace_id must be a canonical UUID")
		return "", false
	}
	return workspaceID, true
}

func canonicalWorkflowOperatingProgramUUID(value string) (string, error) {
	id, err := uuid.Parse(value)
	if err != nil || id.String() != value {
		return "", errors.New("canonical UUID required")
	}
	return value, nil
}

// ListWorkflowOperatingPrograms returns L3 operating subjects and their
// existing Project assignments. It is a read projection, not a Project store.
func (h *Handler) ListWorkflowOperatingPrograms(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := h.workflowOperatingProgramWorkspace(w, r)
	if !ok {
		return
	}
	programs, err := h.workflowOperatingProgramRepository().List(r.Context(), workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list workflow operating programs")
		return
	}
	result := make([]workflowOperatingProgramResponse, 0, len(programs))
	for _, program := range programs {
		result = append(result, workflowOperatingProgramResponseFromModel(program))
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) CreateWorkflowOperatingProgram(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := h.workflowOperatingProgramWorkspace(w, r)
	if !ok {
		return
	}
	var req createWorkflowOperatingProgramRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.IdempotencyKey) == "" {
		writeError(w, http.StatusBadRequest, "name and idempotency_key are required")
		return
	}
	program, created, err := h.workflowOperatingProgramRepository().Create(r.Context(), workspaceID, req.Name, req.Description, req.IdempotencyKey)
	if errors.Is(err, workflow.ErrOperatingProgramConflict) {
		writeError(w, http.StatusConflict, "idempotency_key was already used for another operating program payload")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create workflow operating program")
		return
	}
	status := http.StatusCreated
	if !created {
		status = http.StatusOK
	}
	writeJSON(w, status, workflowOperatingProgramCommandResponse{
		Program: workflowOperatingProgramResponseFromModel(program),
		Receipt: workflowOperatingProgramReceipt{Changed: created, Accepted: true, Replayed: !created},
	})
}

func (h *Handler) UpdateWorkflowOperatingProgram(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := h.workflowOperatingProgramWorkspace(w, r)
	if !ok {
		return
	}
	programID, ok := canonicalWorkflowOperatingProgramPathID(w, chi.URLParam(r, "id"), "program id")
	if !ok {
		return
	}
	var req updateWorkflowOperatingProgramRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	program, err := h.workflowOperatingProgramRepository().Update(r.Context(), workspaceID, programID, req.Name, req.Description)
	if errors.Is(err, workflow.ErrOperatingProgramNotFound) {
		writeError(w, http.StatusNotFound, "workflow operating program not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, workflowOperatingProgramCommandResponse{
		Program: workflowOperatingProgramResponseFromModel(program),
		Receipt: workflowOperatingProgramReceipt{Changed: true, Accepted: true},
	})
}

func (h *Handler) AssignWorkflowOperatingProgramProject(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := h.workflowOperatingProgramWorkspace(w, r)
	if !ok {
		return
	}
	programID, ok := canonicalWorkflowOperatingProgramPathID(w, chi.URLParam(r, "id"), "program id")
	if !ok {
		return
	}
	var req workflowOperatingProgramProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	projectID, ok := canonicalWorkflowOperatingProgramPathID(w, req.ProjectID, "project_id")
	if !ok {
		return
	}
	if h.TxStarter == nil {
		writeError(w, http.StatusServiceUnavailable, "workflow operating program transaction is not configured")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "workflow operating program transaction is unavailable")
		return
	}
	defer tx.Rollback(r.Context())
	repo := workflow.NewOperatingProgramRepository(db.New(tx))
	changed, err := repo.AssignExistingProject(r.Context(), workspaceID, programID, projectID)
	if !writeWorkflowOperatingProgramMutationError(w, err) {
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit workflow operating program assignment")
		return
	}
	writeJSON(w, map[bool]int{true: http.StatusCreated, false: http.StatusOK}[changed], workflowOperatingProgramProjectCommandResponse{
		ProgramID: programID, ProjectID: projectID,
		Receipt: workflowOperatingProgramReceipt{Changed: changed, Accepted: true, Replayed: !changed},
	})
}

func (h *Handler) UnassignWorkflowOperatingProgramProject(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := h.workflowOperatingProgramWorkspace(w, r)
	if !ok {
		return
	}
	programID, ok := canonicalWorkflowOperatingProgramPathID(w, chi.URLParam(r, "id"), "program id")
	if !ok {
		return
	}
	projectID, ok := canonicalWorkflowOperatingProgramPathID(w, chi.URLParam(r, "projectId"), "project id")
	if !ok {
		return
	}
	if h.TxStarter == nil {
		writeError(w, http.StatusServiceUnavailable, "workflow operating program transaction is not configured")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "workflow operating program transaction is unavailable")
		return
	}
	defer tx.Rollback(r.Context())
	repo := workflow.NewOperatingProgramRepository(db.New(tx))
	changed, err := repo.UnassignExistingProject(r.Context(), workspaceID, programID, projectID)
	if !writeWorkflowOperatingProgramMutationError(w, err) {
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit workflow operating program unassignment")
		return
	}
	writeJSON(w, http.StatusOK, workflowOperatingProgramProjectCommandResponse{
		ProgramID: programID, ProjectID: projectID,
		Receipt: workflowOperatingProgramReceipt{Changed: changed, Accepted: true, Replayed: !changed},
	})
}

func (h *Handler) DeleteWorkflowOperatingProgram(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := h.workflowOperatingProgramWorkspace(w, r)
	if !ok {
		return
	}
	programID, ok := canonicalWorkflowOperatingProgramPathID(w, chi.URLParam(r, "id"), "program id")
	if !ok {
		return
	}
	if h.TxStarter == nil {
		writeError(w, http.StatusServiceUnavailable, "workflow operating program transaction is not configured")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "workflow operating program transaction is unavailable")
		return
	}
	defer tx.Rollback(r.Context())
	repo := workflow.NewOperatingProgramRepository(db.New(tx))
	if err := repo.Delete(r.Context(), workspaceID, programID); !writeWorkflowOperatingProgramMutationError(w, err) {
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit workflow operating program deletion")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func canonicalWorkflowOperatingProgramPathID(w http.ResponseWriter, value, label string) (string, bool) {
	id, err := canonicalWorkflowOperatingProgramUUID(value)
	if err != nil {
		writeError(w, http.StatusBadRequest, label+" must be a canonical UUID")
		return "", false
	}
	return id, true
}

func writeWorkflowOperatingProgramMutationError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return true
	}
	switch {
	case errors.Is(err, workflow.ErrOperatingProgramNotFound), errors.Is(err, workflow.ErrProjectNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, workflow.ErrProjectAlreadyAssigned):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, http.StatusNotFound, "workflow operating program or project not found")
	default:
		writeError(w, http.StatusInternalServerError, "workflow operating program mutation failed")
	}
	return false
}

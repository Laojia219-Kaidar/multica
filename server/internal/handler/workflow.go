package handler

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/workflow"
)

// The workflow kernel (Slice-W1/W2) is now exposed over HTTP. The in-memory
// Engine is a process-global singleton; persistence goes through
// workflow.Repository (migration 342). It drives the W2 HIV-553 project
// lifecycle stages; it does NOT write Task/Run/Project/Outcome state directly.

var (
	wfEngineOnce sync.Once
	wfEngine     *workflow.Engine
)

func workflowEngine() *workflow.Engine {
	wfEngineOnce.Do(func() {
		wfEngine = workflow.NewEngine()
		_ = wfEngine.Register(workflow.ProjectLifecycleDefinition())
	})
	return wfEngine
}

func (h *Handler) workflowRepo() *workflow.Repository { return workflow.NewRepository(h.Queries) }

type workflowContextDTO struct {
	ProjectID string `json:"project_id,omitempty"`
	IssueID   string `json:"issue_id,omitempty"`
	OutcomeID string `json:"outcome_id,omitempty"`
}

type workflowInstanceDTO struct {
	ID                string            `json:"id"`
	DefinitionID      string            `json:"definition_id"`
	DefinitionVersion int               `json:"definition_version"`
	Context           workflowContextDTO `json:"context"`
	StageIndex        int               `json:"stage_index"`
	Status            string            `json:"status"`
}

func toWorkflowInstanceDTO(i workflow.WorkflowInstance) workflowInstanceDTO {
	return workflowInstanceDTO{
		ID:                i.ID,
		DefinitionID:      i.DefinitionID,
		DefinitionVersion: i.DefinitionVersion,
		Context: workflowContextDTO{
			ProjectID: i.Context.ProjectID,
			IssueID:   i.Context.IssueID,
			OutcomeID: i.Context.OutcomeID,
		},
		StageIndex: i.StageIndex,
		Status:     string(i.Status),
	}
}

type workflowEventDTO struct {
	Sequence       int64  `json:"sequence"`
	InstanceID     string `json:"instance_id"`
	Kind           string `json:"kind"`
	SourceRef      string `json:"source_ref"`
	Actor          string `json:"actor"`
	OccurredAt     string `json:"occurred_at"`
	ObservedAt     string `json:"observed_at"`
	IdempotencyKey string `json:"idempotency_key"`
}

func toWorkflowEventDTO(e workflow.Event) workflowEventDTO {
	return workflowEventDTO{
		Sequence:       e.Sequence,
		InstanceID:     e.InstanceID,
		Kind:           e.Kind,
		SourceRef:      e.SourceRef,
		Actor:          e.Actor,
		OccurredAt:     e.OccurredAt.UTC().Format(time.RFC3339),
		ObservedAt:     e.ObservedAt.UTC().Format(time.RFC3339),
		IdempotencyKey: e.IdempotencyKey,
	}
}

type startWorkflowRequest struct {
	DefinitionID   string            `json:"definition_id,omitempty"`
	InstanceID     string            `json:"instance_id,omitempty"`
	Context        workflowContextDTO `json:"context"`
	IdempotencyKey string            `json:"idempotency_key,omitempty"`
}

// StartWorkflowInstance POST /api/workflow/instances
func (h *Handler) StartWorkflowInstance(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}
	var req startWorkflowRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	defID := req.DefinitionID
	if defID == "" {
		defID = workflow.ProjectLifecycleDefinition().ID
	}
	instanceID := req.InstanceID
	if instanceID == "" {
		instanceID = uuid.NewString()
	}
	key := req.IdempotencyKey
	if key == "" {
		key = uuid.NewString()
	}
	ctx := workflow.ContextRef{
		ProjectID: req.Context.ProjectID,
		IssueID:   req.Context.IssueID,
		OutcomeID: req.Context.OutcomeID,
	}
	inst, receipt, err := workflowEngine().Start(defID, instanceID, ctx, key)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	repo := h.workflowRepo()
	if err := repo.SaveInstance(r.Context(), inst); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to persist workflow instance")
		return
	}
	for _, ev := range workflowEngine().Events(instanceID) {
		if err := repo.AppendEvent(r.Context(), ev); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to persist workflow event")
			return
		}
	}
	_ = receipt
	writeJSON(w, http.StatusOK, toWorkflowInstanceDTO(inst))
}

// GetWorkflowInstance GET /api/workflow/instances/{id}
func (h *Handler) GetWorkflowInstance(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}
	id := r.PathValue("id")
	// Resume-after-restart read-back.
	if _, ok := workflowEngine().Get(id); !ok {
		if err := workflowEngine().Hydrate(r.Context(), h.workflowRepo(), id); err != nil {
			writeError(w, http.StatusNotFound, "workflow instance not found")
			return
		}
	}
	inst, ok := workflowEngine().Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "workflow instance not found")
		return
	}
	writeJSON(w, http.StatusOK, toWorkflowInstanceDTO(inst))
}

type advanceWorkflowRequest struct {
	ReviewPassed  bool     `json:"review_passed"`
	OwnerApproved bool     `json:"owner_approved"`
	TaskID        string   `json:"task_id,omitempty"`
	RunID         string   `json:"run_id,omitempty"`
	Notes         []string `json:"notes,omitempty"`
	IdempotencyKey string  `json:"idempotency_key,omitempty"`
}

// AdvanceWorkflowInstance POST /api/workflow/instances/{id}/advance
func (h *Handler) AdvanceWorkflowInstance(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}
	id := r.PathValue("id")
	var req advanceWorkflowRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	key := req.IdempotencyKey
	if key == "" {
		key = uuid.NewString()
	}
	actorType, actorID := h.resolveActor(r, requestUserID(r), workspaceID)
	_ = actorType
	ev := workflow.AdvanceEvidence{
		ReviewPassed:  req.ReviewPassed,
		OwnerApproved: req.OwnerApproved,
		TaskID:        req.TaskID,
		RunID:         req.RunID,
		ActorID:       actorID,
		Notes:         req.Notes,
	}
	inst, _, err := workflowEngine().Advance(id, ev, key)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	repo := h.workflowRepo()
	if err := repo.UpdateInstance(r.Context(), inst); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to persist workflow advance")
		return
	}
	for _, e := range workflowEngine().Events(id) {
		if err := repo.AppendEvent(r.Context(), e); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to persist workflow event")
			return
		}
	}
	writeJSON(w, http.StatusOK, toWorkflowInstanceDTO(inst))
}

// WorkflowInstanceEvents GET /api/workflow/instances/{id}/events
func (h *Handler) WorkflowInstanceEvents(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}
	id := r.PathValue("id")
	if _, ok := workflowEngine().Get(id); !ok {
		if err := workflowEngine().Hydrate(r.Context(), h.workflowRepo(), id); err != nil {
			writeError(w, http.StatusNotFound, "workflow instance not found")
			return
		}
	}
	events := workflowEngine().Events(id)
	dtos := make([]workflowEventDTO, 0, len(events))
	for _, e := range events {
		dtos = append(dtos, toWorkflowEventDTO(e))
	}
	writeJSON(w, http.StatusOK, dtos)
}

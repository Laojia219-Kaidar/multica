package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
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

type workflowDefinitionVersionDTO struct {
	DefinitionID string                 `json:"definition_id"`
	WorkspaceID  string                 `json:"workspace_id"`
	ProjectID    string                 `json:"project_id,omitempty"`
	Version      int                    `json:"version"`
	Risk         workflow.RiskTier      `json:"risk"`
	Stages       []workflow.Stage       `json:"stages"`
	Graph        workflow.WorkflowGraph `json:"graph"`
	Digest       string                 `json:"digest"`
	CreatedAt    string                 `json:"created_at"`
	PublishedAt  string                 `json:"published_at"`
}

func toWorkflowDefinitionVersionDTO(v workflow.WorkflowDefinitionVersion) workflowDefinitionVersionDTO {
	return workflowDefinitionVersionDTO{
		DefinitionID: v.DefinitionID, WorkspaceID: v.WorkspaceID, ProjectID: v.ProjectID, Version: v.Version,
		Risk: v.Risk, Stages: v.Stages, Graph: v.Graph, Digest: v.Digest,
		CreatedAt: v.CreatedAt.UTC().Format(time.RFC3339), PublishedAt: v.PublishedAt.UTC().Format(time.RFC3339),
	}
}

// ListWorkflowDefinitions GET /api/workflow/definitions. The default is one
// latest immutable version per definition; latest_only=false is available for
// the version history view.
func (h *Handler) ListWorkflowDefinitions(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}
	latestOnly := r.URL.Query().Get("latest_only") != "false"
	versions, err := h.workflowRepo().ListPublishedDefinitionVersions(r.Context(), workspaceID, latestOnly)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list workflow definitions")
		return
	}
	result := make([]workflowDefinitionVersionDTO, 0, len(versions))
	for _, version := range versions {
		result = append(result, toWorkflowDefinitionVersionDTO(version))
	}
	writeJSON(w, http.StatusOK, result)
}

type publishWorkflowDefinitionVersionRequest struct {
	ProjectID      string                `json:"project_id,omitempty"`
	Risk           workflow.RiskTier      `json:"risk"`
	Stages         []workflow.Stage       `json:"stages"`
	Graph          workflow.WorkflowGraph `json:"graph"`
	IdempotencyKey string                 `json:"idempotency_key,omitempty"`
}

type publishWorkflowDefinitionVersionResponse struct {
	Version workflowDefinitionVersionDTO `json:"version"`
	Receipt struct {
		IdempotencyKey string `json:"idempotency_key"`
		Changed        bool   `json:"changed"`
		Accepted       bool   `json:"accepted"`
	} `json:"receipt"`
}

// PublishWorkflowDefinitionVersion POST /api/workflow/definitions/{id}/versions.
// It creates the next immutable version and returns the durable receipt. A
// repeated idempotency key returns the original version with changed=false.
func (h *Handler) PublishWorkflowDefinitionVersion(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}
	var req publishWorkflowDefinitionVersionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	key := req.IdempotencyKey
	if key == "" {
		key = r.Header.Get("Idempotency-Key")
	}
	if key == "" {
		writeError(w, http.StatusBadRequest, "idempotency_key is required")
		return
	}
	v, changed, err := h.workflowRepo().PublishDefinitionVersion(r.Context(), workflow.WorkflowDefinitionVersion{
		DefinitionID: r.PathValue("id"), WorkspaceID: workspaceID, ProjectID: req.ProjectID,
		Risk: req.Risk, Stages: req.Stages, Graph: req.Graph,
	}, key)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	resp := publishWorkflowDefinitionVersionResponse{Version: toWorkflowDefinitionVersionDTO(v)}
	resp.Receipt.IdempotencyKey = key
	resp.Receipt.Changed = changed
	resp.Receipt.Accepted = true
	status := http.StatusCreated
	if !changed {
		status = http.StatusOK
	}
	writeJSON(w, status, resp)
}

// GetWorkflowDefinitionVersion GET /api/workflow/definitions/{id}/versions/{version}.
func (h *Handler) GetWorkflowDefinitionVersion(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}
	version, err := strconv.Atoi(r.PathValue("version"))
	if err != nil || version < 1 {
		writeError(w, http.StatusBadRequest, "invalid version")
		return
	}
	result, err := h.workflowRepo().LoadPublishedDefinitionVersion(r.Context(), workspaceID, r.PathValue("id"), version)
	if err != nil {
		writeError(w, http.StatusNotFound, "workflow definition version not found")
		return
	}
	writeJSON(w, http.StatusOK, toWorkflowDefinitionVersionDTO(result))
}

type workflowContextDTO struct {
	ProjectID string `json:"project_id,omitempty"`
	IssueID   string `json:"issue_id,omitempty"`
	OutcomeID string `json:"outcome_id,omitempty"`
}

type workflowInstanceDTO struct {
	ID                string             `json:"id"`
	WorkspaceID       string             `json:"workspace_id,omitempty"`
	DefinitionID      string             `json:"definition_id"`
	DefinitionVersion int                `json:"definition_version"`
	Context           workflowContextDTO `json:"context"`
	StageIndex        int                `json:"stage_index"`
	Status            string             `json:"status"`
}

func toWorkflowInstanceDTO(i workflow.WorkflowInstance) workflowInstanceDTO {
	return workflowInstanceDTO{
		ID:                i.ID,
		WorkspaceID:       i.WorkspaceID,
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

// ListWorkflowInstances GET /api/workflow/instances. The database query is
// intentionally the source of truth; the process-global engine is only a
// runtime cache and must not determine which instances are visible after a
// refresh or restart.
func (h *Handler) ListWorkflowInstances(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}
	instances, err := h.workflowRepo().ListInstances(r.Context(), workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list workflow instances")
		return
	}
	dtos := make([]workflowInstanceDTO, 0, len(instances))
	for _, inst := range instances {
		dtos = append(dtos, toWorkflowInstanceDTO(inst))
	}
	writeJSON(w, http.StatusOK, dtos)
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
	DefinitionID   string             `json:"definition_id,omitempty"`
	InstanceID     string             `json:"instance_id,omitempty"`
	Context        workflowContextDTO `json:"context"`
	IdempotencyKey string             `json:"idempotency_key,omitempty"`
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
	inst, receipt, err := workflowEngine().StartForWorkspace(defID, instanceID, ctx, workspaceID, key)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	inst.WorkspaceID = workspaceID
	workflowEngine().AttachWorkspace(instanceID, workspaceID)
	repo := h.workflowRepo()
	if err := repo.SaveInstanceInWorkspace(r.Context(), workspaceID, inst); err != nil {
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
	// Always validate visibility from the workspace-scoped database row before
	// consulting the process-global engine cache.
	if _, err := h.workflowRepo().LoadInstanceInWorkspace(r.Context(), workspaceID, id); err != nil {
		if err := workflowEngine().HydrateInWorkspace(r.Context(), h.workflowRepo(), workspaceID, id); err != nil {
			writeError(w, http.StatusNotFound, "workflow instance not found")
			return
		}
	}
	inst, ok := workflowEngine().Get(id)
	if !ok {
		if err := workflowEngine().HydrateInWorkspace(r.Context(), h.workflowRepo(), workspaceID, id); err != nil {
			writeError(w, http.StatusNotFound, "workflow instance not found")
			return
		}
		inst, ok = workflowEngine().Get(id)
	}
	if !ok || inst.WorkspaceID != "" && inst.WorkspaceID != workspaceID {
		writeError(w, http.StatusNotFound, "workflow instance not found")
		return
	}
	writeJSON(w, http.StatusOK, toWorkflowInstanceDTO(inst))
}

type advanceWorkflowRequest struct {
	ReviewPassed   bool     `json:"review_passed"`
	OwnerApproved  bool     `json:"owner_approved"`
	TaskID         string   `json:"task_id,omitempty"`
	RunID          string   `json:"run_id,omitempty"`
	Notes          []string `json:"notes,omitempty"`
	IdempotencyKey string   `json:"idempotency_key,omitempty"`
}

// AdvanceWorkflowInstance POST /api/workflow/instances/{id}/advance
func (h *Handler) AdvanceWorkflowInstance(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}
	id := r.PathValue("id")
	if _, err := h.workflowRepo().LoadInstanceInWorkspace(r.Context(), workspaceID, id); err != nil {
		if err := workflowEngine().HydrateInWorkspace(r.Context(), h.workflowRepo(), workspaceID, id); err != nil {
			writeError(w, http.StatusNotFound, "workflow instance not found")
			return
		}
	}
	if cached, ok := workflowEngine().Get(id); ok && cached.WorkspaceID != "" && cached.WorkspaceID != workspaceID {
		writeError(w, http.StatusNotFound, "workflow instance not found")
		return
	}
	if _, ok := workflowEngine().Get(id); !ok {
		if err := workflowEngine().HydrateInWorkspace(r.Context(), h.workflowRepo(), workspaceID, id); err != nil {
			writeError(w, http.StatusNotFound, "workflow instance not found")
			return
		}
	}
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
	inst.WorkspaceID = workspaceID
	if err := repo.UpdateInstanceInWorkspace(r.Context(), workspaceID, inst); err != nil {
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
	if _, err := h.workflowRepo().LoadInstanceInWorkspace(r.Context(), workspaceID, id); err != nil {
		if err := workflowEngine().HydrateInWorkspace(r.Context(), h.workflowRepo(), workspaceID, id); err != nil {
			writeError(w, http.StatusNotFound, "workflow instance not found")
			return
		}
	}
	events, err := h.workflowRepo().ListEventsInWorkspace(r.Context(), workspaceID, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "workflow instance not found")
		return
	}
	dtos := make([]workflowEventDTO, 0, len(events))
	for _, e := range events {
		dtos = append(dtos, toWorkflowEventDTO(e))
	}
	writeJSON(w, http.StatusOK, dtos)
}

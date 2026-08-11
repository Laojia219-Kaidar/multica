package handler

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
)

type companyOpsOutcomeIssueResponse struct {
	ID         string `json:"id"`
	Number     int32  `json:"number"`
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
	Status     string `json:"status"`
	ProjectID  string `json:"project_id,omitempty"`
}

type companyOpsOutcomeWorkOrderResponse struct {
	SourceRef string `json:"source_ref"`
	Revision  string `json:"revision"`
	Digest    string `json:"digest"`
}

type companyOpsOutcomeEntityResponse struct {
	SourceRef string `json:"source_ref"`
	ID        string `json:"id"`
}

type companyOpsOutcomeExecTargetResponse struct {
	LocalAgentID  string `json:"local_agent_id"`
	AgentRef      string `json:"agent_ref"`
	AgentRevision string `json:"agent_revision"`
	AgentDigest   string `json:"agent_digest"`
}

type companyOpsOutcomeAgentDisplayResponse struct {
	Name   string `json:"name"`
	Model  string `json:"model,omitempty"`
	Status string `json:"status"`
}

type companyOpsOutcomeArtifactResponse struct {
	ID                string `json:"id"`
	Revision          int32  `json:"revision"`
	DurableObjectRef  string `json:"durable_object_ref"`
	Digest            string `json:"digest"`
	ContentType       string `json:"content_type,omitempty"`
	Status            string `json:"status"`
	FormalVisible     bool   `json:"formal_visible"`
	FormalArtifactRef string `json:"formal_artifact_ref,omitempty"`
}

type companyOpsOutcomeSummaryResponse struct {
	ID                  string                                `json:"id"`
	Issue               companyOpsOutcomeIssueResponse        `json:"issue"`
	WorkOrder           companyOpsOutcomeWorkOrderResponse    `json:"work_order"`
	Employee            companyOpsOutcomeEntityResponse       `json:"employee"`
	IdentityBinding     companyOpsOutcomeEntityResponse       `json:"identity_binding"`
	ExecutionTarget     companyOpsOutcomeExecTargetResponse   `json:"execution_target"`
	CurrentAgentDisplay companyOpsOutcomeAgentDisplayResponse `json:"current_agent_display"`
	InitialTaskID       string                                `json:"initial_task_id"`
	CurrentTaskID       string                                `json:"current_task_id"`
	ExecutionState      string                                `json:"execution_state"`
	ActiveArtifact      *companyOpsOutcomeArtifactResponse    `json:"active_artifact,omitempty"`
	VersionCount        int32                                 `json:"version_count"`
	LatestEventAt       string                                `json:"latest_event_at,omitempty"`
}

type companyOpsOutcomeVersionResponse struct {
	ID               string `json:"id"`
	Revision         int32  `json:"revision"`
	SupersedesID     string `json:"supersedes_id,omitempty"`
	DurableObjectRef string `json:"durable_object_ref"`
	Digest           string `json:"digest"`
	ContentType      string `json:"content_type,omitempty"`
}

type companyOpsOutcomeEventResponse struct {
	ID                string `json:"id"`
	Sequence          int32  `json:"sequence"`
	Type              string `json:"type"`
	CandidateID       string `json:"candidate_id"`
	CandidateRevision int32  `json:"candidate_revision"`
	FormalArtifactRef string `json:"formal_artifact_ref,omitempty"`
}

type companyOpsOutcomeRunResponse struct {
	TaskID        string `json:"task_id"`
	Status        string `json:"status"`
	CompletedAt   string `json:"completed_at,omitempty"`
	OutputDigest  string `json:"output_digest,omitempty"`
	TerminalError string `json:"terminal_error,omitempty"`
}

type companyOpsOutcomeListResponse struct {
	SchemaVersion string                             `json:"schema_version"`
	Items         []companyOpsOutcomeSummaryResponse `json:"items"`
	Total         int64                              `json:"total"`
	Limit         int                                `json:"limit"`
	Offset        int                                `json:"offset"`
}

type companyOpsOutcomeDetailResponse struct {
	SchemaVersion string                             `json:"schema_version"`
	Summary       companyOpsOutcomeSummaryResponse   `json:"summary"`
	Versions      []companyOpsOutcomeVersionResponse `json:"versions"`
	Events        []companyOpsOutcomeEventResponse   `json:"events"`
	Runs          []companyOpsOutcomeRunResponse     `json:"runs"`
}

// writeCompanyOpsOutcomeServiceError maps outcome-center service errors to
// HTTP responses. It handles the outcome-specific ledger conflict locally so
// the shared writeCompanyOpsServiceError in companyops.go remains unchanged.
func writeCompanyOpsOutcomeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrCompanyOpsOutcomeLedgerConflict):
		writeCompanyOpsError(w, http.StatusConflict, "outcome_ledger_conflict", err.Error())
	default:
		writeCompanyOpsServiceError(w, err)
	}
}

// GetCompanyOpsOutcomes returns the paginated, filtered outcome list. It is
// read-only, requires no session_id, and touches no authority adapter.
func (h *Handler) GetCompanyOpsOutcomes(w http.ResponseWriter, r *http.Request) {
	if h.CompanyOpsOutcomeCenter == nil {
		writeCompanyOpsError(w, http.StatusServiceUnavailable, "source_gap", "CompanyOps outcome center is not configured")
		return
	}
	workspaceIDStr := ctxWorkspaceID(r.Context())
	workspaceID, err := util.ParseUUID(workspaceIDStr)
	if err != nil || !workspaceID.Valid {
		writeCompanyOpsError(w, http.StatusBadRequest, "invalid_request", "workspace_id is required")
		return
	}

	req := service.CompanyOpsOutcomeListRequest{
		WorkspaceID: workspaceID,
	}

	values := r.URL.Query()
	if v := values.Get("q"); v != "" {
		req.Q = v
	}
	if v := values.Get("status"); v != "" {
		if !service.IsValidCompanyOpsOutcomeStatus(v) {
			writeCompanyOpsError(w, http.StatusBadRequest, "invalid_request", "status must be a recognized outcome status")
			return
		}
		req.Status = v
	}
	if v := values.Get("agent_id"); v != "" {
		agentID, parseErr := util.ParseUUID(v)
		if parseErr != nil || util.UUIDToString(agentID) != v {
			writeCompanyOpsError(w, http.StatusBadRequest, "invalid_request", "agent_id must be a canonical UUID")
			return
		}
		req.AgentID = agentID
	}
	if v := values.Get("project_id"); v != "" {
		projectID, parseErr := util.ParseUUID(v)
		if parseErr != nil || util.UUIDToString(projectID) != v {
			writeCompanyOpsError(w, http.StatusBadRequest, "invalid_request", "project_id must be a canonical UUID")
			return
		}
		req.ProjectID = projectID
	}
	if v := values.Get("employee_id"); v != "" {
		if strings.TrimSpace(v) != v || v == "" || strings.ContainsAny(v, "?#/") || strings.Contains(v, "/") {
			writeCompanyOpsError(w, http.StatusBadRequest, "invalid_request", "employee_id must be a canonical opaque business id")
			return
		}
		req.EmployeeID = v
	}
	if v := values.Get("type"); v != "" {
		req.Type = v
	}
	if v := values.Get("formal_visible"); v != "" {
		switch strings.ToLower(v) {
		case "true", "1":
			b := true
			req.FormalVisible = &b
		case "false", "0":
			b := false
			req.FormalVisible = &b
		default:
			writeCompanyOpsError(w, http.StatusBadRequest, "invalid_request", "formal_visible must be true or false")
			return
		}
	}
	if v := values.Get("limit"); v != "" {
		n, parseErr := strconv.Atoi(v)
		if parseErr != nil || n < 0 {
			writeCompanyOpsError(w, http.StatusBadRequest, "invalid_request", "limit must be a non-negative integer")
			return
		}
		req.Limit = n
	}
	if v := values.Get("offset"); v != "" {
		n, parseErr := strconv.Atoi(v)
		if parseErr != nil || n < 0 {
			writeCompanyOpsError(w, http.StatusBadRequest, "invalid_request", "offset must be a non-negative integer")
			return
		}
		req.Offset = n
	}

	// Reject unknown query keys to keep the contract explicit.
	for key := range values {
		switch key {
		case "q", "status", "agent_id", "project_id", "employee_id", "type", "formal_visible", "limit", "offset":
		default:
			writeCompanyOpsError(w, http.StatusBadRequest, "invalid_request", "unknown query parameter: "+key)
			return
		}
	}

	summaries, total, err := h.CompanyOpsOutcomeCenter.ListOutcomes(r.Context(), req)
	if err != nil {
		writeCompanyOpsOutcomeServiceError(w, err)
		return
	}

	limit := req.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}

	items := make([]companyOpsOutcomeSummaryResponse, 0, len(summaries))
	for i := range summaries {
		items = append(items, companyOpsOutcomeSummaryWire(summaries[i]))
	}

	writeJSON(w, http.StatusOK, companyOpsOutcomeListResponse{
		SchemaVersion: service.CompanyOpsOutcomeCenterSchemaVersion,
		Items:         items,
		Total:         total,
		Limit:         limit,
		Offset:        req.Offset,
	})
}

// GetCompanyOpsOutcome returns the full detail for one outcome keyed by its
// stable assignment command_id.
func (h *Handler) GetCompanyOpsOutcome(w http.ResponseWriter, r *http.Request) {
	if h.CompanyOpsOutcomeCenter == nil {
		writeCompanyOpsError(w, http.StatusServiceUnavailable, "source_gap", "CompanyOps outcome center is not configured")
		return
	}
	workspaceIDStr := ctxWorkspaceID(r.Context())
	workspaceID, err := util.ParseUUID(workspaceIDStr)
	if err != nil || !workspaceID.Valid {
		writeCompanyOpsError(w, http.StatusBadRequest, "invalid_request", "workspace_id is required")
		return
	}

	commandIDStr := chi.URLParam(r, "commandId")
	commandID, err := util.ParseUUID(commandIDStr)
	if err != nil || util.UUIDToString(commandID) != commandIDStr {
		writeCompanyOpsError(w, http.StatusBadRequest, "invalid_request", "command_id must be a canonical UUID")
		return
	}

	detail, err := h.CompanyOpsOutcomeCenter.GetOutcome(r.Context(), workspaceID, commandID)
	if errors.Is(err, service.ErrCompanyOpsOutcomeNotFound) {
		writeCompanyOpsError(w, http.StatusNotFound, "not_found", "outcome not found in this workspace")
		return
	}
	if err != nil {
		writeCompanyOpsOutcomeServiceError(w, err)
		return
	}

	versions := make([]companyOpsOutcomeVersionResponse, 0, len(detail.Versions))
	for i := range detail.Versions {
		v := detail.Versions[i]
		versions = append(versions, companyOpsOutcomeVersionResponse{
			ID:               v.ID,
			Revision:         v.Revision,
			SupersedesID:     v.SupersedesID,
			DurableObjectRef: v.DurableObjectRef,
			Digest:           v.Digest,
			ContentType:      v.ContentType,
		})
	}

	events := make([]companyOpsOutcomeEventResponse, 0, len(detail.Events))
	for i := range detail.Events {
		events = append(events, companyOpsOutcomeEventWire(detail.Events[i]))
	}

	runs := make([]companyOpsOutcomeRunResponse, 0, len(detail.Runs))
	for i := range detail.Runs {
		r := detail.Runs[i]
		run := companyOpsOutcomeRunResponse{
			TaskID:        r.TaskID,
			Status:        r.Status,
			OutputDigest:  r.OutputDigest,
			TerminalError: r.TerminalError,
		}
		if r.CompletedAt != nil {
			run.CompletedAt = *r.CompletedAt
		}
		runs = append(runs, run)
	}

	writeJSON(w, http.StatusOK, companyOpsOutcomeDetailResponse{
		SchemaVersion: service.CompanyOpsOutcomeCenterSchemaVersion,
		Summary:       companyOpsOutcomeSummaryWire(detail.Summary),
		Versions:      versions,
		Events:        events,
		Runs:          runs,
	})
}

func companyOpsOutcomeEventWire(e service.CompanyOpsOutcomeEvent) companyOpsOutcomeEventResponse {
	formalArtifactRef := ""
	if e.Type == "authority_readback_confirmed" {
		formalArtifactRef = e.FormalArtifactRef
	}
	return companyOpsOutcomeEventResponse{
		ID:                e.ID,
		Sequence:          e.Sequence,
		Type:              e.Type,
		CandidateID:       e.CandidateID,
		CandidateRevision: e.CandidateRevision,
		FormalArtifactRef: formalArtifactRef,
	}
}

func companyOpsOutcomeSummaryWire(s service.CompanyOpsOutcomeSummary) companyOpsOutcomeSummaryResponse {
	resp := companyOpsOutcomeSummaryResponse{
		ID: s.ID,
		Issue: companyOpsOutcomeIssueResponse{
			ID:         s.Issue.ID,
			Number:     s.Issue.Number,
			Identifier: s.Issue.Identifier,
			Title:      s.Issue.Title,
			Status:     s.Issue.Status,
			ProjectID:  s.Issue.ProjectID,
		},
		WorkOrder: companyOpsOutcomeWorkOrderResponse{
			SourceRef: s.WorkOrder.SourceRef,
			Revision:  s.WorkOrder.Revision,
			Digest:    s.WorkOrder.Digest,
		},
		Employee: companyOpsOutcomeEntityResponse{
			SourceRef: s.Employee.SourceRef,
			ID:        s.Employee.ID,
		},
		IdentityBinding: companyOpsOutcomeEntityResponse{
			SourceRef: s.IdentityBinding.SourceRef,
			ID:        s.IdentityBinding.ID,
		},
		ExecutionTarget: companyOpsOutcomeExecTargetResponse{
			LocalAgentID:  s.ExecutionTarget.LocalAgentID,
			AgentRef:      s.ExecutionTarget.AgentRef,
			AgentRevision: s.ExecutionTarget.AgentRevision,
			AgentDigest:   s.ExecutionTarget.AgentDigest,
		},
		CurrentAgentDisplay: companyOpsOutcomeAgentDisplayResponse{
			Name:   s.CurrentAgentDisplay.Name,
			Model:  s.CurrentAgentDisplay.Model,
			Status: s.CurrentAgentDisplay.Status,
		},
		InitialTaskID:  s.InitialTaskID,
		CurrentTaskID:  s.CurrentTaskID,
		ExecutionState: s.ExecutionState,
		VersionCount:   s.VersionCount,
	}
	if s.ActiveArtifact != nil {
		formalVisible := s.ActiveArtifact.Status == "authority_readback_confirmed" &&
			s.ActiveArtifact.FormalVisible && strings.TrimSpace(s.ActiveArtifact.FormalArtifactRef) != ""
		formalArtifactRef := ""
		if formalVisible {
			formalArtifactRef = s.ActiveArtifact.FormalArtifactRef
		}
		resp.ActiveArtifact = &companyOpsOutcomeArtifactResponse{
			ID:                s.ActiveArtifact.ID,
			Revision:          s.ActiveArtifact.Revision,
			DurableObjectRef:  s.ActiveArtifact.DurableObjectRef,
			Digest:            s.ActiveArtifact.Digest,
			ContentType:       s.ActiveArtifact.ContentType,
			Status:            s.ActiveArtifact.Status,
			FormalVisible:     formalVisible,
			FormalArtifactRef: formalArtifactRef,
		}
	}
	if s.LatestEventAt != nil {
		resp.LatestEventAt = *s.LatestEventAt
	}
	return resp
}

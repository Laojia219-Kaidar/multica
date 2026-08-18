package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/companyops"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	companyOpsWorkContextSchemaVersion             = "hivecrew.owner-work-context.v1"
	companyOpsAssignmentSchemaVersion              = "hivecrew.assignment-dispatch.v1"
	companyOpsArtifactReviewSchemaVersion          = "hivecrew.artifact-review.v1"
	companyOpsFormalArtifactPromotionSchemaVersion = "hivecrew.formal-artifact-promotion.v1"
	maxCompanyOpsAssignmentBodySize                = 64 << 10
)

type companyOpsSelectors struct {
	WorkOrderSourceRef string `json:"work_order_source_ref"`
	EmployeeID         string `json:"employee_id"`
	IdentityBindingID  string `json:"identity_binding_id"`
	AgentID            string `json:"agent_id"`
	SessionID          string `json:"session_id"`
}

func (s companyOpsSelectors) lookup() companyops.HiveCosmAuthorityLookup {
	return companyops.HiveCosmAuthorityLookup{
		WorkOrderSourceRef: s.WorkOrderSourceRef,
		EmployeeID:         s.EmployeeID,
		IdentityBindingID:  s.IdentityBindingID,
		AgentID:            s.AgentID,
	}
}

type companyOpsAuthorityResponse struct {
	Kind          string `json:"kind"`
	SourceRef     string `json:"source_ref"`
	Revision      string `json:"revision"`
	ContentDigest string `json:"content_digest"`
	Freshness     string `json:"freshness"`
	DisplayName   string `json:"display_name,omitempty"`
	Model         string `json:"model,omitempty"`
}

type companyOpsBindingResponse struct {
	IdentityBindingID string                      `json:"identity_binding_id"`
	EmployeeRef       string                      `json:"employee_ref"`
	AgentRef          string                      `json:"agent_ref"`
	Active            bool                        `json:"active"`
	Authority         companyOpsAuthorityResponse `json:"authority"`
}

type companyOpsEmployeeResponse struct {
	EmployeeID string                      `json:"employee_id"`
	Authority  companyOpsAuthorityResponse `json:"authority"`
}

type companyOpsAgentResponse struct {
	ID          string                      `json:"id"`
	Name        string                      `json:"name"`
	Status      string                      `json:"status"`
	RuntimeMode string                      `json:"runtime_mode"`
	Model       string                      `json:"model,omitempty"`
	Authority   companyOpsAuthorityResponse `json:"authority"`
}

type companyOpsSessionResponse struct {
	ID      string `json:"id"`
	AgentID string `json:"agent_id"`
	Status  string `json:"status"`
}

type companyOpsIssueResponse struct {
	ID         string `json:"id"`
	Number     int32  `json:"number"`
	Title      string `json:"title"`
	Status     string `json:"status"`
	AssigneeID string `json:"assignee_id,omitempty"`
	ProjectID  string `json:"project_id,omitempty"`
}

type companyOpsWorkContextResponse struct {
	SchemaVersion   string                      `json:"schema_version"`
	Request         companyOpsSelectors         `json:"request"`
	WorkOrder       companyOpsAuthorityResponse `json:"work_order"`
	Employee        companyOpsEmployeeResponse  `json:"employee"`
	IdentityBinding companyOpsBindingResponse   `json:"identity_binding"`
	Agent           companyOpsAgentResponse     `json:"agent"`
	Session         companyOpsSessionResponse   `json:"session"`
	Issue           *companyOpsIssueResponse    `json:"issue"`
	ProjectionState string                      `json:"projection_state"`
	ObservedAt      string                      `json:"observed_at"`
	Outcome         *companyOpsOutcomeResponse  `json:"outcome,omitempty"`
}

type companyOpsArtifactResponse struct {
	ID                string `json:"id"`
	Revision          int    `json:"revision"`
	DurableObjectRef  string `json:"durable_object_ref"`
	Digest            string `json:"digest"`
	Status            string `json:"status"`
	FormalVisible     bool   `json:"formal_visible"`
	FormalArtifactRef string `json:"formal_artifact_ref,omitempty"`
}

type companyOpsOutcomeResponse struct {
	CommandID      string                      `json:"command_id"`
	IssueID        string                      `json:"issue_id"`
	InitialTaskID  string                      `json:"initial_task_id"`
	CurrentTaskID  string                      `json:"current_task_id"`
	ExecutionState string                      `json:"execution_state"`
	Artifact       *companyOpsArtifactResponse `json:"artifact,omitempty"`
}

type createCompanyOpsAssignmentRequest struct {
	CommandID          string `json:"command_id"`
	WorkOrderSourceRef string `json:"work_order_source_ref"`
	EmployeeID         string `json:"employee_id"`
	IdentityBindingID  string `json:"identity_binding_id"`
	AgentID            string `json:"agent_id"`
	SessionID          string `json:"session_id"`
	ProjectID          string `json:"project_id,omitempty"`
	HandoffNote        string `json:"handoff_note"`
}

type companyOpsAssignmentResponse struct {
	SchemaVersion    string                             `json:"schema_version"`
	CommandID        string                             `json:"command_id"`
	IssueID          string                             `json:"issue_id"`
	InitialTaskID    string                             `json:"initial_task_id"`
	ProjectID        string                             `json:"project_id,omitempty"`
	ExecutionReceipt companyOpsExecutionReceiptResponse `json:"execution_receipt"`
}

type companyOpsExecutionReceiptResponse struct {
	State  string `json:"state"`
	TaskID string `json:"task_id"`
}

type companyOpsErrorResponse struct {
	Error      string `json:"error"`
	ReasonCode string `json:"reason_code"`
}

type createCompanyOpsArtifactReviewRequest struct {
	companyOpsSelectors
	CandidateID string `json:"candidate_id"`
	Decision    string `json:"decision"`
	ReviewID    string `json:"review_id"`
	Feedback    string `json:"feedback"`
}

type companyOpsArtifactReviewResponse struct {
	SchemaVersion string `json:"schema_version"`
	ReviewID      string `json:"review_id"`
	EventID       string `json:"event_id"`
	Sequence      int    `json:"sequence"`
	Decision      string `json:"decision"`
	CandidateID   string `json:"candidate_id"`
	ReworkTaskID  string `json:"rework_task_id,omitempty"`
}

type createCompanyOpsFormalArtifactPromotionRequest struct {
	companyOpsSelectors
	CandidateID string `json:"candidate_id"`
	PromotionID string `json:"promotion_id"`
}

type companyOpsFormalArtifactPromotionResponse struct {
	SchemaVersion     string `json:"schema_version"`
	PromotionID       string `json:"promotion_id"`
	CandidateID       string `json:"candidate_id"`
	LifecycleStatus   string `json:"lifecycle_status"`
	FormalArtifactRef string `json:"formal_artifact_ref,omitempty"`
	FormalVisible     bool   `json:"formal_visible"`
	WritePerformed    bool   `json:"write_performed"`
	EventID           string `json:"event_id"`
	Sequence          int    `json:"sequence"`
}

// GetCompanyOpsWorkContext resolves the complete read authority and exact
// handling session. It performs no projection or assignment write.
func (h *Handler) GetCompanyOpsWorkContext(w http.ResponseWriter, r *http.Request) {
	selectors, err := companyOpsSelectorsFromQuery(r)
	if err != nil {
		writeCompanyOpsError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	workspaceID, actorUserID, session, resolved, ok := h.resolveCompanyOpsRequest(w, r, selectors)
	if !ok {
		return
	}

	issue, state, err := h.readCompanyOpsIssueProjectionForGet(r, workspaceID, selectors.lookup(), resolved)
	if err != nil {
		writeCompanyOpsError(w, http.StatusConflict, "projection_conflict", err.Error())
		return
	}
	_ = actorUserID
	var outcome *service.CompanyOpsArtifactOutcome
	if issue.ID.Valid && h.CompanyOpsArtifacts != nil {
		outcome, err = h.CompanyOpsArtifacts.GetIssueOutcome(r.Context(), workspaceID, issue.ID)
		if err != nil {
			writeCompanyOpsServiceError(w, err)
			return
		}
	}

	response := companyOpsWorkContextResponse{
		SchemaVersion: companyOpsWorkContextSchemaVersion,
		Request:       selectors,
		WorkOrder:     companyOpsAuthorityWire(resolved.WorkOrder),
		Employee: companyOpsEmployeeResponse{
			EmployeeID: selectors.EmployeeID,
			Authority:  companyOpsAuthorityWire(resolved.Employee),
		},
		IdentityBinding: companyOpsBindingResponse{
			IdentityBindingID: selectors.IdentityBindingID,
			EmployeeRef:       resolved.IdentityBinding.EmployeeRef,
			AgentRef:          resolved.IdentityBinding.AgentRef,
			Active:            resolved.IdentityBinding.Active,
			Authority:         companyOpsAuthorityWire(resolved.IdentityBinding.Authority),
		},
		Agent: companyOpsAgentResponse{
			ID:          selectors.AgentID,
			Name:        resolved.Agent.Name,
			Status:      resolved.Agent.Status,
			RuntimeMode: resolved.Agent.RuntimeMode,
			Model:       resolved.AgentAuthority.Model,
			Authority:   companyOpsAuthorityWire(resolved.AgentAuthority),
		},
		Session: companyOpsSessionResponse{
			ID:      selectors.SessionID,
			AgentID: selectors.AgentID,
			Status:  session.Status,
		},
		Issue:           companyOpsIssueWire(issue),
		ProjectionState: state,
		ObservedAt:      time.Now().UTC().Format(time.RFC3339Nano),
		Outcome:         companyOpsOutcomeWire(outcome),
	}
	writeJSON(w, http.StatusOK, response)
}

// CreateCompanyOpsAssignment re-observes all authority, projects the
// WorkOrder through IssueService, and dispatches through the sole CompanyOps
// assignment writer. The browser cannot provide revisions or digests.
func (h *Handler) CreateCompanyOpsAssignment(w http.ResponseWriter, r *http.Request) {
	request, err := decodeCompanyOpsAssignmentRequest(r)
	if err != nil {
		writeCompanyOpsError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	selectors := companyOpsSelectors{
		WorkOrderSourceRef: request.WorkOrderSourceRef,
		EmployeeID:         request.EmployeeID,
		IdentityBindingID:  request.IdentityBindingID,
		AgentID:            request.AgentID,
		SessionID:          request.SessionID,
	}
	workspaceID, actorUserID, _, resolved, ok := h.resolveCompanyOpsRequest(w, r, selectors)
	if !ok {
		return
	}
	if h.CompanyOpsAssignment == nil || (request.ProjectID == "" && h.CompanyOpsEnsureWorkOrderIssue == nil) {
		writeCompanyOpsError(w, http.StatusServiceUnavailable, "source_gap", "CompanyOps writer is not configured")
		return
	}

	commandID, err := util.ParseUUID(request.CommandID)
	if err != nil || util.UUIDToString(commandID) != request.CommandID {
		writeCompanyOpsError(w, http.StatusBadRequest, "invalid_request", "command_id must be a canonical UUID")
		return
	}
	projectID := pgtype.UUID{}
	if request.ProjectID != "" {
		projectID, err = util.ParseUUID(request.ProjectID)
		if err != nil || util.UUIDToString(projectID) != request.ProjectID {
			writeCompanyOpsError(w, http.StatusBadRequest, "invalid_request", "project_id must be a canonical UUID")
			return
		}
	}
	issueID := pgtype.UUID{}
	if !projectID.Valid {
		issue, err := h.CompanyOpsEnsureWorkOrderIssue(r.Context(), workspaceID, actorUserID, resolved.WorkOrder)
		if err != nil {
			writeCompanyOpsServiceError(w, err)
			return
		}
		issueID = issue.ID
	}

	assignmentRequest := resolved.AssignmentRequest(
		commandID,
		workspaceID,
		issueID,
		actorUserID,
		request.HandoffNote,
	)
	assignmentRequest.ProjectID = projectID
	receipt, err := h.CompanyOpsAssignment.Dispatch(r.Context(), assignmentRequest)
	if err != nil {
		writeCompanyOpsServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, companyOpsAssignmentResponse{
		SchemaVersion: companyOpsAssignmentSchemaVersion,
		CommandID:     util.UUIDToString(receipt.CommandID),
		IssueID:       util.UUIDToString(receipt.IssueID),
		InitialTaskID: util.UUIDToString(receipt.InitialTaskID),
		ProjectID:     request.ProjectID,
		ExecutionReceipt: companyOpsExecutionReceiptResponse{
			State:  "awaiting_claim",
			TaskID: util.UUIDToString(receipt.InitialTaskID),
		},
	})
}

// CreateCompanyOpsArtifactReview appends an Owner decision to the exact active
// temporary candidate. Detailed feedback remains in the existing Chat; this
// endpoint is the durable lifecycle receipt used by automation and readback.
func (h *Handler) CreateCompanyOpsArtifactReview(w http.ResponseWriter, r *http.Request) {
	if h.CompanyOpsArtifacts == nil {
		writeCompanyOpsError(w, http.StatusServiceUnavailable, "writer_unavailable", "CompanyOps artifact writer is not configured")
		return
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxCompanyOpsAssignmentBodySize+1))
	decoder.DisallowUnknownFields()
	var request createCompanyOpsArtifactReviewRequest
	if err := decoder.Decode(&request); err != nil {
		writeCompanyOpsError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("artifact review request contains multiple JSON values")
		}
		writeCompanyOpsError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	for name, value := range map[string]string{
		"work_order_source_ref": request.WorkOrderSourceRef,
		"employee_id":           request.EmployeeID,
		"identity_binding_id":   request.IdentityBindingID,
		"agent_id":              request.AgentID,
		"session_id":            request.SessionID,
		"candidate_id":          request.CandidateID,
		"review_id":             request.ReviewID,
	} {
		if value == "" || strings.TrimSpace(value) != value {
			writeCompanyOpsError(w, http.StatusBadRequest, "invalid_request", name+" must be a canonical non-empty value")
			return
		}
	}
	for name, value := range map[string]string{"candidate_id": request.CandidateID, "review_id": request.ReviewID} {
		parsed, err := util.ParseUUID(value)
		if err != nil || util.UUIDToString(parsed) != value {
			writeCompanyOpsError(w, http.StatusBadRequest, "invalid_request", name+" must be a canonical UUID")
			return
		}
	}
	decision := companyops.ArtifactEventType(request.Decision)
	if decision != companyops.ArtifactEventChangesRequested && decision != companyops.ArtifactEventApproved {
		writeCompanyOpsError(w, http.StatusBadRequest, "invalid_request", "decision must be changes_requested or approved")
		return
	}
	workspaceID, actorUserID, _, resolved, ok := h.resolveCompanyOpsRequest(w, r, request.companyOpsSelectors)
	if !ok {
		return
	}
	issue, state, err := h.readCompanyOpsIssueProjection(r, workspaceID, resolved.WorkOrder)
	if err != nil || state != "projected" || !issue.ID.Valid {
		writeCompanyOpsError(w, http.StatusConflict, "projection_conflict", "the WorkOrder has no exact local Issue projection")
		return
	}
	receipt, err := h.CompanyOpsArtifacts.ReviewArtifact(r.Context(), workspaceID, issue.ID, service.CompanyOpsArtifactReview{
		CandidateID:   request.CandidateID,
		Decision:      decision,
		IdempotencyID: request.ReviewID,
		Feedback:      request.Feedback,
		ActorUserID:   actorUserID,
	})
	if err != nil {
		writeCompanyOpsServiceError(w, err)
		return
	}
	reworkTaskID := ""
	if receipt.ReworkTask != nil {
		reworkTaskID = util.UUIDToString(receipt.ReworkTask.ID)
	}
	writeJSON(w, http.StatusOK, companyOpsArtifactReviewResponse{
		SchemaVersion: companyOpsArtifactReviewSchemaVersion,
		ReviewID:      request.ReviewID,
		EventID:       receipt.Event.ID,
		Sequence:      receipt.Event.Sequence,
		Decision:      string(receipt.Event.Type),
		CandidateID:   receipt.Event.CandidateID,
		ReworkTaskID:  reworkTaskID,
	})
}

// CreateCompanyOpsFormalArtifactPromotion promotes the exact approved temporary
// candidate into a HiveCosm Formal Artifact and confirms it through an
// independent GET readback. The promotion_id is the caller-owned idempotency
// anchor; replays and retries reuse it so HiveCosm collapses duplicate POSTs.
func (h *Handler) CreateCompanyOpsFormalArtifactPromotion(w http.ResponseWriter, r *http.Request) {
	if h.CompanyOpsArtifacts == nil {
		writeCompanyOpsError(w, http.StatusServiceUnavailable, "writer_unavailable", "CompanyOps artifact writer is not configured")
		return
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxCompanyOpsAssignmentBodySize+1))
	decoder.DisallowUnknownFields()
	var request createCompanyOpsFormalArtifactPromotionRequest
	if err := decoder.Decode(&request); err != nil {
		writeCompanyOpsError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("formal artifact promotion request contains multiple JSON values")
		}
		writeCompanyOpsError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	for name, value := range map[string]string{
		"work_order_source_ref": request.WorkOrderSourceRef,
		"employee_id":           request.EmployeeID,
		"identity_binding_id":   request.IdentityBindingID,
		"agent_id":              request.AgentID,
		"session_id":            request.SessionID,
		"candidate_id":          request.CandidateID,
		"promotion_id":          request.PromotionID,
	} {
		if value == "" || strings.TrimSpace(value) != value {
			writeCompanyOpsError(w, http.StatusBadRequest, "invalid_request", name+" must be a canonical non-empty value")
			return
		}
	}
	for name, value := range map[string]string{"candidate_id": request.CandidateID, "promotion_id": request.PromotionID} {
		parsed, err := util.ParseUUID(value)
		if err != nil || util.UUIDToString(parsed) != value {
			writeCompanyOpsError(w, http.StatusBadRequest, "invalid_request", name+" must be a canonical UUID")
			return
		}
	}
	workspaceID, actorUserID, _, resolved, ok := h.resolveCompanyOpsRequest(w, r, request.companyOpsSelectors)
	if !ok {
		return
	}
	issue, state, err := h.readCompanyOpsIssueProjection(r, workspaceID, resolved.WorkOrder)
	if err != nil || state != "projected" || !issue.ID.Valid {
		writeCompanyOpsError(w, http.StatusConflict, "projection_conflict", "the WorkOrder has no exact local Issue projection")
		return
	}
	receipt, err := h.CompanyOpsArtifacts.PromoteArtifact(r.Context(), workspaceID, issue.ID, service.CompanyOpsArtifactPromotion{
		CandidateID:     request.CandidateID,
		PromotionID:     request.PromotionID,
		ActorUserID:     actorUserID,
		Lookup:          request.lookup(),
		WorkOrder:       resolved.WorkOrder,
		Employee:        resolved.Employee,
		IdentityBinding: resolved.IdentityBinding.Authority,
		Agent:           resolved.AgentAuthority,
	})
	if err != nil {
		var authorityErr *companyops.HiveCosmAuthorityError
		if errors.As(err, &authorityErr) {
			writeCompanyOpsAuthorityError(w, err)
			return
		}
		writeCompanyOpsServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, companyOpsFormalArtifactPromotionResponse{
		SchemaVersion:     companyOpsFormalArtifactPromotionSchemaVersion,
		PromotionID:       receipt.PromotionID,
		CandidateID:       receipt.CandidateID,
		LifecycleStatus:   string(receipt.LifecycleStatus),
		FormalArtifactRef: receipt.FormalArtifactRef,
		FormalVisible:     receipt.FormalVisible,
		WritePerformed:    receipt.WritePerformed,
		EventID:           receipt.TerminalEvent.ID,
		Sequence:          receipt.TerminalEvent.Sequence,
	})
}

func (h *Handler) resolveCompanyOpsRequest(
	w http.ResponseWriter,
	r *http.Request,
	selectors companyOpsSelectors,
) (pgtype.UUID, pgtype.UUID, db.ChatSession, service.ResolvedCompanyOpsAuthority, bool) {
	if h.CompanyOpsAuthority == nil {
		writeCompanyOpsError(w, http.StatusServiceUnavailable, "source_gap", "HiveCosm authority adapter is not configured")
		return pgtype.UUID{}, pgtype.UUID{}, db.ChatSession{}, service.ResolvedCompanyOpsAuthority{}, false
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return pgtype.UUID{}, pgtype.UUID{}, db.ChatSession{}, service.ResolvedCompanyOpsAuthority{}, false
	}
	workspaceIDString := h.resolveWorkspaceID(r)
	workspaceID, err := util.ParseUUID(workspaceIDString)
	if err != nil || !workspaceID.Valid {
		writeCompanyOpsError(w, http.StatusBadRequest, "invalid_request", "workspace_id is required")
		return pgtype.UUID{}, pgtype.UUID{}, db.ChatSession{}, service.ResolvedCompanyOpsAuthority{}, false
	}
	actorUserID, err := util.ParseUUID(userID)
	if err != nil || !actorUserID.Valid {
		writeCompanyOpsError(w, http.StatusUnauthorized, "unauthorized", "user is not a canonical member")
		return pgtype.UUID{}, pgtype.UUID{}, db.ChatSession{}, service.ResolvedCompanyOpsAuthority{}, false
	}
	sessionID, err := util.ParseUUID(selectors.SessionID)
	if err != nil || util.UUIDToString(sessionID) != selectors.SessionID {
		writeCompanyOpsError(w, http.StatusBadRequest, "invalid_request", "session_id must be a canonical UUID")
		return pgtype.UUID{}, pgtype.UUID{}, db.ChatSession{}, service.ResolvedCompanyOpsAuthority{}, false
	}
	session, err := h.Queries.GetChatSessionInWorkspace(r.Context(), db.GetChatSessionInWorkspaceParams{
		ID:          sessionID,
		WorkspaceID: workspaceID,
	})
	if err != nil || session.Status != "active" || util.UUIDToString(session.CreatorID) != userID || util.UUIDToString(session.AgentID) != selectors.AgentID {
		writeCompanyOpsError(w, http.StatusConflict, "session_conflict", "the selected session is not an active conversation for this Owner and Agent")
		return pgtype.UUID{}, pgtype.UUID{}, db.ChatSession{}, service.ResolvedCompanyOpsAuthority{}, false
	}

	resolved, err := h.CompanyOpsAuthority.Resolve(r.Context(), workspaceID, selectors.lookup())
	if err != nil {
		writeCompanyOpsAuthorityError(w, err)
		return pgtype.UUID{}, pgtype.UUID{}, db.ChatSession{}, service.ResolvedCompanyOpsAuthority{}, false
	}
	if resolved.Agent.Kind != "user" || resolved.Agent.ArchivedAt.Valid {
		writeCompanyOpsError(w, http.StatusUnprocessableEntity, "authority_invalid", "the selected Agent is not an active employee execution carrier")
		return pgtype.UUID{}, pgtype.UUID{}, db.ChatSession{}, service.ResolvedCompanyOpsAuthority{}, false
	}
	if !h.canInvokeAgent(r.Context(), resolved.Agent, "member", userID, userID, workspaceIDString) {
		writeCompanyOpsError(w, http.StatusForbidden, "forbidden", "the Owner cannot invoke the selected Agent")
		return pgtype.UUID{}, pgtype.UUID{}, db.ChatSession{}, service.ResolvedCompanyOpsAuthority{}, false
	}
	return workspaceID, actorUserID, session, resolved, true
}

func (h *Handler) readCompanyOpsIssueProjection(
	r *http.Request,
	workspaceID pgtype.UUID,
	workOrder companyops.AuthoritySnapshot,
) (db.Issue, string, error) {
	link, err := h.Queries.GetExternalWorkOrderLink(r.Context(), db.GetExternalWorkOrderLinkParams{
		WorkspaceID:  workspaceID,
		WorkOrderRef: workOrder.SourceRef,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Issue{}, "not_projected", nil
	}
	if err != nil {
		return db.Issue{}, "", fmt.Errorf("read WorkOrder projection link: %w", err)
	}
	if !companyOpsWorkOrderLinkMatches(link.LinkedRevision, link.LinkedDigest, workOrder) {
		return db.Issue{}, "", errors.New("the local WorkOrder projection was linked from a different authority revision")
	}
	issue, err := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{
		ID:          link.IssueID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		return db.Issue{}, "", fmt.Errorf("linked local Issue is unavailable: %w", err)
	}
	return issue, "projected", nil
}

// readCompanyOpsIssueProjectionForGet is the sole stale-link exception. It
// keeps the persisted link immutable and permits only this read endpoint to
// follow a WorkOrder revision advanced by Formal Artifact Promotion, after the
// artifact service independently re-reads and verifies the exact transition.
func (h *Handler) readCompanyOpsIssueProjectionForGet(
	r *http.Request,
	workspaceID pgtype.UUID,
	lookup companyops.HiveCosmAuthorityLookup,
	resolved service.ResolvedCompanyOpsAuthority,
) (db.Issue, string, error) {
	workOrder := resolved.WorkOrder
	link, err := h.Queries.GetExternalWorkOrderLink(r.Context(), db.GetExternalWorkOrderLinkParams{
		WorkspaceID:  workspaceID,
		WorkOrderRef: workOrder.SourceRef,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Issue{}, "not_projected", nil
	}
	if err != nil {
		return db.Issue{}, "", fmt.Errorf("read WorkOrder projection link: %w", err)
	}
	if !companyOpsWorkOrderLinkMatches(link.LinkedRevision, link.LinkedDigest, workOrder) {
		if h.CompanyOpsArtifacts == nil {
			return db.Issue{}, "", errors.New("the local WorkOrder projection was linked from a different authority revision")
		}
		if err := h.CompanyOpsArtifacts.VerifyWorkOrderTransitionForGet(r.Context(), workspaceID, link.IssueID, service.CompanyOpsWorkOrderTransitionExpectation{
			Lookup:          lookup,
			Employee:        resolved.Employee,
			IdentityBinding: resolved.IdentityBinding.Authority,
			Agent:           resolved.AgentAuthority,
			LocalAgentID:    resolved.Agent.ID,
			PreviousAuthority: companyops.HiveCosmAuthorityTransitionSnapshot{
				Revision:      link.LinkedRevision,
				ContentDigest: link.LinkedDigest,
			},
			ResultingAuthority: workOrder,
		}); err != nil {
			return db.Issue{}, "", fmt.Errorf("verify WorkOrder authority transition: %w", err)
		}
	}
	issue, err := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{
		ID:          link.IssueID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		return db.Issue{}, "", fmt.Errorf("linked local Issue is unavailable: %w", err)
	}
	return issue, "projected", nil
}

func companyOpsWorkOrderLinkMatches(linkedRevision, linkedDigest string, workOrder companyops.AuthoritySnapshot) bool {
	return linkedRevision == workOrder.Revision && linkedDigest == workOrder.ContentDigest
}

func companyOpsSelectorsFromQuery(r *http.Request) (companyOpsSelectors, error) {
	values := r.URL.Query()
	if len(values) != 5 {
		return companyOpsSelectors{}, errors.New("the work-context query must contain exactly the five canonical selectors")
	}
	selectors := companyOpsSelectors{}
	for name, target := range map[string]*string{
		"work_order_source_ref": &selectors.WorkOrderSourceRef,
		"employee_id":           &selectors.EmployeeID,
		"identity_binding_id":   &selectors.IdentityBindingID,
		"agent_id":              &selectors.AgentID,
		"session_id":            &selectors.SessionID,
	} {
		observed := values[name]
		if len(observed) != 1 || observed[0] == "" || strings.TrimSpace(observed[0]) != observed[0] {
			return companyOpsSelectors{}, fmt.Errorf("%s must appear exactly once with a canonical value", name)
		}
		*target = observed[0]
	}
	return selectors, nil
}

func decodeCompanyOpsAssignmentRequest(r *http.Request) (createCompanyOpsAssignmentRequest, error) {
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxCompanyOpsAssignmentBodySize+1))
	decoder.DisallowUnknownFields()
	var request createCompanyOpsAssignmentRequest
	if err := decoder.Decode(&request); err != nil {
		return createCompanyOpsAssignmentRequest{}, fmt.Errorf("decode assignment request: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return createCompanyOpsAssignmentRequest{}, errors.New("assignment request contains multiple JSON values")
		}
		return createCompanyOpsAssignmentRequest{}, fmt.Errorf("decode assignment request tail: %w", err)
	}
	if request.CommandID == "" || request.WorkOrderSourceRef == "" || request.EmployeeID == "" ||
		request.IdentityBindingID == "" || request.AgentID == "" || request.SessionID == "" {
		return createCompanyOpsAssignmentRequest{}, errors.New("all assignment target fields are required")
	}
	for name, value := range map[string]string{
		"command_id":            request.CommandID,
		"work_order_source_ref": request.WorkOrderSourceRef,
		"employee_id":           request.EmployeeID,
		"identity_binding_id":   request.IdentityBindingID,
		"agent_id":              request.AgentID,
		"session_id":            request.SessionID,
	} {
		if strings.TrimSpace(value) != value {
			return createCompanyOpsAssignmentRequest{}, fmt.Errorf("%s must be canonical and must not contain surrounding whitespace", name)
		}
	}
	if request.ProjectID != "" {
		if strings.TrimSpace(request.ProjectID) != request.ProjectID {
			return createCompanyOpsAssignmentRequest{}, errors.New("project_id must be canonical and must not contain surrounding whitespace")
		}
		projectID, err := util.ParseUUID(request.ProjectID)
		if err != nil || util.UUIDToString(projectID) != request.ProjectID {
			return createCompanyOpsAssignmentRequest{}, errors.New("project_id must be a canonical UUID")
		}
	}
	if strings.TrimSpace(request.HandoffNote) == "" {
		return createCompanyOpsAssignmentRequest{}, errors.New("handoff_note must describe the work to dispatch")
	}
	if len(request.HandoffNote) > 32<<10 {
		return createCompanyOpsAssignmentRequest{}, errors.New("handoff_note is too large")
	}
	return request, nil
}

func companyOpsAuthorityWire(snapshot companyops.AuthoritySnapshot) companyOpsAuthorityResponse {
	return companyOpsAuthorityResponse{
		Kind:          snapshot.Kind,
		SourceRef:     snapshot.SourceRef,
		Revision:      snapshot.Revision,
		ContentDigest: snapshot.ContentDigest,
		Freshness:     snapshot.Freshness,
		DisplayName:   snapshot.DisplayName,
		Model:         snapshot.Model,
	}
}

func companyOpsIssueWire(issue db.Issue) *companyOpsIssueResponse {
	if !issue.ID.Valid {
		return nil
	}
	return &companyOpsIssueResponse{
		ID:         util.UUIDToString(issue.ID),
		Number:     issue.Number,
		Title:      issue.Title,
		Status:     issue.Status,
		AssigneeID: util.UUIDToString(issue.AssigneeID),
		ProjectID:  util.UUIDToString(issue.ProjectID),
	}
}

func companyOpsOutcomeWire(outcome *service.CompanyOpsArtifactOutcome) *companyOpsOutcomeResponse {
	if outcome == nil {
		return nil
	}
	response := &companyOpsOutcomeResponse{
		CommandID:      util.UUIDToString(outcome.CommandID),
		IssueID:        util.UUIDToString(outcome.IssueID),
		InitialTaskID:  util.UUIDToString(outcome.InitialTaskID),
		CurrentTaskID:  util.UUIDToString(outcome.CurrentTaskID),
		ExecutionState: outcome.ExecutionState,
	}
	if outcome.Candidate != nil && outcome.Projection != nil {
		response.Artifact = &companyOpsArtifactResponse{
			ID:                outcome.Candidate.ID,
			Revision:          outcome.Candidate.Revision,
			DurableObjectRef:  outcome.Candidate.DurableObjectRef,
			Digest:            outcome.Candidate.Digest,
			Status:            string(outcome.Projection.Status),
			FormalVisible:     outcome.Projection.FormalVisible,
			FormalArtifactRef: outcome.Projection.FormalArtifactRef,
		}
	}
	return response
}

func writeCompanyOpsAuthorityError(w http.ResponseWriter, err error) {
	var authorityErr *companyops.HiveCosmAuthorityError
	if !errors.As(err, &authorityErr) {
		writeCompanyOpsError(w, http.StatusUnprocessableEntity, "authority_invalid", err.Error())
		return
	}
	switch authorityErr.Kind {
	case companyops.HiveCosmAuthorityNotFound:
		writeCompanyOpsError(w, http.StatusNotFound, "not_found", authorityErr.Error())
	case companyops.HiveCosmAuthoritySourceGap:
		writeCompanyOpsError(w, http.StatusServiceUnavailable, "source_gap", authorityErr.Error())
	case companyops.HiveCosmAuthorityUnsupported:
		writeCompanyOpsError(w, http.StatusNotImplemented, "unsupported", authorityErr.Error())
	case companyops.HiveCosmAuthorityConflict:
		writeCompanyOpsError(w, http.StatusConflict, "authority_conflict", authorityErr.Error())
	default:
		writeCompanyOpsError(w, http.StatusUnprocessableEntity, "authority_invalid", authorityErr.Error())
	}
}

func writeCompanyOpsServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, companyops.ErrArtifactIdempotencyRequired):
		writeCompanyOpsError(w, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, service.ErrProjectNotFound):
		writeCompanyOpsError(w, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, service.ErrCompanyOpsAssignmentConflict),
		errors.Is(err, service.ErrExternalWorkOrderLinkConflict),
		errors.Is(err, service.ErrCompanyOpsWorkOrderProjectionConflict),
		errors.Is(err, service.ErrCompanyOpsWorkOrderProjectionOrphan):
		writeCompanyOpsError(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, service.ErrCompanyOpsIssueNotAssignable):
		writeCompanyOpsError(w, http.StatusConflict, "not_assignable", err.Error())
	case errors.Is(err, companyops.ErrArtifactPromotionInProgress):
		writeCompanyOpsError(w, http.StatusConflict, "artifact_promotion_in_progress", err.Error())
	case errors.Is(err, service.ErrCompanyOpsArtifactConflict),
		errors.Is(err, companyops.ErrInvalidArtifactTransition),
		errors.Is(err, companyops.ErrInvalidArtifactCandidate),
		errors.Is(err, companyops.ErrArtifactRevisionMismatch),
		errors.Is(err, companyops.ErrArtifactDigestMismatch),
		errors.Is(err, companyops.ErrArtifactObjectRefMismatch),
		errors.Is(err, companyops.ErrFormalArtifactRefMismatch),
		errors.Is(err, companyops.ErrArtifactPromotionConflict),
		errors.Is(err, companyops.ErrArtifactIdempotencyConflict):
		writeCompanyOpsError(w, http.StatusConflict, "artifact_conflict", err.Error())
	case errors.Is(err, companyops.ErrArtifactCandidateNotFound):
		writeCompanyOpsError(w, http.StatusNotFound, "artifact_not_found", err.Error())
	case errors.Is(err, service.ErrCompanyOpsArtifactUnavailable):
		writeCompanyOpsError(w, http.StatusServiceUnavailable, "writer_unavailable", err.Error())
	case errors.Is(err, service.ErrCompanyOpsCapacityReject):
		writeCompanyOpsError(w, http.StatusConflict, "capacity_reject", err.Error())
	case errors.Is(err, service.ErrCompanyOpsCapacityDefer):
		writeCompanyOpsError(w, http.StatusServiceUnavailable, "capacity_defer", err.Error())
	default:
		writeCompanyOpsError(w, http.StatusServiceUnavailable, "writer_unavailable", err.Error())
	}
}

func writeCompanyOpsError(w http.ResponseWriter, status int, reasonCode, message string) {
	writeJSON(w, status, companyOpsErrorResponse{Error: message, ReasonCode: reasonCode})
}

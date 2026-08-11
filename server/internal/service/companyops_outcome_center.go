package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/companyops"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	CompanyOpsOutcomeCenterSchemaVersion = "hivecrew.outcome-center.v1"
	companyOpsOutcomeCenterMaxLimit      = 100
	companyOpsOutcomeCenterDefaultLimit  = 50
)

var (
	ErrCompanyOpsOutcomeNotFound       = errors.New("companyops outcome not found")
	ErrCompanyOpsOutcomeLedgerConflict = errors.New("companyops outcome ledger conflict")
)

const (
	hiveCosmEmployeeRefPrefix        = "hivecosm://employees/"
	hiveCosmIdentityBindingRefPrefix = "hivecosm://identity-bindings/"
)

var companyOpsOutcomeValidStatuses = map[string]struct{}{
	"awaiting_claim":               {},
	"running":                      {},
	"completed":                    {},
	"failed":                       {},
	"cancelled":                    {},
	"submitted":                    {},
	"changes_requested":            {},
	"approved":                     {},
	"promotion_requested":          {},
	"promotion_succeeded":          {},
	"promotion_failed":             {},
	"authority_readback_confirmed": {},
}

// IsValidCompanyOpsOutcomeStatus reports whether s is in the closed status
// contract accepted by the outcome center list filter.
func IsValidCompanyOpsOutcomeStatus(s string) bool {
	_, ok := companyOpsOutcomeValidStatuses[s]
	return ok
}

// parseHiveCosmEmployeeAuthorityRef extracts the canonical opaque business ID
// from a hivecosm://employees/{opaque-id} URI. It rejects query strings,
// fragments, empty path segments, extra slashes, and non-exact prefixes.
func parseHiveCosmEmployeeAuthorityRef(ref string) (string, error) {
	return parseHiveCosmAuthorityRef(ref, hiveCosmEmployeeRefPrefix, "employee")
}

// parseHiveCosmIdentityBindingAuthorityRef extracts the canonical opaque
// business ID from a hivecosm://identity-bindings/{opaque-id} URI.
func parseHiveCosmIdentityBindingAuthorityRef(ref string) (string, error) {
	return parseHiveCosmAuthorityRef(ref, hiveCosmIdentityBindingRefPrefix, "identity_binding")
}

func parseHiveCosmAuthorityRef(ref, prefix, label string) (string, error) {
	if !strings.HasPrefix(ref, prefix) {
		return "", fmt.Errorf("%w: %s ref is not canonical", ErrCompanyOpsOutcomeLedgerConflict, label)
	}
	rest := ref[len(prefix):]
	if rest == "" || strings.TrimSpace(rest) != rest {
		return "", fmt.Errorf("%w: %s id is missing or non-canonical", ErrCompanyOpsOutcomeLedgerConflict, label)
	}
	if idx := strings.IndexAny(rest, "?#/"); idx >= 0 {
		return "", fmt.Errorf("%w: %s ref must not contain query, fragment, or extra path", ErrCompanyOpsOutcomeLedgerConflict, label)
	}
	if strings.Contains(rest, "/") {
		return "", fmt.Errorf("%w: %s ref must not contain extra path segments", ErrCompanyOpsOutcomeLedgerConflict, label)
	}
	return rest, nil
}

// IsValidCompanyOpsArtifactEventType reports whether s is a recognized
// artifact lifecycle event type stored in artifact_event.event_type.
func IsValidCompanyOpsArtifactEventType(s string) bool {
	switch companyops.ArtifactEventType(s) {
	case companyops.ArtifactEventSubmitted,
		companyops.ArtifactEventChangesRequested,
		companyops.ArtifactEventApproved,
		companyops.ArtifactEventPromotionRequested,
		companyops.ArtifactEventPromotionSucceeded,
		companyops.ArtifactEventPromotionFailed,
		companyops.ArtifactEventAuthorityReadbackConfirmed:
		return true
	}
	return false
}

// CompanyOpsOutcomeListRequest carries the read-only filters for the outcome
// list endpoint. All filters except WorkspaceID are optional.
type CompanyOpsOutcomeListRequest struct {
	WorkspaceID   pgtype.UUID
	Q             string
	Status        string
	AgentID       pgtype.UUID
	ProjectID     pgtype.UUID
	EmployeeID    string
	Type          string
	FormalVisible *bool
	Limit         int
	Offset        int
}

// CompanyOpsOutcomeIssue is the issue projection carried by an outcome summary.
type CompanyOpsOutcomeIssue struct {
	ID         string
	Number     int32
	Identifier string
	Title      string
	Status     string
	ProjectID  string
}

// CompanyOpsOutcomeWorkOrder is the provenance-only WorkOrder link snapshot.
type CompanyOpsOutcomeWorkOrder struct {
	SourceRef string
	Revision  string
	Digest    string
}

// CompanyOpsOutcomeEntity is an employee or identity-binding authority ref.
type CompanyOpsOutcomeEntity struct {
	SourceRef string
	ID        string
}

// CompanyOpsOutcomeExecTarget is the frozen execution target from the receipt.
type CompanyOpsOutcomeExecTarget struct {
	LocalAgentID  string
	AgentRef      string
	AgentRevision string
	AgentDigest   string
}

// CompanyOpsOutcomeAgentDisplay is the current display row for the local
// agent. It is explicitly a display hint and never overwrites the execution
// snapshot frozen in the receipt.
type CompanyOpsOutcomeAgentDisplay struct {
	Name   string
	Model  string
	Status string
}

// CompanyOpsOutcomeArtifact is the active artifact candidate snapshot.
type CompanyOpsOutcomeArtifact struct {
	ID                string
	Revision          int32
	DurableObjectRef  string
	Digest            string
	ContentType       string
	Status            string
	FormalVisible     bool
	FormalArtifactRef string
}

// CompanyOpsOutcomeSummary is the read model for one outcome in the list.
type CompanyOpsOutcomeSummary struct {
	ID                  string
	Issue               CompanyOpsOutcomeIssue
	WorkOrder           CompanyOpsOutcomeWorkOrder
	Employee            CompanyOpsOutcomeEntity
	IdentityBinding     CompanyOpsOutcomeEntity
	ExecutionTarget     CompanyOpsOutcomeExecTarget
	CurrentAgentDisplay CompanyOpsOutcomeAgentDisplay
	InitialTaskID       string
	CurrentTaskID       string
	ExecutionState      string
	ActiveArtifact      *CompanyOpsOutcomeArtifact
	VersionCount        int32
	LatestEventAt       *string
}

// CompanyOpsOutcomeVersion is one candidate revision in the detail view.
type CompanyOpsOutcomeVersion struct {
	ID               string
	Revision         int32
	SupersedesID     string
	DurableObjectRef string
	Digest           string
	ContentType      string
}

// CompanyOpsOutcomeEvent is one lifecycle event in the detail view.
type CompanyOpsOutcomeEvent struct {
	ID                string
	Sequence          int32
	Type              string
	CandidateID       string
	CandidateRevision int32
	FormalArtifactRef string
}

// CompanyOpsOutcomeRun is one execution receipt in the detail view.
type CompanyOpsOutcomeRun struct {
	TaskID        string
	Status        string
	CompletedAt   *string
	OutputDigest  string
	TerminalError string
}

// CompanyOpsOutcomeDetail is the full detail response for one outcome.
type CompanyOpsOutcomeDetail struct {
	Summary  CompanyOpsOutcomeSummary
	Versions []CompanyOpsOutcomeVersion
	Events   []CompanyOpsOutcomeEvent
	Runs     []CompanyOpsOutcomeRun
}

// CompanyOpsOutcomeCenterService is the read-only projection over assignment
// receipts, artifact candidates, artifact events, and execution receipts. It
// never writes and never becomes a second lifecycle authority.
type CompanyOpsOutcomeCenterService struct {
	queries *db.Queries
}

// NewCompanyOpsOutcomeCenterService binds the read model to the shared query
// handle. Production passes db.New(pool); tests pass db.New(pool) against the
// isolated 55432 database.
func NewCompanyOpsOutcomeCenterService(queries *db.Queries) *CompanyOpsOutcomeCenterService {
	return &CompanyOpsOutcomeCenterService{queries: queries}
}

// ListOutcomes returns the paginated, filtered outcome summaries and total
// count. It performs no writes and touches no session or authority adapter.
func (s *CompanyOpsOutcomeCenterService) ListOutcomes(
	ctx context.Context,
	req CompanyOpsOutcomeListRequest,
) ([]CompanyOpsOutcomeSummary, int64, error) {
	if s == nil || s.queries == nil {
		return nil, 0, ErrCompanyOpsArtifactUnavailable
	}
	if !req.WorkspaceID.Valid || req.WorkspaceID.Bytes == ([16]byte{}) {
		return nil, 0, fmt.Errorf("workspace_id is required")
	}

	limit := req.Limit
	if limit <= 0 {
		limit = companyOpsOutcomeCenterDefaultLimit
	}
	if limit > companyOpsOutcomeCenterMaxLimit {
		limit = companyOpsOutcomeCenterMaxLimit
	}
	offset := req.Offset
	if offset < 0 {
		offset = 0
	}

	params := db.ListCompanyOpsOutcomeRowsParams{
		WorkspaceID: req.WorkspaceID,
		LimitRows:   int32(limit),
		OffsetRows:  int32(offset),
	}
	if q := strings.TrimSpace(req.Q); q != "" {
		params.QText = pgtype.Text{String: q, Valid: true}
	}
	if req.AgentID.Valid && req.AgentID.Bytes != ([16]byte{}) {
		params.AgentFilter = req.AgentID
	}
	if req.ProjectID.Valid && req.ProjectID.Bytes != ([16]byte{}) {
		params.ProjectFilter = req.ProjectID
	}
	if status := strings.TrimSpace(req.Status); status != "" {
		params.StatusFilter = pgtype.Text{String: status, Valid: true}
	}
	if employeeID := strings.TrimSpace(req.EmployeeID); employeeID != "" {
		params.EmployeeFilter = pgtype.Text{String: employeeID, Valid: true}
	}
	if contentType := strings.TrimSpace(req.Type); contentType != "" {
		params.TypeFilter = pgtype.Text{String: contentType, Valid: true}
	}
	if req.FormalVisible != nil {
		params.FormalVisibleFilter = pgtype.Bool{Bool: *req.FormalVisible, Valid: true}
	}

	rows, err := s.queries.ListCompanyOpsOutcomeRows(ctx, params)
	if err != nil {
		return nil, 0, fmt.Errorf("list companyops outcome rows: %w", err)
	}

	summaries := make([]CompanyOpsOutcomeSummary, 0, len(rows))
	for i := range rows {
		summary, err := companyOpsOutcomeSummaryFromRow(rows[i])
		if err != nil {
			return nil, 0, err
		}
		summaries = append(summaries, summary)
	}

	total, err := s.queries.CountCompanyOpsOutcomeRows(ctx, db.CountCompanyOpsOutcomeRowsParams{
		WorkspaceID:         params.WorkspaceID,
		QText:               params.QText,
		AgentFilter:         params.AgentFilter,
		ProjectFilter:       params.ProjectFilter,
		EmployeeFilter:      params.EmployeeFilter,
		TypeFilter:          params.TypeFilter,
		StatusFilter:        params.StatusFilter,
		FormalVisibleFilter: params.FormalVisibleFilter,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("count companyops outcome rows: %w", err)
	}

	return summaries, total, nil
}

// GetOutcome returns the full detail for one outcome keyed by its stable
// assignment command_id. The command_id is the Outcome identity; the
// candidate/task ids are versions, not the identity.
func (s *CompanyOpsOutcomeCenterService) GetOutcome(
	ctx context.Context,
	workspaceID pgtype.UUID,
	commandID pgtype.UUID,
) (*CompanyOpsOutcomeDetail, error) {
	if s == nil || s.queries == nil {
		return nil, ErrCompanyOpsArtifactUnavailable
	}
	receiptRow, err := s.queries.GetAssignmentDispatchReceipt(ctx, db.GetAssignmentDispatchReceiptParams{
		WorkspaceID: workspaceID,
		CommandID:   commandID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCompanyOpsOutcomeNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read assignment dispatch receipt: %w", err)
	}
	receipt := assignmentDispatchReceiptFromDB(receiptRow)

	// Validate canonical authority IDs from the immutable receipt. A
	// non-canonical employee or identity-binding ref means the ledger row was
	// written outside the closed contract — surface as a conflict.
	if _, err := parseHiveCosmEmployeeAuthorityRef(receipt.Target.EmployeeRef); err != nil {
		return nil, err
	}
	if _, err := parseHiveCosmIdentityBindingAuthorityRef(receipt.Target.BindingRef); err != nil {
		return nil, err
	}

	issue, err := s.queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{
		ID:          receipt.IssueID,
		WorkspaceID: workspaceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: orphan issue for assignment receipt", ErrCompanyOpsOutcomeLedgerConflict)
	}
	if err != nil {
		return nil, fmt.Errorf("read linked issue: %w", err)
	}

	workspace, err := s.queries.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("read workspace: %w", err)
	}

	agent, err := s.queries.GetAgent(ctx, receipt.LocalAgentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: orphan local agent for assignment receipt", ErrCompanyOpsOutcomeLedgerConflict)
	}
	if err != nil {
		return nil, fmt.Errorf("read local agent: %w", err)
	}

	candidateRows, err := s.queries.ListArtifactCandidatesByLineage(ctx, db.ListArtifactCandidatesByLineageParams{
		WorkspaceID: workspaceID,
		LineageID:   receipt.CommandID,
	})
	if err != nil {
		return nil, fmt.Errorf("list artifact candidates: %w", err)
	}
	eventRows, err := s.queries.ListArtifactEventsByLineage(ctx, db.ListArtifactEventsByLineageParams{
		WorkspaceID: workspaceID,
		LineageID:   receipt.CommandID,
	})
	if err != nil {
		return nil, fmt.Errorf("list artifact events: %w", err)
	}
	runRows, err := s.queries.ListExecutionReceiptsByAssignmentCommand(ctx, db.ListExecutionReceiptsByAssignmentCommandParams{
		WorkspaceID:         workspaceID,
		AssignmentCommandID: receipt.CommandID,
	})
	if err != nil {
		return nil, fmt.Errorf("list execution receipts: %w", err)
	}

	projection, latestCandidate := companyOpsOutcomeComputeProjection(candidateRows, eventRows)

	// Reject unknown artifact event types in the ledger as conflicts.
	if projection.Status != "" && !IsValidCompanyOpsArtifactEventType(string(projection.Status)) {
		return nil, fmt.Errorf("%w: unknown artifact event type %q", ErrCompanyOpsOutcomeLedgerConflict, projection.Status)
	}

	// formal_visible triple-gate: confirmed status requires a non-empty
	// formal_artifact_ref. If confirmed but ref is empty, the ledger is
	// inconsistent.
	if projection.Status == companyops.ArtifactEventAuthorityReadbackConfirmed &&
		strings.TrimSpace(projection.FormalArtifactRef) == "" {
		return nil, fmt.Errorf("%w: authority_readback_confirmed without formal_artifact_ref", ErrCompanyOpsOutcomeLedgerConflict)
	}

	currentTaskID := receipt.InitialTaskID
	if latestCandidate != nil {
		currentTaskID = latestCandidate.ID
	}
	if projection.Status == companyops.ArtifactEventChangesRequested && latestCandidate != nil {
		lastEventID := companyOpsOutcomeLastEventForCandidate(eventRows, latestCandidate.ID)
		if !lastEventID.Valid {
			return nil, fmt.Errorf("%w: changes_requested event missing for active candidate", ErrCompanyOpsOutcomeLedgerConflict)
		}
		reworkCount, reworkCountErr := s.queries.CountCompanyOpsTasksByTriggerEvidence(ctx, db.CountCompanyOpsTasksByTriggerEvidenceParams{
			IssueID:              receipt.IssueID,
			TriggerEvidenceKind:  pgtype.Text{String: artifactRevisionEvidenceKind, Valid: true},
			TriggerEvidenceRefID: lastEventID,
		})
		if reworkCountErr != nil {
			return nil, fmt.Errorf("count artifact rework tasks: %w", reworkCountErr)
		}
		if reworkCount != 1 {
			return nil, fmt.Errorf("%w: changes_requested rework task count = %d, expected exactly 1", ErrCompanyOpsOutcomeLedgerConflict, reworkCount)
		}
		reworkTask, reworkErr := s.queries.GetCompanyOpsTaskByTriggerEvidence(ctx, db.GetCompanyOpsTaskByTriggerEvidenceParams{
			IssueID:              receipt.IssueID,
			TriggerEvidenceKind:  pgtype.Text{String: artifactRevisionEvidenceKind, Valid: true},
			TriggerEvidenceRefID: lastEventID,
		})
		if reworkErr != nil {
			return nil, fmt.Errorf("read artifact rework task: %w", reworkErr)
		}
		currentTaskID = reworkTask.ID
	}

	executionState, err := companyOpsCurrentExecutionState(ctx, s.queries, currentTaskID)
	if err != nil {
		return nil, err
	}

	summary, err := companyOpsOutcomeSummaryFromParts(
		receipt,
		issue,
		workspace.IssuePrefix,
		agent,
		projection,
		latestCandidate,
		currentTaskID,
		executionState,
		len(candidateRows),
		companyOpsLatestEventAt(eventRows),
	)
	if err != nil {
		return nil, err
	}

	versions := make([]CompanyOpsOutcomeVersion, 0, len(candidateRows))
	for i := range candidateRows {
		c := candidateRows[i]
		versions = append(versions, CompanyOpsOutcomeVersion{
			ID:               util.UUIDToString(c.ID),
			Revision:         c.Revision,
			SupersedesID:     util.UUIDToString(c.SupersedesID),
			DurableObjectRef: c.DurableObjectRef,
			Digest:           c.Digest,
			ContentType:      c.ContentType,
		})
	}

	events := make([]CompanyOpsOutcomeEvent, 0, len(eventRows))
	for i := range eventRows {
		e := eventRows[i]
		formalArtifactRef := ""
		if e.EventType == string(companyops.ArtifactEventAuthorityReadbackConfirmed) {
			formalArtifactRef = e.FormalArtifactRef.String
		}
		events = append(events, CompanyOpsOutcomeEvent{
			ID:                util.UUIDToString(e.ID),
			Sequence:          e.Sequence,
			Type:              e.EventType,
			CandidateID:       util.UUIDToString(e.CandidateID),
			CandidateRevision: e.CandidateRevision,
			FormalArtifactRef: formalArtifactRef,
		})
	}

	runs := make([]CompanyOpsOutcomeRun, 0, len(runRows))
	for i := range runRows {
		run, runErr := companyOpsOutcomeRunFromRow(runRows[i])
		if runErr != nil {
			return nil, runErr
		}
		runs = append(runs, run)
	}

	return &CompanyOpsOutcomeDetail{
		Summary:  summary,
		Versions: versions,
		Events:   events,
		Runs:     runs,
	}, nil
}

func companyOpsOutcomeRunFromRow(row db.ExecutionReceipt) (CompanyOpsOutcomeRun, error) {
	run := CompanyOpsOutcomeRun{
		TaskID:        util.UUIDToString(row.TaskID),
		Status:        "running",
		OutputDigest:  row.OutputDigest.String,
		TerminalError: row.TerminalError.String,
	}
	if !row.TerminalStatus.Valid {
		return run, nil
	}
	switch row.TerminalStatus.String {
	case "completed", "failed", "cancelled":
		run.Status = row.TerminalStatus.String
	default:
		return CompanyOpsOutcomeRun{}, fmt.Errorf(
			"%w: unknown execution receipt terminal status %q",
			ErrCompanyOpsOutcomeLedgerConflict,
			row.TerminalStatus.String,
		)
	}
	if !row.CompletedAt.Valid {
		return CompanyOpsOutcomeRun{}, fmt.Errorf(
			"%w: terminal execution receipt missing completed_at",
			ErrCompanyOpsOutcomeLedgerConflict,
		)
	}
	run.CompletedAt = ptrString(row.CompletedAt.Time.UTC().Format("2006-01-02T15:04:05.999999999Z"))
	return run, nil
}

func companyOpsOutcomeSummaryFromRow(row db.ListCompanyOpsOutcomeRowsRow) (CompanyOpsOutcomeSummary, error) {
	identifier := ""
	if row.WorkspaceIssuePrefix.Valid && row.IssueNumber.Valid {
		identifier = fmt.Sprintf("%s-%d", row.WorkspaceIssuePrefix.String, row.IssueNumber.Int32)
	}

	employeeID, empErr := parseHiveCosmEmployeeAuthorityRef(row.EmployeeRef)
	if empErr != nil {
		return CompanyOpsOutcomeSummary{}, empErr
	}
	bindingID, bindErr := parseHiveCosmIdentityBindingAuthorityRef(row.BindingRef)
	if bindErr != nil {
		return CompanyOpsOutcomeSummary{}, bindErr
	}

	// When the latest lifecycle event is changes_requested, current_task_id
	// must resolve to exactly one rework task matching the active candidate.
	// Missing or duplicate rework tasks mean the ledger is inconsistent.
	if row.ArtifactLifecycleStatus == string(companyops.ArtifactEventChangesRequested) {
		if row.ReworkTaskCount != 1 {
			return CompanyOpsOutcomeSummary{}, fmt.Errorf(
				"%w: changes_requested rework task count = %d, expected exactly 1",
				ErrCompanyOpsOutcomeLedgerConflict, row.ReworkTaskCount,
			)
		}
	}

	currentTaskID := row.InitialTaskID
	if row.CurrentTaskID.Valid {
		currentTaskID = row.CurrentTaskID
	}

	summary := CompanyOpsOutcomeSummary{
		ID: util.UUIDToString(row.CommandID),
		Issue: CompanyOpsOutcomeIssue{
			ID:         util.UUIDToString(row.IssueID),
			Number:     row.IssueNumber.Int32,
			Identifier: identifier,
			Title:      row.IssueTitle.String,
			Status:     row.IssueStatus.String,
			ProjectID:  util.UUIDToString(row.IssueProjectID),
		},
		WorkOrder: CompanyOpsOutcomeWorkOrder{
			SourceRef: row.WorkOrderRef,
			Revision:  row.WorkOrderRevision,
			Digest:    row.WorkOrderDigest,
		},
		Employee: CompanyOpsOutcomeEntity{
			SourceRef: row.EmployeeRef,
			ID:        employeeID,
		},
		IdentityBinding: CompanyOpsOutcomeEntity{
			SourceRef: row.BindingRef,
			ID:        bindingID,
		},
		ExecutionTarget: CompanyOpsOutcomeExecTarget{
			LocalAgentID:  util.UUIDToString(row.LocalAgentID),
			AgentRef:      row.AgentRef,
			AgentRevision: row.AgentRevision,
			AgentDigest:   row.AgentDigest,
		},
		CurrentAgentDisplay: CompanyOpsOutcomeAgentDisplay{
			Name:   row.AgentDisplayName.String,
			Model:  row.AgentModel.String,
			Status: row.AgentRuntimeStatus.String,
		},
		InitialTaskID:  util.UUIDToString(row.InitialTaskID),
		CurrentTaskID:  util.UUIDToString(currentTaskID),
		ExecutionState: companyOpsOutcomeExecutionStateFromRow(row),
		VersionCount:   row.VersionCount,
	}

	if row.ArtifactCandidateID.Valid {
		if row.ArtifactLifecycleStatus == string(companyops.ArtifactEventAuthorityReadbackConfirmed) &&
			strings.TrimSpace(row.ArtifactFormalRef.String) == "" {
			return CompanyOpsOutcomeSummary{}, fmt.Errorf(
				"%w: authority_readback_confirmed without formal_artifact_ref",
				ErrCompanyOpsOutcomeLedgerConflict,
			)
		}
		formalVisible := row.ArtifactLifecycleStatus == string(companyops.ArtifactEventAuthorityReadbackConfirmed) &&
			strings.TrimSpace(row.ArtifactFormalRef.String) != ""
		formalArtifactRef := ""
		if formalVisible {
			formalArtifactRef = row.ArtifactFormalRef.String
		}
		summary.ActiveArtifact = &CompanyOpsOutcomeArtifact{
			ID:                util.UUIDToString(row.ArtifactCandidateID),
			Revision:          row.ArtifactCandidateRevision,
			DurableObjectRef:  row.ArtifactDurableObjectRef,
			Digest:            row.ArtifactDigest,
			ContentType:       row.ArtifactContentType,
			Status:            row.ArtifactLifecycleStatus,
			FormalVisible:     formalVisible,
			FormalArtifactRef: formalArtifactRef,
		}
	}

	if row.LatestEventAt != nil {
		summary.LatestEventAt = companyOpsLatestEventAtRaw(row.LatestEventAt)
	}

	return summary, nil
}

func companyOpsOutcomeExecutionStateFromRow(row db.ListCompanyOpsOutcomeRowsRow) string {
	if row.CurrentExecutionTerminalStatus.Valid {
		return row.CurrentExecutionTerminalStatus.String
	}
	return companyOpsOutcomeTaskStatusToExecutionState(row.CurrentTaskStatus)
}

func companyOpsOutcomeTaskStatusToExecutionState(status pgtype.Text) string {
	if !status.Valid {
		return "awaiting_claim"
	}
	switch status.String {
	case "queued", "dispatched", "waiting_local_directory", "deferred":
		return "awaiting_claim"
	default:
		return status.String
	}
}

func companyOpsOutcomeLastEventForCandidate(eventRows []db.ArtifactEvent, candidateID pgtype.UUID) pgtype.UUID {
	for i := len(eventRows) - 1; i >= 0; i-- {
		if eventRows[i].CandidateID == candidateID {
			return eventRows[i].ID
		}
	}
	return pgtype.UUID{}
}

func companyOpsOutcomeComputeProjection(
	candidateRows []db.ArtifactCandidate,
	eventRows []db.ArtifactEvent,
) (companyops.ArtifactLifecycleProjection, *db.ArtifactCandidate) {
	if len(candidateRows) == 0 {
		return companyops.ArtifactLifecycleProjection{}, nil
	}
	var latest db.ArtifactCandidate
	for i := range candidateRows {
		if candidateRows[i].Revision > latest.Revision {
			latest = candidateRows[i]
		}
	}
	projection := companyops.ArtifactLifecycleProjection{
		CandidateID:       util.UUIDToString(latest.ID),
		CandidateRevision: int(latest.Revision),
	}
	for i := len(eventRows) - 1; i >= 0; i-- {
		if eventRows[i].CandidateID == latest.ID {
			projection.Status = companyops.ArtifactEventType(eventRows[i].EventType)
			if projection.Status == companyops.ArtifactEventAuthorityReadbackConfirmed {
				projection.FormalVisible = true
				projection.FormalArtifactRef = eventRows[i].FormalArtifactRef.String
			}
			break
		}
	}
	return projection, &latest
}

func companyOpsOutcomeSummaryFromParts(
	receipt AssignmentDispatchReceipt,
	issue db.Issue,
	issuePrefix string,
	agent db.Agent,
	projection companyops.ArtifactLifecycleProjection,
	latestCandidate *db.ArtifactCandidate,
	currentTaskID pgtype.UUID,
	executionState string,
	versionCount int,
	latestEventAt *string,
) (CompanyOpsOutcomeSummary, error) {
	identifier := fmt.Sprintf("%s-%d", issuePrefix, issue.Number)
	employeeID, empErr := parseHiveCosmEmployeeAuthorityRef(receipt.Target.EmployeeRef)
	if empErr != nil {
		return CompanyOpsOutcomeSummary{}, empErr
	}
	bindingID, bindErr := parseHiveCosmIdentityBindingAuthorityRef(receipt.Target.BindingRef)
	if bindErr != nil {
		return CompanyOpsOutcomeSummary{}, bindErr
	}
	summary := CompanyOpsOutcomeSummary{
		ID: util.UUIDToString(receipt.CommandID),
		Issue: CompanyOpsOutcomeIssue{
			ID:         util.UUIDToString(issue.ID),
			Number:     issue.Number,
			Identifier: identifier,
			Title:      issue.Title,
			Status:     issue.Status,
			ProjectID:  util.UUIDToString(issue.ProjectID),
		},
		WorkOrder: CompanyOpsOutcomeWorkOrder{
			SourceRef: receipt.Target.WorkOrderRef,
			Revision:  receipt.Target.WorkOrderRevision,
			Digest:    receipt.Target.WorkOrderDigest,
		},
		Employee: CompanyOpsOutcomeEntity{
			SourceRef: receipt.Target.EmployeeRef,
			ID:        employeeID,
		},
		IdentityBinding: CompanyOpsOutcomeEntity{
			SourceRef: receipt.Target.BindingRef,
			ID:        bindingID,
		},
		ExecutionTarget: CompanyOpsOutcomeExecTarget{
			LocalAgentID:  util.UUIDToString(receipt.LocalAgentID),
			AgentRef:      receipt.Target.AgentRef,
			AgentRevision: receipt.Target.AgentRevision,
			AgentDigest:   receipt.Target.AgentDigest,
		},
		CurrentAgentDisplay: CompanyOpsOutcomeAgentDisplay{
			Name:   agent.Name,
			Model:  agent.Model.String,
			Status: agent.Status,
		},
		InitialTaskID:  util.UUIDToString(receipt.InitialTaskID),
		CurrentTaskID:  util.UUIDToString(currentTaskID),
		ExecutionState: executionState,
		VersionCount:   int32(versionCount),
		LatestEventAt:  latestEventAt,
	}

	if latestCandidate != nil && projection.CandidateID != "" {
		summary.ActiveArtifact = &CompanyOpsOutcomeArtifact{
			ID:                projection.CandidateID,
			Revision:          int32(projection.CandidateRevision),
			DurableObjectRef:  latestCandidate.DurableObjectRef,
			Digest:            latestCandidate.Digest,
			ContentType:       latestCandidate.ContentType,
			Status:            string(projection.Status),
			FormalVisible:     projection.FormalVisible && strings.TrimSpace(projection.FormalArtifactRef) != "",
			FormalArtifactRef: projection.FormalArtifactRef,
		}
	}

	return summary, nil
}

func companyOpsLatestEventAt(eventRows []db.ArtifactEvent) *string {
	if len(eventRows) == 0 {
		return nil
	}
	last := eventRows[len(eventRows)-1]
	if !last.CreatedAt.Valid {
		return nil
	}
	return ptrString(last.CreatedAt.Time.UTC().Format("2006-01-02T15:04:05.999999999Z"))
}

func companyOpsLatestEventAtRaw(raw any) *string {
	if raw == nil {
		return nil
	}
	switch v := raw.(type) {
	case string:
		return &v
	case pgtype.Timestamptz:
		if !v.Valid {
			return nil
		}
		return ptrString(v.Time.UTC().Format("2006-01-02T15:04:05.999999999Z"))
	default:
		return nil
	}
}

func ptrString(s string) *string {
	return &s
}

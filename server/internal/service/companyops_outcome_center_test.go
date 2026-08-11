package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/companyops"
	"github.com/multica-ai/multica/server/internal/util"
)

func TestCompanyOpsOutcomeCenter_SchemaVersion(t *testing.T) {
	if CompanyOpsOutcomeCenterSchemaVersion != "hivecrew.outcome-center.v1" {
		t.Fatalf("schema version = %q", CompanyOpsOutcomeCenterSchemaVersion)
	}
}

// outcomeCenterPromotionRequest builds a promotion request whose Agent
// SourceRef matches the fixture's actual local agent, unlike the shared
// companyOpsArtifactPromotionRequest helper which hardcodes a test ref.
func outcomeCenterPromotionRequest(
	fixture companyOpsExecutionTestFixture,
	candidateID, promotionID string,
) CompanyOpsArtifactPromotion {
	agentRef := "/api/agents/" + util.UUIDToString(fixture.company.agentID)
	return CompanyOpsArtifactPromotion{
		CandidateID: candidateID,
		PromotionID: promotionID,
		ActorUserID: fixture.company.userID,
		Lookup: companyops.HiveCosmAuthorityLookup{
			WorkOrderSourceRef: assignmentWorkOrderRef,
			EmployeeID:         "EMP-ASSIGNMENT-001",
			IdentityBindingID:  "BIND-ASSIGNMENT-001",
			AgentID:            util.UUIDToString(fixture.company.agentID),
		},
		WorkOrder:       assignmentAuthority("WorkOrder", assignmentWorkOrderRef, "wo-rev-7", "a"),
		Employee:        assignmentAuthority("Employee", assignmentEmployeeRef, "employee-rev-5", "c"),
		IdentityBinding: assignmentAuthority("IdentityBinding", assignmentBindingRef, "binding-rev-11", "d"),
		Agent:           assignmentAuthority("Agent", agentRef, "agent-rev-19", "e"),
	}
}

// materializeCompanyOpsArtifact materializes a completed task into a temporary
// artifact WITHOUT approving it, leaving the status at "submitted".
func materializeCompanyOpsArtifact(
	t *testing.T,
	ctx context.Context,
	fixture companyOpsExecutionTestFixture,
	artifactService *CompanyOpsArtifactService,
) *CompanyOpsArtifactOutcome {
	t.Helper()
	task := claimAndFinalizeCompanyOpsExecutionTestTask(t, ctx, fixture)
	if _, err := fixture.service.StartTask(ctx, task.ID); err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	completed, err := fixture.service.CompleteTask(ctx, task.ID, []byte(`{"output":"implemented"}`), "", "", false, "")
	if err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}
	outcome, err := artifactService.MaterializeCompletedTask(
		ctx,
		util.UUIDToString(fixture.company.workspaceID),
		*completed,
		"Implemented the requested operator workflow and verified the focused tests.",
		"https://example.invalid/hivecrew/pull/1",
	)
	if err != nil {
		t.Fatalf("MaterializeCompletedTask: %v", err)
	}
	return outcome
}

func TestCompanyOpsOutcomeCenter_Empty(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	svc := NewCompanyOpsOutcomeCenterService(fixture.queries)

	summaries, total, err := svc.ListOutcomes(ctx, CompanyOpsOutcomeListRequest{
		WorkspaceID: fixture.company.workspaceID,
	})
	if err != nil {
		t.Fatalf("ListOutcomes: %v", err)
	}
	if total != 1 || len(summaries) != 1 {
		t.Fatalf("expected 1 outcome (the fixture dispatch), got total=%d items=%d", total, len(summaries))
	}
	if summaries[0].ActiveArtifact != nil {
		t.Fatalf("expected no active artifact, got %+v", summaries[0].ActiveArtifact)
	}
	if summaries[0].ExecutionState != "awaiting_claim" {
		t.Fatalf("execution_state = %q, want awaiting_claim", summaries[0].ExecutionState)
	}
}

func TestCompanyOpsOutcomeCenter_AwaitingNoCandidate(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	svc := NewCompanyOpsOutcomeCenterService(fixture.queries)

	detail, err := svc.GetOutcome(ctx, fixture.company.workspaceID, fixture.assignment.CommandID)
	if err != nil {
		t.Fatalf("GetOutcome: %v", err)
	}
	if detail.Summary.ActiveArtifact != nil {
		t.Fatalf("expected no active artifact, got %+v", detail.Summary.ActiveArtifact)
	}
	if detail.Summary.ExecutionState != "awaiting_claim" {
		t.Fatalf("execution_state = %q, want awaiting_claim", detail.Summary.ExecutionState)
	}
	if len(detail.Versions) != 0 || len(detail.Events) != 0 {
		t.Fatalf("expected empty versions/events, got %d/%d", len(detail.Versions), len(detail.Events))
	}
	if detail.Summary.ID != util.UUIDToString(fixture.assignment.CommandID) {
		t.Fatalf("summary id = %q, want %q", detail.Summary.ID, util.UUIDToString(fixture.assignment.CommandID))
	}
	if detail.Summary.Issue.Title == "" {
		t.Fatal("expected non-empty issue title")
	}
	if detail.Summary.Issue.Identifier == "" {
		t.Fatal("expected non-empty issue identifier")
	}
}

func TestCompanyOpsOutcomeCenter_SubmittedCandidate(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	fake := &fakeFormalArtifactAuthority{formalArtifactRef: promotionTestFormalArtifactRef}
	artifactService := newCompanyOpsArtifactServiceWithFakeAuthority(t, fixture, fake)
	materializeCompanyOpsArtifact(t, ctx, fixture, artifactService)

	svc := NewCompanyOpsOutcomeCenterService(fixture.queries)
	summaries, _, err := svc.ListOutcomes(ctx, CompanyOpsOutcomeListRequest{
		WorkspaceID: fixture.company.workspaceID,
	})
	if err != nil {
		t.Fatalf("ListOutcomes: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("expected 1 outcome, got %d", len(summaries))
	}
	s := summaries[0]
	if s.ActiveArtifact == nil {
		t.Fatal("expected active artifact")
	}
	if s.ActiveArtifact.Status != "submitted" {
		t.Fatalf("artifact status = %q, want submitted", s.ActiveArtifact.Status)
	}
	if s.ActiveArtifact.FormalVisible {
		t.Fatal("submitted artifact must not be formal_visible")
	}
	if s.VersionCount != 1 {
		t.Fatalf("version_count = %d, want 1", s.VersionCount)
	}
	if s.ExecutionState != "completed" {
		t.Fatalf("execution_state = %q, want completed", s.ExecutionState)
	}
}

func TestCompanyOpsOutcomeCenter_ChangesRequestedAndReworkLineage(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	fake := &fakeFormalArtifactAuthority{formalArtifactRef: promotionTestFormalArtifactRef}
	artifactService := newCompanyOpsArtifactServiceWithFakeAuthority(t, fixture, fake)
	outcome := materializeCompanyOpsArtifact(t, ctx, fixture, artifactService)

	// Transition: submitted → changes_requested (valid)
	if _, err := artifactService.ReviewArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, CompanyOpsArtifactReview{
		CandidateID:   outcome.Candidate.ID,
		Decision:      companyops.ArtifactEventChangesRequested,
		IdempotencyID: uuid.NewString(),
		Feedback:      "Add browser evidence.",
		ActorUserID:   fixture.company.userID,
	}); err != nil {
		t.Fatalf("ReviewArtifact(changes_requested): %v", err)
	}

	svc := NewCompanyOpsOutcomeCenterService(fixture.queries)
	detail, err := svc.GetOutcome(ctx, fixture.company.workspaceID, fixture.assignment.CommandID)
	if err != nil {
		t.Fatalf("GetOutcome after changes_requested: %v", err)
	}
	if detail.Summary.ActiveArtifact == nil {
		t.Fatal("expected active artifact")
	}
	if detail.Summary.ActiveArtifact.Status != "changes_requested" {
		t.Fatalf("status = %q, want changes_requested", detail.Summary.ActiveArtifact.Status)
	}
	if detail.Summary.ActiveArtifact.FormalVisible {
		t.Fatal("changes_requested artifact must not be formal_visible")
	}
	if detail.Summary.ExecutionState != "awaiting_claim" {
		t.Fatalf("execution_state = %q, want awaiting_claim (rework task)", detail.Summary.ExecutionState)
	}
	if detail.Summary.CurrentTaskID == detail.Summary.InitialTaskID {
		t.Fatal("current_task_id should be the rework task, not the initial task")
	}
	if len(detail.Events) < 2 {
		t.Fatalf("expected at least 2 events (submitted, changes_requested), got %d", len(detail.Events))
	}

	// Complete the rework task and produce v2
	rework := claimAndFinalizeCompanyOpsExecutionTestTask(t, ctx, fixture)
	if _, err := fixture.service.StartTask(ctx, rework.ID); err != nil {
		t.Fatalf("StartTask(rework): %v", err)
	}
	completedRework, err := fixture.service.CompleteTask(ctx, rework.ID, []byte(`{"output":"revised"}`), "", "", false, "")
	if err != nil {
		t.Fatalf("CompleteTask(rework): %v", err)
	}
	if _, err := artifactService.MaterializeCompletedTask(
		ctx,
		util.UUIDToString(fixture.company.workspaceID),
		*completedRework,
		"Added operator-facing browser evidence.",
		"",
	); err != nil {
		t.Fatalf("MaterializeCompletedTask(rework): %v", err)
	}

	detail2, err := svc.GetOutcome(ctx, fixture.company.workspaceID, fixture.assignment.CommandID)
	if err != nil {
		t.Fatalf("GetOutcome after rework: %v", err)
	}
	if detail2.Summary.ActiveArtifact == nil {
		t.Fatal("expected active artifact after rework")
	}
	if detail2.Summary.ActiveArtifact.Revision != 2 {
		t.Fatalf("revision = %d, want 2", detail2.Summary.ActiveArtifact.Revision)
	}
	if detail2.Summary.ActiveArtifact.Status != "submitted" {
		t.Fatalf("status = %q, want submitted after rework", detail2.Summary.ActiveArtifact.Status)
	}
	if detail2.Summary.VersionCount != 2 {
		t.Fatalf("version_count = %d, want 2", detail2.Summary.VersionCount)
	}
	if len(detail2.Versions) != 2 {
		t.Fatalf("detail versions = %d, want 2", len(detail2.Versions))
	}
	if detail2.Versions[1].Revision != 2 || detail2.Versions[1].SupersedesID != detail2.Versions[0].ID {
		t.Fatalf("v2 does not supersede v1: %+v", detail2.Versions)
	}
}

func TestCompanyOpsOutcomeCenter_Approved(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	fake := &fakeFormalArtifactAuthority{formalArtifactRef: promotionTestFormalArtifactRef}
	artifactService := newCompanyOpsArtifactServiceWithFakeAuthority(t, fixture, fake)
	materializeAndApproveCompanyOpsArtifact(t, ctx, fixture, artifactService)

	svc := NewCompanyOpsOutcomeCenterService(fixture.queries)
	detail, err := svc.GetOutcome(ctx, fixture.company.workspaceID, fixture.assignment.CommandID)
	if err != nil {
		t.Fatalf("GetOutcome: %v", err)
	}
	if detail.Summary.ActiveArtifact == nil {
		t.Fatal("expected active artifact")
	}
	if detail.Summary.ActiveArtifact.Status != "approved" {
		t.Fatalf("status = %q, want approved", detail.Summary.ActiveArtifact.Status)
	}
	if detail.Summary.ActiveArtifact.FormalVisible {
		t.Fatal("approved artifact must not be formal_visible")
	}
}

func TestCompanyOpsOutcomeCenter_PromotionSucceededNotFormal(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	simulatedReadErr := errors.New("simulated readback failure")
	fake := &fakeFormalArtifactAuthority{
		formalArtifactRef: promotionTestFormalArtifactRef,
		readErr:           simulatedReadErr,
	}
	artifactService := newCompanyOpsArtifactServiceWithFakeAuthority(t, fixture, fake)
	outcome := materializeAndApproveCompanyOpsArtifact(t, ctx, fixture, artifactService)

	promotion := outcomeCenterPromotionRequest(fixture, outcome.Candidate.ID, uuid.NewString())
	if _, err := artifactService.PromoteArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, promotion); !errors.Is(err, simulatedReadErr) {
		t.Fatalf("PromoteArtifact error = %v, want simulatedReadErr", err)
	}

	svc := NewCompanyOpsOutcomeCenterService(fixture.queries)
	detail, err := svc.GetOutcome(ctx, fixture.company.workspaceID, fixture.assignment.CommandID)
	if err != nil {
		t.Fatalf("GetOutcome: %v", err)
	}
	if detail.Summary.ActiveArtifact == nil {
		t.Fatal("expected active artifact")
	}
	if detail.Summary.ActiveArtifact.Status != "promotion_succeeded" {
		t.Fatalf("status = %q, want promotion_succeeded", detail.Summary.ActiveArtifact.Status)
	}
	if detail.Summary.ActiveArtifact.FormalVisible {
		t.Fatal("promotion_succeeded artifact must not be formal_visible until readback")
	}
}

func TestCompanyOpsOutcomeCenter_AuthorityReadbackConfirmedFormal(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	fake := &fakeFormalArtifactAuthority{formalArtifactRef: promotionTestFormalArtifactRef}
	artifactService := newCompanyOpsArtifactServiceWithFakeAuthority(t, fixture, fake)
	outcome := materializeAndApproveCompanyOpsArtifact(t, ctx, fixture, artifactService)

	promotion := outcomeCenterPromotionRequest(fixture, outcome.Candidate.ID, uuid.NewString())
	if _, err := artifactService.PromoteArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, promotion); err != nil {
		t.Fatalf("PromoteArtifact: %v", err)
	}

	svc := NewCompanyOpsOutcomeCenterService(fixture.queries)
	detail, err := svc.GetOutcome(ctx, fixture.company.workspaceID, fixture.assignment.CommandID)
	if err != nil {
		t.Fatalf("GetOutcome: %v", err)
	}
	if detail.Summary.ActiveArtifact == nil {
		t.Fatal("expected active artifact")
	}
	if detail.Summary.ActiveArtifact.Status != "authority_readback_confirmed" {
		t.Fatalf("status = %q, want authority_readback_confirmed", detail.Summary.ActiveArtifact.Status)
	}
	if !detail.Summary.ActiveArtifact.FormalVisible {
		t.Fatal("authority_readback_confirmed artifact must be formal_visible")
	}
	if detail.Summary.ActiveArtifact.FormalArtifactRef != promotionTestFormalArtifactRef {
		t.Fatalf("formal_artifact_ref = %q, want %q", detail.Summary.ActiveArtifact.FormalArtifactRef, promotionTestFormalArtifactRef)
	}

	// Verify formal_visible=true filter finds it.
	formalTrue := true
	summaries, total, err := svc.ListOutcomes(ctx, CompanyOpsOutcomeListRequest{
		WorkspaceID:   fixture.company.workspaceID,
		FormalVisible: &formalTrue,
	})
	if err != nil {
		t.Fatalf("ListOutcomes(formal_visible=true): %v", err)
	}
	if total != 1 || len(summaries) != 1 {
		t.Fatalf("formal_visible=true: total=%d items=%d", total, len(summaries))
	}
}

func TestCompanyOpsOutcomeCenter_SearchAndFilter(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	fake := &fakeFormalArtifactAuthority{formalArtifactRef: promotionTestFormalArtifactRef}
	artifactService := newCompanyOpsArtifactServiceWithFakeAuthority(t, fixture, fake)
	materializeAndApproveCompanyOpsArtifact(t, ctx, fixture, artifactService)

	svc := NewCompanyOpsOutcomeCenterService(fixture.queries)

	// Search by issue title fragment.
	summaries, total, err := svc.ListOutcomes(ctx, CompanyOpsOutcomeListRequest{
		WorkspaceID: fixture.company.workspaceID,
		Q:           "assignment",
	})
	if err != nil {
		t.Fatalf("ListOutcomes(q=assignment): %v", err)
	}
	if total != 1 || len(summaries) != 1 {
		t.Fatalf("q=assignment: total=%d items=%d", total, len(summaries))
	}

	// Search by work_order_ref fragment.
	summaries, _, err = svc.ListOutcomes(ctx, CompanyOpsOutcomeListRequest{
		WorkspaceID: fixture.company.workspaceID,
		Q:           "WO-ASSIGNMENT",
	})
	if err != nil {
		t.Fatalf("ListOutcomes(q=WO-ASSIGNMENT): %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("q=WO-ASSIGNMENT: items=%d", len(summaries))
	}

	// Search by non-matching term.
	summaries, total, err = svc.ListOutcomes(ctx, CompanyOpsOutcomeListRequest{
		WorkspaceID: fixture.company.workspaceID,
		Q:           "nonexistent-term-xyz",
	})
	if err != nil {
		t.Fatalf("ListOutcomes(q=nonexistent): %v", err)
	}
	if total != 0 || len(summaries) != 0 {
		t.Fatalf("q=nonexistent: total=%d items=%d", total, len(summaries))
	}

	// Filter by agent_id.
	summaries, total, err = svc.ListOutcomes(ctx, CompanyOpsOutcomeListRequest{
		WorkspaceID: fixture.company.workspaceID,
		AgentID:     fixture.company.agentID,
	})
	if err != nil {
		t.Fatalf("ListOutcomes(agent_id): %v", err)
	}
	if total != 1 || len(summaries) != 1 {
		t.Fatalf("agent_id filter: total=%d items=%d", total, len(summaries))
	}

	// Filter by wrong agent_id.
	wrongAgent := util.MustParseUUID(uuid.NewString())
	summaries, total, err = svc.ListOutcomes(ctx, CompanyOpsOutcomeListRequest{
		WorkspaceID: fixture.company.workspaceID,
		AgentID:     wrongAgent,
	})
	if err != nil {
		t.Fatalf("ListOutcomes(wrong agent_id): %v", err)
	}
	if total != 0 || len(summaries) != 0 {
		t.Fatalf("wrong agent_id filter: total=%d items=%d", total, len(summaries))
	}

	// Filter by status=approved.
	summaries, total, err = svc.ListOutcomes(ctx, CompanyOpsOutcomeListRequest{
		WorkspaceID: fixture.company.workspaceID,
		Status:      "approved",
	})
	if err != nil {
		t.Fatalf("ListOutcomes(status=approved): %v", err)
	}
	if total != 1 || len(summaries) != 1 {
		t.Fatalf("status=approved: total=%d items=%d", total, len(summaries))
	}

	// Filter by formal_visible=false.
	formalFalse := false
	summaries, total, err = svc.ListOutcomes(ctx, CompanyOpsOutcomeListRequest{
		WorkspaceID:   fixture.company.workspaceID,
		FormalVisible: &formalFalse,
	})
	if err != nil {
		t.Fatalf("ListOutcomes(formal_visible=false): %v", err)
	}
	if total != 1 || len(summaries) != 1 {
		t.Fatalf("formal_visible=false: total=%d items=%d", total, len(summaries))
	}
}

func TestCompanyOpsOutcomeCenter_LimitOffset(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	svc := NewCompanyOpsOutcomeCenterService(fixture.queries)

	summaries, total, err := svc.ListOutcomes(ctx, CompanyOpsOutcomeListRequest{
		WorkspaceID: fixture.company.workspaceID,
		Limit:       10,
		Offset:      0,
	})
	if err != nil {
		t.Fatalf("ListOutcomes(limit=10): %v", err)
	}
	if total != 1 {
		t.Fatalf("total = %d, want 1", total)
	}
	if len(summaries) != 1 {
		t.Fatalf("items = %d, want 1", len(summaries))
	}

	// Offset beyond results.
	summaries, _, err = svc.ListOutcomes(ctx, CompanyOpsOutcomeListRequest{
		WorkspaceID: fixture.company.workspaceID,
		Limit:       50,
		Offset:      10,
	})
	if err != nil {
		t.Fatalf("ListOutcomes(offset=10): %v", err)
	}
	if len(summaries) != 0 {
		t.Fatalf("offset=10 items = %d, want 0", len(summaries))
	}

	// Limit=0 should default to 50.
	summaries, _, err = svc.ListOutcomes(ctx, CompanyOpsOutcomeListRequest{
		WorkspaceID: fixture.company.workspaceID,
	})
	if err != nil {
		t.Fatalf("ListOutcomes(default): %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("default limit items = %d, want 1", len(summaries))
	}

	// Limit > max should be clamped to 100.
	summaries, _, err = svc.ListOutcomes(ctx, CompanyOpsOutcomeListRequest{
		WorkspaceID: fixture.company.workspaceID,
		Limit:       500,
	})
	if err != nil {
		t.Fatalf("ListOutcomes(limit=500): %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("clamped limit items = %d, want 1", len(summaries))
	}
}

func TestCompanyOpsOutcomeCenter_WrongWorkspaceNotFound(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	svc := NewCompanyOpsOutcomeCenterService(fixture.queries)

	otherWorkspace := util.MustParseUUID(uuid.NewString())
	_, err := svc.GetOutcome(ctx, otherWorkspace, fixture.assignment.CommandID)
	if !errors.Is(err, ErrCompanyOpsOutcomeNotFound) {
		t.Fatalf("GetOutcome wrong workspace error = %v, want ErrCompanyOpsOutcomeNotFound", err)
	}
}

func TestCompanyOpsOutcomeCenter_DetailRunsIncludeExecutionReceipts(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	fake := &fakeFormalArtifactAuthority{formalArtifactRef: promotionTestFormalArtifactRef}
	artifactService := newCompanyOpsArtifactServiceWithFakeAuthority(t, fixture, fake)
	materializeCompanyOpsArtifact(t, ctx, fixture, artifactService)

	svc := NewCompanyOpsOutcomeCenterService(fixture.queries)
	detail, err := svc.GetOutcome(ctx, fixture.company.workspaceID, fixture.assignment.CommandID)
	if err != nil {
		t.Fatalf("GetOutcome: %v", err)
	}
	if len(detail.Runs) == 0 {
		t.Fatal("expected at least one execution receipt in runs")
	}
	foundCompleted := false
	for _, run := range detail.Runs {
		if run.Status == "completed" {
			foundCompleted = true
		}
	}
	if !foundCompleted {
		t.Fatalf("no completed run found in %d runs", len(detail.Runs))
	}
	if len(detail.Events) < 1 {
		t.Fatalf("expected at least 1 event, got %d", len(detail.Events))
	}
}

func TestCompanyOpsOutcomeCenter_NilServiceErrors(t *testing.T) {
	ctx := context.Background()
	var svc *CompanyOpsOutcomeCenterService
	_, _, err := svc.ListOutcomes(ctx, CompanyOpsOutcomeListRequest{})
	if !errors.Is(err, ErrCompanyOpsArtifactUnavailable) {
		t.Fatalf("nil service ListOutcomes error = %v", err)
	}
	_, err = svc.GetOutcome(ctx, util.MustParseUUID(uuid.NewString()), util.MustParseUUID(uuid.NewString()))
	if !errors.Is(err, ErrCompanyOpsArtifactUnavailable) {
		t.Fatalf("nil service GetOutcome error = %v", err)
	}
}

func TestCompanyOpsOutcomeCenter_DefaultLimitAndMaxClamp(t *testing.T) {
	if companyOpsOutcomeCenterDefaultLimit != 50 {
		t.Fatalf("default limit = %d, want 50", companyOpsOutcomeCenterDefaultLimit)
	}
	if companyOpsOutcomeCenterMaxLimit != 100 {
		t.Fatalf("max limit = %d, want 100", companyOpsOutcomeCenterMaxLimit)
	}
}

func TestCompanyOpsOutcomeCenter_CanonicalEmployeeAndBindingIDs(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	svc := NewCompanyOpsOutcomeCenterService(fixture.queries)

	// Detail: opaque business IDs, not full URIs.
	detail, err := svc.GetOutcome(ctx, fixture.company.workspaceID, fixture.assignment.CommandID)
	if err != nil {
		t.Fatalf("GetOutcome: %v", err)
	}
	if detail.Summary.Employee.ID != "EMP-ASSIGNMENT-001" {
		t.Fatalf("employee.id = %q, want EMP-ASSIGNMENT-001", detail.Summary.Employee.ID)
	}
	if detail.Summary.Employee.SourceRef != assignmentEmployeeRef {
		t.Fatalf("employee.source_ref = %q, want %q", detail.Summary.Employee.SourceRef, assignmentEmployeeRef)
	}
	if detail.Summary.IdentityBinding.ID != "BIND-ASSIGNMENT-001" {
		t.Fatalf("identity_binding.id = %q, want BIND-ASSIGNMENT-001", detail.Summary.IdentityBinding.ID)
	}
	if detail.Summary.IdentityBinding.SourceRef != assignmentBindingRef {
		t.Fatalf("identity_binding.source_ref = %q, want %q", detail.Summary.IdentityBinding.SourceRef, assignmentBindingRef)
	}

	// List: same opaque business IDs.
	summaries, _, err := svc.ListOutcomes(ctx, CompanyOpsOutcomeListRequest{
		WorkspaceID: fixture.company.workspaceID,
	})
	if err != nil {
		t.Fatalf("ListOutcomes: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("expected 1 summary, got %d", len(summaries))
	}
	if summaries[0].Employee.ID != "EMP-ASSIGNMENT-001" {
		t.Fatalf("list employee.id = %q, want EMP-ASSIGNMENT-001", summaries[0].Employee.ID)
	}
	if summaries[0].IdentityBinding.ID != "BIND-ASSIGNMENT-001" {
		t.Fatalf("list identity_binding.id = %q, want BIND-ASSIGNMENT-001", summaries[0].IdentityBinding.ID)
	}
}

func TestCompanyOpsOutcomeCenter_NonCanonicalEmployeeRefConflict(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	svc := NewCompanyOpsOutcomeCenterService(fixture.queries)

	// Corrupt the employee_ref to a non-canonical URI.
	if _, err := fixture.pool.Exec(ctx,
		`UPDATE assignment_dispatch_receipt SET employee_ref = $2 WHERE command_id = $1`,
		fixture.assignment.CommandID, "hivecosm://employees/",
	); err != nil {
		t.Fatalf("corrupt employee_ref: %v", err)
	}

	_, err := svc.GetOutcome(ctx, fixture.company.workspaceID, fixture.assignment.CommandID)
	if !errors.Is(err, ErrCompanyOpsOutcomeLedgerConflict) {
		t.Fatalf("GetOutcome with non-canonical employee_ref error = %v, want ErrCompanyOpsOutcomeLedgerConflict", err)
	}
}

func TestCompanyOpsOutcomeCenter_NonCanonicalBindingRefConflict(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	svc := NewCompanyOpsOutcomeCenterService(fixture.queries)

	if _, err := fixture.pool.Exec(ctx,
		`UPDATE assignment_dispatch_receipt SET binding_ref = $2 WHERE command_id = $1`,
		fixture.assignment.CommandID, "hivecosm://identity-bindings/BIND/extra",
	); err != nil {
		t.Fatalf("corrupt binding_ref: %v", err)
	}

	_, err := svc.GetOutcome(ctx, fixture.company.workspaceID, fixture.assignment.CommandID)
	if !errors.Is(err, ErrCompanyOpsOutcomeLedgerConflict) {
		t.Fatalf("GetOutcome with non-canonical binding_ref error = %v, want ErrCompanyOpsOutcomeLedgerConflict", err)
	}
}

func TestCompanyOpsOutcomeCenter_OrphanIssueConflict(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	svc := NewCompanyOpsOutcomeCenterService(fixture.queries)

	// Delete the issue to create an orphan assignment receipt.
	if _, err := fixture.pool.Exec(ctx,
		`DELETE FROM issue WHERE id = $1`, fixture.company.issueID,
	); err != nil {
		t.Fatalf("delete issue: %v", err)
	}

	_, err := svc.GetOutcome(ctx, fixture.company.workspaceID, fixture.assignment.CommandID)
	if !errors.Is(err, ErrCompanyOpsOutcomeLedgerConflict) {
		t.Fatalf("GetOutcome with orphan issue error = %v, want ErrCompanyOpsOutcomeLedgerConflict", err)
	}
}

func TestCompanyOpsOutcomeCenter_ReworkListDetailConsistency(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	fake := &fakeFormalArtifactAuthority{formalArtifactRef: promotionTestFormalArtifactRef}
	artifactService := newCompanyOpsArtifactServiceWithFakeAuthority(t, fixture, fake)
	outcome := materializeCompanyOpsArtifact(t, ctx, fixture, artifactService)

	svc := NewCompanyOpsOutcomeCenterService(fixture.queries)

	// v1 submitted: list and detail agree on current_task_id + execution_state.
	listSummaries, _, _ := svc.ListOutcomes(ctx, CompanyOpsOutcomeListRequest{
		WorkspaceID: fixture.company.workspaceID,
	})
	detail, _ := svc.GetOutcome(ctx, fixture.company.workspaceID, fixture.assignment.CommandID)
	if len(listSummaries) != 1 {
		t.Fatalf("expected 1 list summary, got %d", len(listSummaries))
	}
	if listSummaries[0].CurrentTaskID != detail.Summary.CurrentTaskID {
		t.Fatalf("v1 list current_task_id = %q, detail = %q", listSummaries[0].CurrentTaskID, detail.Summary.CurrentTaskID)
	}
	if listSummaries[0].ExecutionState != detail.Summary.ExecutionState {
		t.Fatalf("v1 list execution_state = %q, detail = %q", listSummaries[0].ExecutionState, detail.Summary.ExecutionState)
	}
	if listSummaries[0].ExecutionState != "completed" {
		t.Fatalf("v1 execution_state = %q, want completed", listSummaries[0].ExecutionState)
	}

	// Transition to changes_requested.
	if _, err := artifactService.ReviewArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, CompanyOpsArtifactReview{
		CandidateID:   outcome.Candidate.ID,
		Decision:      companyops.ArtifactEventChangesRequested,
		IdempotencyID: uuid.NewString(),
		Feedback:      "Add browser evidence.",
		ActorUserID:   fixture.company.userID,
	}); err != nil {
		t.Fatalf("ReviewArtifact(changes_requested): %v", err)
	}

	// changes_requested: list and detail agree, current = rework task.
	listSummaries, _, _ = svc.ListOutcomes(ctx, CompanyOpsOutcomeListRequest{
		WorkspaceID: fixture.company.workspaceID,
	})
	detail, _ = svc.GetOutcome(ctx, fixture.company.workspaceID, fixture.assignment.CommandID)
	if listSummaries[0].CurrentTaskID != detail.Summary.CurrentTaskID {
		t.Fatalf("changes_requested list current_task_id = %q, detail = %q",
			listSummaries[0].CurrentTaskID, detail.Summary.CurrentTaskID)
	}
	if listSummaries[0].ExecutionState != detail.Summary.ExecutionState {
		t.Fatalf("changes_requested list execution_state = %q, detail = %q",
			listSummaries[0].ExecutionState, detail.Summary.ExecutionState)
	}
	if listSummaries[0].CurrentTaskID == listSummaries[0].InitialTaskID {
		t.Fatal("changes_requested current_task_id should be rework task, not initial")
	}
	if listSummaries[0].ExecutionState != "awaiting_claim" {
		t.Fatalf("changes_requested execution_state = %q, want awaiting_claim", listSummaries[0].ExecutionState)
	}

	// Complete rework and produce v2.
	rework := claimAndFinalizeCompanyOpsExecutionTestTask(t, ctx, fixture)
	if _, err := fixture.service.StartTask(ctx, rework.ID); err != nil {
		t.Fatalf("StartTask(rework): %v", err)
	}
	completedRework, err := fixture.service.CompleteTask(ctx, rework.ID, []byte(`{"output":"revised"}`), "", "", false, "")
	if err != nil {
		t.Fatalf("CompleteTask(rework): %v", err)
	}
	if _, err := artifactService.MaterializeCompletedTask(
		ctx,
		util.UUIDToString(fixture.company.workspaceID),
		*completedRework,
		"Added operator-facing browser evidence.",
		"",
	); err != nil {
		t.Fatalf("MaterializeCompletedTask(rework): %v", err)
	}

	// v2 submitted: list and detail agree on current = v2 candidate (= rework task).
	listSummaries, _, _ = svc.ListOutcomes(ctx, CompanyOpsOutcomeListRequest{
		WorkspaceID: fixture.company.workspaceID,
	})
	detail, _ = svc.GetOutcome(ctx, fixture.company.workspaceID, fixture.assignment.CommandID)
	if listSummaries[0].CurrentTaskID != detail.Summary.CurrentTaskID {
		t.Fatalf("v2 list current_task_id = %q, detail = %q",
			listSummaries[0].CurrentTaskID, detail.Summary.CurrentTaskID)
	}
	if listSummaries[0].ExecutionState != detail.Summary.ExecutionState {
		t.Fatalf("v2 list execution_state = %q, detail = %q",
			listSummaries[0].ExecutionState, detail.Summary.ExecutionState)
	}
	if listSummaries[0].ActiveArtifact == nil || listSummaries[0].ActiveArtifact.Revision != 2 {
		t.Fatalf("v2 list active artifact revision = %v", listSummaries[0].ActiveArtifact)
	}
	if listSummaries[0].ActiveArtifact.Status != detail.Summary.ActiveArtifact.Status {
		t.Fatalf("v2 list artifact status = %q, detail = %q",
			listSummaries[0].ActiveArtifact.Status, detail.Summary.ActiveArtifact.Status)
	}
}

func TestCompanyOpsOutcomeCenter_ConfirmedEmptyFormalRefConflict(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	fake := &fakeFormalArtifactAuthority{formalArtifactRef: promotionTestFormalArtifactRef}
	artifactService := newCompanyOpsArtifactServiceWithFakeAuthority(t, fixture, fake)
	outcome := materializeAndApproveCompanyOpsArtifact(t, ctx, fixture, artifactService)

	promotion := outcomeCenterPromotionRequest(fixture, outcome.Candidate.ID, uuid.NewString())
	if _, err := artifactService.PromoteArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, promotion); err != nil {
		t.Fatalf("PromoteArtifact: %v", err)
	}

	// Corrupt: disable the immutability trigger, blank out formal_ref on
	// the authority_readback_confirmed event, then re-enable.
	if _, err := fixture.pool.Exec(ctx,
		`ALTER TABLE artifact_event DISABLE TRIGGER artifact_event_reject_mutation`,
	); err != nil {
		t.Fatalf("disable trigger: %v", err)
	}
	if _, err := fixture.pool.Exec(ctx,
		`UPDATE artifact_event SET formal_artifact_ref = '' WHERE event_type = 'authority_readback_confirmed'`,
	); err != nil {
		t.Fatalf("corrupt formal_ref: %v", err)
	}
	if _, err := fixture.pool.Exec(ctx,
		`ALTER TABLE artifact_event ENABLE TRIGGER artifact_event_reject_mutation`,
	); err != nil {
		t.Fatalf("re-enable trigger: %v", err)
	}

	svc := NewCompanyOpsOutcomeCenterService(fixture.queries)
	_, err := svc.GetOutcome(ctx, fixture.company.workspaceID, fixture.assignment.CommandID)
	if !errors.Is(err, ErrCompanyOpsOutcomeLedgerConflict) {
		t.Fatalf("GetOutcome with confirmed-empty-ref error = %v, want ErrCompanyOpsOutcomeLedgerConflict", err)
	}
}

func TestCompanyOpsOutcomeCenter_EmployeeAndTypeFilters(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	fake := &fakeFormalArtifactAuthority{formalArtifactRef: promotionTestFormalArtifactRef}
	artifactService := newCompanyOpsArtifactServiceWithFakeAuthority(t, fixture, fake)
	materializeCompanyOpsArtifact(t, ctx, fixture, artifactService)

	svc := NewCompanyOpsOutcomeCenterService(fixture.queries)

	// Filter by exact employee opaque ID.
	summaries, total, err := svc.ListOutcomes(ctx, CompanyOpsOutcomeListRequest{
		WorkspaceID: fixture.company.workspaceID,
		EmployeeID:  "EMP-ASSIGNMENT-001",
	})
	if err != nil {
		t.Fatalf("ListOutcomes(employee_id): %v", err)
	}
	if total != 1 || len(summaries) != 1 {
		t.Fatalf("employee_id filter: total=%d items=%d", total, len(summaries))
	}

	// Filter by wrong employee opaque ID.
	summaries, total, err = svc.ListOutcomes(ctx, CompanyOpsOutcomeListRequest{
		WorkspaceID: fixture.company.workspaceID,
		EmployeeID:  "EMP-OTHER",
	})
	if err != nil {
		t.Fatalf("ListOutcomes(wrong employee_id): %v", err)
	}
	if total != 0 || len(summaries) != 0 {
		t.Fatalf("wrong employee_id filter: total=%d items=%d", total, len(summaries))
	}

	// Filter by content type (artifact materialized as text/markdown; charset=utf-8).
	summaries, total, err = svc.ListOutcomes(ctx, CompanyOpsOutcomeListRequest{
		WorkspaceID: fixture.company.workspaceID,
		Type:        "text/markdown; charset=utf-8",
	})
	if err != nil {
		t.Fatalf("ListOutcomes(type): %v", err)
	}
	if total != 1 || len(summaries) != 1 {
		t.Fatalf("type filter: total=%d items=%d", total, len(summaries))
	}

	// Filter by wrong content type.
	summaries, total, err = svc.ListOutcomes(ctx, CompanyOpsOutcomeListRequest{
		WorkspaceID: fixture.company.workspaceID,
		Type:        "application/json",
	})
	if err != nil {
		t.Fatalf("ListOutcomes(wrong type): %v", err)
	}
	if total != 0 || len(summaries) != 0 {
		t.Fatalf("wrong type filter: total=%d items=%d", total, len(summaries))
	}
}

func TestCompanyOpsOutcomeCenter_ListCountSameSemantics(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	fake := &fakeFormalArtifactAuthority{formalArtifactRef: promotionTestFormalArtifactRef}
	artifactService := newCompanyOpsArtifactServiceWithFakeAuthority(t, fixture, fake)
	materializeAndApproveCompanyOpsArtifact(t, ctx, fixture, artifactService)

	svc := NewCompanyOpsOutcomeCenterService(fixture.queries)

	cases := []struct {
		name    string
		modify  func(req *CompanyOpsOutcomeListRequest)
	}{
		{"q", func(r *CompanyOpsOutcomeListRequest) { r.Q = "assignment" }},
		{"status", func(r *CompanyOpsOutcomeListRequest) { r.Status = "approved" }},
		{"agent", func(r *CompanyOpsOutcomeListRequest) { r.AgentID = fixture.company.agentID }},
		{"employee", func(r *CompanyOpsOutcomeListRequest) { r.EmployeeID = "EMP-ASSIGNMENT-001" }},
		{"type", func(r *CompanyOpsOutcomeListRequest) { r.Type = "text/markdown; charset=utf-8" }},
		{"formal_false", func(r *CompanyOpsOutcomeListRequest) { b := false; r.FormalVisible = &b }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := CompanyOpsOutcomeListRequest{WorkspaceID: fixture.company.workspaceID}
			tc.modify(&req)
			summaries, total, err := svc.ListOutcomes(ctx, req)
			if err != nil {
				t.Fatalf("ListOutcomes(%s): %v", tc.name, err)
			}
			if int64(len(summaries)) != total {
				t.Fatalf("%s: len(items)=%d total=%d — list/count mismatch", tc.name, len(summaries), total)
			}
		})
	}
}

func TestCompanyOpsOutcomeCenter_ContentTypeInDetail(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	fake := &fakeFormalArtifactAuthority{formalArtifactRef: promotionTestFormalArtifactRef}
	artifactService := newCompanyOpsArtifactServiceWithFakeAuthority(t, fixture, fake)
	materializeCompanyOpsArtifact(t, ctx, fixture, artifactService)

	svc := NewCompanyOpsOutcomeCenterService(fixture.queries)
	detail, err := svc.GetOutcome(ctx, fixture.company.workspaceID, fixture.assignment.CommandID)
	if err != nil {
		t.Fatalf("GetOutcome: %v", err)
	}
	if detail.Summary.ActiveArtifact == nil {
		t.Fatal("expected active artifact")
	}
	if detail.Summary.ActiveArtifact.ContentType != "text/markdown; charset=utf-8" {
		t.Fatalf("content_type = %q, want text/markdown; charset=utf-8", detail.Summary.ActiveArtifact.ContentType)
	}
	if len(detail.Versions) != 1 || detail.Versions[0].ContentType != "text/markdown; charset=utf-8" {
		t.Fatalf("version content_type mismatch: %+v", detail.Versions)
	}
}

func TestIsValidCompanyOpsOutcomeStatus_ClosedSet(t *testing.T) {
	valid := []string{
		"awaiting_claim", "running", "completed", "failed", "cancelled",
		"submitted", "changes_requested", "approved",
		"promotion_requested", "promotion_succeeded", "promotion_failed",
		"authority_readback_confirmed",
	}
	for _, s := range valid {
		if !IsValidCompanyOpsOutcomeStatus(s) {
			t.Fatalf("expected %q to be valid", s)
		}
	}
	if IsValidCompanyOpsOutcomeStatus("nonsense") {
		t.Fatal("expected nonsense to be invalid")
	}
	if IsValidCompanyOpsOutcomeStatus("") {
		t.Fatal("expected empty to be invalid")
	}
}

// TestCompanyOpsOutcomeCenter_ListMalformedEmployeeRefConflict verifies the
// List path is fail-closed on a non-canonical employee ref, matching the
// existing Detail-path conflict test.
func TestCompanyOpsOutcomeCenter_ListMalformedEmployeeRefConflict(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	svc := NewCompanyOpsOutcomeCenterService(fixture.queries)

	if _, err := fixture.pool.Exec(ctx,
		`UPDATE assignment_dispatch_receipt SET employee_ref = $2 WHERE command_id = $1`,
		fixture.assignment.CommandID, "hivecosm://employees/",
	); err != nil {
		t.Fatalf("corrupt employee_ref: %v", err)
	}

	_, _, err := svc.ListOutcomes(ctx, CompanyOpsOutcomeListRequest{
		WorkspaceID: fixture.company.workspaceID,
	})
	if !errors.Is(err, ErrCompanyOpsOutcomeLedgerConflict) {
		t.Fatalf("ListOutcomes with non-canonical employee_ref error = %v, want ErrCompanyOpsOutcomeLedgerConflict", err)
	}
}

// TestCompanyOpsOutcomeCenter_ListMalformedBindingRefConflict verifies the
// List path is fail-closed on a non-canonical identity-binding ref.
func TestCompanyOpsOutcomeCenter_ListMalformedBindingRefConflict(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	svc := NewCompanyOpsOutcomeCenterService(fixture.queries)

	if _, err := fixture.pool.Exec(ctx,
		`UPDATE assignment_dispatch_receipt SET binding_ref = $2 WHERE command_id = $1`,
		fixture.assignment.CommandID, "hivecosm://identity-bindings/BIND/extra",
	); err != nil {
		t.Fatalf("corrupt binding_ref: %v", err)
	}

	_, _, err := svc.ListOutcomes(ctx, CompanyOpsOutcomeListRequest{
		WorkspaceID: fixture.company.workspaceID,
	})
	if !errors.Is(err, ErrCompanyOpsOutcomeLedgerConflict) {
		t.Fatalf("ListOutcomes with non-canonical binding_ref error = %v, want ErrCompanyOpsOutcomeLedgerConflict", err)
	}
}

// TestCompanyOpsOutcomeCenter_ChangesRequestedMissingReworkConflict verifies
// that when the latest lifecycle event is changes_requested but no rework task
// exists, both List and Detail return a ledger conflict instead of falling
// back to the producing task or initial task.
func TestCompanyOpsOutcomeCenter_ChangesRequestedMissingReworkConflict(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	fake := &fakeFormalArtifactAuthority{formalArtifactRef: promotionTestFormalArtifactRef}
	artifactService := newCompanyOpsArtifactServiceWithFakeAuthority(t, fixture, fake)
	outcome := materializeCompanyOpsArtifact(t, ctx, fixture, artifactService)

	if _, err := artifactService.ReviewArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, CompanyOpsArtifactReview{
		CandidateID:   outcome.Candidate.ID,
		Decision:      companyops.ArtifactEventChangesRequested,
		IdempotencyID: uuid.NewString(),
		Feedback:      "Add browser evidence.",
		ActorUserID:   fixture.company.userID,
	}); err != nil {
		t.Fatalf("ReviewArtifact(changes_requested): %v", err)
	}

	// Delete the rework task so the ledger has changes_requested without a
	// resolvable rework task.
	if _, err := fixture.pool.Exec(ctx,
		`DELETE FROM agent_task_queue WHERE issue_id = $1 AND trigger_evidence_kind = 'artifact_revision'`,
		fixture.company.issueID,
	); err != nil {
		t.Fatalf("delete rework task: %v", err)
	}

	svc := NewCompanyOpsOutcomeCenterService(fixture.queries)

	// List: fail-closed.
	_, _, listErr := svc.ListOutcomes(ctx, CompanyOpsOutcomeListRequest{
		WorkspaceID: fixture.company.workspaceID,
	})
	if !errors.Is(listErr, ErrCompanyOpsOutcomeLedgerConflict) {
		t.Fatalf("ListOutcomes changes_requested missing rework error = %v, want ErrCompanyOpsOutcomeLedgerConflict", listErr)
	}

	// Detail: fail-closed.
	_, detailErr := svc.GetOutcome(ctx, fixture.company.workspaceID, fixture.assignment.CommandID)
	if !errors.Is(detailErr, ErrCompanyOpsOutcomeLedgerConflict) {
		t.Fatalf("GetOutcome changes_requested missing rework error = %v, want ErrCompanyOpsOutcomeLedgerConflict", detailErr)
	}
}

// TestCompanyOpsOutcomeCenter_ChangesRequestedDuplicateReworkConflict verifies
// that when the latest lifecycle event is changes_requested and there are
// multiple rework tasks for the same trigger evidence, both List and Detail
// return a ledger conflict.
func TestCompanyOpsOutcomeCenter_ChangesRequestedDuplicateReworkConflict(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	fake := &fakeFormalArtifactAuthority{formalArtifactRef: promotionTestFormalArtifactRef}
	artifactService := newCompanyOpsArtifactServiceWithFakeAuthority(t, fixture, fake)
	outcome := materializeCompanyOpsArtifact(t, ctx, fixture, artifactService)

	if _, err := artifactService.ReviewArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, CompanyOpsArtifactReview{
		CandidateID:   outcome.Candidate.ID,
		Decision:      companyops.ArtifactEventChangesRequested,
		IdempotencyID: uuid.NewString(),
		Feedback:      "Add browser evidence.",
		ActorUserID:   fixture.company.userID,
	}); err != nil {
		t.Fatalf("ReviewArtifact(changes_requested): %v", err)
	}

	// Fetch the changes_requested event ID to create a duplicate rework task
	// pointing at the same trigger evidence.
	var changesRequestedEventID pgtype.UUID
	if err := fixture.pool.QueryRow(ctx,
		`SELECT id FROM artifact_event WHERE workspace_id = $1 AND lineage_id = $2 AND event_type = 'changes_requested' ORDER BY sequence DESC LIMIT 1`,
		fixture.company.workspaceID, fixture.assignment.CommandID,
	).Scan(&changesRequestedEventID); err != nil {
		t.Fatalf("query changes_requested event: %v", err)
	}

	// Mark the existing rework task as completed so the partial unique index
	// idx_one_pending_task_per_issue_agent does not block the duplicate insert.
	if _, err := fixture.pool.Exec(ctx,
		`UPDATE agent_task_queue SET status = 'completed', completed_at = now() WHERE issue_id = $1 AND trigger_evidence_kind = 'artifact_revision'`,
		fixture.company.issueID,
	); err != nil {
		t.Fatalf("complete existing rework task: %v", err)
	}

	// Insert a DUPLICATE rework task pointing at the same trigger evidence.
	if _, err := fixture.pool.Exec(ctx,
		`INSERT INTO agent_task_queue (id, agent_id, issue_id, status, runtime_id, trigger_evidence_kind, trigger_evidence_ref_id) VALUES ($1, $2, $3, 'queued', $4, 'artifact_revision', $5)`,
		util.MustParseUUID(uuid.NewString()), fixture.company.agentID, fixture.company.issueID, fixture.company.runtimeID, changesRequestedEventID,
	); err != nil {
		t.Fatalf("insert duplicate rework task: %v", err)
	}

	svc := NewCompanyOpsOutcomeCenterService(fixture.queries)

	// List: fail-closed.
	_, _, listErr := svc.ListOutcomes(ctx, CompanyOpsOutcomeListRequest{
		WorkspaceID: fixture.company.workspaceID,
	})
	if !errors.Is(listErr, ErrCompanyOpsOutcomeLedgerConflict) {
		t.Fatalf("ListOutcomes changes_requested duplicate rework error = %v, want ErrCompanyOpsOutcomeLedgerConflict", listErr)
	}

	// Detail: fail-closed.
	_, detailErr := svc.GetOutcome(ctx, fixture.company.workspaceID, fixture.assignment.CommandID)
	if !errors.Is(detailErr, ErrCompanyOpsOutcomeLedgerConflict) {
		t.Fatalf("GetOutcome changes_requested duplicate rework error = %v, want ErrCompanyOpsOutcomeLedgerConflict", detailErr)
	}
}

// TestCompanyOpsOutcomeCenter_ChangesRequestedWrongCandidateReworkConflict
// verifies that when the active candidate's latest lifecycle event is
// changes_requested but the sole rework task's trigger evidence binds to a
// DIFFERENT candidate/event (not the changes_requested event on the active
// candidate), both List and Detail return a ledger conflict instead of
// silently falling back to the producing task or initial task.
//
// This closes the last acceptance gap flagged by independent review: the
// product code's active-candidate exact binding is correct, but the scenario
// previously lacked a direct database test.
func TestCompanyOpsOutcomeCenter_ChangesRequestedWrongCandidateReworkConflict(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	fake := &fakeFormalArtifactAuthority{formalArtifactRef: promotionTestFormalArtifactRef}
	artifactService := newCompanyOpsArtifactServiceWithFakeAuthority(t, fixture, fake)
	outcome := materializeCompanyOpsArtifact(t, ctx, fixture, artifactService)

	// v1 submitted → changes_requested. This creates a rework task whose
	// trigger_evidence_ref_id correctly binds to the changes_requested event
	// on v1.
	if _, err := artifactService.ReviewArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, CompanyOpsArtifactReview{
		CandidateID:   outcome.Candidate.ID,
		Decision:      companyops.ArtifactEventChangesRequested,
		IdempotencyID: uuid.NewString(),
		Feedback:      "Add browser evidence.",
		ActorUserID:   fixture.company.userID,
	}); err != nil {
		t.Fatalf("ReviewArtifact(changes_requested): %v", err)
	}

	// Fetch the submitted event ID — the WRONG event to bind a rework task
	// to when the active candidate's latest event is changes_requested.
	var submittedEventID pgtype.UUID
	if err := fixture.pool.QueryRow(ctx,
		`SELECT id FROM artifact_event WHERE workspace_id = $1 AND lineage_id = $2 AND event_type = 'submitted' ORDER BY sequence DESC LIMIT 1`,
		fixture.company.workspaceID, fixture.assignment.CommandID,
	).Scan(&submittedEventID); err != nil {
		t.Fatalf("query submitted event: %v", err)
	}

	// Rebind the sole rework task's trigger_evidence_ref_id to the submitted
	// event instead of the changes_requested event. The task count for the
	// changes_requested event is now 0 (missing), and the task count for the
	// submitted event is 1 — but the active candidate's latest lifecycle is
	// changes_requested, so the ledger is inconsistent.
	if _, err := fixture.pool.Exec(ctx,
		`UPDATE agent_task_queue SET trigger_evidence_ref_id = $2 WHERE issue_id = $1 AND trigger_evidence_kind = 'artifact_revision'`,
		fixture.company.issueID, submittedEventID,
	); err != nil {
		t.Fatalf("rebind rework task to wrong event: %v", err)
	}

	svc := NewCompanyOpsOutcomeCenterService(fixture.queries)

	// List: fail-closed — must NOT return a summary with the producing or
	// initial task as current_task_id.
	listSummaries, _, listErr := svc.ListOutcomes(ctx, CompanyOpsOutcomeListRequest{
		WorkspaceID: fixture.company.workspaceID,
	})
	if !errors.Is(listErr, ErrCompanyOpsOutcomeLedgerConflict) {
		t.Fatalf("ListOutcomes wrong-candidate rework error = %v, want ErrCompanyOpsOutcomeLedgerConflict (summaries=%v)", listErr, listSummaries)
	}

	// Detail: fail-closed — must NOT return a detail with the producing or
	// initial task as current_task_id.
	detail, detailErr := svc.GetOutcome(ctx, fixture.company.workspaceID, fixture.assignment.CommandID)
	if !errors.Is(detailErr, ErrCompanyOpsOutcomeLedgerConflict) {
		t.Fatalf("GetOutcome wrong-candidate rework error = %v, want ErrCompanyOpsOutcomeLedgerConflict (detail=%v)", detailErr, detail)
	}
}

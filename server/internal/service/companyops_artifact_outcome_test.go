package service

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/companyops"
	"github.com/multica-ai/multica/server/internal/storage"
	"github.com/multica-ai/multica/server/internal/util"
)

const promotionTestFormalArtifactRef = assignmentWorkOrderRef + "/formal-artifact/FA-PROMOTION-001"

type fakeFormalArtifactAuthority struct {
	promoteErr        error
	readErr           error
	promoteCount      int
	readCount         int
	formalArtifactRef string
	lastReadCandidate companyops.HiveCosmFormalArtifactCandidate
}

func (f *fakeFormalArtifactAuthority) PromoteFormalArtifact(
	_ context.Context,
	input companyops.HiveCosmFormalArtifactPromotionRequest,
) (companyops.HiveCosmFormalArtifactPromotionReceipt, error) {
	f.promoteCount++
	if f.promoteErr != nil {
		return companyops.HiveCosmFormalArtifactPromotionReceipt{}, f.promoteErr
	}
	return companyops.HiveCosmFormalArtifactPromotionReceipt{
		PromotionID:    input.PromotionID,
		WritePerformed: true,
		Artifact: companyops.HiveCosmFormalArtifact{
			FormalArtifactRef: f.formalArtifactRef,
		},
	}, nil
}

func (f *fakeFormalArtifactAuthority) ReadFormalArtifact(
	_ context.Context,
	_ companyops.HiveCosmAuthorityLookup,
	expectedCandidate companyops.HiveCosmFormalArtifactCandidate,
	_ string,
) (companyops.HiveCosmFormalArtifact, error) {
	f.readCount++
	f.lastReadCandidate = expectedCandidate
	if f.readErr != nil {
		return companyops.HiveCosmFormalArtifact{}, f.readErr
	}
	return companyops.HiveCosmFormalArtifact{FormalArtifactRef: f.formalArtifactRef}, nil
}

func newCompanyOpsArtifactServiceWithFakeAuthority(
	t *testing.T,
	fixture companyOpsExecutionTestFixture,
	fake *fakeFormalArtifactAuthority,
) *CompanyOpsArtifactService {
	t.Helper()
	t.Setenv("LOCAL_UPLOAD_DIR", t.TempDir())
	store := storage.NewLocalStorageFromEnv()
	artifactService, err := NewCompanyOpsArtifactService(fixture.queries, fixture.pool, store, fixture.service, fake)
	if err != nil {
		t.Fatalf("NewCompanyOpsArtifactService: %v", err)
	}
	return artifactService
}

func materializeAndApproveCompanyOpsArtifact(
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
	if _, err := artifactService.ReviewArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, CompanyOpsArtifactReview{
		CandidateID:   outcome.Candidate.ID,
		Decision:      companyops.ArtifactEventApproved,
		IdempotencyID: uuid.NewString(),
		ActorUserID:   fixture.company.userID,
	}); err != nil {
		t.Fatalf("ReviewArtifact(approved): %v", err)
	}
	return outcome
}

func companyOpsArtifactPromotionRequest(fixture companyOpsExecutionTestFixture, candidateID, promotionID string) CompanyOpsArtifactPromotion {
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
	}
}

func TestCompanyOpsArtifactOutcome_MaterializeReadbackAndOwnerReview(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	task := claimAndFinalizeCompanyOpsExecutionTestTask(t, ctx, fixture)
	if _, err := fixture.service.StartTask(ctx, task.ID); err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	completed, err := fixture.service.CompleteTask(ctx, task.ID, []byte(`{"output":"implemented"}`), "", "", false, "")
	if err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}

	t.Setenv("LOCAL_UPLOAD_DIR", t.TempDir())
	store := storage.NewLocalStorageFromEnv()
	artifactService, err := NewCompanyOpsArtifactService(fixture.queries, fixture.pool, store, fixture.service, &fakeFormalArtifactAuthority{formalArtifactRef: promotionTestFormalArtifactRef})
	if err != nil {
		t.Fatalf("NewCompanyOpsArtifactService: %v", err)
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
	if outcome == nil || outcome.Candidate == nil || outcome.Projection == nil {
		t.Fatalf("materialized outcome is incomplete: %+v", outcome)
	}
	if outcome.Candidate.ID != util.UUIDToString(task.ID) || outcome.Candidate.Revision != 1 ||
		outcome.Projection.Status != companyops.ArtifactEventSubmitted || outcome.ExecutionState != "completed" {
		t.Fatalf("materialized outcome = %+v", outcome)
	}

	key := store.KeyFromURL(outcome.Candidate.DurableObjectRef)
	reader, err := store.GetReader(ctx, key)
	if err != nil {
		t.Fatalf("GetReader(%q): %v", key, err)
	}
	body, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil {
		t.Fatalf("read temporary artifact: %v", err)
	}
	for _, want := range []string{
		"# HiveCrew Temporary Artifact",
		fixture.assignment.Target.WorkOrderRef,
		"Implemented the requested operator workflow",
		"https://example.invalid/hivecrew/pull/1",
	} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("temporary artifact missing %q:\n%s", want, body)
		}
	}

	review := CompanyOpsArtifactReview{
		CandidateID:   outcome.Candidate.ID,
		Decision:      companyops.ArtifactEventChangesRequested,
		IdempotencyID: uuid.NewString(),
		Feedback:      "Please add the operator-facing browser evidence.",
		ActorUserID:   fixture.company.userID,
	}
	first, err := artifactService.ReviewArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, review)
	if err != nil {
		t.Fatalf("ReviewArtifact(changes requested): %v", err)
	}
	replayed, err := artifactService.ReviewArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, review)
	if err != nil {
		t.Fatalf("ReviewArtifact(exact replay): %v", err)
	}
	if replayed.Event.ID != first.Event.ID || replayed.Event.Sequence != first.Event.Sequence {
		t.Fatalf("review replay changed receipt: first=%+v replay=%+v", first, replayed)
	}
	if first.ReworkTask == nil || first.ReworkTask.TriggerEvidenceKind.String != artifactRevisionEvidenceKind ||
		util.UUIDToString(first.ReworkTask.TriggerEvidenceRefID) != first.Event.ID {
		t.Fatalf("rework Run does not reference review event: %+v", first)
	}
	readback, err := artifactService.GetIssueOutcome(ctx, fixture.company.workspaceID, fixture.company.issueID)
	if err != nil {
		t.Fatalf("GetIssueOutcome: %v", err)
	}
	if readback == nil || readback.Projection == nil || readback.Projection.Status != companyops.ArtifactEventChangesRequested {
		t.Fatalf("review readback = %+v", readback)
	}
	if readback.CurrentTaskID != first.ReworkTask.ID || readback.ExecutionState != "awaiting_claim" {
		t.Fatalf("review current Run = %+v, want rework Run awaiting_claim", readback)
	}

	conflicting := review
	conflicting.Decision = companyops.ArtifactEventApproved
	if _, err := artifactService.ReviewArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, conflicting); !errors.Is(err, companyops.ErrArtifactIdempotencyConflict) {
		t.Fatalf("conflicting review replay error = %v, want ErrArtifactIdempotencyConflict", err)
	}

	rework := claimAndFinalizeCompanyOpsExecutionTestTask(t, ctx, fixture)
	if rework.ID != first.ReworkTask.ID {
		t.Fatalf("claimed rework Run = %s, want %s", util.UUIDToString(rework.ID), util.UUIDToString(first.ReworkTask.ID))
	}
	if _, err := fixture.service.StartTask(ctx, rework.ID); err != nil {
		t.Fatalf("StartTask(rework): %v", err)
	}
	completedRework, err := fixture.service.CompleteTask(ctx, rework.ID, []byte(`{"output":"revised"}`), "", "", false, "")
	if err != nil {
		t.Fatalf("CompleteTask(rework): %v", err)
	}
	revised, err := artifactService.MaterializeCompletedTask(
		ctx,
		util.UUIDToString(fixture.company.workspaceID),
		*completedRework,
		"Added the requested operator-facing browser evidence.",
		"",
	)
	if err != nil {
		t.Fatalf("MaterializeCompletedTask(rework): %v", err)
	}
	if revised == nil || revised.Candidate == nil || revised.Projection == nil ||
		revised.Candidate.Revision != 2 || revised.Candidate.SupersedesID != outcome.Candidate.ID ||
		revised.Projection.Status != companyops.ArtifactEventSubmitted {
		t.Fatalf("revised outcome does not extend revision 1: %+v", revised)
	}
}

func TestCompanyOpsArtifactOutcome_FormalPromotionHappyPathAndReplay(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	fake := &fakeFormalArtifactAuthority{formalArtifactRef: promotionTestFormalArtifactRef}
	artifactService := newCompanyOpsArtifactServiceWithFakeAuthority(t, fixture, fake)
	outcome := materializeAndApproveCompanyOpsArtifact(t, ctx, fixture, artifactService)

	promotion := companyOpsArtifactPromotionRequest(fixture, outcome.Candidate.ID, uuid.NewString())
	receipt, err := artifactService.PromoteArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, promotion)
	if err != nil {
		t.Fatalf("PromoteArtifact: %v", err)
	}
	if receipt.LifecycleStatus != companyops.ArtifactEventAuthorityReadbackConfirmed {
		t.Fatalf("lifecycle status = %q, want authority_readback_confirmed", receipt.LifecycleStatus)
	}
	if !receipt.FormalVisible || receipt.FormalArtifactRef != promotionTestFormalArtifactRef {
		t.Fatalf("formal artifact not visible after readback: %+v", receipt)
	}
	if fake.promoteCount != 1 || fake.readCount != 1 {
		t.Fatalf("authority call counts = promote %d read %d, want 1/1", fake.promoteCount, fake.readCount)
	}
	if fake.lastReadCandidate.ID != outcome.Candidate.ID ||
		fake.lastReadCandidate.Revision != outcome.Candidate.Revision ||
		fake.lastReadCandidate.ContentDigest != outcome.Candidate.Digest ||
		fake.lastReadCandidate.DurableObjectRef != outcome.Candidate.DurableObjectRef ||
		fake.lastReadCandidate.ApprovalEventID == "" {
		t.Fatalf("readback was not pinned to the approved candidate: %+v", fake.lastReadCandidate)
	}

	readback, err := artifactService.GetIssueOutcome(ctx, fixture.company.workspaceID, fixture.company.issueID)
	if err != nil {
		t.Fatalf("GetIssueOutcome: %v", err)
	}
	if readback == nil || readback.Projection == nil || !readback.Projection.FormalVisible ||
		readback.Projection.FormalArtifactRef != promotionTestFormalArtifactRef {
		t.Fatalf("projection does not expose confirmed formal ref: %+v", readback)
	}

	replay, err := artifactService.PromoteArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, promotion)
	if err != nil {
		t.Fatalf("PromoteArtifact(replay): %v", err)
	}
	if replay.LifecycleStatus != companyops.ArtifactEventAuthorityReadbackConfirmed {
		t.Fatalf("replay lifecycle status = %q", replay.LifecycleStatus)
	}
	if fake.promoteCount != 1 {
		t.Fatalf("replay caused a duplicate POST: promote count = %d, want 1", fake.promoteCount)
	}
}

func TestCompanyOpsArtifactOutcome_FormalPromotionFailureAndRetry(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	simulatedFailure := errors.New("simulated HiveCosm formal Artifact promotion failure")
	fake := &fakeFormalArtifactAuthority{promoteErr: simulatedFailure, formalArtifactRef: promotionTestFormalArtifactRef}
	artifactService := newCompanyOpsArtifactServiceWithFakeAuthority(t, fixture, fake)
	outcome := materializeAndApproveCompanyOpsArtifact(t, ctx, fixture, artifactService)

	promotion := companyOpsArtifactPromotionRequest(fixture, outcome.Candidate.ID, uuid.NewString())
	if _, err := artifactService.PromoteArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, promotion); !errors.Is(err, simulatedFailure) {
		t.Fatalf("PromoteArtifact(failing authority) error = %v, want simulated failure", err)
	}
	if fake.promoteCount != 1 {
		t.Fatalf("promote count = %d, want 1 after first attempt", fake.promoteCount)
	}
	failedOutcome, err := artifactService.GetIssueOutcome(ctx, fixture.company.workspaceID, fixture.company.issueID)
	if err != nil {
		t.Fatalf("GetIssueOutcome: %v", err)
	}
	if failedOutcome == nil || failedOutcome.Projection == nil || failedOutcome.Projection.Status != companyops.ArtifactEventPromotionFailed {
		t.Fatalf("projection status = %q, want promotion_failed", statusOrEmpty(failedOutcome))
	}

	fake.promoteErr = nil
	retry, err := artifactService.PromoteArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, promotion)
	if err != nil {
		t.Fatalf("PromoteArtifact(retry): %v", err)
	}
	if retry.LifecycleStatus != companyops.ArtifactEventAuthorityReadbackConfirmed {
		t.Fatalf("retry lifecycle status = %q, want authority_readback_confirmed", retry.LifecycleStatus)
	}
	if fake.promoteCount != 2 {
		t.Fatalf("promote count = %d, want 2 after retry", fake.promoteCount)
	}
	if !retry.FormalVisible || retry.FormalArtifactRef != promotionTestFormalArtifactRef {
		t.Fatalf("retry did not confirm formal ref: %+v", retry)
	}
}

func TestCompanyOpsArtifactOutcome_FormalPromotionResumeWithoutDuplicatePost(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	fake := &fakeFormalArtifactAuthority{
		formalArtifactRef: promotionTestFormalArtifactRef,
		readErr:           errors.New("simulated HiveCosm readback failure"),
	}
	artifactService := newCompanyOpsArtifactServiceWithFakeAuthority(t, fixture, fake)
	outcome := materializeAndApproveCompanyOpsArtifact(t, ctx, fixture, artifactService)

	promotion := companyOpsArtifactPromotionRequest(fixture, outcome.Candidate.ID, uuid.NewString())
	if _, err := artifactService.PromoteArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, promotion); err == nil {
		t.Fatal("PromoteArtifact expected readback failure on first attempt")
	}
	if fake.promoteCount != 1 {
		t.Fatalf("promote count = %d, want 1 after POST-then-readback-fail", fake.promoteCount)
	}
	succeededOutcome, err := artifactService.GetIssueOutcome(ctx, fixture.company.workspaceID, fixture.company.issueID)
	if err != nil {
		t.Fatalf("GetIssueOutcome: %v", err)
	}
	if succeededOutcome == nil || succeededOutcome.Projection == nil || succeededOutcome.Projection.Status != companyops.ArtifactEventPromotionSucceeded {
		t.Fatalf("projection status = %q, want promotion_succeeded", statusOrEmpty(succeededOutcome))
	}
	if succeededOutcome.Projection.FormalVisible {
		t.Fatalf("formal ref must not be visible before readback confirmation")
	}

	fake.readErr = nil
	resume, err := artifactService.PromoteArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, promotion)
	if err != nil {
		t.Fatalf("PromoteArtifact(resume): %v", err)
	}
	if resume.LifecycleStatus != companyops.ArtifactEventAuthorityReadbackConfirmed {
		t.Fatalf("resume lifecycle status = %q, want authority_readback_confirmed", resume.LifecycleStatus)
	}
	if fake.promoteCount != 1 {
		t.Fatalf("resume caused a duplicate POST: promote count = %d, want 1", fake.promoteCount)
	}
	if fake.readCount != 2 {
		t.Fatalf("read count = %d, want 2 (first failed, second confirmed)", fake.readCount)
	}
	if !resume.FormalVisible || resume.FormalArtifactRef != promotionTestFormalArtifactRef {
		t.Fatalf("resume did not confirm formal ref: %+v", resume)
	}
}

func statusOrEmpty(outcome *CompanyOpsArtifactOutcome) string {
	if outcome == nil || outcome.Projection == nil {
		return ""
	}
	return string(outcome.Projection.Status)
}

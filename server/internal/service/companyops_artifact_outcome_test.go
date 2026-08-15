package service

import (
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
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
	readArtifact      *companyops.HiveCosmFormalArtifact
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
	if f.readArtifact != nil {
		return *f.readArtifact, nil
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
	return companyOpsArtifactPromotionRequestForTarget(fixture.assignment.Target, fixture.company.userID, fixture.company.agentID, candidateID, promotionID)
}

func companyOpsArtifactPromotionRequestForTarget(target companyops.ExecutionTargetSnapshot, actorUserID, agentID pgtype.UUID, candidateID, promotionID string) CompanyOpsArtifactPromotion {
	return CompanyOpsArtifactPromotion{
		CandidateID: candidateID,
		PromotionID: promotionID,
		ActorUserID: actorUserID,
		Lookup: companyops.HiveCosmAuthorityLookup{
			WorkOrderSourceRef: target.WorkOrderRef,
			EmployeeID:         "EMP-ASSIGNMENT-001",
			IdentityBindingID:  "BIND-ASSIGNMENT-001",
			AgentID:            util.UUIDToString(agentID),
		},
		WorkOrder: companyops.AuthoritySnapshot{
			Kind:          "WorkOrder",
			SourceRef:     target.WorkOrderRef,
			Revision:      target.WorkOrderRevision,
			ContentDigest: target.WorkOrderDigest,
			Freshness:     "current",
		},
		Employee: companyops.AuthoritySnapshot{
			Kind:          "Employee",
			SourceRef:     target.EmployeeRef,
			Revision:      target.EmployeeRevision,
			ContentDigest: target.EmployeeDigest,
			Freshness:     "current",
		},
		IdentityBinding: companyops.AuthoritySnapshot{
			Kind:          "IdentityBinding",
			SourceRef:     target.BindingRef,
			Revision:      target.BindingRevision,
			ContentDigest: target.BindingDigest,
			Freshness:     "current",
		},
		Agent: companyops.AuthoritySnapshot{
			Kind:          "Agent",
			SourceRef:     target.AgentRef,
			Revision:      target.AgentRevision,
			ContentDigest: target.AgentDigest,
			Freshness:     "current",
		},
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

func TestVerifyCompanyOpsWorkOrderTransitionArtifactRejectsEveryDrift(t *testing.T) {
	approvalID := uuid.NewString()
	candidate := companyops.ArtifactCandidate{
		ID:               uuid.NewString(),
		LineageID:        uuid.NewString(),
		Revision:         2,
		SupersedesID:     uuid.NewString(),
		DurableObjectRef: "/uploads/hivecrew/outcome-r2.md",
		Digest:           "sha256:" + strings.Repeat("d", 64),
	}
	baseProof := companyops.HiveCosmWorkOrderTransitionProof{
		WorkOrderSourceRef: assignmentWorkOrderRef,
		PreviousAuthority: companyops.HiveCosmAuthorityTransitionSnapshot{
			Revision:      "sha256:" + strings.Repeat("e", 64),
			ContentDigest: "sha256:" + strings.Repeat("e", 64),
		},
		ResultingAuthority: companyops.HiveCosmAuthorityTransitionSnapshot{
			Revision:      "sha256:" + strings.Repeat("f", 64),
			ContentDigest: "sha256:" + strings.Repeat("f", 64),
		},
		PromotionID:       uuid.NewString(),
		CandidateID:       candidate.ID,
		ApprovalEventID:   approvalID,
		FormalArtifactRef: promotionTestFormalArtifactRef,
	}
	baseArtifact := companyops.HiveCosmFormalArtifact{
		FormalArtifactRef:   baseProof.FormalArtifactRef,
		CandidateID:         candidate.ID,
		CandidateRevision:   candidate.Revision,
		CandidateDigest:     candidate.Digest,
		ApprovalEventID:     approvalID,
		WorkOrderTransition: &baseProof,
	}
	if err := verifyCompanyOpsWorkOrderTransitionArtifact(baseArtifact, baseProof, candidate, approvalID); err != nil {
		t.Fatalf("exact artifact rejected: %v", err)
	}

	tests := []struct {
		name   string
		tamper func(*companyops.HiveCosmFormalArtifact)
	}{
		{name: "formal artifact core ref", tamper: func(artifact *companyops.HiveCosmFormalArtifact) { artifact.FormalArtifactRef += "-other" }},
		{name: "formal artifact core candidate", tamper: func(artifact *companyops.HiveCosmFormalArtifact) { artifact.CandidateID = uuid.NewString() }},
		{name: "formal artifact core revision", tamper: func(artifact *companyops.HiveCosmFormalArtifact) { artifact.CandidateRevision++ }},
		{name: "formal artifact core digest", tamper: func(artifact *companyops.HiveCosmFormalArtifact) {
			artifact.CandidateDigest = "sha256:" + strings.Repeat("0", 64)
		}},
		{name: "formal artifact core approval", tamper: func(artifact *companyops.HiveCosmFormalArtifact) { artifact.ApprovalEventID = uuid.NewString() }},
		{name: "missing proof", tamper: func(artifact *companyops.HiveCosmFormalArtifact) { artifact.WorkOrderTransition = nil }},
		{name: "source ref", tamper: func(artifact *companyops.HiveCosmFormalArtifact) {
			artifact.WorkOrderTransition.WorkOrderSourceRef += "-other"
		}},
		{name: "previous revision", tamper: func(artifact *companyops.HiveCosmFormalArtifact) {
			artifact.WorkOrderTransition.PreviousAuthority.Revision += "-other"
		}},
		{name: "previous digest", tamper: func(artifact *companyops.HiveCosmFormalArtifact) {
			artifact.WorkOrderTransition.PreviousAuthority.ContentDigest = "sha256:" + strings.Repeat("0", 64)
		}},
		{name: "resulting revision", tamper: func(artifact *companyops.HiveCosmFormalArtifact) {
			artifact.WorkOrderTransition.ResultingAuthority.Revision += "-other"
		}},
		{name: "resulting digest", tamper: func(artifact *companyops.HiveCosmFormalArtifact) {
			artifact.WorkOrderTransition.ResultingAuthority.ContentDigest = "sha256:" + strings.Repeat("0", 64)
		}},
		{name: "promotion id", tamper: func(artifact *companyops.HiveCosmFormalArtifact) {
			artifact.WorkOrderTransition.PromotionID = uuid.NewString()
		}},
		{name: "candidate id", tamper: func(artifact *companyops.HiveCosmFormalArtifact) {
			artifact.WorkOrderTransition.CandidateID = uuid.NewString()
		}},
		{name: "approval id", tamper: func(artifact *companyops.HiveCosmFormalArtifact) {
			artifact.WorkOrderTransition.ApprovalEventID = uuid.NewString()
		}},
		{name: "formal ref", tamper: func(artifact *companyops.HiveCosmFormalArtifact) {
			artifact.WorkOrderTransition.FormalArtifactRef += "-other"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			artifact := baseArtifact
			proof := baseProof
			artifact.WorkOrderTransition = &proof
			test.tamper(&artifact)
			if err := verifyCompanyOpsWorkOrderTransitionArtifact(artifact, baseProof, candidate, approvalID); !errors.Is(err, ErrCompanyOpsArtifactConflict) {
				t.Fatalf("verification error = %v, want ErrCompanyOpsArtifactConflict", err)
			}
		})
	}
}

func TestCompanyOpsArtifactOutcome_VerifyWorkOrderTransitionForGetFailsClosedOnDrift(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	fake := &fakeFormalArtifactAuthority{formalArtifactRef: promotionTestFormalArtifactRef}
	artifactService := newCompanyOpsArtifactServiceWithFakeAuthority(t, fixture, fake)
	outcome := materializeAndApproveCompanyOpsArtifact(t, ctx, fixture, artifactService)

	promotionID := uuid.NewString()
	promotion := companyOpsArtifactPromotionRequest(fixture, outcome.Candidate.ID, promotionID)
	if _, err := artifactService.PromoteArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, promotion); err != nil {
		t.Fatalf("PromoteArtifact: %v", err)
	}
	confirmed, err := artifactService.GetIssueOutcome(ctx, fixture.company.workspaceID, fixture.company.issueID)
	if err != nil {
		t.Fatalf("GetIssueOutcome: %v", err)
	}
	previous := companyops.HiveCosmAuthorityTransitionSnapshot{
		Revision:      promotion.WorkOrder.Revision,
		ContentDigest: promotion.WorkOrder.ContentDigest,
	}
	resulting := promotion.WorkOrder
	resulting.Revision = "sha256:" + strings.Repeat("f", 64)
	resulting.ContentDigest = resulting.Revision
	expectation := CompanyOpsWorkOrderTransitionExpectation{
		Lookup:             promotion.Lookup,
		PreviousAuthority:  previous,
		ResultingAuthority: resulting,
		Employee:           promotion.Employee,
		IdentityBinding:    promotion.IdentityBinding,
		Agent:              promotion.Agent,
		LocalAgentID:       fixture.company.agentID,
	}
	baseProof := companyops.HiveCosmWorkOrderTransitionProof{
		WorkOrderSourceRef: promotion.Lookup.WorkOrderSourceRef,
		PreviousAuthority:  previous,
		ResultingAuthority: companyops.HiveCosmAuthorityTransitionSnapshot{
			Revision:      resulting.Revision,
			ContentDigest: resulting.ContentDigest,
		},
		PromotionID:       promotionID,
		CandidateID:       confirmed.Candidate.ID,
		ApprovalEventID:   fake.lastReadCandidate.ApprovalEventID,
		FormalArtifactRef: promotionTestFormalArtifactRef,
	}
	baseArtifact := companyops.HiveCosmFormalArtifact{
		FormalArtifactRef:   promotionTestFormalArtifactRef,
		CandidateID:         confirmed.Candidate.ID,
		CandidateRevision:   confirmed.Candidate.Revision,
		CandidateDigest:     confirmed.Candidate.Digest,
		ApprovalEventID:     fake.lastReadCandidate.ApprovalEventID,
		WorkOrderTransition: &baseProof,
	}
	fake.readArtifact = &baseArtifact
	if err := artifactService.VerifyWorkOrderTransitionForGet(ctx, fixture.company.workspaceID, fixture.company.issueID, expectation); err != nil {
		t.Fatalf("VerifyWorkOrderTransitionForGet: %v", err)
	}

	expectationDrifts := []struct {
		name   string
		tamper func(*CompanyOpsWorkOrderTransitionExpectation)
	}{
		{name: "local Agent ID", tamper: func(value *CompanyOpsWorkOrderTransitionExpectation) {
			value.LocalAgentID = util.MustParseUUID(uuid.NewString())
		}},
		{name: "previous WorkOrder revision", tamper: func(value *CompanyOpsWorkOrderTransitionExpectation) { value.PreviousAuthority.Revision += "-other" }},
		{name: "Employee ref", tamper: func(value *CompanyOpsWorkOrderTransitionExpectation) { value.Employee.SourceRef += "-other" }},
		{name: "IdentityBinding ref", tamper: func(value *CompanyOpsWorkOrderTransitionExpectation) { value.IdentityBinding.SourceRef += "-other" }},
		{name: "Agent ref", tamper: func(value *CompanyOpsWorkOrderTransitionExpectation) {
			value.Agent.SourceRef = "/api/agents/77777777-7777-4777-8777-777777777777"
		}},
	}
	for _, test := range expectationDrifts {
		t.Run("assignment receipt "+test.name, func(t *testing.T) {
			drifted := expectation
			test.tamper(&drifted)
			readCountBefore := fake.readCount
			if err := artifactService.VerifyWorkOrderTransitionForGet(ctx, fixture.company.workspaceID, fixture.company.issueID, drifted); !errors.Is(err, ErrCompanyOpsArtifactConflict) {
				t.Fatalf("VerifyWorkOrderTransitionForGet error = %v, want ErrCompanyOpsArtifactConflict", err)
			}
			if fake.readCount != readCountBefore {
				t.Fatalf("assignment drift touched Formal Artifact authority: read count %d, want %d", fake.readCount, readCountBefore)
			}
		})
	}

	t.Run("same Agent identity may evolve after the formal Run", func(t *testing.T) {
		evolved := expectation
		evolved.Agent.Revision = "updated_at:2026-08-11T11:32:36.054371Z"
		evolved.Agent.ContentDigest = "sha256:" + strings.Repeat("9", 64)
		readCountBefore := fake.readCount
		if err := artifactService.VerifyWorkOrderTransitionForGet(ctx, fixture.company.workspaceID, fixture.company.issueID, evolved); err != nil {
			t.Fatalf("VerifyWorkOrderTransitionForGet after Agent configuration evolution: %v", err)
		}
		if fake.readCount != readCountBefore+1 {
			t.Fatalf("formal readback count = %d, want %d", fake.readCount, readCountBefore+1)
		}
	})

	tests := []struct {
		name   string
		tamper func(*companyops.HiveCosmFormalArtifact)
	}{
		{name: "formal artifact core ref", tamper: func(artifact *companyops.HiveCosmFormalArtifact) { artifact.FormalArtifactRef += "-other" }},
		{name: "formal artifact core candidate", tamper: func(artifact *companyops.HiveCosmFormalArtifact) { artifact.CandidateID = uuid.NewString() }},
		{name: "formal artifact core revision", tamper: func(artifact *companyops.HiveCosmFormalArtifact) { artifact.CandidateRevision++ }},
		{name: "formal artifact core digest", tamper: func(artifact *companyops.HiveCosmFormalArtifact) {
			artifact.CandidateDigest = "sha256:" + strings.Repeat("0", 64)
		}},
		{name: "formal artifact core approval", tamper: func(artifact *companyops.HiveCosmFormalArtifact) { artifact.ApprovalEventID = uuid.NewString() }},
		{name: "missing proof", tamper: func(artifact *companyops.HiveCosmFormalArtifact) { artifact.WorkOrderTransition = nil }},
		{name: "source ref", tamper: func(artifact *companyops.HiveCosmFormalArtifact) {
			artifact.WorkOrderTransition.WorkOrderSourceRef += "-other"
		}},
		{name: "previous revision", tamper: func(artifact *companyops.HiveCosmFormalArtifact) {
			artifact.WorkOrderTransition.PreviousAuthority.Revision += "-other"
		}},
		{name: "previous digest", tamper: func(artifact *companyops.HiveCosmFormalArtifact) {
			artifact.WorkOrderTransition.PreviousAuthority.ContentDigest = "sha256:" + strings.Repeat("0", 64)
		}},
		{name: "resulting revision", tamper: func(artifact *companyops.HiveCosmFormalArtifact) {
			artifact.WorkOrderTransition.ResultingAuthority.Revision += "-other"
		}},
		{name: "resulting digest", tamper: func(artifact *companyops.HiveCosmFormalArtifact) {
			artifact.WorkOrderTransition.ResultingAuthority.ContentDigest = "sha256:" + strings.Repeat("0", 64)
		}},
		{name: "promotion id", tamper: func(artifact *companyops.HiveCosmFormalArtifact) {
			artifact.WorkOrderTransition.PromotionID = uuid.NewString()
		}},
		{name: "candidate id", tamper: func(artifact *companyops.HiveCosmFormalArtifact) {
			artifact.WorkOrderTransition.CandidateID = uuid.NewString()
		}},
		{name: "approval id", tamper: func(artifact *companyops.HiveCosmFormalArtifact) {
			artifact.WorkOrderTransition.ApprovalEventID = uuid.NewString()
		}},
		{name: "formal ref", tamper: func(artifact *companyops.HiveCosmFormalArtifact) {
			artifact.WorkOrderTransition.FormalArtifactRef += "-other"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			artifact := baseArtifact
			proof := baseProof
			artifact.WorkOrderTransition = &proof
			test.tamper(&artifact)
			fake.readArtifact = &artifact
			if err := artifactService.VerifyWorkOrderTransitionForGet(ctx, fixture.company.workspaceID, fixture.company.issueID, expectation); !errors.Is(err, ErrCompanyOpsArtifactConflict) {
				t.Fatalf("VerifyWorkOrderTransitionForGet error = %v, want ErrCompanyOpsArtifactConflict", err)
			}
		})
	}
}

func TestCompanyOpsArtifactOutcome_PromotionRejectsAssignmentTargetDriftBeforeAuthority(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	fake := &fakeFormalArtifactAuthority{formalArtifactRef: promotionTestFormalArtifactRef}
	artifactService := newCompanyOpsArtifactServiceWithFakeAuthority(t, fixture, fake)
	outcome := materializeAndApproveCompanyOpsArtifact(t, ctx, fixture, artifactService)
	base := companyOpsArtifactPromotionRequest(fixture, outcome.Candidate.ID, uuid.NewString())

	tests := []struct {
		name   string
		tamper func(*CompanyOpsArtifactPromotion)
	}{
		{name: "local Agent ID", tamper: func(value *CompanyOpsArtifactPromotion) { value.Lookup.AgentID = uuid.NewString() }},
		{name: "WorkOrder revision", tamper: func(value *CompanyOpsArtifactPromotion) { value.WorkOrder.Revision += "-other" }},
		{name: "WorkOrder digest", tamper: func(value *CompanyOpsArtifactPromotion) {
			value.WorkOrder.ContentDigest = "sha256:" + strings.Repeat("0", 64)
		}},
		{name: "Employee ref", tamper: func(value *CompanyOpsArtifactPromotion) { value.Employee.SourceRef += "-other" }},
		{name: "Binding ref", tamper: func(value *CompanyOpsArtifactPromotion) { value.IdentityBinding.SourceRef += "-other" }},
		{name: "Agent digest", tamper: func(value *CompanyOpsArtifactPromotion) {
			value.Agent.ContentDigest = "sha256:" + strings.Repeat("0", 64)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			drifted := base
			drifted.PromotionID = uuid.NewString()
			test.tamper(&drifted)
			if _, err := artifactService.PromoteArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, drifted); !errors.Is(err, ErrCompanyOpsArtifactConflict) {
				t.Fatalf("PromoteArtifact error = %v, want ErrCompanyOpsArtifactConflict", err)
			}
			if fake.promoteCount != 0 || fake.readCount != 0 {
				t.Fatalf("assignment drift touched authority: promote %d read %d, want 0/0", fake.promoteCount, fake.readCount)
			}
		})
	}
}

// appendCompanyOpsPromotionEvent writes a promotion-phase ledger event
// directly through the repository so a test can construct an exact ledger
// state (already-requested, mixed-id) without driving the external authority.
// The idempotency key follows the service grammar so the resolver can parse it.
func appendCompanyOpsPromotionEvent(
	t *testing.T,
	ctx context.Context,
	artifactService *CompanyOpsArtifactService,
	workspace, lineageID string,
	candidate companyops.ArtifactCandidate,
	eventType companyops.ArtifactEventType,
	promotionID, suffix string,
) companyops.ArtifactEvent {
	t.Helper()
	event, err := artifactService.repo.AppendArtifactEvent(ctx, workspace, lineageID, companyops.ArtifactEventInput{
		Type:               eventType,
		CandidateID:        candidate.ID,
		CandidateRevision:  candidate.Revision,
		CandidateDigest:    candidate.Digest,
		CandidateObjectRef: candidate.DurableObjectRef,
		IdempotencyKey:     "formal-promotion:" + promotionID + ":" + suffix,
	})
	if err != nil {
		t.Fatalf("append %s promotion event: %v", eventType, err)
	}
	return event
}

// TestCompanyOpsArtifactOutcome_PromotionSameIDExactReplayNoDuplicateCalls
// verifies that once a candidate reaches authority_readback_confirmed, an
// exact replay with the same promotion id returns the original receipt and
// performs no authority POST or GET.
func TestCompanyOpsArtifactOutcome_PromotionSameIDExactReplayNoDuplicateCalls(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	fake := &fakeFormalArtifactAuthority{formalArtifactRef: promotionTestFormalArtifactRef}
	artifactService := newCompanyOpsArtifactServiceWithFakeAuthority(t, fixture, fake)
	outcome := materializeAndApproveCompanyOpsArtifact(t, ctx, fixture, artifactService)

	originalPromotionID := uuid.NewString()
	promotion := companyOpsArtifactPromotionRequest(fixture, outcome.Candidate.ID, originalPromotionID)
	if _, err := artifactService.PromoteArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, promotion); err != nil {
		t.Fatalf("PromoteArtifact to confirmed: %v", err)
	}
	if fake.promoteCount != 1 || fake.readCount != 1 {
		t.Fatalf("authority call counts = promote %d read %d, want 1/1", fake.promoteCount, fake.readCount)
	}

	replay, err := artifactService.PromoteArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, promotion)
	if err != nil {
		t.Fatalf("PromoteArtifact(exact replay): %v", err)
	}
	if replay.LifecycleStatus != companyops.ArtifactEventAuthorityReadbackConfirmed {
		t.Fatalf("replay lifecycle status = %q, want authority_readback_confirmed", replay.LifecycleStatus)
	}
	if replay.PromotionID != originalPromotionID {
		t.Fatalf("replay promotion id = %q, want original %q", replay.PromotionID, originalPromotionID)
	}
	if !replay.FormalVisible || replay.FormalArtifactRef != promotionTestFormalArtifactRef {
		t.Fatalf("replay did not return the original confirmed receipt: %+v", replay)
	}
	if fake.promoteCount != 1 || fake.readCount != 1 {
		t.Fatalf("exact replay duplicated authority calls: promote %d read %d, want 1/1", fake.promoteCount, fake.readCount)
	}
}

// TestCompanyOpsArtifactOutcome_PromotionConflictOnConfirmedDifferentID
// verifies that after authority_readback_confirmed, a replay carrying a
// different promotion id fails closed without re-reading the authority or
// appending any event, while the original id still replays cleanly.
func TestCompanyOpsArtifactOutcome_PromotionConflictOnConfirmedDifferentID(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	fake := &fakeFormalArtifactAuthority{formalArtifactRef: promotionTestFormalArtifactRef}
	artifactService := newCompanyOpsArtifactServiceWithFakeAuthority(t, fixture, fake)
	outcome := materializeAndApproveCompanyOpsArtifact(t, ctx, fixture, artifactService)

	originalPromotionID := uuid.NewString()
	promotion := companyOpsArtifactPromotionRequest(fixture, outcome.Candidate.ID, originalPromotionID)
	if _, err := artifactService.PromoteArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, promotion); err != nil {
		t.Fatalf("PromoteArtifact to confirmed: %v", err)
	}

	workspace := util.UUIDToString(fixture.company.workspaceID)
	lineageID := util.UUIDToString(outcome.CommandID)
	eventsBefore, err := artifactService.repo.ListArtifactEvents(ctx, workspace, lineageID)
	if err != nil {
		t.Fatalf("ListArtifactEvents before: %v", err)
	}

	conflicting := promotion
	conflicting.PromotionID = uuid.NewString()
	if _, err := artifactService.PromoteArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, conflicting); !errors.Is(err, ErrCompanyOpsArtifactConflict) {
		t.Fatalf("different-id replay on confirmed error = %v, want ErrCompanyOpsArtifactConflict", err)
	}
	if fake.promoteCount != 1 || fake.readCount != 1 {
		t.Fatalf("different-id conflict touched authority: promote %d read %d, want 1/1", fake.promoteCount, fake.readCount)
	}
	eventsAfter, err := artifactService.repo.ListArtifactEvents(ctx, workspace, lineageID)
	if err != nil {
		t.Fatalf("ListArtifactEvents after: %v", err)
	}
	if len(eventsAfter) != len(eventsBefore) {
		t.Fatalf("different-id conflict appended events: before %d after %d", len(eventsBefore), len(eventsAfter))
	}

	replay, err := artifactService.PromoteArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, promotion)
	if err != nil {
		t.Fatalf("PromoteArtifact(original replay after conflict): %v", err)
	}
	if replay.PromotionID != originalPromotionID {
		t.Fatalf("original replay promotion id = %q, want %q", replay.PromotionID, originalPromotionID)
	}
	if fake.promoteCount != 1 || fake.readCount != 1 {
		t.Fatalf("original replay duplicated authority calls: promote %d read %d", fake.promoteCount, fake.readCount)
	}
}

// TestCompanyOpsArtifactOutcome_PromotionConflictOnSucceededDifferentID
// verifies that a candidate paused at promotion_succeeded (POST done, readback
// pending) rejects a different promotion id without a duplicate POST or GET.
func TestCompanyOpsArtifactOutcome_PromotionConflictOnSucceededDifferentID(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	fake := &fakeFormalArtifactAuthority{
		formalArtifactRef: promotionTestFormalArtifactRef,
		readErr:           errors.New("simulated HiveCosm readback failure"),
	}
	artifactService := newCompanyOpsArtifactServiceWithFakeAuthority(t, fixture, fake)
	outcome := materializeAndApproveCompanyOpsArtifact(t, ctx, fixture, artifactService)

	originalPromotionID := uuid.NewString()
	promotion := companyOpsArtifactPromotionRequest(fixture, outcome.Candidate.ID, originalPromotionID)
	if _, err := artifactService.PromoteArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, promotion); err == nil {
		t.Fatal("PromoteArtifact expected readback failure leaving candidate at promotion_succeeded")
	}
	succeededOutcome, err := artifactService.GetIssueOutcome(ctx, fixture.company.workspaceID, fixture.company.issueID)
	if err != nil {
		t.Fatalf("GetIssueOutcome: %v", err)
	}
	if succeededOutcome.Projection.Status != companyops.ArtifactEventPromotionSucceeded {
		t.Fatalf("projection status = %q, want promotion_succeeded", statusOrEmpty(succeededOutcome))
	}

	workspace := util.UUIDToString(fixture.company.workspaceID)
	lineageID := util.UUIDToString(outcome.CommandID)
	eventsBefore, err := artifactService.repo.ListArtifactEvents(ctx, workspace, lineageID)
	if err != nil {
		t.Fatalf("ListArtifactEvents before: %v", err)
	}
	promoteCountBefore, readCountBefore := fake.promoteCount, fake.readCount

	conflicting := promotion
	conflicting.PromotionID = uuid.NewString()
	if _, err := artifactService.PromoteArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, conflicting); !errors.Is(err, ErrCompanyOpsArtifactConflict) {
		t.Fatalf("different-id replay on succeeded error = %v, want ErrCompanyOpsArtifactConflict", err)
	}
	if fake.promoteCount != promoteCountBefore || fake.readCount != readCountBefore {
		t.Fatalf("different-id conflict touched authority: promote %d read %d, want %d/%d", fake.promoteCount, fake.readCount, promoteCountBefore, readCountBefore)
	}
	eventsAfter, err := artifactService.repo.ListArtifactEvents(ctx, workspace, lineageID)
	if err != nil {
		t.Fatalf("ListArtifactEvents after: %v", err)
	}
	if len(eventsAfter) != len(eventsBefore) {
		t.Fatalf("different-id conflict appended events: before %d after %d", len(eventsBefore), len(eventsAfter))
	}
}

// TestCompanyOpsArtifactOutcome_PromotionConflictOnRequestedDifferentID
// verifies that an anchored promotion_requested event cannot be borrowed by a
// command carrying a different promotion id: no fresh POST, no appended event.
// The original id must still resume and reach the authority exactly once.
func TestCompanyOpsArtifactOutcome_PromotionConflictOnRequestedDifferentID(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	fake := &fakeFormalArtifactAuthority{formalArtifactRef: promotionTestFormalArtifactRef}
	artifactService := newCompanyOpsArtifactServiceWithFakeAuthority(t, fixture, fake)
	outcome := materializeAndApproveCompanyOpsArtifact(t, ctx, fixture, artifactService)
	candidate := *outcome.Candidate

	workspace := util.UUIDToString(fixture.company.workspaceID)
	lineageID := util.UUIDToString(outcome.CommandID)
	originalPromotionID := uuid.NewString()
	appendCompanyOpsPromotionEvent(t, ctx, artifactService, workspace, lineageID, candidate,
		companyops.ArtifactEventPromotionRequested, originalPromotionID, "requested:after:0")

	requestedOutcome, err := artifactService.GetIssueOutcome(ctx, fixture.company.workspaceID, fixture.company.issueID)
	if err != nil {
		t.Fatalf("GetIssueOutcome: %v", err)
	}
	if requestedOutcome.Projection.Status != companyops.ArtifactEventPromotionRequested {
		t.Fatalf("projection status = %q, want promotion_requested", statusOrEmpty(requestedOutcome))
	}

	eventsBefore, err := artifactService.repo.ListArtifactEvents(ctx, workspace, lineageID)
	if err != nil {
		t.Fatalf("ListArtifactEvents before: %v", err)
	}
	promoteCountBefore := fake.promoteCount

	conflicting := companyOpsArtifactPromotionRequest(fixture, candidate.ID, uuid.NewString())
	if _, err := artifactService.PromoteArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, conflicting); !errors.Is(err, ErrCompanyOpsArtifactConflict) {
		t.Fatalf("different-id replay on requested error = %v, want ErrCompanyOpsArtifactConflict", err)
	}
	if fake.promoteCount != promoteCountBefore {
		t.Fatalf("different-id conflict caused a POST: promote count %d, want %d", fake.promoteCount, promoteCountBefore)
	}
	eventsAfter, err := artifactService.repo.ListArtifactEvents(ctx, workspace, lineageID)
	if err != nil {
		t.Fatalf("ListArtifactEvents after: %v", err)
	}
	if len(eventsAfter) != len(eventsBefore) {
		t.Fatalf("different-id conflict appended events: before %d after %d", len(eventsBefore), len(eventsAfter))
	}

	resume := companyOpsArtifactPromotionRequest(fixture, candidate.ID, originalPromotionID)
	receipt, err := artifactService.PromoteArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, resume)
	if err != nil {
		t.Fatalf("PromoteArtifact(original resume): %v", err)
	}
	if receipt.LifecycleStatus != companyops.ArtifactEventAuthorityReadbackConfirmed {
		t.Fatalf("resume lifecycle status = %q, want authority_readback_confirmed", receipt.LifecycleStatus)
	}
	if fake.promoteCount != promoteCountBefore+1 {
		t.Fatalf("resume promote count = %d, want %d", fake.promoteCount, promoteCountBefore+1)
	}
	if receipt.PromotionID != originalPromotionID {
		t.Fatalf("resume receipt promotion id = %q, want %q", receipt.PromotionID, originalPromotionID)
	}
}

// TestCompanyOpsArtifactOutcome_PromotionConflictOnMixedIDLedger verifies that
// a ledger whose valid-transition promotion-phase events anchor two different
// ids is rejected for either id without touching the authority or the ledger.
func TestCompanyOpsArtifactOutcome_PromotionConflictOnMixedIDLedger(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	fake := &fakeFormalArtifactAuthority{formalArtifactRef: promotionTestFormalArtifactRef}
	artifactService := newCompanyOpsArtifactServiceWithFakeAuthority(t, fixture, fake)
	outcome := materializeAndApproveCompanyOpsArtifact(t, ctx, fixture, artifactService)
	candidate := *outcome.Candidate

	workspace := util.UUIDToString(fixture.company.workspaceID)
	lineageID := util.UUIDToString(outcome.CommandID)
	firstID := uuid.NewString()
	secondID := uuid.NewString()
	// Valid transition chain requested(A) -> failed(A) -> requested(B) whose
	// promotion-phase events anchor two distinct ids.
	appendCompanyOpsPromotionEvent(t, ctx, artifactService, workspace, lineageID, candidate,
		companyops.ArtifactEventPromotionRequested, firstID, "requested:after:0")
	appendCompanyOpsPromotionEvent(t, ctx, artifactService, workspace, lineageID, candidate,
		companyops.ArtifactEventPromotionFailed, firstID, "failed:after:0")
	appendCompanyOpsPromotionEvent(t, ctx, artifactService, workspace, lineageID, candidate,
		companyops.ArtifactEventPromotionRequested, secondID, "requested:after:1")

	eventsBefore, err := artifactService.repo.ListArtifactEvents(ctx, workspace, lineageID)
	if err != nil {
		t.Fatalf("ListArtifactEvents before: %v", err)
	}
	promoteCountBefore := fake.promoteCount

	for _, promotionID := range []string{firstID, secondID} {
		conflicting := companyOpsArtifactPromotionRequest(fixture, candidate.ID, promotionID)
		if _, err := artifactService.PromoteArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, conflicting); !errors.Is(err, ErrCompanyOpsArtifactConflict) {
			t.Fatalf("mixed-id ledger replay with %s error = %v, want ErrCompanyOpsArtifactConflict", promotionID, err)
		}
	}
	if fake.promoteCount != promoteCountBefore {
		t.Fatalf("mixed-id conflict caused a POST: promote count %d, want %d", fake.promoteCount, promoteCountBefore)
	}
	eventsAfter, err := artifactService.repo.ListArtifactEvents(ctx, workspace, lineageID)
	if err != nil {
		t.Fatalf("ListArtifactEvents after: %v", err)
	}
	if len(eventsAfter) != len(eventsBefore) {
		t.Fatalf("mixed-id conflict appended events: before %d after %d", len(eventsBefore), len(eventsAfter))
	}
}

// TestCompanyOpsArtifactPromotionKeyGrammar is a table-driven test for the
// strict suffix grammar and promotion-id extraction. It covers valid keys for
// every phase and a battery of malformed/extra/cross-phase rejections.
func TestCompanyOpsArtifactPromotionKeyGrammar(t *testing.T) {
	validID := uuid.NewString()
	prefix := "formal-promotion:" + validID + ":"

	tests := []struct {
		name      string
		eventType companyops.ArtifactEventType
		suffix    string
		wantParse bool
		wantValid bool
	}{
		// Valid suffixes per phase
		{"requested after 0", companyops.ArtifactEventPromotionRequested, "requested:after:0", true, true},
		{"requested after 1", companyops.ArtifactEventPromotionRequested, "requested:after:1", true, true},
		{"requested after 42", companyops.ArtifactEventPromotionRequested, "requested:after:42", true, true},
		{"failed after 0", companyops.ArtifactEventPromotionFailed, "failed:after:0", true, true},
		{"failed after 3", companyops.ArtifactEventPromotionFailed, "failed:after:3", true, true},
		{"succeeded", companyops.ArtifactEventPromotionSucceeded, "succeeded", true, true},
		{"readback", companyops.ArtifactEventAuthorityReadbackConfirmed, "readback", true, true},

		// Malformed decimal
		{"requested after empty", companyops.ArtifactEventPromotionRequested, "requested:after:", true, false},
		{"requested after negative", companyops.ArtifactEventPromotionRequested, "requested:after:-1", true, false},
		{"requested after hex", companyops.ArtifactEventPromotionRequested, "requested:after:0x1", true, false},
		{"requested after leading zero", companyops.ArtifactEventPromotionRequested, "requested:after:00", true, false},
		{"requested after double zero", companyops.ArtifactEventPromotionRequested, "requested:after:01", true, false},
		{"failed after empty", companyops.ArtifactEventPromotionFailed, "failed:after:", true, false},
		{"failed after letters", companyops.ArtifactEventPromotionFailed, "failed:after:abc", true, false},

		// Wrong phase in suffix (cross-phase)
		{"succeeded with requested suffix", companyops.ArtifactEventPromotionSucceeded, "requested:after:0", true, false},
		{"readback with succeeded suffix", companyops.ArtifactEventAuthorityReadbackConfirmed, "succeeded", true, false},
		{"requested with succeeded suffix", companyops.ArtifactEventPromotionRequested, "succeeded", true, false},
		{"requested with readback suffix", companyops.ArtifactEventPromotionRequested, "readback", true, false},
		{"failed with succeeded suffix", companyops.ArtifactEventPromotionFailed, "succeeded", true, false},

		// Extra/omitted segments
		{"succeeded with extra", companyops.ArtifactEventPromotionSucceeded, "succeeded:extra", true, false},
		{"readback with extra", companyops.ArtifactEventAuthorityReadbackConfirmed, "readback:extra", true, false},
		{"requested missing after", companyops.ArtifactEventPromotionRequested, "requested:0", true, false},
		{"requested wrong prefix", companyops.ArtifactEventPromotionRequested, "promote:after:0", true, false},
		{"arbitrary", companyops.ArtifactEventPromotionRequested, "arbitrary", true, false},

		// Non-promotion event types always invalid
		{"approved is not promotion phase", companyops.ArtifactEventApproved, "succeeded", true, false},
		{"submitted is not promotion phase", companyops.ArtifactEventSubmitted, "readback", true, false},

		// Structural parse failures (suffix empty)
		{"empty suffix", companyops.ArtifactEventPromotionRequested, "", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := prefix + tt.suffix
			if tt.suffix == "" {
				key = "formal-promotion:" + validID + ":"
			}
			promotionID, suffix, ok := companyOpsPromotionIDAndSuffixFromEventKey(key)
			if ok != tt.wantParse {
				t.Fatalf("parse key %q: ok=%v, want %v (promotionID=%q suffix=%q)", key, ok, tt.wantParse, promotionID, suffix)
			}
			if !ok {
				return
			}
			if promotionID != validID {
				t.Fatalf("promotionID = %q, want %q", promotionID, validID)
			}
			valid := companyOpsArtifactValidatePromotionSuffix(tt.eventType, suffix)
			if valid != tt.wantValid {
				t.Fatalf("validate suffix %q for %s: valid=%v, want %v", suffix, tt.eventType, valid, tt.wantValid)
			}
		})
	}
}

// TestCompanyOpsArtifactPromotionKeyParseFailures covers structural parse
// failures that do not depend on suffix grammar.
func TestCompanyOpsArtifactPromotionKeyParseFailures(t *testing.T) {
	validID := uuid.NewString()
	tests := []struct {
		name string
		key  string
	}{
		{"wrong prefix", "informal-promotion:" + validID + ":succeeded"},
		{"missing colon after uuid", "formal-promotion:" + validID + "succeeded"},
		{"short uuid", "formal-promotion:abc:succeeded"},
		{"uppercase uuid", "formal-promotion:" + strings.ToUpper(validID) + ":succeeded"},
		{"braced uuid", "formal-promotion:{" + validID + "}:succeeded"},
		{"empty", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, ok := companyOpsPromotionIDAndSuffixFromEventKey(tt.key)
			if ok {
				t.Fatalf("parse key %q: unexpectedly succeeded", tt.key)
			}
		})
	}
}

// TestCompanyOpsArtifactOutcome_PromotionClaimSameIDDifferentCandidate verifies
// that the durable promotion claim table prevents the same promotion_id from
// being claimed for a different candidate, even when the scan-based resolver
// has no events to detect the conflict. The claim fails closed before any
// authority POST, event append, or GET.
func TestCompanyOpsArtifactOutcome_PromotionClaimSameIDDifferentCandidate(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	fake := &fakeFormalArtifactAuthority{formalArtifactRef: promotionTestFormalArtifactRef}
	artifactService := newCompanyOpsArtifactServiceWithFakeAuthority(t, fixture, fake)
	outcome := materializeAndApproveCompanyOpsArtifact(t, ctx, fixture, artifactService)
	candidate := *outcome.Candidate

	workspace := util.UUIDToString(fixture.company.workspaceID)
	lineageID := util.UUIDToString(outcome.CommandID)

	// Pre-claim the promotion_id for a different candidate+lineage using a
	// direct repository call. The claim table has no FK, so phantom UUIDs work.
	otherCandidate := uuid.NewString()
	otherLineage := uuid.NewString()
	sharedPromotionID := uuid.NewString()
	preclaimPayload := companyops.PromotionClaimPayload{
		ActorUserID:       "preclaim-actor",
		CandidateRevision: 99,
		CandidateDigest:   "sha256:preclaim",
	}
	if err := artifactService.repo.ClaimPromotion(ctx, workspace, sharedPromotionID, otherCandidate, otherLineage, preclaimPayload); err != nil {
		t.Fatalf("pre-claim for phantom candidate: %v", err)
	}

	eventsBefore, err := artifactService.repo.ListArtifactEvents(ctx, workspace, lineageID)
	if err != nil {
		t.Fatalf("ListArtifactEvents before: %v", err)
	}

	promotion := companyOpsArtifactPromotionRequest(fixture, candidate.ID, sharedPromotionID)
	if _, err := artifactService.PromoteArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, promotion); !errors.Is(err, companyops.ErrArtifactPromotionConflict) {
		t.Fatalf("same-id different-candidate error = %v, want ErrArtifactPromotionConflict", err)
	}
	if fake.promoteCount != 0 || fake.readCount != 0 {
		t.Fatalf("conflict touched authority: promote %d read %d, want 0/0", fake.promoteCount, fake.readCount)
	}
	eventsAfter, err := artifactService.repo.ListArtifactEvents(ctx, workspace, lineageID)
	if err != nil {
		t.Fatalf("ListArtifactEvents after: %v", err)
	}
	if len(eventsAfter) != len(eventsBefore) {
		t.Fatalf("conflict appended events: before %d after %d", len(eventsBefore), len(eventsAfter))
	}

	// The real candidate can still be promoted with its own fresh promotion_id.
	freshPromotion := companyOpsArtifactPromotionRequest(fixture, candidate.ID, uuid.NewString())
	receipt, err := artifactService.PromoteArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, freshPromotion)
	if err != nil {
		t.Fatalf("PromoteArtifact with fresh id after conflict: %v", err)
	}
	if receipt.LifecycleStatus != companyops.ArtifactEventAuthorityReadbackConfirmed {
		t.Fatalf("lifecycle status = %q, want authority_readback_confirmed", receipt.LifecycleStatus)
	}
	if fake.promoteCount != 1 {
		t.Fatalf("fresh promotion promote count = %d, want 1", fake.promoteCount)
	}
}

// TestCompanyOpsArtifactOutcome_PromotionClaimDifferentIDSameCandidate verifies
// that once a candidate has claimed a promotion_id, a subsequent attempt with
// a different promotion_id for the same candidate fails at the durable claim.
func TestCompanyOpsArtifactOutcome_PromotionClaimDifferentIDSameCandidate(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	fake := &fakeFormalArtifactAuthority{formalArtifactRef: promotionTestFormalArtifactRef}
	artifactService := newCompanyOpsArtifactServiceWithFakeAuthority(t, fixture, fake)
	outcome := materializeAndApproveCompanyOpsArtifact(t, ctx, fixture, artifactService)
	candidate := *outcome.Candidate

	workspace := util.UUIDToString(fixture.company.workspaceID)
	lineageID := util.UUIDToString(outcome.CommandID)

	firstID := uuid.NewString()
	promotion := companyOpsArtifactPromotionRequest(fixture, candidate.ID, firstID)
	if _, err := artifactService.PromoteArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, promotion); err != nil {
		t.Fatalf("first PromoteArtifact: %v", err)
	}
	if fake.promoteCount != 1 {
		t.Fatalf("first promote count = %d, want 1", fake.promoteCount)
	}

	eventsBefore, err := artifactService.repo.ListArtifactEvents(ctx, workspace, lineageID)
	if err != nil {
		t.Fatalf("ListArtifactEvents before: %v", err)
	}

	conflicting := companyOpsArtifactPromotionRequest(fixture, candidate.ID, uuid.NewString())
	if _, err := artifactService.PromoteArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, conflicting); !errors.Is(err, ErrCompanyOpsArtifactConflict) {
		t.Fatalf("different-id same-candidate error = %v, want ErrCompanyOpsArtifactConflict", err)
	}
	if fake.promoteCount != 1 || fake.readCount != 1 {
		t.Fatalf("conflict touched authority: promote %d read %d, want 1/1", fake.promoteCount, fake.readCount)
	}
	eventsAfter, err := artifactService.repo.ListArtifactEvents(ctx, workspace, lineageID)
	if err != nil {
		t.Fatalf("ListArtifactEvents after: %v", err)
	}
	if len(eventsAfter) != len(eventsBefore) {
		t.Fatalf("conflict appended events: before %d after %d", len(eventsBefore), len(eventsAfter))
	}
}

// TestCompanyOpsArtifactOutcome_PromotionClaimConcurrentDifferentID verifies
// that two concurrent promotion attempts with different ids for the same
// candidate result in exactly one successful authority POST.
func TestCompanyOpsArtifactOutcome_PromotionClaimConcurrentDifferentID(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	fake := &fakeFormalArtifactAuthority{formalArtifactRef: promotionTestFormalArtifactRef}
	artifactService := newCompanyOpsArtifactServiceWithFakeAuthority(t, fixture, fake)
	outcome := materializeAndApproveCompanyOpsArtifact(t, ctx, fixture, artifactService)
	candidate := *outcome.Candidate

	firstID := uuid.NewString()
	secondID := uuid.NewString()
	first := companyOpsArtifactPromotionRequest(fixture, candidate.ID, firstID)
	second := companyOpsArtifactPromotionRequest(fixture, candidate.ID, secondID)

	type result struct {
		receipt CompanyOpsArtifactPromotionReceipt
		err     error
	}
	results := make(chan result, 2)
	go func() {
		r, err := artifactService.PromoteArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, first)
		results <- result{r, err}
	}()
	go func() {
		r, err := artifactService.PromoteArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, second)
		results <- result{r, err}
	}()

	var successes, conflicts int
	for i := 0; i < 2; i++ {
		select {
		case res := <-results:
			if res.err == nil {
				successes++
			} else if errors.Is(res.err, ErrCompanyOpsArtifactConflict) || errors.Is(res.err, companyops.ErrArtifactPromotionConflict) {
				conflicts++
			} else {
				t.Fatalf("unexpected error from concurrent promotion: %v", res.err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("concurrent promotion timed out")
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent different-id promotions: successes = %d, want 1", successes)
	}
	if conflicts != 1 {
		t.Fatalf("concurrent different-id promotions: conflicts = %d, want 1", conflicts)
	}
	if fake.promoteCount != 1 {
		t.Fatalf("concurrent promote count = %d, want 1", fake.promoteCount)
	}
}

// TestCompanyOpsArtifactOutcome_PromotionMalformedSuffixRejection verifies that
// a promotion-phase event carrying a suffix that violates the strict per-type
// grammar causes the resolver to fail closed, preventing any authority call or
// event append.
func TestCompanyOpsArtifactOutcome_PromotionMalformedSuffixRejection(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	fake := &fakeFormalArtifactAuthority{formalArtifactRef: promotionTestFormalArtifactRef}
	artifactService := newCompanyOpsArtifactServiceWithFakeAuthority(t, fixture, fake)
	outcome := materializeAndApproveCompanyOpsArtifact(t, ctx, fixture, artifactService)
	candidate := *outcome.Candidate

	workspace := util.UUIDToString(fixture.company.workspaceID)
	lineageID := util.UUIDToString(outcome.CommandID)
	promotionID := uuid.NewString()

	// Inject a promotion_requested event whose suffix violates the strict
	// grammar (arbitrary text instead of requested:after:<decimal>).
	malformedEvent, err := artifactService.repo.AppendArtifactEvent(ctx, workspace, lineageID, companyops.ArtifactEventInput{
		Type:               companyops.ArtifactEventPromotionRequested,
		CandidateID:        candidate.ID,
		CandidateRevision:  candidate.Revision,
		CandidateDigest:    candidate.Digest,
		CandidateObjectRef: candidate.DurableObjectRef,
		IdempotencyKey:     "formal-promotion:" + promotionID + ":arbitrary-text",
	})
	if err != nil {
		t.Fatalf("append malformed event: %v", err)
	}
	_ = malformedEvent

	promoteCountBefore := fake.promoteCount
	eventsBefore, err := artifactService.repo.ListArtifactEvents(ctx, workspace, lineageID)
	if err != nil {
		t.Fatalf("ListArtifactEvents before: %v", err)
	}

	promotion := companyOpsArtifactPromotionRequest(fixture, candidate.ID, promotionID)
	if _, err := artifactService.PromoteArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, promotion); !errors.Is(err, ErrCompanyOpsArtifactConflict) {
		t.Fatalf("malformed suffix error = %v, want ErrCompanyOpsArtifactConflict", err)
	}
	if fake.promoteCount != promoteCountBefore {
		t.Fatalf("malformed suffix caused a POST: promote count %d, want %d", fake.promoteCount, promoteCountBefore)
	}
	eventsAfter, err := artifactService.repo.ListArtifactEvents(ctx, workspace, lineageID)
	if err != nil {
		t.Fatalf("ListArtifactEvents after: %v", err)
	}
	if len(eventsAfter) != len(eventsBefore) {
		t.Fatalf("malformed suffix appended events: before %d after %d", len(eventsBefore), len(eventsAfter))
	}
}

// TestCompanyOpsArtifactOutcome_PromotionPayloadDriftConflict verifies that a
// replay carrying the same promotion_id but a different payload fails closed
// before any authority POST, GET, or event append. Authority snapshot drift is
// rejected by the latest-dispatch guard; claim-only drift is rejected by the
// durable promotion claim.
func TestCompanyOpsArtifactOutcome_PromotionPayloadDriftConflict(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	fake := &fakeFormalArtifactAuthority{formalArtifactRef: promotionTestFormalArtifactRef}
	artifactService := newCompanyOpsArtifactServiceWithFakeAuthority(t, fixture, fake)
	outcome := materializeAndApproveCompanyOpsArtifact(t, ctx, fixture, artifactService)

	originalPromotionID := uuid.NewString()
	promotion := companyOpsArtifactPromotionRequest(fixture, outcome.Candidate.ID, originalPromotionID)
	if _, err := artifactService.PromoteArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, promotion); err != nil {
		t.Fatalf("PromoteArtifact to confirmed: %v", err)
	}
	if fake.promoteCount != 1 || fake.readCount != 1 {
		t.Fatalf("authority call counts = promote %d read %d, want 1/1", fake.promoteCount, fake.readCount)
	}

	workspace := util.UUIDToString(fixture.company.workspaceID)
	lineageID := util.UUIDToString(outcome.CommandID)
	eventsBefore, err := artifactService.repo.ListArtifactEvents(ctx, workspace, lineageID)
	if err != nil {
		t.Fatalf("ListArtifactEvents before: %v", err)
	}

	drifts := []struct {
		name    string
		mutate  func(*CompanyOpsArtifactPromotion)
		wantErr error
	}{
		{"work order content digest", func(p *CompanyOpsArtifactPromotion) { p.WorkOrder.ContentDigest = "sha256:drifted" }, ErrCompanyOpsArtifactConflict},
		{"employee content digest", func(p *CompanyOpsArtifactPromotion) { p.Employee.ContentDigest = "sha256:drifted" }, ErrCompanyOpsArtifactConflict},
		{"identity binding content digest", func(p *CompanyOpsArtifactPromotion) { p.IdentityBinding.ContentDigest = "sha256:drifted" }, ErrCompanyOpsArtifactConflict},
		{"lookup employee id", func(p *CompanyOpsArtifactPromotion) { p.Lookup.EmployeeID += "-drifted" }, companyops.ErrArtifactPromotionConflict},
	}
	for _, drift := range drifts {
		t.Run(drift.name, func(t *testing.T) {
			drifted := promotion
			drift.mutate(&drifted)
			if _, err := artifactService.PromoteArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, drifted); !errors.Is(err, drift.wantErr) {
				t.Fatalf("payload drift error = %v, want %v", err, drift.wantErr)
			}
			if fake.promoteCount != 1 || fake.readCount != 1 {
				t.Fatalf("payload drift touched authority: promote %d read %d, want 1/1", fake.promoteCount, fake.readCount)
			}
			eventsAfter, err := artifactService.repo.ListArtifactEvents(ctx, workspace, lineageID)
			if err != nil {
				t.Fatalf("ListArtifactEvents after: %v", err)
			}
			if len(eventsAfter) != len(eventsBefore) {
				t.Fatalf("payload drift appended events: before %d after %d", len(eventsBefore), len(eventsAfter))
			}
		})
	}

	// Original id still replays cleanly.
	replay, err := artifactService.PromoteArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, promotion)
	if err != nil {
		t.Fatalf("PromoteArtifact(original replay after drift): %v", err)
	}
	if replay.PromotionID != originalPromotionID {
		t.Fatalf("replay promotion id = %q, want %q", replay.PromotionID, originalPromotionID)
	}
	if fake.promoteCount != 1 || fake.readCount != 1 {
		t.Fatalf("original replay duplicated authority calls: promote %d read %d", fake.promoteCount, fake.readCount)
	}
}

// TestCompanyOpsArtifactOutcome_PromotionPayloadDriftOnSucceededConflict
// verifies that a candidate paused at promotion_succeeded rejects a replay
// carrying the same promotion_id but drifted payload without a GET.
func TestCompanyOpsArtifactOutcome_PromotionPayloadDriftOnSucceededConflict(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	fake := &fakeFormalArtifactAuthority{
		formalArtifactRef: promotionTestFormalArtifactRef,
		readErr:           errors.New("simulated HiveCosm readback failure"),
	}
	artifactService := newCompanyOpsArtifactServiceWithFakeAuthority(t, fixture, fake)
	outcome := materializeAndApproveCompanyOpsArtifact(t, ctx, fixture, artifactService)

	originalPromotionID := uuid.NewString()
	promotion := companyOpsArtifactPromotionRequest(fixture, outcome.Candidate.ID, originalPromotionID)
	if _, err := artifactService.PromoteArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, promotion); err == nil {
		t.Fatal("PromoteArtifact expected readback failure leaving candidate at promotion_succeeded")
	}
	succeededOutcome, err := artifactService.GetIssueOutcome(ctx, fixture.company.workspaceID, fixture.company.issueID)
	if err != nil {
		t.Fatalf("GetIssueOutcome: %v", err)
	}
	if succeededOutcome.Projection.Status != companyops.ArtifactEventPromotionSucceeded {
		t.Fatalf("projection status = %q, want promotion_succeeded", statusOrEmpty(succeededOutcome))
	}

	readCountBefore := fake.readCount
	drifted := promotion
	// Lookup.EmployeeID is part of the durable promotion claim payload but is
	// not an execution-target snapshot field, so this exercises VerifyPromotion
	// rather than the earlier latest-dispatch guard.
	drifted.Lookup.EmployeeID += "-drifted"
	if _, err := artifactService.PromoteArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, drifted); !errors.Is(err, companyops.ErrArtifactPromotionConflict) {
		t.Fatalf("payload drift on succeeded error = %v, want ErrArtifactPromotionConflict", err)
	}
	if fake.readCount != readCountBefore {
		t.Fatalf("payload drift on succeeded caused a GET: read count %d, want %d", fake.readCount, readCountBefore)
	}
}

func TestCompanyOpsArtifactOutcome_LegacyTerminalWithoutClaimFailsClosed(t *testing.T) {
	tests := []struct {
		name      string
		confirmed bool
	}{
		{"promotion succeeded", false},
		{"authority readback confirmed", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, fixture := newCompanyOpsExecutionTestFixture(t)
			fake := &fakeFormalArtifactAuthority{formalArtifactRef: promotionTestFormalArtifactRef}
			artifactService := newCompanyOpsArtifactServiceWithFakeAuthority(t, fixture, fake)
			outcome := materializeAndApproveCompanyOpsArtifact(t, ctx, fixture, artifactService)
			candidate := *outcome.Candidate
			workspace := util.UUIDToString(fixture.company.workspaceID)
			lineageID := util.UUIDToString(outcome.CommandID)
			promotionID := uuid.NewString()

			events, err := artifactService.repo.ListArtifactEvents(ctx, workspace, lineageID)
			if err != nil {
				t.Fatalf("ListArtifactEvents: %v", err)
			}
			_, last, hasLast := companyOpsArtifactPromotionAnchor(events, candidate.ID)
			if !hasLast {
				t.Fatal("approved candidate has no ledger anchor")
			}
			requested, err := artifactService.repo.AppendArtifactEvent(ctx, workspace, lineageID, companyops.ArtifactEventInput{
				Type:               companyops.ArtifactEventPromotionRequested,
				CandidateID:        candidate.ID,
				CandidateRevision:  candidate.Revision,
				CandidateDigest:    candidate.Digest,
				CandidateObjectRef: candidate.DurableObjectRef,
				IdempotencyKey:     "formal-promotion:" + promotionID + ":requested:after:" + strconv.Itoa(last.Sequence),
			})
			if err != nil {
				t.Fatalf("append legacy requested: %v", err)
			}
			_, err = artifactService.repo.AppendArtifactEvent(ctx, workspace, lineageID, companyops.ArtifactEventInput{
				Type:               companyops.ArtifactEventPromotionSucceeded,
				CandidateID:        candidate.ID,
				CandidateRevision:  candidate.Revision,
				CandidateDigest:    candidate.Digest,
				CandidateObjectRef: candidate.DurableObjectRef,
				FormalArtifactRef:  promotionTestFormalArtifactRef,
				IdempotencyKey:     "formal-promotion:" + promotionID + ":succeeded",
			})
			if err != nil {
				t.Fatalf("append legacy succeeded after requested %d: %v", requested.Sequence, err)
			}
			if test.confirmed {
				_, err = artifactService.repo.AppendArtifactEvent(ctx, workspace, lineageID, companyops.ArtifactEventInput{
					Type:               companyops.ArtifactEventAuthorityReadbackConfirmed,
					CandidateID:        candidate.ID,
					CandidateRevision:  candidate.Revision,
					CandidateDigest:    candidate.Digest,
					CandidateObjectRef: candidate.DurableObjectRef,
					FormalArtifactRef:  promotionTestFormalArtifactRef,
					IdempotencyKey:     "formal-promotion:" + promotionID + ":readback",
				})
				if err != nil {
					t.Fatalf("append legacy readback: %v", err)
				}
			}

			eventsBefore, err := artifactService.repo.ListArtifactEvents(ctx, workspace, lineageID)
			if err != nil {
				t.Fatalf("ListArtifactEvents before replay: %v", err)
			}
			promotion := companyOpsArtifactPromotionRequest(fixture, candidate.ID, promotionID)
			if _, err := artifactService.PromoteArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, promotion); !errors.Is(err, companyops.ErrArtifactPromotionConflict) {
				t.Fatalf("legacy terminal replay error = %v, want ErrArtifactPromotionConflict", err)
			}
			if fake.promoteCount != 0 || fake.readCount != 0 {
				t.Fatalf("legacy terminal replay touched authority: promote %d read %d", fake.promoteCount, fake.readCount)
			}
			eventsAfter, err := artifactService.repo.ListArtifactEvents(ctx, workspace, lineageID)
			if err != nil {
				t.Fatalf("ListArtifactEvents after replay: %v", err)
			}
			if len(eventsAfter) != len(eventsBefore) {
				t.Fatalf("legacy terminal replay appended events: before %d after %d", len(eventsBefore), len(eventsAfter))
			}
		})
	}
}

// WO-40A (HIVECREW-WECHAT-REAL-OPERATIONS-V1): an Owner changes_requested
// decision on the WeChat production slice must durably block promotion of the
// rejected candidate, queue exactly one rework Run on the same issue, and
// replay idempotently.
func TestCompanyOpsArtifactOutcome_ChangesRequestedBlocksPromotionAndQueuesRework(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	fake := &fakeFormalArtifactAuthority{formalArtifactRef: promotionTestFormalArtifactRef}
	artifactService := newCompanyOpsArtifactServiceWithFakeAuthority(t, fixture, fake)

	task := claimAndFinalizeCompanyOpsExecutionTestTask(t, ctx, fixture)
	if _, err := fixture.service.StartTask(ctx, task.ID); err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	completed, err := fixture.service.CompleteTask(ctx, task.ID, []byte(`{"output":"draft v1"}`), "", "", false, "")
	if err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}
	outcome, err := artifactService.MaterializeCompletedTask(
		ctx,
		util.UUIDToString(fixture.company.workspaceID),
		*completed,
		"Draft v1 of the node artifact.",
		"",
	)
	if err != nil {
		t.Fatalf("MaterializeCompletedTask: %v", err)
	}

	// Feedback is mandatory for a rejection.
	if _, err := artifactService.ReviewArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, CompanyOpsArtifactReview{
		CandidateID:   outcome.Candidate.ID,
		Decision:      companyops.ArtifactEventChangesRequested,
		IdempotencyID: uuid.NewString(),
		Feedback:      "   ",
		ActorUserID:   fixture.company.userID,
	}); !errors.Is(err, ErrCompanyOpsArtifactConflict) {
		t.Fatalf("changes_requested without feedback error = %v, want ErrCompanyOpsArtifactConflict", err)
	}

	reviewID := uuid.NewString()
	review := CompanyOpsArtifactReview{
		CandidateID:   outcome.Candidate.ID,
		Decision:      companyops.ArtifactEventChangesRequested,
		IdempotencyID: reviewID,
		Feedback:      "Rework: tighten the summary and cite the exact source rows.",
		ActorUserID:   fixture.company.userID,
	}
	receipt, err := artifactService.ReviewArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, review)
	if err != nil {
		t.Fatalf("ReviewArtifact(changes_requested): %v", err)
	}
	if receipt.Event.Type != companyops.ArtifactEventChangesRequested || receipt.Event.CandidateID != outcome.Candidate.ID {
		t.Fatalf("rejection event = %+v", receipt.Event)
	}
	if receipt.ReworkTask == nil {
		t.Fatal("changes_requested must queue a rework Run on the same issue")
	}
	if receipt.ReworkTask.IssueID != fixture.company.issueID ||
		receipt.ReworkTask.TriggerEvidenceKind != (pgtype.Text{String: artifactRevisionEvidenceKind, Valid: true}) ||
		util.UUIDToString(receipt.ReworkTask.TriggerEvidenceRefID) != receipt.Event.ID {
		t.Fatalf("rework Run is not pinned to the rejection event on the same issue: %+v", receipt.ReworkTask)
	}

	// Idempotent replay of the same review collapses to the same event and the
	// same rework Run; no second Run is queued.
	replay, err := artifactService.ReviewArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, review)
	if err != nil {
		t.Fatalf("replay ReviewArtifact(changes_requested): %v", err)
	}
	if replay.Event.ID != receipt.Event.ID {
		t.Fatalf("replay returned a different event: first %q replay %q", receipt.Event.ID, replay.Event.ID)
	}
	if replay.ReworkTask == nil || replay.ReworkTask.ID != receipt.ReworkTask.ID {
		t.Fatalf("replay queued a duplicate rework Run: first %+v replay %+v", receipt.ReworkTask, replay.ReworkTask)
	}

	// The rejected candidate is not promotable, and the blocked attempt must
	// not touch the formal authority.
	promotion := companyOpsArtifactPromotionRequest(fixture, outcome.Candidate.ID, uuid.NewString())
	if _, err := artifactService.PromoteArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, promotion); !errors.Is(err, ErrCompanyOpsArtifactConflict) {
		t.Fatalf("PromoteArtifact after changes_requested error = %v, want ErrCompanyOpsArtifactConflict", err)
	}
	if fake.promoteCount != 0 || fake.readCount != 0 {
		t.Fatalf("blocked promotion touched authority: promote %d read %d", fake.promoteCount, fake.readCount)
	}

	// The same revision can never be approved after rejection; only a new
	// materialized revision may continue.
	if _, err := artifactService.ReviewArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, CompanyOpsArtifactReview{
		CandidateID:   outcome.Candidate.ID,
		Decision:      companyops.ArtifactEventApproved,
		IdempotencyID: uuid.NewString(),
		ActorUserID:   fixture.company.userID,
	}); !errors.Is(err, companyops.ErrInvalidArtifactTransition) {
		t.Fatalf("approve after changes_requested error = %v, want ErrInvalidArtifactTransition", err)
	}

	after, err := artifactService.GetIssueOutcome(ctx, fixture.company.workspaceID, fixture.company.issueID)
	if err != nil {
		t.Fatalf("GetIssueOutcome after rejection: %v", err)
	}
	if after == nil || after.Projection == nil ||
		after.Projection.Status != companyops.ArtifactEventChangesRequested || after.Projection.FormalVisible {
		t.Fatalf("projection after rejection = %+v, want changes_requested and not formally visible", after)
	}
}

// WO-40A (HIVECREW-WECHAT-REAL-OPERATIONS-V1): each WeChat slice node owns an
// independent issue → candidate → lifecycle lineage. A candidate materialized
// on one node's issue can never be reviewed or promoted through another node's
// issue, and a promotion carrying another lineage's authority snapshots fails
// closed against the issue's latest dispatch receipt.
func TestCompanyOpsArtifactOutcome_SliceNodeLineageIsolation(t *testing.T) {
	ctx, fixture := newCompanyOpsExecutionTestFixture(t)
	fake := &fakeFormalArtifactAuthority{formalArtifactRef: promotionTestFormalArtifactRef}
	artifactService := newCompanyOpsArtifactServiceWithFakeAuthority(t, fixture, fake)

	// Node A (the fixture issue): materialize and approve its candidate.
	outcomeA := materializeAndApproveCompanyOpsArtifact(t, ctx, fixture, artifactService)

	// Node B: a second issue in the same workspace with its own dispatch that
	// carries a distinct WorkOrder authority revision/digest.
	issueB := insertProductionCompanyOpsIssue(t, ctx, fixture.pool, fixture.company, "todo", "WeChat slice node B")
	backend, err := NewProductionCompanyOpsAssignmentBackend(fixture.queries, fixture.pool, fixture.service)
	if err != nil {
		t.Fatalf("NewProductionCompanyOpsAssignmentBackend: %v", err)
	}
	requestB := productionCompanyOpsRequest(fixture.company, issueB, util.MustParseUUID(uuid.NewString()))
	requestB.WorkOrder = assignmentAuthority("WorkOrder", assignmentWorkOrderRef, "wo-rev-8", "b")
	receiptB, err := NewCompanyOpsAssignmentService(backend).Dispatch(ctx, requestB)
	if err != nil {
		t.Fatalf("Dispatch node B: %v", err)
	}
	taskB := claimAndFinalizeCompanyOpsExecutionTestTask(t, ctx, fixture)
	if taskB.IssueID != issueB {
		t.Fatalf("claimed Run issue = %v, want node B issue %v", taskB.IssueID, issueB)
	}
	if _, err := fixture.service.StartTask(ctx, taskB.ID); err != nil {
		t.Fatalf("StartTask node B: %v", err)
	}
	completedB, err := fixture.service.CompleteTask(ctx, taskB.ID, []byte(`{"output":"node B draft"}`), "", "", false, "")
	if err != nil {
		t.Fatalf("CompleteTask node B: %v", err)
	}
	outcomeB, err := artifactService.MaterializeCompletedTask(
		ctx,
		util.UUIDToString(fixture.company.workspaceID),
		*completedB,
		"Node B draft artifact.",
		"",
	)
	if err != nil {
		t.Fatalf("MaterializeCompletedTask node B: %v", err)
	}
	if outcomeB.Candidate.ID == outcomeA.Candidate.ID {
		t.Fatalf("node lineages share a candidate: %q", outcomeB.Candidate.ID)
	}

	// Cross-lineage review is fail-closed in both directions.
	for _, cross := range []struct {
		name        string
		issueID     pgtype.UUID
		candidateID string
	}{
		{"node A candidate reviewed through node B issue", issueB, outcomeA.Candidate.ID},
		{"node B candidate reviewed through node A issue", fixture.company.issueID, outcomeB.Candidate.ID},
	} {
		t.Run(cross.name, func(t *testing.T) {
			if _, err := artifactService.ReviewArtifact(ctx, fixture.company.workspaceID, cross.issueID, CompanyOpsArtifactReview{
				CandidateID:   cross.candidateID,
				Decision:      companyops.ArtifactEventApproved,
				IdempotencyID: uuid.NewString(),
				ActorUserID:   fixture.company.userID,
			}); !errors.Is(err, companyops.ErrArtifactCandidateNotFound) {
				t.Fatalf("cross-lineage review error = %v, want ErrArtifactCandidateNotFound", err)
			}
		})
	}

	// Cross-lineage promotion is fail-closed: node B's candidate cannot be
	// promoted through node A's issue.
	crossPromotion := companyOpsArtifactPromotionRequest(fixture, outcomeB.Candidate.ID, uuid.NewString())
	if _, err := artifactService.PromoteArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, crossPromotion); !errors.Is(err, companyops.ErrArtifactCandidateNotFound) {
		t.Fatalf("cross-lineage promotion error = %v, want ErrArtifactCandidateNotFound", err)
	}
	if fake.promoteCount != 0 || fake.readCount != 0 {
		t.Fatalf("cross-lineage attempts touched authority: promote %d read %d", fake.promoteCount, fake.readCount)
	}

	// Node B approved on its own issue, but promoted with node A's authority
	// snapshots: fails closed against node B's latest dispatch receipt.
	if _, err := artifactService.ReviewArtifact(ctx, fixture.company.workspaceID, issueB, CompanyOpsArtifactReview{
		CandidateID:   outcomeB.Candidate.ID,
		Decision:      companyops.ArtifactEventApproved,
		IdempotencyID: uuid.NewString(),
		ActorUserID:   fixture.company.userID,
	}); err != nil {
		t.Fatalf("ReviewArtifact(approved) node B: %v", err)
	}
	foreignAuthority := companyOpsArtifactPromotionRequest(fixture, outcomeB.Candidate.ID, uuid.NewString())
	if _, err := artifactService.PromoteArtifact(ctx, fixture.company.workspaceID, issueB, foreignAuthority); !errors.Is(err, ErrCompanyOpsArtifactConflict) {
		t.Fatalf("promotion with foreign authority snapshots error = %v, want ErrCompanyOpsArtifactConflict", err)
	}
	if fake.promoteCount != 0 {
		t.Fatalf("foreign-authority promotion POSTed to the authority: promote %d", fake.promoteCount)
	}

	// Control: both lineages remain independently operable through their own
	// issue, candidate, and dispatch authority.
	promoteB := companyOpsArtifactPromotionRequestForTarget(receiptB.Target, fixture.company.userID, fixture.company.agentID, outcomeB.Candidate.ID, uuid.NewString())
	receiptPromoteB, err := artifactService.PromoteArtifact(ctx, fixture.company.workspaceID, issueB, promoteB)
	if err != nil {
		t.Fatalf("PromoteArtifact node B: %v", err)
	}
	if receiptPromoteB.LifecycleStatus != companyops.ArtifactEventAuthorityReadbackConfirmed || !receiptPromoteB.FormalVisible {
		t.Fatalf("node B promotion receipt = %+v, want authority readback confirmed", receiptPromoteB)
	}
	promoteA := companyOpsArtifactPromotionRequest(fixture, outcomeA.Candidate.ID, uuid.NewString())
	receiptPromoteA, err := artifactService.PromoteArtifact(ctx, fixture.company.workspaceID, fixture.company.issueID, promoteA)
	if err != nil {
		t.Fatalf("PromoteArtifact node A: %v", err)
	}
	if receiptPromoteA.LifecycleStatus != companyops.ArtifactEventAuthorityReadbackConfirmed || !receiptPromoteA.FormalVisible {
		t.Fatalf("node A promotion receipt = %+v, want authority readback confirmed", receiptPromoteA)
	}
	if fake.promoteCount != 2 {
		t.Fatalf("authority POST count = %d, want exactly 2 (one per lineage)", fake.promoteCount)
	}
}

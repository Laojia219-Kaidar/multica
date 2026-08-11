package service

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/companyops"
	"github.com/multica-ai/multica/server/internal/storage"
	"github.com/multica-ai/multica/server/internal/util"
)

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
	artifactService, err := NewCompanyOpsArtifactService(fixture.queries, fixture.pool, store, fixture.service)
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

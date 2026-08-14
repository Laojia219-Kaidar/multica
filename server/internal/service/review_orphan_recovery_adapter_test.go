package service

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/continuousdispatch"
)

const (
	recoveryWorkspace = "00000000-0000-0000-0000-000000000001"
	recoveryProject   = "00000000-0000-0000-0000-000000000002"
	recoveryIssue     = "00000000-0000-0000-0000-000000000003"
	recoveryActor     = "00000000-0000-0000-0000-000000000004"
	recoveryRepair    = "00000000-0000-0000-0000-000000000005"
	recoveryReviewer  = "00000000-0000-0000-0000-000000000006"
)

type recoveryReaderFake struct {
	snapshot ReviewOrphanRecoverySnapshot
	open     continuousdispatch.ReviewOpenTaskEvidence
	readOpen int
}

func (f *recoveryReaderFake) ReadReviewOrphan(context.Context, ReviewOrphanRecoveryKey) (ReviewOrphanRecoverySnapshot, error) {
	return f.snapshot, nil
}
func (f *recoveryReaderFake) ReadOpenReview(context.Context, ReviewOrphanRecoveryKey, string) (continuousdispatch.ReviewOpenTaskEvidence, error) {
	f.readOpen++
	return f.open, nil
}

type recoveryDispatcherFake struct {
	calls int
	err   error
}

func (f *recoveryDispatcherFake) DispatchReviewIssueWithRecoveryPrecondition(context.Context, pgtype.UUID, pgtype.UUID, pgtype.UUID, pgtype.UUID, ReviewOrphanRecoveryPrecondition) (ContinuousDispatchTriggerResult, error) {
	f.calls++
	if f.err != nil {
		return ContinuousDispatchTriggerResult{}, f.err
	}
	return ContinuousDispatchTriggerResult{Receipt: ContinuousDispatchReceipt{}}, nil
}

func validRecoverySnapshot() ReviewOrphanRecoverySnapshot {
	identity := continuousdispatch.ReviewOrphanIdentity{WorkspaceID: recoveryWorkspace, IssueID: recoveryIssue, CandidateRevision: "sha256:candidate-1", Generation: "generation-1"}
	return ReviewOrphanRecoverySnapshot{
		IssueWorkspaceID: recoveryWorkspace, IssueProjectID: recoveryProject, IssueID: recoveryIssue, IssueStatus: "in_review", IssueStage: "review", ReviewState: continuousdispatch.ReviewStateReviseRequested,
		Identity:       identity,
		RepairTask:     continuousdispatch.ReviewOrphanTask{ID: recoveryRepair, Kind: continuousdispatch.TaskKindRepair, Status: continuousdispatch.TaskStatusCompleted, WorkspaceID: recoveryWorkspace, IssueID: recoveryIssue, CandidateRevision: identity.CandidateRevision, Generation: identity.Generation, AgentID: "00000000-0000-0000-0000-000000000007"},
		RepairEvidence: ReviewOrphanRepairEvidence{Kind: continuousdispatch.TaskKindRepair, ContextRef: "issue-context:1", EvidenceRef: "receipt:1"},
		RepairComment:  ReviewOrphanRepairComment{SourceTaskID: recoveryRepair, AuthorID: "00000000-0000-0000-0000-000000000007", WorkspaceID: recoveryWorkspace, IssueID: recoveryIssue},
		OpenReview:     continuousdispatch.ReviewOpenTaskEvidence{Known: true, Found: false}, CapacityKnown: true, CapacityReconciled: true, ActiveReviewWIP: 1, MaxReviewWIP: 3, ReviewerID: recoveryReviewer, SourceRef: continuousDispatchReviewCommentRef(parseDispatchUUID("00000000-0000-0000-0000-000000000009")),
	}
}

func openRecoveryEvidence() continuousdispatch.ReviewOpenTaskEvidence {
	s := validRecoverySnapshot()
	return continuousdispatch.ReviewOpenTaskEvidence{Known: true, Found: true, ReviewerID: recoveryReviewer, Task: continuousdispatch.ReviewOrphanTask{ID: "00000000-0000-0000-0000-000000000008", Kind: continuousdispatch.TaskKindReview, Status: continuousdispatch.TaskStatusQueued, WorkspaceID: recoveryWorkspace, IssueID: recoveryIssue, CandidateRevision: s.Identity.CandidateRevision, Generation: s.Identity.Generation, AgentID: recoveryReviewer, TargetTaskID: recoveryRepair}}
}

func TestReviewOrphanRecoveryAdapterDispatchesOnlyAfterStrictReadModel(t *testing.T) {
	reader := &recoveryReaderFake{snapshot: validRecoverySnapshot(), open: openRecoveryEvidence()}
	dispatcher := &recoveryDispatcherFake{}
	result, err := NewReviewOrphanRecoveryAdapter(reader, dispatcher).Recover(context.Background(), ReviewOrphanRecoveryKey{WorkspaceID: recoveryWorkspace, ProjectID: recoveryProject, IssueID: recoveryIssue, ActorUserID: recoveryActor})
	if err != nil || !result.Decision.Processed || dispatcher.calls != 1 || reader.readOpen != 1 {
		t.Fatalf("result=%+v err=%v calls=%d readback=%d", result, err, dispatcher.calls, reader.readOpen)
	}
}

func TestReviewOrphanRecoveryAdapterFailsClosedBeforeDispatch(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*ReviewOrphanRecoverySnapshot)
	}{
		{"workspace drift", func(s *ReviewOrphanRecoverySnapshot) { s.IssueWorkspaceID = "00000000-0000-0000-0000-000000000099" }},
		{"project drift", func(s *ReviewOrphanRecoverySnapshot) { s.IssueProjectID = "00000000-0000-0000-0000-000000000099" }},
		{"issue drift", func(s *ReviewOrphanRecoverySnapshot) { s.IssueID = "00000000-0000-0000-0000-000000000099" }},
		{"missing repair kind", func(s *ReviewOrphanRecoverySnapshot) { s.RepairEvidence.Kind = "" }},
		{"comment identity mismatch", func(s *ReviewOrphanRecoverySnapshot) { s.RepairComment.AuthorID = recoveryReviewer }},
		{"revision drift", func(s *ReviewOrphanRecoverySnapshot) { s.RepairTask.CandidateRevision = "sha256:other" }},
		{"stage drift", func(s *ReviewOrphanRecoverySnapshot) { s.IssueStage = "implementation" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := validRecoverySnapshot()
			tc.mutate(&s)
			d := &recoveryDispatcherFake{}
			_, err := NewReviewOrphanRecoveryAdapter(&recoveryReaderFake{snapshot: s}, d).Recover(context.Background(), ReviewOrphanRecoveryKey{WorkspaceID: recoveryWorkspace, ProjectID: recoveryProject, IssueID: recoveryIssue, ActorUserID: recoveryActor})
			if !errors.Is(err, ErrReviewOrphanSourceGap) || d.calls != 0 {
				t.Fatalf("err=%v calls=%d", err, d.calls)
			}
		})
	}
}

func TestReviewOrphanRecoveryAdapterDefersUnknownOrFullWIPWithoutWrite(t *testing.T) {
	for _, tc := range []struct {
		name   string
		known  bool
		active int
	}{
		{"unknown", false, 0}, {"full", true, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := validRecoverySnapshot()
			s.CapacityKnown, s.ActiveReviewWIP = tc.known, tc.active
			d := &recoveryDispatcherFake{}
			result, err := NewReviewOrphanRecoveryAdapter(&recoveryReaderFake{snapshot: s}, d).Recover(context.Background(), ReviewOrphanRecoveryKey{WorkspaceID: recoveryWorkspace, ProjectID: recoveryProject, IssueID: recoveryIssue, ActorUserID: recoveryActor})
			if err != nil || !result.Decision.Retryable || d.calls != 0 {
				t.Fatalf("result=%+v err=%v calls=%d", result, err, d.calls)
			}
		})
	}
}

func TestReviewOrphanRecoveryAdapterRequiresExactReadback(t *testing.T) {
	reader := &recoveryReaderFake{snapshot: validRecoverySnapshot(), open: continuousdispatch.ReviewOpenTaskEvidence{Known: true, Found: false}}
	d := &recoveryDispatcherFake{}
	_, err := NewReviewOrphanRecoveryAdapter(reader, d).Recover(context.Background(), ReviewOrphanRecoveryKey{WorkspaceID: recoveryWorkspace, ProjectID: recoveryProject, IssueID: recoveryIssue, ActorUserID: recoveryActor})
	if !errors.Is(err, ErrReviewOrphanReadbackFailed) || d.calls != 1 {
		t.Fatalf("err=%v calls=%d", err, d.calls)
	}
}

func TestReviewOrphanRecoveryAdapterReturnsDispatchConflict(t *testing.T) {
	d := &recoveryDispatcherFake{err: errors.New("conflict")}
	_, err := NewReviewOrphanRecoveryAdapter(&recoveryReaderFake{snapshot: validRecoverySnapshot()}, d).Recover(context.Background(), ReviewOrphanRecoveryKey{WorkspaceID: recoveryWorkspace, ProjectID: recoveryProject, IssueID: recoveryIssue, ActorUserID: recoveryActor})
	if !errors.Is(err, ErrReviewOrphanDispatchFailed) || d.calls != 1 {
		t.Fatalf("err=%v calls=%d", err, d.calls)
	}
}

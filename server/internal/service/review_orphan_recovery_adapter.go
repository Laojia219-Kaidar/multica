package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/continuousdispatch"
)

// ReviewOrphanRecoveryReader is the read-only boundary for the canonical
// Issue/Task/Comment projections. It must not be implemented with local agent
// state or a static registry. The adapter intentionally has no write method.
type ReviewOrphanRecoveryReader interface {
	ReadReviewOrphan(context.Context, ReviewOrphanRecoveryKey) (ReviewOrphanRecoverySnapshot, error)
	ReadOpenReview(context.Context, ReviewOrphanRecoveryKey, string) (continuousdispatch.ReviewOpenTaskEvidence, error)
}

// ReviewOrphanRecoveryDispatcher is the existing server trigger seam. A
// production implementation is *ContinuousDispatchTriggerService; tests can
// inject a fake that records delegation without writing a database.
type ReviewOrphanRecoveryDispatcher interface {
	DispatchReviewIssue(context.Context, pgtype.UUID, pgtype.UUID, pgtype.UUID, pgtype.UUID, string, pgtype.UUID) (ContinuousDispatchTriggerResult, error)
}

// ReviewOrphanRecoveryKey is the only caller input. Reviewer, route, runtime,
// model, account, generation, and source task are all read from canonical
// evidence and are never caller-selected.
type ReviewOrphanRecoveryKey struct {
	WorkspaceID string
	ProjectID   string
	IssueID     string
	ActorUserID string
}

// ReviewOrphanRepairEvidence proves that the completed repair was explicitly
// classified and linked to a context/evidence record. Empty fields are a
// source gap, not permission to infer from task titles.
type ReviewOrphanRepairEvidence struct {
	Kind        string
	ContextRef  string
	EvidenceRef string
}

// ReviewOrphanRepairComment is the canonical agent comment carrying
// source_task_id. Its author and scope must match the completed repair Task.
type ReviewOrphanRepairComment struct {
	SourceTaskID string
	AuthorID     string
	WorkspaceID  string
	IssueID      string
}

// ReviewOrphanRecoverySnapshot is a read model assembled by the canonical
// store. Issue fields remain separate from task identity so drift cannot be
// hidden by copying one field into another.
type ReviewOrphanRecoverySnapshot struct {
	IssueWorkspaceID   string
	IssueStatus        string
	IssueStage         string
	ReviewState        string
	Identity           continuousdispatch.ReviewOrphanIdentity
	RepairTask         continuousdispatch.ReviewOrphanTask
	RepairEvidence     ReviewOrphanRepairEvidence
	RepairComment      ReviewOrphanRepairComment
	OpenReview         continuousdispatch.ReviewOpenTaskEvidence
	CapacityKnown      bool
	CapacityReconciled bool
	ActiveReviewWIP    int
	MaxReviewWIP       int
	ReviewerID         string
	SourceRef          string
}

type ReviewOrphanRecoveryResult struct {
	Decision continuousdispatch.ReviewOrphanRecoveryDecision
	Receipt  *ContinuousDispatchReceipt
}

var (
	ErrReviewOrphanSourceGap      = errors.New("review orphan recovery source gap")
	ErrReviewOrphanDispatchFailed = errors.New("review orphan recovery dispatch failed")
	ErrReviewOrphanReadbackFailed = errors.New("review orphan recovery readback failed")
)

// ReviewOrphanRecoveryAdapter performs one bounded recovery attempt. It is a
// candidate service only: no scheduler, handler, migration, or production
// registration is performed here.
type ReviewOrphanRecoveryAdapter struct {
	reader     ReviewOrphanRecoveryReader
	dispatcher ReviewOrphanRecoveryDispatcher
}

func NewReviewOrphanRecoveryAdapter(reader ReviewOrphanRecoveryReader, dispatcher ReviewOrphanRecoveryDispatcher) *ReviewOrphanRecoveryAdapter {
	return &ReviewOrphanRecoveryAdapter{reader: reader, dispatcher: dispatcher}
}

func (a *ReviewOrphanRecoveryAdapter) Recover(ctx context.Context, key ReviewOrphanRecoveryKey) (ReviewOrphanRecoveryResult, error) {
	if a == nil || a.reader == nil || a.dispatcher == nil {
		return ReviewOrphanRecoveryResult{}, fmt.Errorf("%w: reader and dispatcher are required", ErrReviewOrphanSourceGap)
	}
	workspaceID, projectID, issueID, actorUserID, err := parseRecoveryKey(key)
	if err != nil {
		return ReviewOrphanRecoveryResult{}, err
	}
	snapshot, err := a.reader.ReadReviewOrphan(ctx, key)
	if err != nil {
		return ReviewOrphanRecoveryResult{}, fmt.Errorf("read review orphan: %w", err)
	}
	if err := validateRecoverySnapshot(snapshot); err != nil {
		return ReviewOrphanRecoveryResult{}, err
	}
	decision := continuousdispatch.EvaluateReviewOrphan(continuousdispatch.ReviewOrphanRecoveryInput{
		ReviewState: snapshot.ReviewState, RepairTask: snapshot.RepairTask, OpenReview: snapshot.OpenReview,
		Identity: snapshot.Identity, CapacityKnown: snapshot.CapacityKnown, CapacityReconciled: snapshot.CapacityReconciled,
		ActiveReviewWIP: snapshot.ActiveReviewWIP, MaxReviewWIP: snapshot.MaxReviewWIP, ReviewerID: snapshot.ReviewerID,
	})
	if decision.State != continuousdispatch.ReviewOrphanReady {
		return ReviewOrphanRecoveryResult{Decision: decision}, nil
	}
	if snapshot.SourceRef == "" {
		return ReviewOrphanRecoveryResult{}, fmt.Errorf("%w: source_ref is required", ErrReviewOrphanSourceGap)
	}
	repairTaskID := parseDispatchUUID(snapshot.RepairTask.ID)
	if !repairTaskID.Valid {
		return ReviewOrphanRecoveryResult{}, fmt.Errorf("%w: repair task id is not a UUID", ErrReviewOrphanSourceGap)
	}
	dispatch, err := a.dispatcher.DispatchReviewIssue(ctx, workspaceID, projectID, issueID, actorUserID, snapshot.SourceRef, repairTaskID)
	if err != nil {
		return ReviewOrphanRecoveryResult{}, fmt.Errorf("%w: %v", ErrReviewOrphanDispatchFailed, err)
	}
	open, err := a.reader.ReadOpenReview(ctx, key, snapshot.RepairTask.ID)
	if err != nil {
		return ReviewOrphanRecoveryResult{}, fmt.Errorf("%w: %v", ErrReviewOrphanReadbackFailed, err)
	}
	snapshot.OpenReview = open
	post := continuousdispatch.EvaluateReviewOrphan(continuousdispatch.ReviewOrphanRecoveryInput{
		ReviewState: snapshot.ReviewState, RepairTask: snapshot.RepairTask, OpenReview: open,
		Identity: snapshot.Identity, CapacityKnown: snapshot.CapacityKnown, CapacityReconciled: snapshot.CapacityReconciled,
		ActiveReviewWIP: snapshot.ActiveReviewWIP, MaxReviewWIP: snapshot.MaxReviewWIP, ReviewerID: snapshot.ReviewerID,
	})
	if !post.Processed || post.State != continuousdispatch.ReviewOrphanAlreadyConfirmed {
		return ReviewOrphanRecoveryResult{Decision: post}, fmt.Errorf("%w: exact open review readback not confirmed", ErrReviewOrphanReadbackFailed)
	}
	return ReviewOrphanRecoveryResult{Decision: post, Receipt: &dispatch.Receipt}, nil
}

func validateRecoverySnapshot(s ReviewOrphanRecoverySnapshot) error {
	if !canonicalEqual(s.IssueWorkspaceID, s.Identity.WorkspaceID) || s.IssueStatus != "in_review" || s.IssueStage != "review" || s.ReviewState != continuousdispatch.ReviewStateReviseRequested {
		return fmt.Errorf("%w: issue status/stage/review state/workspace drift", ErrReviewOrphanSourceGap)
	}
	if !s.Identity.Complete() {
		return fmt.Errorf("%w: candidate identity incomplete", ErrReviewOrphanSourceGap)
	}
	if s.RepairTask.Kind != continuousdispatch.TaskKindRepair || !canonicalNonEmpty(s.RepairTask.ID, s.RepairTask.WorkspaceID, s.RepairTask.IssueID, s.RepairTask.CandidateRevision, s.RepairTask.Generation, s.RepairTask.AgentID) ||
		s.RepairTask.WorkspaceID != s.Identity.WorkspaceID || s.RepairTask.IssueID != s.Identity.IssueID ||
		s.RepairTask.CandidateRevision != s.Identity.CandidateRevision || s.RepairTask.Generation != s.Identity.Generation {
		return fmt.Errorf("%w: repair task kind or candidate identity drift", ErrReviewOrphanSourceGap)
	}
	e := s.RepairEvidence
	if !canonicalEqual(e.Kind, continuousdispatch.TaskKindRepair) || !canonicalNonEmpty(e.ContextRef, e.EvidenceRef) {
		return fmt.Errorf("%w: repair kind/context/evidence is not explicit", ErrReviewOrphanSourceGap)
	}
	c := s.RepairComment
	if !canonicalEqual(c.SourceTaskID, s.RepairTask.ID) || !canonicalEqual(c.AuthorID, s.RepairTask.AgentID) || !canonicalEqual(c.WorkspaceID, s.Identity.WorkspaceID) || !canonicalEqual(c.IssueID, s.Identity.IssueID) {
		return fmt.Errorf("%w: repair comment source_task_id/author/scope mismatch", ErrReviewOrphanSourceGap)
	}
	return nil
}

func parseRecoveryKey(k ReviewOrphanRecoveryKey) (pgtype.UUID, pgtype.UUID, pgtype.UUID, pgtype.UUID, error) {
	ids := []string{k.WorkspaceID, k.ProjectID, k.IssueID, k.ActorUserID}
	for _, id := range ids {
		if !canonicalNonEmpty(id) || !parseDispatchUUID(id).Valid {
			return pgtype.UUID{}, pgtype.UUID{}, pgtype.UUID{}, pgtype.UUID{}, fmt.Errorf("%w: key contains invalid UUID", ErrReviewOrphanSourceGap)
		}
	}
	return parseDispatchUUID(k.WorkspaceID), parseDispatchUUID(k.ProjectID), parseDispatchUUID(k.IssueID), parseDispatchUUID(k.ActorUserID), nil
}

func canonicalNonEmpty(values ...string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return false
		}
	}
	return true
}

func canonicalEqual(a, b string) bool { return canonicalNonEmpty(a, b) && a == b }

var _ ReviewOrphanRecoveryDispatcher = (*ContinuousDispatchTriggerService)(nil)

package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/continuousdispatch"
)

var (
	ErrContinuousDispatchIssueAbsent = errors.New("continuous dispatch issue not found in project")
	ErrContinuousDispatchNotReady    = errors.New("continuous dispatch issue has no executable next action")
)

const continuousDispatchTriggerPageSize = 200

type ContinuousDispatchProjectInspector interface {
	InspectProject(context.Context, pgtype.UUID, pgtype.UUID, int, int) (*ContinuousDispatchShadowResult, error)
}

type ContinuousDispatchExactDispatcher interface {
	Dispatch(context.Context, ContinuousDispatchRequest) (ContinuousDispatchReceipt, error)
}

// AuthorityReviewDispatchIdentityProvider resolves the server-side Authority
// identity for an Authority-gated review dispatch. Implementations must read
// the canonical Authority mapping; they must not derive selectors from local
// Issue/Task/name data or choose a fallback employee/Agent.
//
// The candidate is already built by this Trigger from the current Shadow
// decision. Passing it into the resolver lets the resolver bind the Authority
// read to the exact candidate without allowing an external caller to construct
// or replace the dispatch request.
type AuthorityReviewDispatchIdentityProvider interface {
	ResolveReviewDispatchIdentity(context.Context, pgtype.UUID, pgtype.UUID, pgtype.UUID, AuthorityReviewDispatchCandidate) (AuthorityReviewDispatchIdentity, error)
}

// ContinuousDispatchRecoveryPreconditionVerifier must re-read canonical
// Issue/Task/Comment evidence immediately before a recovery dispatch. It is
// deliberately separate from the Shadow projection because review_state and
// repair-comment lineage are not safe to infer from a route decision.
type ContinuousDispatchRecoveryPreconditionVerifier interface {
	VerifyReviewOrphanRecoveryPrecondition(context.Context, ReviewOrphanRecoveryPrecondition) error
}

// ContinuousDispatchTriggerResult carries the exact server-recomputed action
// and committed receipt. It is a response projection, not a new Task state.
type ContinuousDispatchTriggerResult struct {
	Action  continuousdispatch.NextAction
	Receipt ContinuousDispatchReceipt
}

// ContinuousDispatchTriggerService prevents callers from choosing an
// employee, Agent, Runtime, model, account, or generation. It recomputes the
// current shadow decision and only dispatches a ready/fallback server result.
type ContinuousDispatchTriggerService struct {
	inspector             ContinuousDispatchProjectInspector
	dispatcher            ContinuousDispatchExactDispatcher
	recoveryPreconditions ContinuousDispatchRecoveryPreconditionVerifier
	authorityReviewGate   *AuthorityReviewDispatchGate
	authorityReviewIDs    AuthorityReviewDispatchIdentityProvider
	authorityReviewMode   bool
}

func NewContinuousDispatchTriggerService(
	inspector ContinuousDispatchProjectInspector,
	dispatcher ContinuousDispatchExactDispatcher,
) *ContinuousDispatchTriggerService {
	return &ContinuousDispatchTriggerService{inspector: inspector, dispatcher: dispatcher}
}

// WithReviewOrphanRecoveryPreconditionVerifier is intentionally opt-in. The
// ordinary bounded review drain remains unchanged; the orphan-recovery adapter
// fails closed unless this canonical re-read verifier is explicitly wired.
func (s *ContinuousDispatchTriggerService) WithReviewOrphanRecoveryPreconditionVerifier(verifier ContinuousDispatchRecoveryPreconditionVerifier) *ContinuousDispatchTriggerService {
	if s == nil {
		return nil
	}
	cp := *s
	cp.recoveryPreconditions = verifier
	return &cp
}

// WithAuthorityReviewDispatchGate enables the explicit candidate-only
// Authority-gated review mode. The mode is deliberately opt-in so existing
// non-Authority candidate tests and paths retain their behavior. Once enabled,
// a missing gate or identity provider is a source gap and the exact dispatcher
// is never called.
func (s *ContinuousDispatchTriggerService) WithAuthorityReviewDispatchGate(
	gate *AuthorityReviewDispatchGate,
	provider AuthorityReviewDispatchIdentityProvider,
) *ContinuousDispatchTriggerService {
	if s == nil {
		return nil
	}
	cp := *s
	cp.authorityReviewGate = gate
	cp.authorityReviewIDs = provider
	cp.authorityReviewMode = true
	return &cp
}

func (s *ContinuousDispatchTriggerService) DispatchIssue(
	ctx context.Context,
	workspaceID, projectID, issueID, actorUserID pgtype.UUID,
	handoffNote string,
) (ContinuousDispatchTriggerResult, error) {
	if s == nil || s.inspector == nil || s.dispatcher == nil {
		return ContinuousDispatchTriggerResult{}, fmt.Errorf("continuous dispatch trigger dependencies are required")
	}
	for name, value := range map[string]pgtype.UUID{
		"workspace_id": workspaceID, "project_id": projectID, "issue_id": issueID, "actor_user_id": actorUserID,
	} {
		if !value.Valid || value.Bytes == ([16]byte{}) {
			return ContinuousDispatchTriggerResult{}, fmt.Errorf("%s is required", name)
		}
	}

	wantedIssueID := shadowUUIDString(issueID)
	for offset := 0; ; offset += continuousDispatchTriggerPageSize {
		page, err := s.inspector.InspectProject(ctx, workspaceID, projectID, continuousDispatchTriggerPageSize, offset)
		if err != nil {
			return ContinuousDispatchTriggerResult{}, err
		}
		if page == nil || page.SchemaVersion != ContinuousDispatchShadowSchemaV1 ||
			page.WorkspaceID != shadowUUIDString(workspaceID) || page.ProjectID != shadowUUIDString(projectID) {
			return ContinuousDispatchTriggerResult{}, ErrContinuousDispatchSourceGap
		}
		for _, item := range page.Items {
			if item.IssueID != wantedIssueID {
				continue
			}
			// The generic entry has no immutable review source provenance and no
			// Authority identity provider input. It must never turn an in-review
			// item into a bare dispatch: callers must use DispatchReviewIssue,
			// which re-reads source lineage and, when composed, crosses the
			// Authority gate before any Task/receipt write.
			if item.Status == "in_review" || item.DispatchIdentity.Stage == "review" {
				return ContinuousDispatchTriggerResult{}, ErrContinuousDispatchSourceGap
			}
			return s.dispatchShadowItem(ctx, workspaceID, projectID, item, actorUserID, handoffNote)
		}
		if len(page.Items) == 0 || offset+len(page.Items) >= page.Total {
			return ContinuousDispatchTriggerResult{}, ErrContinuousDispatchIssueAbsent
		}
	}
}

// DispatchReviewIssue is the review-only variant used by the bounded drain
// command. It re-reads the Shadow immediately before write and requires the
// exact, previously-previewed source implementation Task to remain proven.
// It never accepts a reviewer, runtime, model, account, or generation from a
// caller.
func (s *ContinuousDispatchTriggerService) DispatchReviewIssue(
	ctx context.Context,
	workspaceID, projectID, issueID, actorUserID pgtype.UUID,
	sourceRef string,
	sourceTaskID pgtype.UUID,
) (ContinuousDispatchTriggerResult, error) {
	return s.dispatchReviewIssue(ctx, workspaceID, projectID, issueID, actorUserID, sourceRef, sourceTaskID, nil)
}

// DispatchReviewIssueWithRecoveryPrecondition is the orphan-recovery-only
// path. It re-reads the standard Shadow, validates its immutable identity, and
// then invokes the canonical provenance verifier before any Task/receipt write
// is delegated. A missing verifier is a source gap, never a permissive mode.
func (s *ContinuousDispatchTriggerService) DispatchReviewIssueWithRecoveryPrecondition(
	ctx context.Context,
	workspaceID, projectID, issueID, actorUserID pgtype.UUID,
	precondition ReviewOrphanRecoveryPrecondition,
) (ContinuousDispatchTriggerResult, error) {
	if err := validateRecoveryPreconditionRequest(workspaceID, projectID, issueID, precondition); err != nil {
		return ContinuousDispatchTriggerResult{}, err
	}
	return s.dispatchReviewIssue(ctx, workspaceID, projectID, issueID, actorUserID, precondition.SourceRef, parseDispatchUUID(precondition.RepairTaskID), &precondition)
}

func (s *ContinuousDispatchTriggerService) dispatchReviewIssue(
	ctx context.Context,
	workspaceID, projectID, issueID, actorUserID pgtype.UUID,
	sourceRef string,
	sourceTaskID pgtype.UUID,
	recoveryPrecondition *ReviewOrphanRecoveryPrecondition,
) (ContinuousDispatchTriggerResult, error) {
	if s == nil || s.inspector == nil || s.dispatcher == nil {
		return ContinuousDispatchTriggerResult{}, fmt.Errorf("continuous dispatch trigger dependencies are required")
	}
	for name, value := range map[string]pgtype.UUID{
		"workspace_id": workspaceID, "project_id": projectID, "issue_id": issueID, "actor_user_id": actorUserID,
	} {
		if !value.Valid || value.Bytes == ([16]byte{}) {
			return ContinuousDispatchTriggerResult{}, fmt.Errorf("%s is required", name)
		}
	}
	if !sourceTaskID.Valid || sourceTaskID.Bytes == ([16]byte{}) {
		return ContinuousDispatchTriggerResult{}, ErrContinuousDispatchNotReady
	}
	if _, ok := parseContinuousDispatchReviewCommentRef(sourceRef); !ok {
		return ContinuousDispatchTriggerResult{}, ErrContinuousDispatchNotReady
	}
	wantedIssueID := shadowUUIDString(issueID)
	for offset := 0; ; offset += continuousDispatchTriggerPageSize {
		page, err := s.inspector.InspectProject(ctx, workspaceID, projectID, continuousDispatchTriggerPageSize, offset)
		if err != nil {
			return ContinuousDispatchTriggerResult{}, err
		}
		if page == nil || page.SchemaVersion != ContinuousDispatchShadowSchemaV1 ||
			page.WorkspaceID != shadowUUIDString(workspaceID) || page.ProjectID != shadowUUIDString(projectID) {
			return ContinuousDispatchTriggerResult{}, ErrContinuousDispatchSourceGap
		}
		for _, item := range page.Items {
			if item.IssueID != wantedIssueID {
				continue
			}
			if item.Status != "in_review" || item.SourceRef != sourceRef || item.SourceTaskID != shadowUUIDString(sourceTaskID) {
				return ContinuousDispatchTriggerResult{}, ErrContinuousDispatchIssueDrift
			}
			if recoveryPrecondition != nil {
				if err := s.verifyReviewOrphanRecoveryPrecondition(ctx, item, *recoveryPrecondition); err != nil {
					return ContinuousDispatchTriggerResult{}, err
				}
			}
			provenance := &ContinuousDispatchReviewProvenance{
				SourceRef:       item.SourceRef,
				SourceIssueID:   item.IssueID,
				SourceTaskID:    item.SourceTaskID,
				InitiatorSource: continuousDispatchReviewInitiatorSourceV1,
			}
			note := fmt.Sprintf("review_dispatch source_ref=%s source_issue_id=%s source_task_id=%s initiator_source=%s", item.SourceRef, item.IssueID, item.SourceTaskID, provenance.InitiatorSource)
			return s.dispatchReviewShadowItem(ctx, workspaceID, projectID, item, actorUserID, note, provenance)
		}
		if len(page.Items) == 0 || offset+len(page.Items) >= page.Total {
			return ContinuousDispatchTriggerResult{}, ErrContinuousDispatchIssueAbsent
		}
	}
}

func validateRecoveryPreconditionRequest(workspaceID, projectID, issueID pgtype.UUID, p ReviewOrphanRecoveryPrecondition) error {
	if !canonicalEqual(p.WorkspaceID, shadowUUIDString(workspaceID)) || !canonicalEqual(p.ProjectID, shadowUUIDString(projectID)) || !canonicalEqual(p.IssueID, shadowUUIDString(issueID)) ||
		p.IssueStatus != "in_review" || p.IssueStage != "review" || p.ReviewState != continuousdispatch.ReviewStateReviseRequested ||
		!canonicalNonEmpty(p.CandidateRevision, p.Generation, p.RepairTaskID, p.RepairTaskAgentID, p.SourceRef) ||
		!canonicalEqual(p.RepairEvidence.Kind, continuousdispatch.TaskKindRepair) || !canonicalNonEmpty(p.RepairEvidence.ContextRef, p.RepairEvidence.EvidenceRef) ||
		!canonicalEqual(p.RepairComment.SourceTaskID, p.RepairTaskID) || !canonicalEqual(p.RepairComment.AuthorID, p.RepairTaskAgentID) ||
		!canonicalEqual(p.RepairComment.WorkspaceID, p.WorkspaceID) || !canonicalEqual(p.RepairComment.IssueID, p.IssueID) ||
		!parseDispatchUUID(p.RepairTaskID).Valid {
		return ErrContinuousDispatchSourceGap
	}
	return nil
}

func (s *ContinuousDispatchTriggerService) verifyReviewOrphanRecoveryPrecondition(
	ctx context.Context,
	item ContinuousDispatchShadowItem,
	p ReviewOrphanRecoveryPrecondition,
) error {
	if s.recoveryPreconditions == nil {
		return ErrContinuousDispatchSourceGap
	}
	if item.Status != p.IssueStatus || item.DispatchIdentity.Stage != p.IssueStage ||
		item.DispatchIdentity.CandidateRevision != p.CandidateRevision || item.DispatchIdentity.Generation != p.Generation ||
		item.SourceRef != p.SourceRef || item.SourceTaskID != p.RepairTaskID {
		return ErrContinuousDispatchIssueDrift
	}
	if err := s.recoveryPreconditions.VerifyReviewOrphanRecoveryPrecondition(ctx, p); err != nil {
		return fmt.Errorf("%w: %v", ErrContinuousDispatchIssueDrift, err)
	}
	return nil
}

func (s *ContinuousDispatchTriggerService) dispatchShadowItem(
	ctx context.Context,
	workspaceID, projectID pgtype.UUID,
	item ContinuousDispatchShadowItem,
	actorUserID pgtype.UUID,
	handoffNote string,
) (ContinuousDispatchTriggerResult, error) {
	return s.dispatchShadowItemWithPrecondition(ctx, workspaceID, projectID, item, actorUserID, handoffNote, false, nil)
}

func (s *ContinuousDispatchTriggerService) dispatchReviewShadowItem(
	ctx context.Context,
	workspaceID, projectID pgtype.UUID,
	item ContinuousDispatchShadowItem,
	actorUserID pgtype.UUID,
	handoffNote string,
	provenance *ContinuousDispatchReviewProvenance,
) (ContinuousDispatchTriggerResult, error) {
	return s.dispatchShadowItemWithPrecondition(ctx, workspaceID, projectID, item, actorUserID, handoffNote, true, provenance)
}

func (s *ContinuousDispatchTriggerService) dispatchShadowItemWithPrecondition(
	ctx context.Context,
	workspaceID, projectID pgtype.UUID,
	item ContinuousDispatchShadowItem,
	actorUserID pgtype.UUID,
	handoffNote string,
	requireInReview bool,
	reviewProvenance *ContinuousDispatchReviewProvenance,
) (ContinuousDispatchTriggerResult, error) {
	action := item.NextAction
	if (action.State != continuousdispatch.StateReady && action.State != continuousdispatch.StateFallback) || action.Selected == nil {
		return ContinuousDispatchTriggerResult{}, ErrContinuousDispatchNotReady
	}
	selected := action.Selected
	if !item.DispatchIdentity.Complete() || item.DispatchIdentity.IssueID != item.IssueID ||
		strings.TrimSpace(selected.EmployeeID) == "" || strings.TrimSpace(selected.Model) == "" ||
		strings.TrimSpace(selected.AccountRef) == "" {
		return ContinuousDispatchTriggerResult{}, ErrContinuousDispatchSourceGap
	}
	agentID := parseDispatchUUID(selected.AgentID)
	runtimeID := parseDispatchUUID(selected.RuntimeID)
	if !agentID.Valid || !runtimeID.Valid {
		return ContinuousDispatchTriggerResult{}, ErrContinuousDispatchSourceGap
	}

	request := ContinuousDispatchRequest{
		Identity: item.DispatchIdentity,
		Route: ContinuousDispatchRoute{
			EmployeeRef:  continuousDispatchEmployeeRefPrefix + selected.EmployeeID,
			LocalAgentID: agentID,
			RuntimeID:    runtimeID,
			Model:        selected.Model,
			AccountRef:   selected.AccountRef,
		},
		ActorUserID:      actorUserID,
		HandoffNote:      handoffNote,
		requireInReview:  requireInReview,
		reviewProvenance: cloneContinuousDispatchReviewProvenance(reviewProvenance),
	}
	if s.authorityReviewMode && requireInReview {
		if s.authorityReviewGate == nil || s.authorityReviewIDs == nil {
			return ContinuousDispatchTriggerResult{}, ErrAuthorityReviewDispatchSourceGap
		}
		candidate := authorityReviewDispatchCandidateFromServer(request)
		issueID := itemIssueUUID(item)
		if !issueID.Valid || issueID.Bytes == ([16]byte{}) {
			return ContinuousDispatchTriggerResult{}, ErrAuthorityReviewDispatchSourceGap
		}
		identity, err := s.authorityReviewIDs.ResolveReviewDispatchIdentity(ctx, workspaceID, projectID, issueID, candidate)
		if err != nil {
			return ContinuousDispatchTriggerResult{}, fmt.Errorf("%w: authority identity resolution failed", ErrAuthorityReviewDispatchSourceGap)
		}
		receipt, err := s.authorityReviewGate.DispatchReview(ctx, identity, candidate)
		if err != nil {
			return ContinuousDispatchTriggerResult{}, err
		}
		return ContinuousDispatchTriggerResult{Action: action, Receipt: receipt}, nil
	}
	receipt, err := s.dispatcher.Dispatch(ctx, request)
	if err != nil {
		return ContinuousDispatchTriggerResult{}, err
	}
	return ContinuousDispatchTriggerResult{Action: action, Receipt: receipt}, nil
}

func itemIssueUUID(item ContinuousDispatchShadowItem) pgtype.UUID {
	return parseDispatchUUID(item.IssueID)
}

var _ ContinuousDispatchExactDispatcher = (*ContinuousDispatchService)(nil)

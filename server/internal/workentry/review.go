package workentry

import (
	"context"
	"fmt"
	"strings"
)

// ReviewRequest records an independent review verdict for a completion
// candidate. The reviewer must differ from the implementer (independent
// review); the verdict is persisted as an artifact_event so the existing
// artifact/promotion machinery consumes it.
type ReviewRequest struct {
	WorkRef         string         `json:"work_ref"`
	WorkspaceID     string         `json:"workspace_id"`
	ReviewerActorID string         `json:"reviewer_actor_id"`
	ReviewerWorkRef string         `json:"reviewer_work_ref"`
	Decision        ReviewDecision `json:"decision"`
	EvidenceRefs    []string       `json:"evidence_refs,omitempty"`
	ReviewedAt      string         `json:"reviewed_at,omitempty"`
}

// ReviewResult is the recorded verdict projection.
type ReviewResult struct {
	WorkRef                string         `json:"work_ref"`
	Decision               ReviewDecision `json:"decision"`
	Passed                 bool           `json:"passed"`
	EventID                string         `json:"event_id,omitempty"`
	ImplementerAuthorityID string         `json:"implementer_authority_id"`
	ReviewerAuthorityID    string         `json:"reviewer_authority_id"`
}

// reviewEventType maps a review decision to the reused artifact_event type.
func reviewEventType(d ReviewDecision) string {
	switch d {
	case ReviewPass:
		return "approved"
	case ReviewRevise:
		return "changes_requested"
	default:
		return "rejected"
	}
}

// artifactEventRecorder is the optional capability the Review path uses to
// persist the verdict into the existing artifact_event ledger.
type artifactEventRecorder interface {
	RecordArtifactEvent(ctx context.Context, in ArtifactEventInput) error
}

// ArtifactEventInput is the verdict event the Review path persists.
type ArtifactEventInput struct {
	WorkspaceID         string
	LineageID           string
	EventType           string
	CandidateID         string
	Revision            int32
	Digest              string
	ObjectRef           string
	ReviewerAuthorityID string
	IdempotencyKey      string
}

// authoritativeActorID returns the strongest immutable identity available in
// a registration receipt. Registered employees are keyed by employee_id;
// every other actor is keyed by the closed actor_type plus actor_id.
func authoritativeActorID(actor WorkActorIdentityV1) string {
	if actor.ActorType == ActorRegisteredEmployee && strings.TrimSpace(actor.EmployeeID) != "" {
		return "employee:" + actor.EmployeeID
	}
	return string(actor.ActorType) + ":" + actor.ActorID
}

// sameAuthoritativeActor rejects both an exact actor-id match and the same
// employee appearing through two runtime/session projections.
func sameAuthoritativeActor(implementer, reviewer WorkActorIdentityV1) bool {
	if strings.TrimSpace(implementer.ActorID) != "" && implementer.ActorID == reviewer.ActorID {
		return true
	}
	return strings.TrimSpace(implementer.EmployeeID) != "" && implementer.EmployeeID == reviewer.EmployeeID
}

// Review records the independent verdict (PASS→approved, REVISE→changes_requested)
// as an artifact_event and returns the result. It never auto-passes: the verdict
// is exactly what the reviewer provided.
func (s *Service) Review(ctx context.Context, req ReviewRequest) (ReviewResult, error) {
	if s == nil || s.store == nil {
		return ReviewResult{}, ErrUnavailable
	}
	if strings.TrimSpace(req.WorkRef) == "" {
		return ReviewResult{}, ErrInvalidRequest
	}
	if strings.TrimSpace(req.ReviewerActorID) == "" {
		return ReviewResult{}, ErrInvalidRequest
	}
	if strings.TrimSpace(req.ReviewerWorkRef) == "" {
		return ReviewResult{}, ErrInvalidRequest
	}
	if req.Decision != ReviewPass && req.Decision != ReviewRevise {
		return ReviewResult{}, fmt.Errorf("decision must be PASS or REVISE")
	}
	ws, _, issueID, _ := ParseWorkRef(req.WorkRef)
	reviewerWS, _, _, _ := ParseWorkRef(req.ReviewerWorkRef)
	if issueID == "" || ws == "" || reviewerWS == "" || req.WorkspaceID != ws || reviewerWS != ws {
		return ReviewResult{}, ErrInvalidRequest
	}
	implementerReceipt, err := s.store.FindReceiptByWorkRef(ctx, ws, req.WorkRef)
	if err != nil {
		return ReviewResult{}, err
	}
	if implementerReceipt == nil {
		return ReviewResult{}, ErrNotFound
	}
	reviewerReceipt, err := s.store.FindReceiptByWorkRef(ctx, ws, req.ReviewerWorkRef)
	if err != nil {
		return ReviewResult{}, err
	}
	if reviewerReceipt == nil {
		return ReviewResult{}, ErrNotFound
	}
	if req.ReviewerActorID != reviewerReceipt.Actor.ActorID {
		return ReviewResult{}, ErrInvalidRequest
	}
	if sameAuthoritativeActor(implementerReceipt.Actor, reviewerReceipt.Actor) {
		return ReviewResult{}, ErrSelfReview
	}
	implementerAuthorityID := authoritativeActorID(implementerReceipt.Actor)
	reviewerAuthorityID := authoritativeActorID(reviewerReceipt.Actor)
	rec, ok := s.store.(artifactEventRecorder)
	if !ok {
		// Stores without the artifact-event capability still return a coherent
		// verdict (memory store); the event is only persisted on the PG path.
		return ReviewResult{WorkRef: req.WorkRef, Decision: req.Decision, Passed: req.Decision == ReviewPass,
			ImplementerAuthorityID: implementerAuthorityID, ReviewerAuthorityID: reviewerAuthorityID}, nil
	}
	err = rec.RecordArtifactEvent(ctx, ArtifactEventInput{
		WorkspaceID:         ws,
		LineageID:           issueID,
		EventType:           reviewEventType(req.Decision),
		Revision:            1,
		ReviewerAuthorityID: reviewerAuthorityID,
		IdempotencyKey:      "review:" + req.WorkRef + ":" + reviewerAuthorityID + ":" + string(req.Decision),
	})
	if err != nil {
		return ReviewResult{}, err
	}
	return ReviewResult{WorkRef: req.WorkRef, Decision: req.Decision, Passed: req.Decision == ReviewPass,
		ImplementerAuthorityID: implementerAuthorityID, ReviewerAuthorityID: reviewerAuthorityID}, nil
}

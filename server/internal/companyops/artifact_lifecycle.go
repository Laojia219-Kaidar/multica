package companyops

import (
	"errors"
	"fmt"
	"sync"
)

var (
	ErrInvalidArtifactCandidate    = errors.New("invalid artifact candidate")
	ErrArtifactCandidateNotFound   = errors.New("artifact candidate not found")
	ErrArtifactRevisionMismatch    = errors.New("artifact revision mismatch")
	ErrArtifactDigestMismatch      = errors.New("artifact digest mismatch")
	ErrArtifactObjectRefMismatch   = errors.New("artifact object reference mismatch")
	ErrFormalArtifactRefMismatch   = errors.New("formal artifact reference mismatch")
	ErrInvalidArtifactTransition   = errors.New("invalid artifact transition")
	ErrArtifactIdempotencyRequired = errors.New("artifact event idempotency key is required")
	ErrArtifactIdempotencyConflict = errors.New("artifact event idempotency conflict")
	ErrArtifactPromotionConflict   = errors.New("artifact promotion id conflict")
)

type ArtifactCandidateInput struct {
	ID                 string
	LineageID          string
	Revision           int
	DurableObjectRef   string
	Digest             string
	SourceAttachmentID string
	SourceCommentID    string
}

type ArtifactCandidateRevisionInput struct {
	ID               string
	DurableObjectRef string
	Digest           string
}

// ArtifactCandidate is an immutable value snapshot. SourceAttachmentID and
// SourceCommentID retain provenance only; DurableObjectRef and Digest identify
// content whose lifetime is independent from either source row.
type ArtifactCandidate struct {
	ID                 string
	LineageID          string
	Revision           int
	SupersedesID       string
	DurableObjectRef   string
	Digest             string
	SourceAttachmentID string
	SourceCommentID    string
}

func NewArtifactCandidate(input ArtifactCandidateInput) (ArtifactCandidate, error) {
	candidate := ArtifactCandidate{
		ID:                 input.ID,
		LineageID:          input.LineageID,
		Revision:           input.Revision,
		DurableObjectRef:   input.DurableObjectRef,
		Digest:             input.Digest,
		SourceAttachmentID: input.SourceAttachmentID,
		SourceCommentID:    input.SourceCommentID,
	}
	if err := validateArtifactCandidate(candidate); err != nil {
		return ArtifactCandidate{}, err
	}
	if candidate.Revision != 1 {
		return ArtifactCandidate{}, fmt.Errorf("%w: initial revision must be 1", ErrInvalidArtifactCandidate)
	}
	return candidate, nil
}

func ReviseArtifactCandidate(previous ArtifactCandidate, input ArtifactCandidateRevisionInput) (ArtifactCandidate, error) {
	if err := validateArtifactCandidate(previous); err != nil {
		return ArtifactCandidate{}, err
	}
	revision := ArtifactCandidate{
		ID:               input.ID,
		LineageID:        previous.LineageID,
		Revision:         previous.Revision + 1,
		SupersedesID:     previous.ID,
		DurableObjectRef: input.DurableObjectRef,
		Digest:           input.Digest,
	}
	if err := validateArtifactCandidate(revision); err != nil {
		return ArtifactCandidate{}, err
	}
	if revision.ID == previous.ID {
		return ArtifactCandidate{}, fmt.Errorf("%w: revision id must be new", ErrInvalidArtifactCandidate)
	}
	if revision.DurableObjectRef == previous.DurableObjectRef {
		return ArtifactCandidate{}, fmt.Errorf("%w: revision requires an independent durable object", ErrInvalidArtifactCandidate)
	}
	if revision.Digest == previous.Digest {
		return ArtifactCandidate{}, fmt.Errorf("%w: revision digest must change", ErrInvalidArtifactCandidate)
	}
	return revision, nil
}

func validateArtifactCandidate(candidate ArtifactCandidate) error {
	switch {
	case candidate.ID == "":
		return fmt.Errorf("%w: id is required", ErrInvalidArtifactCandidate)
	case candidate.LineageID == "":
		return fmt.Errorf("%w: lineage id is required", ErrInvalidArtifactCandidate)
	case candidate.Revision < 1:
		return fmt.Errorf("%w: revision must be positive", ErrInvalidArtifactCandidate)
	case candidate.DurableObjectRef == "":
		return fmt.Errorf("%w: durable object reference is required", ErrInvalidArtifactCandidate)
	case candidate.Digest == "":
		return fmt.Errorf("%w: digest is required", ErrInvalidArtifactCandidate)
	case candidate.Revision == 1 && candidate.SupersedesID != "":
		return fmt.Errorf("%w: initial revision cannot supersede another candidate", ErrInvalidArtifactCandidate)
	case candidate.Revision > 1 && candidate.SupersedesID == "":
		return fmt.Errorf("%w: later revision must identify its predecessor", ErrInvalidArtifactCandidate)
	default:
		return nil
	}
}

type ArtifactEventType string

const (
	ArtifactEventSubmitted                  ArtifactEventType = "submitted"
	ArtifactEventChangesRequested           ArtifactEventType = "changes_requested"
	ArtifactEventApproved                   ArtifactEventType = "approved"
	ArtifactEventPromotionRequested         ArtifactEventType = "promotion_requested"
	ArtifactEventPromotionSucceeded         ArtifactEventType = "promotion_succeeded"
	ArtifactEventPromotionFailed            ArtifactEventType = "promotion_failed"
	ArtifactEventAuthorityReadbackConfirmed ArtifactEventType = "authority_readback_confirmed"
)

type ArtifactEventInput struct {
	Type               ArtifactEventType
	CandidateID        string
	CandidateRevision  int
	CandidateDigest    string
	CandidateObjectRef string
	FormalArtifactRef  string
	IdempotencyKey     string
}

type ArtifactEvent struct {
	ID                 string
	Sequence           int
	Type               ArtifactEventType
	CandidateID        string
	CandidateRevision  int
	CandidateDigest    string
	CandidateObjectRef string
	FormalArtifactRef  string
	IdempotencyKey     string
}

type ArtifactLifecycleProjection struct {
	CandidateID       string
	CandidateRevision int
	Status            ArtifactEventType
	FormalVisible     bool
	FormalArtifactRef string
}

type ArtifactLifecycle struct {
	mu sync.RWMutex

	lineageID         string
	activeCandidateID string
	candidates        map[string]ArtifactCandidate
	events            []ArtifactEvent
	idempotentEvents  map[string]ArtifactEvent
}

func NewArtifactLifecycle(candidate ArtifactCandidate) (*ArtifactLifecycle, error) {
	if err := validateArtifactCandidate(candidate); err != nil {
		return nil, err
	}
	if candidate.Revision != 1 {
		return nil, fmt.Errorf("%w: lifecycle must start at revision 1", ErrInvalidArtifactCandidate)
	}
	return &ArtifactLifecycle{
		lineageID:         candidate.LineageID,
		activeCandidateID: candidate.ID,
		candidates:        map[string]ArtifactCandidate{candidate.ID: candidate},
		idempotentEvents:  make(map[string]ArtifactEvent),
	}, nil
}

func (l *ArtifactLifecycle) AddRevision(candidate ArtifactCandidate) error {
	if err := validateArtifactCandidate(candidate); err != nil {
		return err
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if candidate.LineageID != l.lineageID {
		return fmt.Errorf("%w: candidate belongs to another lineage", ErrInvalidArtifactCandidate)
	}
	if _, exists := l.candidates[candidate.ID]; exists {
		return fmt.Errorf("%w: duplicate candidate id", ErrInvalidArtifactCandidate)
	}
	previous, exists := l.candidates[l.activeCandidateID]
	if !exists || candidate.SupersedesID != previous.ID || candidate.Revision != previous.Revision+1 {
		return fmt.Errorf("%w: revision does not extend the active candidate", ErrArtifactRevisionMismatch)
	}
	last, ok := l.lastEventForCandidateLocked(previous.ID)
	if !ok || last.Type != ArtifactEventChangesRequested {
		return fmt.Errorf("%w: a revision requires changes_requested", ErrInvalidArtifactTransition)
	}
	l.candidates[candidate.ID] = candidate
	l.activeCandidateID = candidate.ID
	return nil
}

func (l *ArtifactLifecycle) Append(input ArtifactEventInput) (ArtifactEvent, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if input.IdempotencyKey == "" {
		return ArtifactEvent{}, ErrArtifactIdempotencyRequired
	}
	if existing, ok := l.idempotentEvents[input.IdempotencyKey]; ok {
		if artifactEventMatchesInput(existing, input) {
			return existing, nil
		}
		return ArtifactEvent{}, ErrArtifactIdempotencyConflict
	}

	candidate, ok := l.candidates[input.CandidateID]
	if !ok || input.CandidateID != l.activeCandidateID {
		return ArtifactEvent{}, ErrArtifactCandidateNotFound
	}
	if input.CandidateRevision != candidate.Revision {
		return ArtifactEvent{}, ErrArtifactRevisionMismatch
	}
	if input.CandidateDigest != candidate.Digest {
		return ArtifactEvent{}, ErrArtifactDigestMismatch
	}
	if input.CandidateObjectRef != candidate.DurableObjectRef {
		return ArtifactEvent{}, ErrArtifactObjectRefMismatch
	}
	if err := l.validateTransitionLocked(candidate, input); err != nil {
		return ArtifactEvent{}, err
	}

	event := ArtifactEvent{
		ID:                 fmt.Sprintf("%s:event:%d", l.lineageID, len(l.events)+1),
		Sequence:           len(l.events) + 1,
		Type:               input.Type,
		CandidateID:        input.CandidateID,
		CandidateRevision:  input.CandidateRevision,
		CandidateDigest:    input.CandidateDigest,
		CandidateObjectRef: input.CandidateObjectRef,
		FormalArtifactRef:  input.FormalArtifactRef,
		IdempotencyKey:     input.IdempotencyKey,
	}
	l.events = append(l.events, event)
	l.idempotentEvents[input.IdempotencyKey] = event
	return event, nil
}

func (l *ArtifactLifecycle) validateTransitionLocked(candidate ArtifactCandidate, input ArtifactEventInput) error {
	last, hasLast := l.lastEventForCandidateLocked(candidate.ID)
	if !hasLast {
		if input.Type != ArtifactEventSubmitted {
			return ErrInvalidArtifactTransition
		}
		if input.FormalArtifactRef != "" {
			return ErrFormalArtifactRefMismatch
		}
		return nil
	}

	allowed := false
	switch last.Type {
	case ArtifactEventSubmitted:
		allowed = input.Type == ArtifactEventChangesRequested || input.Type == ArtifactEventApproved
	case ArtifactEventApproved:
		allowed = input.Type == ArtifactEventPromotionRequested
	case ArtifactEventPromotionRequested:
		allowed = input.Type == ArtifactEventPromotionSucceeded || input.Type == ArtifactEventPromotionFailed
	case ArtifactEventPromotionFailed:
		allowed = input.Type == ArtifactEventPromotionRequested
	case ArtifactEventPromotionSucceeded:
		allowed = input.Type == ArtifactEventAuthorityReadbackConfirmed
	}
	if !allowed {
		return ErrInvalidArtifactTransition
	}

	switch input.Type {
	case ArtifactEventPromotionSucceeded:
		if input.FormalArtifactRef == "" {
			return ErrFormalArtifactRefMismatch
		}
	case ArtifactEventAuthorityReadbackConfirmed:
		if input.FormalArtifactRef == "" || input.FormalArtifactRef != last.FormalArtifactRef {
			return ErrFormalArtifactRefMismatch
		}
	default:
		if input.FormalArtifactRef != "" {
			return ErrFormalArtifactRefMismatch
		}
	}
	return nil
}

func (l *ArtifactLifecycle) Events() []ArtifactEvent {
	l.mu.RLock()
	defer l.mu.RUnlock()

	out := make([]ArtifactEvent, len(l.events))
	copy(out, l.events)
	return out
}

func (l *ArtifactLifecycle) Projection() ArtifactLifecycleProjection {
	l.mu.RLock()
	defer l.mu.RUnlock()

	candidate := l.candidates[l.activeCandidateID]
	projection := ArtifactLifecycleProjection{
		CandidateID:       candidate.ID,
		CandidateRevision: candidate.Revision,
	}
	last, ok := l.lastEventForCandidateLocked(candidate.ID)
	if !ok {
		return projection
	}
	projection.Status = last.Type
	if last.Type == ArtifactEventAuthorityReadbackConfirmed {
		projection.FormalVisible = true
		projection.FormalArtifactRef = last.FormalArtifactRef
	}
	return projection
}

func (l *ArtifactLifecycle) lastEventForCandidateLocked(candidateID string) (ArtifactEvent, bool) {
	for i := len(l.events) - 1; i >= 0; i-- {
		if l.events[i].CandidateID == candidateID {
			return l.events[i], true
		}
	}
	return ArtifactEvent{}, false
}

func artifactEventMatchesInput(event ArtifactEvent, input ArtifactEventInput) bool {
	return event.Type == input.Type &&
		event.CandidateID == input.CandidateID &&
		event.CandidateRevision == input.CandidateRevision &&
		event.CandidateDigest == input.CandidateDigest &&
		event.CandidateObjectRef == input.CandidateObjectRef &&
		event.FormalArtifactRef == input.FormalArtifactRef
}

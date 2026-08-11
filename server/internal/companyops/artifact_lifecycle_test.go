package companyops

import (
	"errors"
	"reflect"
	"testing"
)

func TestArtifactCandidateRevisionIsImmutableAndDurable(t *testing.T) {
	input := ArtifactCandidateInput{
		ID:                 "candidate-v1",
		LineageID:          "artifact-lineage-1",
		Revision:           1,
		DurableObjectRef:   "object://hivecrew/artifacts/candidate-v1",
		Digest:             "sha256:v1",
		SourceAttachmentID: "attachment-1",
		SourceCommentID:    "comment-1",
	}
	v1, err := NewArtifactCandidate(input)
	if err != nil {
		t.Fatalf("NewArtifactCandidate() error = %v", err)
	}
	v1BeforeRevision := v1

	v2, err := ReviseArtifactCandidate(v1, ArtifactCandidateRevisionInput{
		ID:               "candidate-v2",
		DurableObjectRef: "object://hivecrew/artifacts/candidate-v2",
		Digest:           "sha256:v2",
	})
	if err != nil {
		t.Fatalf("ReviseArtifactCandidate() error = %v", err)
	}

	if !reflect.DeepEqual(v1, v1BeforeRevision) {
		t.Fatalf("revision mutated v1: before=%+v after=%+v", v1BeforeRevision, v1)
	}
	if v2.Revision != 2 || v2.SupersedesID != v1.ID {
		t.Fatalf("v2 lineage = revision %d supersedes %q, want revision 2 supersedes %q", v2.Revision, v2.SupersedesID, v1.ID)
	}
	if v2.DurableObjectRef == v1.DurableObjectRef || v2.Digest == v1.Digest {
		t.Fatalf("revision must have independent durable content: v1=%+v v2=%+v", v1, v2)
	}

	// Source attachment/comment rows are provenance only. Deleting them must not
	// clear or rewrite the candidate's independently retained value.
	input.SourceAttachmentID = ""
	input.SourceCommentID = ""
	input.DurableObjectRef = ""
	input.Digest = ""
	if v1.DurableObjectRef != "object://hivecrew/artifacts/candidate-v1" || v1.Digest != "sha256:v1" {
		t.Fatalf("source deletion changed candidate value: %+v", v1)
	}
}

func TestArtifactLifecycleFormalVisibilityRequiresAuthorityReadback(t *testing.T) {
	v1 := mustArtifactCandidate(t, ArtifactCandidateInput{
		ID:               "candidate-v1",
		LineageID:        "artifact-lineage-1",
		Revision:         1,
		DurableObjectRef: "object://hivecrew/artifacts/candidate-v1",
		Digest:           "sha256:v1",
	})
	lifecycle, err := NewArtifactLifecycle(v1)
	if err != nil {
		t.Fatalf("NewArtifactLifecycle() error = %v", err)
	}

	mustAppendArtifactEvent(t, lifecycle, v1, ArtifactEventSubmitted, "submit-v1", "")
	mustAppendArtifactEvent(t, lifecycle, v1, ArtifactEventChangesRequested, "changes-v1", "")
	if lifecycle.Projection().FormalVisible {
		t.Fatal("changes_requested candidate must not be formally visible")
	}

	v2 := mustArtifactRevision(t, v1, ArtifactCandidateRevisionInput{
		ID:               "candidate-v2",
		DurableObjectRef: "object://hivecrew/artifacts/candidate-v2",
		Digest:           "sha256:v2",
	})
	if err := lifecycle.AddRevision(v2); err != nil {
		t.Fatalf("AddRevision() error = %v", err)
	}

	formalRef := "hivecosm://formal-artifacts/FA-1"
	sequence := []ArtifactEventType{
		ArtifactEventSubmitted,
		ArtifactEventApproved,
		ArtifactEventPromotionRequested,
		ArtifactEventPromotionSucceeded,
		ArtifactEventAuthorityReadbackConfirmed,
	}
	for i, eventType := range sequence {
		ref := ""
		if eventType == ArtifactEventPromotionSucceeded || eventType == ArtifactEventAuthorityReadbackConfirmed {
			ref = formalRef
		}
		mustAppendArtifactEvent(t, lifecycle, v2, eventType, "v2-event-"+string(eventType), ref)
		wantVisible := i == len(sequence)-1
		if got := lifecycle.Projection().FormalVisible; got != wantVisible {
			t.Fatalf("after %s FormalVisible = %v, want %v", eventType, got, wantVisible)
		}
	}

	events := lifecycle.Events()
	if got, want := len(events), 7; got != want {
		t.Fatalf("event count = %d, want %d", got, want)
	}
	events[0].ID = "mutated-copy"
	if lifecycle.Events()[0].ID == "mutated-copy" {
		t.Fatal("Events() exposed mutable ledger storage")
	}
	if got := lifecycle.Projection().FormalArtifactRef; got != formalRef {
		t.Fatalf("FormalArtifactRef = %q, want %q", got, formalRef)
	}
}

func TestPromotionFailureNeverLooksSuccessful(t *testing.T) {
	candidate := mustArtifactCandidate(t, ArtifactCandidateInput{
		ID:               "candidate-v1",
		LineageID:        "artifact-lineage-failed",
		Revision:         1,
		DurableObjectRef: "object://hivecrew/artifacts/candidate-v1",
		Digest:           "sha256:v1",
	})
	lifecycle, err := NewArtifactLifecycle(candidate)
	if err != nil {
		t.Fatalf("NewArtifactLifecycle() error = %v", err)
	}
	mustAppendArtifactEvent(t, lifecycle, candidate, ArtifactEventSubmitted, "submit", "")
	mustAppendArtifactEvent(t, lifecycle, candidate, ArtifactEventApproved, "approve", "")
	mustAppendArtifactEvent(t, lifecycle, candidate, ArtifactEventPromotionRequested, "promote", "")
	mustAppendArtifactEvent(t, lifecycle, candidate, ArtifactEventPromotionFailed, "promote-failed", "")

	projection := lifecycle.Projection()
	if projection.FormalVisible || projection.FormalArtifactRef != "" {
		t.Fatalf("failed promotion masqueraded as formal success: %+v", projection)
	}
}

func TestArtifactLifecycleDigestAndReferenceMismatchFailClosed(t *testing.T) {
	candidate := mustArtifactCandidate(t, ArtifactCandidateInput{
		ID:               "candidate-v1",
		LineageID:        "artifact-lineage-mismatch",
		Revision:         1,
		DurableObjectRef: "object://hivecrew/artifacts/candidate-v1",
		Digest:           "sha256:v1",
	})

	tests := []struct {
		name    string
		mutate  func(*ArtifactEventInput)
		wantErr error
	}{
		{
			name: "digest mismatch",
			mutate: func(input *ArtifactEventInput) {
				input.CandidateDigest = "sha256:other"
			},
			wantErr: ErrArtifactDigestMismatch,
		},
		{
			name: "durable object reference mismatch",
			mutate: func(input *ArtifactEventInput) {
				input.CandidateObjectRef = "object://hivecrew/artifacts/other"
			},
			wantErr: ErrArtifactObjectRefMismatch,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lifecycle := mustPromotionRequestedLifecycle(t, candidate)
			input := artifactEventInput(candidate, ArtifactEventPromotionSucceeded, "promotion-result", "hivecosm://formal-artifacts/FA-1")
			tt.mutate(&input)
			before := len(lifecycle.Events())

			if _, err := lifecycle.Append(input); !errors.Is(err, tt.wantErr) {
				t.Fatalf("Append() error = %v, want %v", err, tt.wantErr)
			}
			if lifecycle.Projection().FormalVisible {
				t.Fatal("mismatch must fail closed")
			}
			if got := len(lifecycle.Events()); got != before {
				t.Fatalf("rejected mismatch appended an event: got %d events, want %d", got, before)
			}
		})
	}
}

func TestArtifactEventIdempotencyReturnsSameEvent(t *testing.T) {
	candidate := mustArtifactCandidate(t, ArtifactCandidateInput{
		ID:               "candidate-v1",
		LineageID:        "artifact-lineage-idempotent",
		Revision:         1,
		DurableObjectRef: "object://hivecrew/artifacts/candidate-v1",
		Digest:           "sha256:v1",
	})
	lifecycle, err := NewArtifactLifecycle(candidate)
	if err != nil {
		t.Fatalf("NewArtifactLifecycle() error = %v", err)
	}
	input := artifactEventInput(candidate, ArtifactEventSubmitted, "same-key", "")

	first, err := lifecycle.Append(input)
	if err != nil {
		t.Fatalf("first Append() error = %v", err)
	}
	second, err := lifecycle.Append(input)
	if err != nil {
		t.Fatalf("idempotent Append() error = %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("same idempotency key returned different events: first=%q second=%q", first.ID, second.ID)
	}
	if got := len(lifecycle.Events()); got != 1 {
		t.Fatalf("same idempotency key appended %d events, want 1", got)
	}
}

func TestArtifactLifecycleRejectsOutOfOrderEvents(t *testing.T) {
	candidate := mustArtifactCandidate(t, ArtifactCandidateInput{
		ID:               "candidate-v1",
		LineageID:        "artifact-lineage-order",
		Revision:         1,
		DurableObjectRef: "object://hivecrew/artifacts/candidate-v1",
		Digest:           "sha256:v1",
	})
	lifecycle, err := NewArtifactLifecycle(candidate)
	if err != nil {
		t.Fatalf("NewArtifactLifecycle() error = %v", err)
	}
	mustAppendArtifactEvent(t, lifecycle, candidate, ArtifactEventSubmitted, "submit", "")
	before := len(lifecycle.Events())

	_, err = lifecycle.Append(artifactEventInput(candidate, ArtifactEventPromotionRequested, "premature-promotion", ""))
	if !errors.Is(err, ErrInvalidArtifactTransition) {
		t.Fatalf("out-of-order Append() error = %v, want %v", err, ErrInvalidArtifactTransition)
	}
	if got := len(lifecycle.Events()); got != before {
		t.Fatalf("out-of-order event was appended: got %d events, want %d", got, before)
	}
	if lifecycle.Projection().FormalVisible {
		t.Fatal("out-of-order event made artifact formally visible")
	}
}

func mustArtifactCandidate(t *testing.T, input ArtifactCandidateInput) ArtifactCandidate {
	t.Helper()
	candidate, err := NewArtifactCandidate(input)
	if err != nil {
		t.Fatalf("NewArtifactCandidate() error = %v", err)
	}
	return candidate
}

func mustArtifactRevision(t *testing.T, previous ArtifactCandidate, input ArtifactCandidateRevisionInput) ArtifactCandidate {
	t.Helper()
	candidate, err := ReviseArtifactCandidate(previous, input)
	if err != nil {
		t.Fatalf("ReviseArtifactCandidate() error = %v", err)
	}
	return candidate
}

func artifactEventInput(candidate ArtifactCandidate, eventType ArtifactEventType, idempotencyKey, formalRef string) ArtifactEventInput {
	return ArtifactEventInput{
		Type:               eventType,
		CandidateID:        candidate.ID,
		CandidateRevision:  candidate.Revision,
		CandidateDigest:    candidate.Digest,
		CandidateObjectRef: candidate.DurableObjectRef,
		FormalArtifactRef:  formalRef,
		IdempotencyKey:     idempotencyKey,
	}
}

func mustAppendArtifactEvent(t *testing.T, lifecycle *ArtifactLifecycle, candidate ArtifactCandidate, eventType ArtifactEventType, idempotencyKey, formalRef string) ArtifactEvent {
	t.Helper()
	event, err := lifecycle.Append(artifactEventInput(candidate, eventType, idempotencyKey, formalRef))
	if err != nil {
		t.Fatalf("Append(%s) error = %v", eventType, err)
	}
	return event
}

func mustPromotionRequestedLifecycle(t *testing.T, candidate ArtifactCandidate) *ArtifactLifecycle {
	t.Helper()
	lifecycle, err := NewArtifactLifecycle(candidate)
	if err != nil {
		t.Fatalf("NewArtifactLifecycle() error = %v", err)
	}
	mustAppendArtifactEvent(t, lifecycle, candidate, ArtifactEventSubmitted, "submit", "")
	mustAppendArtifactEvent(t, lifecycle, candidate, ArtifactEventApproved, "approve", "")
	mustAppendArtifactEvent(t, lifecycle, candidate, ArtifactEventPromotionRequested, "promote", "")
	return lifecycle
}

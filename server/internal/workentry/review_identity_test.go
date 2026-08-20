package workentry

import (
	"context"
	"errors"
	"testing"
)

type reviewCaptureStore struct {
	*MemoryStore
	recorded []ArtifactEventInput
}

func (s *reviewCaptureStore) RecordArtifactEvent(_ context.Context, in ArtifactEventInput) error {
	s.recorded = append(s.recorded, in)
	return nil
}

func putReviewReceipt(t *testing.T, store *MemoryStore, workspaceID, workRef, key string, actor WorkActorIdentityV1) {
	t.Helper()
	if err := store.PutReceipt(context.Background(), ReceiptRecord{
		WorkspaceID: workspaceID, WorkRef: workRef, DedupeKey: key, Digest: "sha256:" + key, Actor: actor,
	}); err != nil {
		t.Fatalf("put receipt: %v", err)
	}
}

func TestReviewRejectsSelfReviewByActorID(t *testing.T) {
	const ws = "ws-review"
	implRef := FormatWorkRef(ws, "project", "issue", "")
	reviewerRef := FormatWorkRef(ws, "project", "review-issue", "")
	store := &reviewCaptureStore{MemoryStore: NewMemoryStore()}
	actor := WorkActorIdentityV1{ActorType: ActorExternalAgent, ActorID: "agent-1"}
	putReviewReceipt(t, store.MemoryStore, ws, implRef, "impl", actor)
	putReviewReceipt(t, store.MemoryStore, ws, reviewerRef, "review", actor)

	_, err := NewService(store).Review(context.Background(), ReviewRequest{
		WorkRef: implRef, WorkspaceID: ws, ReviewerActorID: "agent-1", ReviewerWorkRef: reviewerRef, Decision: ReviewPass,
	})
	if !errors.Is(err, ErrSelfReview) {
		t.Fatalf("self-review error = %v, want ErrSelfReview", err)
	}
	if len(store.recorded) != 0 {
		t.Fatalf("self-review persisted %d artifact events", len(store.recorded))
	}
}

func TestReviewRejectsSameEmployeeThroughDifferentActorProjection(t *testing.T) {
	const ws = "ws-review"
	implRef := FormatWorkRef(ws, "project", "issue", "")
	reviewerRef := FormatWorkRef(ws, "project", "review-issue", "")
	store := &reviewCaptureStore{MemoryStore: NewMemoryStore()}
	putReviewReceipt(t, store.MemoryStore, ws, implRef, "impl", WorkActorIdentityV1{
		ActorType: ActorRegisteredEmployee, ActorID: "runtime-projection-a", EmployeeID: "DE-REVIEW-01",
	})
	putReviewReceipt(t, store.MemoryStore, ws, reviewerRef, "review", WorkActorIdentityV1{
		ActorType: ActorRegisteredEmployee, ActorID: "runtime-projection-b", EmployeeID: "DE-REVIEW-01",
	})

	_, err := NewService(store).Review(context.Background(), ReviewRequest{
		WorkRef: implRef, WorkspaceID: ws, ReviewerActorID: "runtime-projection-b", ReviewerWorkRef: reviewerRef, Decision: ReviewPass,
	})
	if !errors.Is(err, ErrSelfReview) {
		t.Fatalf("same employee error = %v, want ErrSelfReview", err)
	}
}

func TestReviewRejectsUnprovenReviewerActorID(t *testing.T) {
	const ws = "ws-review"
	implRef := FormatWorkRef(ws, "project", "issue", "")
	reviewerRef := FormatWorkRef(ws, "project", "review-issue", "")
	store := &reviewCaptureStore{MemoryStore: NewMemoryStore()}
	putReviewReceipt(t, store.MemoryStore, ws, implRef, "impl", WorkActorIdentityV1{ActorType: ActorExternalAgent, ActorID: "worker"})
	putReviewReceipt(t, store.MemoryStore, ws, reviewerRef, "review", WorkActorIdentityV1{ActorType: ActorExternalAgent, ActorID: "reviewer"})

	_, err := NewService(store).Review(context.Background(), ReviewRequest{
		WorkRef: implRef, WorkspaceID: ws, ReviewerActorID: "forged-reviewer", ReviewerWorkRef: reviewerRef, Decision: ReviewPass,
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("forged reviewer error = %v, want ErrInvalidRequest", err)
	}
	if len(store.recorded) != 0 {
		t.Fatalf("forged reviewer persisted %d artifact events", len(store.recorded))
	}
}

func TestReviewRejectsAmbiguousImplementerReceiptLineage(t *testing.T) {
	const ws = "ws-review"
	implRef := FormatWorkRef(ws, "project", "issue", "")
	reviewerRef := FormatWorkRef(ws, "project", "review-issue", "")
	store := &reviewCaptureStore{MemoryStore: NewMemoryStore()}
	putReviewReceipt(t, store.MemoryStore, ws, implRef, "impl-a", WorkActorIdentityV1{ActorType: ActorExternalAgent, ActorID: "worker-a"})
	putReviewReceipt(t, store.MemoryStore, ws, implRef, "impl-b", WorkActorIdentityV1{ActorType: ActorExternalAgent, ActorID: "worker-b"})
	putReviewReceipt(t, store.MemoryStore, ws, reviewerRef, "review", WorkActorIdentityV1{ActorType: ActorExternalAgent, ActorID: "reviewer"})

	_, err := NewService(store).Review(context.Background(), ReviewRequest{
		WorkRef: implRef, WorkspaceID: ws, ReviewerActorID: "reviewer", ReviewerWorkRef: reviewerRef, Decision: ReviewPass,
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("ambiguous receipt error = %v, want ErrConflict", err)
	}
	if len(store.recorded) != 0 {
		t.Fatalf("ambiguous receipt persisted %d artifact events", len(store.recorded))
	}
}

func TestReviewPersistsReceiptBoundReviewerIdentity(t *testing.T) {
	const ws = "ws-review"
	implRef := FormatWorkRef(ws, "project", "issue", "")
	reviewerRef := FormatWorkRef(ws, "project", "review-issue", "")
	store := &reviewCaptureStore{MemoryStore: NewMemoryStore()}
	putReviewReceipt(t, store.MemoryStore, ws, implRef, "impl", WorkActorIdentityV1{ActorType: ActorExternalAgent, ActorID: "worker"})
	putReviewReceipt(t, store.MemoryStore, ws, reviewerRef, "review", WorkActorIdentityV1{ActorType: ActorExternalAgent, ActorID: "reviewer"})

	result, err := NewService(store).Review(context.Background(), ReviewRequest{
		WorkRef: implRef, WorkspaceID: ws, ReviewerActorID: "reviewer", ReviewerWorkRef: reviewerRef, Decision: ReviewPass,
	})
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if !result.Passed || result.ImplementerAuthorityID != "external_agent:worker" || result.ReviewerAuthorityID != "external_agent:reviewer" {
		t.Fatalf("result = %+v", result)
	}
	if len(store.recorded) != 1 || store.recorded[0].ReviewerAuthorityID != "external_agent:reviewer" {
		t.Fatalf("recorded = %+v", store.recorded)
	}
	if got := store.recorded[0].IdempotencyKey; got != "review:"+implRef+":external_agent:reviewer:PASS" {
		t.Fatalf("idempotency key = %q", got)
	}
}

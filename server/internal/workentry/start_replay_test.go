package workentry

import (
	"context"
	"testing"
)

// TestStartReplaySameEvent covers the work.start idempotency bug: the stable
// "start:<work_ref>:<session_id>:<run_id>" key must return the original event
// even when re-observed at a different wall-clock time (OccurredAt is not part
// of the idempotency identity).
func TestStartReplaySameEvent(t *testing.T) {
	store := NewMemoryStore()
	svc := NewService(store)
	ctx := context.Background()

	first, err := svc.Start(ctx, StartRequest{
		WorkRef: "hivecrew://ws-1/work/p1/i1", SessionID: "s1", RunID: "r1",
		ActorID: "EXT-1", WorkspaceID: "ws-1",
	})
	if err != nil {
		t.Fatalf("first start: %v", err)
	}
	second, err := svc.Start(ctx, StartRequest{
		WorkRef: "hivecrew://ws-1/work/p1/i1", SessionID: "s1", RunID: "r1",
		ActorID: "EXT-1", WorkspaceID: "ws-1",
	})
	if err != nil {
		t.Fatalf("second start should replay, got %v", err)
	}
	if second.EventID != first.EventID {
		t.Fatalf("start replay should return the same event id: %s vs %s", first.EventID, second.EventID)
	}
	if !second.Replayed {
		t.Fatalf("second start should be replayed, got %+v", second)
	}
}

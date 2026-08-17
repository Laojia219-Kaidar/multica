package service

import (
	"context"
	"testing"
	"time"
)

// TestWriterLeaseGuard_Real262Lifecycle reuses the existing loopback-only
// migration-262 harness. It intentionally has no router or daemon wiring: the
// test proves that the transport-neutral guard can drive the authoritative
// WriteLeaseService primitive without touching production state.
func TestWriterLeaseGuard_Real262Lifecycle(t *testing.T) {
	pool := newWriteLeaseTestPool(t)
	svc := NewWriteLeaseService(pool)
	key := uniqueMutexKey(t)
	cleanupLease(t, pool, key)
	guard, err := NewWriterLeaseGuard(svc, 30*time.Second, 5*time.Second)
	if err != nil {
		t.Fatalf("new guard: %v", err)
	}

	session, err := guard.AcquireForExecution(context.Background(), WriterLeaseTaskKindWork, key, "guard-integration")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := session.WithMutation(context.Background(), func(context.Context) error { return nil }); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if err := session.Heartbeat(context.Background()); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if err := session.ReleaseAfterTerminal(context.Background()); err != nil {
		t.Fatalf("release: %v", err)
	}
}

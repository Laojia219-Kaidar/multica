package scheduler

import (
	"context"
	"errors"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func newWriteLeaseTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL not set; skipping write lease integration test")
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	if parsed.Port() == "5432" {
		t.Skip("refusing to run write lease test against production port 5432")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open DATABASE_URL: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("ping DATABASE_URL: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func newWriteLeaseTestStore(t *testing.T, pool *pgxpool.Pool, start time.Time) (*WriteLeaseStore, *time.Time) {
	t.Helper()
	store := NewWriteLeaseStore(pool)
	now := start
	store.now = func(context.Context) (time.Time, error) { return now, nil }
	suffix := uuid.NewString()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM canonical_write_lease WHERE scope LIKE 'test-%'`)
	})
	_ = suffix
	return store, &now
}

func leaseScope(name string) string { return "test-" + name + "-" + uuid.NewString() }

func TestWriteLease_AcquireHeartbeatRelease(t *testing.T) {
	pool := newWriteLeaseTestPool(t)
	store, now := newWriteLeaseTestStore(t, pool, time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC))
	ctx := context.Background()
	scope := leaseScope("basic")

	lease, err := store.Acquire(ctx, scope, "holder-A", 30*time.Second, "writing")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if lease.Generation != 1 || lease.Status != writeLeaseStatusHeld || lease.HolderID != "holder-A" {
		t.Fatalf("unexpected lease: %+v", lease)
	}

	*now = now.Add(20 * time.Second)
	renewed, err := store.Heartbeat(ctx, scope, "holder-A", lease.Generation, 30*time.Second)
	if err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if !renewed.ExpiresAt.Equal(now.Add(30 * time.Second)) {
		t.Fatalf("heartbeat did not extend expiry: %v", renewed.ExpiresAt)
	}

	if err := store.Release(ctx, scope, "holder-A", lease.Generation, "done"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	released, err := store.Get(ctx, scope)
	if err != nil {
		t.Fatalf("Get after release: %v", err)
	}
	if released.Status != writeLeaseStatusReleased {
		t.Fatalf("status after release = %q, want released", released.Status)
	}

	// Same holder re-acquires after release: fresh generation.
	reacquired, err := store.Acquire(ctx, scope, "holder-A", 30*time.Second, "again")
	if err != nil {
		t.Fatalf("re-Acquire: %v", err)
	}
	if reacquired.Generation != 2 {
		t.Fatalf("generation after re-acquire = %d, want 2", reacquired.Generation)
	}
}

func TestWriteLease_ConcurrentWriterConflict(t *testing.T) {
	pool := newWriteLeaseTestPool(t)
	store, _ := newWriteLeaseTestStore(t, pool, time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC))
	ctx := context.Background()
	scope := leaseScope("conflict")

	if _, err := store.Acquire(ctx, scope, "holder-A", 60*time.Second, "writing"); err != nil {
		t.Fatalf("Acquire A: %v", err)
	}
	if _, err := store.Acquire(ctx, scope, "holder-B", 60*time.Second, "writing"); !errors.Is(err, ErrWriteLeaseHeld) {
		t.Fatalf("expected ErrWriteLeaseHeld for concurrent writer, got %v", err)
	}
}

func TestWriteLease_ExpiryAllowsSteal(t *testing.T) {
	pool := newWriteLeaseTestPool(t)
	store, now := newWriteLeaseTestStore(t, pool, time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC))
	ctx := context.Background()
	scope := leaseScope("expiry")

	first, err := store.Acquire(ctx, scope, "holder-A", 10*time.Second, "writing")
	if err != nil {
		t.Fatalf("Acquire A: %v", err)
	}

	// Advance past expiry, then run the expiry job primitive.
	*now = now.Add(11 * time.Second)
	reclaimed, err := store.Expire(ctx, *now)
	if err != nil {
		t.Fatalf("Expire: %v", err)
	}
	if reclaimed != 1 {
		t.Fatalf("Expire reclaimed %d, want 1", reclaimed)
	}

	stolen, err := store.Acquire(ctx, scope, "holder-B", 30*time.Second, "steal")
	if err != nil {
		t.Fatalf("Acquire B after expiry: %v", err)
	}
	if stolen.HolderID != "holder-B" || stolen.Generation != first.Generation+1 {
		t.Fatalf("unexpected steal: %+v", stolen)
	}
}

func TestWriteLease_GenerationMismatch(t *testing.T) {
	pool := newWriteLeaseTestPool(t)
	store, now := newWriteLeaseTestStore(t, pool, time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC))
	ctx := context.Background()
	scope := leaseScope("generation")

	first, err := store.Acquire(ctx, scope, "holder-A", 10*time.Second, "writing")
	if err != nil {
		t.Fatalf("Acquire A: %v", err)
	}
	*now = now.Add(11 * time.Second)
	if _, err := store.Expire(ctx, *now); err != nil {
		t.Fatalf("Expire: %v", err)
	}
	if _, err := store.Acquire(ctx, scope, "holder-B", 30*time.Second, "steal"); err != nil {
		t.Fatalf("Acquire B: %v", err)
	}

	// Stale holder-A tries to heartbeat and release with the old generation.
	if _, err := store.Heartbeat(ctx, scope, "holder-A", first.Generation, 30*time.Second); !errors.Is(err, ErrWriteLeaseGenerationMismatch) {
		t.Fatalf("expected ErrWriteLeaseGenerationMismatch on stale heartbeat, got %v", err)
	}
	if err := store.Release(ctx, scope, "holder-A", first.Generation, "cancel"); !errors.Is(err, ErrWriteLeaseGenerationMismatch) {
		t.Fatalf("expected ErrWriteLeaseGenerationMismatch on stale release, got %v", err)
	}
}

func TestWriteLease_CrashResume(t *testing.T) {
	pool := newWriteLeaseTestPool(t)
	store, now := newWriteLeaseTestStore(t, pool, time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC))
	ctx := context.Background()
	scope := leaseScope("crash")

	first, err := store.Acquire(ctx, scope, "holder-A", 60*time.Second, "writing")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	// Crash within TTL: resume keeps the same generation.
	resumed, err := store.ResumeAfterCrash(ctx, "holder-A", 60*time.Second, "resume")
	if err != nil {
		t.Fatalf("ResumeAfterCrash: %v", err)
	}
	if len(resumed) != 1 || resumed[0].Generation != first.Generation {
		t.Fatalf("resume did not preserve generation: %+v", resumed)
	}

	// Crash beyond TTL: another holder steals. The original holder's next
	// startup discovers it no longer owns the scope (generation protection) and
	// must Acquire afresh — which fails while the new holder is live.
	*now = now.Add(61 * time.Second)
	if _, err := store.Expire(ctx, *now); err != nil {
		t.Fatalf("Expire: %v", err)
	}
	if _, err := store.Acquire(ctx, scope, "holder-B", 60*time.Second, "steal"); err != nil {
		t.Fatalf("Acquire B: %v", err)
	}
	recovered, err := store.ResumeAfterCrash(ctx, "holder-A", 60*time.Second, "resume-after")
	if err != nil {
		t.Fatalf("ResumeAfterCrash after steal: %v", err)
	}
	if len(recovered) != 0 {
		t.Fatalf("resumed scopes after loss = %d, want 0", len(recovered))
	}
	if _, err := store.Acquire(ctx, scope, "holder-A", 60*time.Second, "re-acquire"); !errors.Is(err, ErrWriteLeaseHeld) {
		t.Fatalf("expected ErrWriteLeaseHeld while holder-B is live, got %v", err)
	}
}

func TestWriteLease_CleanupCancelled(t *testing.T) {
	pool := newWriteLeaseTestPool(t)
	store, now := newWriteLeaseTestStore(t, pool, time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC))
	ctx := context.Background()
	scope := leaseScope("cleanup")

	lease, err := store.Acquire(ctx, scope, "holder-A", 60*time.Second, "writing")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := store.Release(ctx, scope, "holder-A", lease.Generation, "done"); err != nil {
		t.Fatalf("Release: %v", err)
	}

	// Within grace: nothing is deleted.
	*now = now.Add(30 * time.Minute)
	removed, err := store.CleanupCancelled(ctx, *now, time.Hour)
	if err != nil {
		t.Fatalf("CleanupCancelled: %v", err)
	}
	if removed != 0 {
		t.Fatalf("CleanupCancelled removed %d within grace, want 0", removed)
	}

	// Past grace: the released row is deleted.
	*now = now.Add(61 * time.Minute)
	removed, err = store.CleanupCancelled(ctx, *now, time.Hour)
	if err != nil {
		t.Fatalf("CleanupCancelled: %v", err)
	}
	if removed != 1 {
		t.Fatalf("CleanupCancelled removed %d, want 1", removed)
	}
	if _, err := store.Get(ctx, scope); !errors.Is(err, ErrWriteLeaseNotFound) {
		t.Fatalf("expected ErrWriteLeaseNotFound after cleanup, got %v", err)
	}
}

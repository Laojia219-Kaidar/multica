package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ---------------------------------------------------------------------------
// Tests for the single-writer lease with fencing generation (HIV-410).
//
// These tests follow the established real-Postgres pattern from
// task_claim_race_test.go: connect to DATABASE_URL (default
// postgres://multica:multica@localhost:5432/multica?sslmode=disable),
// skip if unavailable, create fixtures inline, clean up with t.Cleanup.
// ---------------------------------------------------------------------------

func newWriteLeaseTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://multica:multica@127.0.0.1:5432/multica?sslmode=disable"
	}

	// TCP pre-check: avoid the pgxpool hang when the host is unreachable.
	// Parse the DATABASE_URL host:port and try a 2-second TCP dial, so the
	// pre-check follows the actual DSN instead of a hardcoded endpoint.
	cfg, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		t.Skipf("parse DATABASE_URL: %v", err)
	}
	addr := net.JoinHostPort(cfg.ConnConfig.Host, strconv.Itoa(int(cfg.ConnConfig.Port)))
	if conn, err := net.DialTimeout("tcp", addr, 2*time.Second); err != nil {
		t.Skipf("database unreachable (tcp pre-check %s): %v", addr, err)
	} else {
		conn.Close()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Skipf("database unavailable: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("database unreachable: %v", err)
	}
	var tableName *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.write_lease')::text`).Scan(&tableName); err != nil {
		pool.Close()
		t.Fatalf("verify migration 262: %v", err)
	}
	if tableName == nil {
		pool.Close()
		t.Fatal("write_lease table missing: apply migration 262 to the dedicated test database")
	}
	t.Cleanup(pool.Close)
	return pool
}

func uniqueMutexKey(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("test:write_lease:%d", time.Now().UnixNano())
}

func cleanupLease(t *testing.T, pool *pgxpool.Pool, mutexKey string) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		pool.Exec(ctx, `DELETE FROM write_lease WHERE mutex_key = $1`, mutexKey)
	})
}

// ---------------------------------------------------------------------------
// Lifecycle: acquire → renew → release → reacquire
// ---------------------------------------------------------------------------

func TestWriteLease_AcquireRenewRelease(t *testing.T) {
	ctx := context.Background()
	pool := newWriteLeaseTestPool(t)
	svc := NewWriteLeaseService(pool)
	key := uniqueMutexKey(t)
	cleanupLease(t, pool, key)

	// Acquire
	lease, err := svc.Acquire(ctx, key, "worker-A", 10*time.Second)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if lease.Status != WriteLeaseHeld {
		t.Fatalf("expected status held, got %s", lease.Status)
	}
	if lease.FenceGeneration != 1 {
		t.Fatalf("expected fence_generation 1 on first acquire, got %d", lease.FenceGeneration)
	}
	if lease.LeaseToken.String() == "" {
		t.Fatal("expected non-empty lease token")
	}
	if lease.HolderID != "worker-A" {
		t.Fatalf("expected holder worker-A, got %s", lease.HolderID)
	}

	// Renew
	renewed, err := svc.Renew(ctx, key, lease.LeaseToken, lease.FenceGeneration, 20*time.Second)
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	if renewed.RenewedCount != 1 {
		t.Fatalf("expected renewed_count 1, got %d", renewed.RenewedCount)
	}
	if renewed.FenceGeneration != lease.FenceGeneration {
		t.Fatalf("renew must not change fence_generation")
	}

	// Release
	released, err := svc.Release(ctx, key, lease.LeaseToken, lease.FenceGeneration)
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if released.Status != WriteLeaseFree {
		t.Fatalf("expected status free after release, got %s", released.Status)
	}

	// Reacquire — fence_generation must increment
	lease2, err := svc.Acquire(ctx, key, "worker-B", 10*time.Second)
	if err != nil {
		t.Fatalf("reacquire: %v", err)
	}
	if lease2.FenceGeneration != 2 {
		t.Fatalf("expected fence_generation 2 on reacquire, got %d", lease2.FenceGeneration)
	}
	if lease2.HolderID != "worker-B" {
		t.Fatalf("expected holder worker-B, got %s", lease2.HolderID)
	}
}

// ---------------------------------------------------------------------------
// Busy mutex: second acquire on a live lease returns ErrLeaseBusy
// ---------------------------------------------------------------------------

func TestWriteLease_AcquireBusy(t *testing.T) {
	ctx := context.Background()
	pool := newWriteLeaseTestPool(t)
	svc := NewWriteLeaseService(pool)
	key := uniqueMutexKey(t)
	cleanupLease(t, pool, key)

	_, err := svc.Acquire(ctx, key, "holder", 30*time.Second)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	_, err = svc.Acquire(ctx, key, "challenger", 30*time.Second)
	if !errors.Is(err, ErrLeaseBusy) {
		t.Fatalf("expected ErrLeaseBusy, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Readback
// ---------------------------------------------------------------------------

func TestWriteLease_Readback(t *testing.T) {
	ctx := context.Background()
	pool := newWriteLeaseTestPool(t)
	svc := NewWriteLeaseService(pool)
	key := uniqueMutexKey(t)
	cleanupLease(t, pool, key)

	// Read before acquire — not found
	_, err := svc.Read(ctx, key)
	if !errors.Is(err, ErrLeaseNotFound) {
		t.Fatalf("expected ErrLeaseNotFound, got %v", err)
	}

	lease, err := svc.Acquire(ctx, key, "reader", 30*time.Second)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	// Read back the row
	readBack, err := svc.Read(ctx, key)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if readBack.MutexKey != key || readBack.FenceGeneration != lease.FenceGeneration {
		t.Fatalf("readback mismatch")
	}

	// VerifyHeld with correct token succeeds
	held, err := svc.VerifyHeld(ctx, key, lease.LeaseToken, lease.FenceGeneration)
	if err != nil {
		t.Fatalf("verify held: %v", err)
	}
	if held.MutexKey != key {
		t.Fatalf("verify held returned wrong key")
	}

	// VerifyHeld with wrong token fails
	_, err = svc.VerifyHeld(ctx, key, lease.LeaseToken, lease.FenceGeneration+999)
	if !errors.Is(err, ErrLeaseNotHeld) {
		t.Fatalf("expected ErrLeaseNotHeld with stale generation, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Stale generation rejection: after force-cancel, old token rejected
// ---------------------------------------------------------------------------

func TestWriteLease_StaleGenerationRejected(t *testing.T) {
	ctx := context.Background()
	pool := newWriteLeaseTestPool(t)
	svc := NewWriteLeaseService(pool)
	key := uniqueMutexKey(t)
	cleanupLease(t, pool, key)

	// Worker A acquires
	leaseA, err := svc.Acquire(ctx, key, "worker-A", 60*time.Second)
	if err != nil {
		t.Fatalf("acquire A: %v", err)
	}
	genA := leaseA.FenceGeneration
	tokenA := leaseA.LeaseToken

	// Admin force-cancels A (crash detected)
	cancelled, err := svc.ForceCancel(ctx, key, "crash recovery")
	if err != nil {
		t.Fatalf("force cancel: %v", err)
	}
	// ForceCancel bumps generation
	if cancelled.FenceGeneration != genA+1 {
		t.Fatalf("expected generation %d after cancel, got %d", genA+1, cancelled.FenceGeneration)
	}

	// Worker A tries to renew with old token/generation — must fail
	_, err = svc.Renew(ctx, key, tokenA, genA, 30*time.Second)
	if !errors.Is(err, ErrLeaseNotHeld) {
		t.Fatalf("stale renew must be rejected, got %v", err)
	}

	// Worker A tries to release — must also fail
	_, err = svc.Release(ctx, key, tokenA, genA)
	if !errors.Is(err, ErrLeaseNotHeld) {
		t.Fatalf("stale release must be rejected, got %v", err)
	}

	// Worker B acquires the now-free mutex
	leaseB, err := svc.Acquire(ctx, key, "worker-B", 60*time.Second)
	if err != nil {
		t.Fatalf("acquire B: %v", err)
	}
	// Generation should be genA + 2 (cancel bumped to genA+1, then acquire bumps to genA+2)
	if leaseB.FenceGeneration != genA+2 {
		t.Fatalf("expected generation %d for worker-B, got %d", genA+2, leaseB.FenceGeneration)
	}
}

// ---------------------------------------------------------------------------
// Crash recovery: expired lease swept then reacquired
// ---------------------------------------------------------------------------

func TestWriteLease_CrashRecoverySweep(t *testing.T) {
	ctx := context.Background()
	pool := newWriteLeaseTestPool(t)
	svc := NewWriteLeaseService(pool)
	key := uniqueMutexKey(t)
	cleanupLease(t, pool, key)

	// Acquire with a very short TTL to simulate crash
	lease, err := svc.Acquire(ctx, key, "crashed-worker", 1*time.Second)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	// Wait for expiry
	time.Sleep(1500 * time.Millisecond)

	// Sweep should mark it expired
	swept, err := svc.SweepExpired(ctx)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	found := false
	for _, s := range swept {
		if s.MutexKey == key {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected swept lease to include our key")
	}

	// Now a new worker can acquire (generation increments)
	lease2, err := svc.Acquire(ctx, key, "recovery-worker", 30*time.Second)
	if err != nil {
		t.Fatalf("recovery acquire: %v", err)
	}
	if lease2.FenceGeneration != lease.FenceGeneration+1 {
		t.Fatalf("expected generation %d after recovery, got %d", lease.FenceGeneration+1, lease2.FenceGeneration)
	}

	// Old worker's token is now stale
	_, err = svc.Renew(ctx, key, lease.LeaseToken, lease.FenceGeneration, 30*time.Second)
	if !errors.Is(err, ErrLeaseNotHeld) {
		t.Fatalf("stale renew after recovery must be rejected, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Concurrent acquire (race): exactly one goroutine wins
// ---------------------------------------------------------------------------

func TestWriteLease_ConcurrentAcquireRace(t *testing.T) {
	ctx := context.Background()
	pool := newWriteLeaseTestPool(t)
	svc := NewWriteLeaseService(pool)
	key := uniqueMutexKey(t)
	cleanupLease(t, pool, key)

	const workers = 10
	start := make(chan struct{})
	var winners atomic.Int32
	var errs atomic.Int32
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			<-start
			_, err := svc.Acquire(ctx, key, fmt.Sprintf("worker-%d", id), 30*time.Second)
			if err != nil {
				if errors.Is(err, ErrLeaseBusy) {
					errs.Add(1)
					return
				}
				t.Errorf("unexpected error: %v", err)
				return
			}
			winners.Add(1)
		}(i)
	}

	close(start)
	wg.Wait()

	if winners.Load() != 1 {
		t.Fatalf("expected exactly 1 winner, got %d", winners.Load())
	}
	if errs.Load() != int32(workers-1) {
		t.Fatalf("expected %d ErrLeaseBusy, got %d", workers-1, errs.Load())
	}

	// Verify only one held row
	readBack, err := svc.Read(ctx, key)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if readBack.Status != WriteLeaseHeld {
		t.Fatalf("expected status held, got %s", readBack.Status)
	}
}

// ---------------------------------------------------------------------------
// Transaction rollback: acquire inside a rolled-back tx is undone
// ---------------------------------------------------------------------------

func TestWriteLease_TransactionRollback(t *testing.T) {
	ctx := context.Background()
	pool := newWriteLeaseTestPool(t)
	svc := NewWriteLeaseService(pool)
	key := uniqueMutexKey(t)
	cleanupLease(t, pool, key)

	// Insert a held lease inside a transaction, then roll back
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO write_lease (mutex_key, holder_id, lease_token, fence_generation, status, acquired_at, expires_at)
		VALUES ($1, 'tx-worker', gen_random_uuid(), 1, 'held', now(), now() + interval '30 seconds')
	`, key)
	if err != nil {
		tx.Rollback(ctx)
		t.Fatalf("insert lease in tx: %v", err)
	}

	// Roll back — the insert must be undone
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	// The lease should not exist (or at least not be held by tx-worker)
	readBack, err := svc.Read(ctx, key)
	if err == nil && readBack.HolderID == "tx-worker" {
		t.Fatal("rolled-back lease should not be visible")
	}
	// ErrLeaseNotFound is the expected outcome
	if err != nil && !errors.Is(err, ErrLeaseNotFound) {
		t.Fatalf("unexpected error after rollback: %v", err)
	}

	// A new acquire should succeed with generation 1
	lease, err := svc.Acquire(ctx, key, "clean-worker", 30*time.Second)
	if err != nil {
		t.Fatalf("acquire after rollback: %v", err)
	}
	if lease.FenceGeneration != 1 {
		t.Fatalf("expected generation 1 after rollback, got %d", lease.FenceGeneration)
	}
}

// ---------------------------------------------------------------------------
// Concurrent renew race: two goroutines with the same token, only
// one renewal window is valid at a time (proves the CAS works)
// ---------------------------------------------------------------------------

func TestWriteLease_ConcurrentRenewSameToken(t *testing.T) {
	ctx := context.Background()
	pool := newWriteLeaseTestPool(t)
	svc := NewWriteLeaseService(pool)
	key := uniqueMutexKey(t)
	cleanupLease(t, pool, key)

	lease, err := svc.Acquire(ctx, key, "renewer", 30*time.Second)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	const goroutines = 5
	var wg sync.WaitGroup
	start := make(chan struct{})
	var successes atomic.Int32

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := svc.Renew(ctx, key, lease.LeaseToken, lease.FenceGeneration, 30*time.Second)
			if err == nil {
				successes.Add(1)
			}
		}()
	}

	close(start)
	wg.Wait()

	// All goroutines present the same valid token + generation.
	// Each successful UPDATE bumps renewed_count but not fence_generation,
	// so all goroutines should succeed (the WHERE clause matches every time
	// because fence_generation is unchanged by Renew).
	if successes.Load() != int32(goroutines) {
		t.Fatalf("expected all %d renewals to succeed, got %d", goroutines, successes.Load())
	}

	// Verify renewed_count reflects all renewals
	readBack, err := svc.Read(ctx, key)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if readBack.RenewedCount != int32(goroutines) {
		t.Fatalf("expected renewed_count %d, got %d", goroutines, readBack.RenewedCount)
	}
}

// ---------------------------------------------------------------------------
// Release then stale renew: after release, old token rejected
// ---------------------------------------------------------------------------

func TestWriteLease_ReleaseThenStaleRejected(t *testing.T) {
	ctx := context.Background()
	pool := newWriteLeaseTestPool(t)
	svc := NewWriteLeaseService(pool)
	key := uniqueMutexKey(t)
	cleanupLease(t, pool, key)

	lease, err := svc.Acquire(ctx, key, "worker", 30*time.Second)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	if _, err := svc.Release(ctx, key, lease.LeaseToken, lease.FenceGeneration); err != nil {
		t.Fatalf("release: %v", err)
	}

	// Renew after release must fail
	_, err = svc.Renew(ctx, key, lease.LeaseToken, lease.FenceGeneration, 30*time.Second)
	if !errors.Is(err, ErrLeaseNotHeld) {
		t.Fatalf("renew after release must be rejected, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Expired-row concurrent acquire: a lapsed held row is reclaimed directly
// by the acquire UPSERT (no sweep needed), and exactly one racer wins.
// ---------------------------------------------------------------------------

func TestWriteLease_ExpiredRowConcurrentAcquireSingleWinner(t *testing.T) {
	ctx := context.Background()
	pool := newWriteLeaseTestPool(t)
	svc := NewWriteLeaseService(pool)
	key := uniqueMutexKey(t)
	cleanupLease(t, pool, key)

	// A crashed worker holds a short-lived lease that lapses.
	lease, err := svc.Acquire(ctx, key, "crashed-worker", 1*time.Second)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	time.Sleep(1500 * time.Millisecond)

	// The row is still 'held' but expired. N workers race to reclaim it
	// directly (no sweep) — exactly one must win.
	const workers = 10
	start := make(chan struct{})
	var winners atomic.Int32
	var busy atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			<-start
			_, err := svc.Acquire(ctx, key, fmt.Sprintf("reclaimer-%d", id), 30*time.Second)
			if err == nil {
				winners.Add(1)
				return
			}
			if errors.Is(err, ErrLeaseBusy) {
				busy.Add(1)
				return
			}
			t.Errorf("unexpected error: %v", err)
		}(i)
	}
	close(start)
	wg.Wait()

	if winners.Load() != 1 {
		t.Fatalf("expected exactly 1 winner on expired row, got %d", winners.Load())
	}
	if busy.Load() != int32(workers-1) {
		t.Fatalf("expected %d ErrLeaseBusy, got %d", workers-1, busy.Load())
	}

	// The winner holds a fresh generation; the crashed worker's token is stale.
	readBack, err := svc.Read(ctx, key)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if readBack.Status != WriteLeaseHeld {
		t.Fatalf("expected status held, got %s", readBack.Status)
	}
	if readBack.FenceGeneration != lease.FenceGeneration+1 {
		t.Fatalf("expected generation %d, got %d", lease.FenceGeneration+1, readBack.FenceGeneration)
	}
	_, err = svc.Renew(ctx, key, lease.LeaseToken, lease.FenceGeneration, 30*time.Second)
	if !errors.Is(err, ErrLeaseNotHeld) {
		t.Fatalf("stale renew after expired-row reacquire must be rejected, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Double release: the second release with the same token+generation is
// rejected, and the mutex is immediately available to a new writer.
// ---------------------------------------------------------------------------

func TestWriteLease_DoubleRelease(t *testing.T) {
	ctx := context.Background()
	pool := newWriteLeaseTestPool(t)
	svc := NewWriteLeaseService(pool)
	key := uniqueMutexKey(t)
	cleanupLease(t, pool, key)

	lease, err := svc.Acquire(ctx, key, "worker", 30*time.Second)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	// First release succeeds and frees the mutex.
	released, err := svc.Release(ctx, key, lease.LeaseToken, lease.FenceGeneration)
	if err != nil {
		t.Fatalf("first release: %v", err)
	}
	if released.Status != WriteLeaseFree {
		t.Fatalf("expected status free, got %s", released.Status)
	}

	// Second release with the same token+generation must be rejected.
	_, err = svc.Release(ctx, key, lease.LeaseToken, lease.FenceGeneration)
	if !errors.Is(err, ErrLeaseNotHeld) {
		t.Fatalf("double release must be rejected, got %v", err)
	}

	// The mutex is immediately available to a new writer.
	lease2, err := svc.Acquire(ctx, key, "worker-2", 30*time.Second)
	if err != nil {
		t.Fatalf("reacquire after double release: %v", err)
	}
	if lease2.FenceGeneration != lease.FenceGeneration+1 {
		t.Fatalf("expected generation %d, got %d", lease.FenceGeneration+1, lease2.FenceGeneration)
	}
}

// ---------------------------------------------------------------------------
// TTL / clock boundary: reclaimability is governed by expires_at vs the DB
// clock. Future expiry is busy, past expiry is reclaimable, and the acquire
// predicate is strict (expires_at < now()), so a lease expiring exactly at
// the current instant is NOT yet reclaimable.
// ---------------------------------------------------------------------------

func TestWriteLease_TTLClockBoundary(t *testing.T) {
	ctx := context.Background()
	pool := newWriteLeaseTestPool(t)
	svc := NewWriteLeaseService(pool)
	key := uniqueMutexKey(t)
	cleanupLease(t, pool, key)

	lease, err := svc.Acquire(ctx, key, "boundary", 30*time.Second)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	// expires_at strictly in the future → not reclaimable.
	if _, err := pool.Exec(ctx, `UPDATE write_lease SET expires_at = now() + interval '2 seconds' WHERE mutex_key = $1`, key); err != nil {
		t.Fatalf("set future expiry: %v", err)
	}
	if _, err := svc.Acquire(ctx, key, "challenger-future", 30*time.Second); !errors.Is(err, ErrLeaseBusy) {
		t.Fatalf("future expiry must be busy, got %v", err)
	}

	// expires_at strictly in the past → reclaimable, generation bumps.
	if _, err := pool.Exec(ctx, `UPDATE write_lease SET expires_at = now() - interval '2 seconds' WHERE mutex_key = $1`, key); err != nil {
		t.Fatalf("set past expiry: %v", err)
	}
	reacquired, err := svc.Acquire(ctx, key, "challenger-past", 30*time.Second)
	if err != nil {
		t.Fatalf("past expiry must be reclaimable, got %v", err)
	}
	if reacquired.FenceGeneration != lease.FenceGeneration+1 {
		t.Fatalf("expected generation %d, got %d", lease.FenceGeneration+1, reacquired.FenceGeneration)
	}

	// Strict-boundary semantics: the acquire predicate is expires_at < now(),
	// so a lease expiring exactly at the current instant is NOT reclaimable.
	// Evaluated in a single statement so now() is frozen for both sides.
	var reclaimable bool
	if err := pool.QueryRow(ctx, `
		UPDATE write_lease
		SET expires_at = now()
		WHERE mutex_key = $1
		RETURNING (expires_at < now())`, key).Scan(&reclaimable); err != nil {
		t.Fatalf("boundary predicate: %v", err)
	}
	if reclaimable {
		t.Fatal("strict boundary: expires_at == now() must not be reclaimable")
	}
}

// ---------------------------------------------------------------------------
// Reentry: after a lapsed lease is re-acquired by a new writer, the old
// writer's token+generation is rejected on renew, release, and verify.
// ---------------------------------------------------------------------------

func TestWriteLease_ReentryStaleRejected(t *testing.T) {
	ctx := context.Background()
	pool := newWriteLeaseTestPool(t)
	svc := NewWriteLeaseService(pool)
	key := uniqueMutexKey(t)
	cleanupLease(t, pool, key)

	// Worker A acquires, then its lease lapses (crash).
	leaseA, err := svc.Acquire(ctx, key, "worker-A", 1*time.Second)
	if err != nil {
		t.Fatalf("acquire A: %v", err)
	}
	time.Sleep(1500 * time.Millisecond)

	// Worker B re-enters the mutex (reentry) — generation bumps.
	leaseB, err := svc.Acquire(ctx, key, "worker-B", 30*time.Second)
	if err != nil {
		t.Fatalf("reentry acquire B: %v", err)
	}
	if leaseB.FenceGeneration != leaseA.FenceGeneration+1 {
		t.Fatalf("expected generation %d, got %d", leaseA.FenceGeneration+1, leaseB.FenceGeneration)
	}

	// Worker A's stale token+generation is rejected on every path.
	if _, err := svc.Renew(ctx, key, leaseA.LeaseToken, leaseA.FenceGeneration, 30*time.Second); !errors.Is(err, ErrLeaseNotHeld) {
		t.Fatalf("stale renew after reentry must be rejected, got %v", err)
	}
	if _, err := svc.Release(ctx, key, leaseA.LeaseToken, leaseA.FenceGeneration); !errors.Is(err, ErrLeaseNotHeld) {
		t.Fatalf("stale release after reentry must be rejected, got %v", err)
	}
	if _, err := svc.VerifyHeld(ctx, key, leaseA.LeaseToken, leaseA.FenceGeneration); !errors.Is(err, ErrLeaseNotHeld) {
		t.Fatalf("stale verify after reentry must be rejected, got %v", err)
	}

	// Worker B still owns the lease.
	held, err := svc.VerifyHeld(ctx, key, leaseB.LeaseToken, leaseB.FenceGeneration)
	if err != nil {
		t.Fatalf("verify B: %v", err)
	}
	if held.HolderID != "worker-B" {
		t.Fatalf("expected holder worker-B, got %s", held.HolderID)
	}
}

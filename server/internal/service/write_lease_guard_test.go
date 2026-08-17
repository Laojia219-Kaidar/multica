package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

type fakeWriterLeaseStore struct {
	mu sync.Mutex

	lease          *WriteLease
	nextGeneration int64
	acquireRow     *WriteLease
	acquireNil     bool
	renewRow       *WriteLease
	verifyRow      *WriteLease
	releaseRow     *WriteLease
	acquireErr     error
	renewErr       error
	verifyErr      error
	releaseErr     error
	blockRenew     chan struct{}
	renewStarted   chan struct{}
	blockVerify    chan struct{}
	verifyStarted  chan struct{}
	acquireCall    atomic.Int32
	renewCall      atomic.Int32
	verifyCall     atomic.Int32
	releaseCall    atomic.Int32
}

func futureLeaseExpiry() *time.Time {
	expires := time.Now().Add(time.Minute)
	return &expires
}

func (f *fakeWriterLeaseStore) Acquire(_ context.Context, key, holder string, _ time.Duration) (*WriteLease, error) {
	f.acquireCall.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.acquireErr != nil {
		if f.acquireRow != nil {
			f.lease = f.acquireRow
		}
		return f.acquireRow, f.acquireErr
	}
	if f.acquireNil {
		return nil, nil
	}
	if f.acquireRow != nil {
		f.lease = f.acquireRow
		return f.acquireRow, nil
	}
	if f.lease != nil && f.lease.Status == WriteLeaseHeld {
		return nil, ErrLeaseBusy
	}
	f.nextGeneration++
	f.lease = &WriteLease{
		MutexKey:        key,
		HolderID:        holder,
		LeaseToken:      uuid.New(),
		FenceGeneration: f.nextGeneration,
		Status:          WriteLeaseHeld,
		ExpiresAt:       futureLeaseExpiry(),
	}
	return f.lease, nil
}

func (f *fakeWriterLeaseStore) Renew(ctx context.Context, _ string, _ uuid.UUID, _ int64, _ time.Duration) (*WriteLease, error) {
	f.renewCall.Add(1)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.renewStarted != nil {
		select {
		case f.renewStarted <- struct{}{}:
		default:
		}
	}
	if f.blockRenew != nil {
		select {
		case <-f.blockRenew:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.renewErr != nil {
		return nil, f.renewErr
	}
	if f.renewRow != nil {
		return f.renewRow, nil
	}
	return f.lease, nil
}

func (f *fakeWriterLeaseStore) VerifyHeld(ctx context.Context, _ string, _ uuid.UUID, _ int64) (*WriteLease, error) {
	f.verifyCall.Add(1)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.verifyStarted != nil {
		select {
		case f.verifyStarted <- struct{}{}:
		default:
		}
	}
	if f.blockVerify != nil {
		select {
		case <-f.blockVerify:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.verifyErr != nil {
		return nil, f.verifyErr
	}
	if f.verifyRow != nil {
		return f.verifyRow, nil
	}
	return f.lease, nil
}

func (f *fakeWriterLeaseStore) Release(_ context.Context, _ string, token uuid.UUID, generation int64) (*WriteLease, error) {
	f.releaseCall.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.releaseErr != nil {
		return nil, f.releaseErr
	}
	if f.releaseRow != nil {
		return f.releaseRow, nil
	}
	if f.lease == nil || f.lease.Status != WriteLeaseHeld || f.lease.LeaseToken != token || f.lease.FenceGeneration != generation {
		return nil, ErrLeaseNotHeld
	}
	f.lease.Status = WriteLeaseFree
	f.lease.HolderID = ""
	f.lease.LeaseToken = uuid.Nil
	f.lease.ExpiresAt = nil
	return f.lease, nil
}

func newFakeWriterLeaseGuard(t *testing.T, store *fakeWriterLeaseStore) *WriterLeaseGuard {
	t.Helper()
	guard, err := NewWriterLeaseGuard(store, 50*time.Millisecond, 5*time.Millisecond)
	if err != nil {
		t.Fatalf("new guard: %v", err)
	}
	return guard
}

func acquireFakeWriterSession(t *testing.T, guard *WriterLeaseGuard, kind WriterLeaseTaskKind) *WriterLeaseSession {
	t.Helper()
	session, err := guard.AcquireForExecution(context.Background(), kind, "worktree:test", "task:test")
	if err != nil {
		t.Fatalf("acquire session: %v", err)
	}
	return session
}

func TestWriterLeaseGuard_AcquireBusyLeavesMutatorUntouched(t *testing.T) {
	store := &fakeWriterLeaseStore{acquireErr: ErrLeaseBusy}
	guard := newFakeWriterLeaseGuard(t, store)
	mutations := 0

	if _, err := guard.AcquireForExecution(context.Background(), WriterLeaseTaskKindWork, "worktree:test", "task:test"); !errors.Is(err, ErrLeaseBusy) {
		t.Fatalf("acquire error = %v, want ErrLeaseBusy", err)
	}
	if mutations != 0 {
		t.Fatalf("mutations = %d, want zero", mutations)
	}
}

func TestWriterLeaseSession_WithMutationStaleLeavesMutatorUntouched(t *testing.T) {
	store := &fakeWriterLeaseStore{verifyErr: ErrLeaseNotHeld}
	session := acquireFakeWriterSession(t, newFakeWriterLeaseGuard(t, store), WriterLeaseTaskKindWork)
	mutations := 0

	if err := session.WithMutation(context.Background(), func(context.Context) error {
		mutations++
		return nil
	}); !errors.Is(err, ErrLeaseNotHeld) {
		t.Fatalf("verify error = %v, want ErrLeaseNotHeld", err)
	}
	if mutations != 0 {
		t.Fatalf("mutations = %d, want zero", mutations)
	}
	if err := session.WithMutation(context.Background(), func(context.Context) error {
		mutations++
		return nil
	}); !errors.Is(err, ErrWriterLeaseStale) {
		t.Fatalf("second verify error = %v, want ErrWriterLeaseStale", err)
	}
}

func TestWriterLeaseSession_WithMutationVerifyFailureZeroCallbackAndSuccessOneCallback(t *testing.T) {
	store := &fakeWriterLeaseStore{verifyErr: ErrLeaseNotHeld}
	session := acquireFakeWriterSession(t, newFakeWriterLeaseGuard(t, store), WriterLeaseTaskKindWork)
	mutations := 0
	if err := session.WithMutation(context.Background(), func(context.Context) error {
		mutations++
		return nil
	}); !errors.Is(err, ErrLeaseNotHeld) {
		t.Fatalf("failed mutation error = %v, want ErrLeaseNotHeld", err)
	}
	if mutations != 0 {
		t.Fatalf("failed mutation callbacks = %d, want zero", mutations)
	}

	store = &fakeWriterLeaseStore{}
	session = acquireFakeWriterSession(t, newFakeWriterLeaseGuard(t, store), WriterLeaseTaskKindWork)
	if err := session.WithMutation(context.Background(), func(context.Context) error {
		mutations++
		return nil
	}); err != nil {
		t.Fatalf("successful mutation: %v", err)
	}
	if mutations != 1 {
		t.Fatalf("successful mutation callbacks = %d, want one", mutations)
	}
}

func TestWriterLeaseSession_HeartbeatFailureCancelsAndUsesBufferedFailure(t *testing.T) {
	store := &fakeWriterLeaseStore{renewErr: ErrLeaseNotHeld}
	guard := newFakeWriterLeaseGuard(t, store)
	session := acquireFakeWriterSession(t, guard, WriterLeaseTaskKindWork)
	mutations := 0
	executionCtx, cancelExecution := context.WithCancel(context.Background())
	defer cancelExecution()
	stop, failures, err := session.StartHeartbeat(executionCtx, cancelExecution)
	if err != nil {
		t.Fatalf("start heartbeat: %v", err)
	}
	defer stop()

	select {
	case <-executionCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("heartbeat failure did not cancel execution")
	}
	select {
	case err := <-failures:
		if !errors.Is(err, ErrLeaseNotHeld) {
			t.Fatalf("failure = %v, want ErrLeaseNotHeld", err)
		}
	case <-time.After(time.Second):
		t.Fatal("heartbeat failure was not delivered")
	}
	if err := session.WithMutation(context.Background(), func(context.Context) error {
		mutations++
		return nil
	}); !errors.Is(err, ErrWriterLeaseStale) {
		t.Fatalf("verify after failed heartbeat = %v, want ErrWriterLeaseStale", err)
	}
	if mutations != 0 {
		t.Fatalf("mutation callbacks after failed heartbeat = %d, want zero", mutations)
	}
}

func TestWriterLeaseSession_HeartbeatTimeoutIsBoundedAndCancels(t *testing.T) {
	store := &fakeWriterLeaseStore{blockRenew: make(chan struct{}), renewStarted: make(chan struct{}, 1)}
	guard := newFakeWriterLeaseGuard(t, store)
	session := acquireFakeWriterSession(t, guard, WriterLeaseTaskKindWork)
	executionCtx, cancelExecution := context.WithCancel(context.Background())
	defer cancelExecution()
	stop, failures, err := session.StartHeartbeat(executionCtx, cancelExecution)
	if err != nil {
		t.Fatalf("start heartbeat: %v", err)
	}
	defer stop()
	select {
	case <-store.renewStarted:
	case <-time.After(time.Second):
		t.Fatal("heartbeat did not begin")
	}
	select {
	case <-executionCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("bounded heartbeat timeout did not cancel execution")
	}
	select {
	case err := <-failures:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("failure = %v, want context deadline", err)
		}
	case <-time.After(time.Second):
		t.Fatal("bounded heartbeat timeout was not delivered")
	}
}

func TestWriterLeaseSession_HeartbeatRenewsWithoutChangingGeneration(t *testing.T) {
	store := &fakeWriterLeaseStore{}
	session := acquireFakeWriterSession(t, newFakeWriterLeaseGuard(t, store), WriterLeaseTaskKindWork)
	if err := session.Heartbeat(context.Background()); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if got := store.renewCall.Load(); got != 1 {
		t.Fatalf("renew calls = %d, want 1", got)
	}
	if err := session.WithMutation(context.Background(), func(context.Context) error { return nil }); err != nil {
		t.Fatalf("verify after heartbeat: %v", err)
	}
}

func TestWriterLeaseSession_CanceledHeartbeatMarksStaleBeforeNextMutation(t *testing.T) {
	store := &fakeWriterLeaseStore{}
	session := acquireFakeWriterSession(t, newFakeWriterLeaseGuard(t, store), WriterLeaseTaskKindWork)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := session.Heartbeat(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled heartbeat = %v, want context.Canceled", err)
	}
	mutations := 0
	if err := session.WithMutation(context.Background(), func(context.Context) error {
		mutations++
		return nil
	}); !errors.Is(err, ErrWriterLeaseStale) {
		t.Fatalf("mutation after canceled heartbeat = %v, want ErrWriterLeaseStale", err)
	}
	if mutations != 0 {
		t.Fatalf("mutation callbacks after canceled heartbeat = %d, want zero", mutations)
	}
}

func TestWriterLeaseSession_StartHeartbeatRenewsUntilStopped(t *testing.T) {
	store := &fakeWriterLeaseStore{}
	guard := newFakeWriterLeaseGuard(t, store)
	session := acquireFakeWriterSession(t, guard, WriterLeaseTaskKindWork)
	stop, failures, err := session.StartHeartbeat(context.Background(), nil)
	if err != nil {
		t.Fatalf("start heartbeat: %v", err)
	}
	deadline := time.After(time.Second)
	for store.renewCall.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("heartbeat loop did not renew")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	stop()
	if _, ok := <-failures; ok {
		t.Fatal("normal stop delivered a failure")
	}
	got := store.renewCall.Load()
	time.Sleep(20 * time.Millisecond)
	if store.renewCall.Load() != got {
		t.Fatalf("renew calls after stop = %d, want %d", store.renewCall.Load(), got)
	}
	if err := session.WithMutation(context.Background(), func(context.Context) error { return nil }); !errors.Is(err, ErrWriterLeaseStale) {
		t.Fatalf("mutation after stop = %v, want ErrWriterLeaseStale", err)
	}
}

func TestWriterLeaseSession_StartHeartbeatDuplicateAndContextCancel(t *testing.T) {
	store := &fakeWriterLeaseStore{}
	guard := newFakeWriterLeaseGuard(t, store)
	session := acquireFakeWriterSession(t, guard, WriterLeaseTaskKindWork)
	ctx, cancel := context.WithCancel(context.Background())
	stop, failures, err := session.StartHeartbeat(ctx, nil)
	if err != nil {
		t.Fatalf("start heartbeat: %v", err)
	}
	if _, _, err := session.StartHeartbeat(context.Background(), nil); !errors.Is(err, ErrWriterLeaseHeartbeatActive) {
		t.Fatalf("duplicate start = %v, want ErrWriterLeaseHeartbeatActive", err)
	}
	cancel()
	stop()
	if _, ok := <-failures; ok {
		t.Fatal("context cancellation delivered a heartbeat failure")
	}
	if err := session.WithMutation(context.Background(), func(context.Context) error { return nil }); !errors.Is(err, ErrWriterLeaseStale) {
		t.Fatalf("mutation after parent cancel = %v, want ErrWriterLeaseStale", err)
	}
}

func TestWriterLeaseSession_ReleaseCancelsBlockedHeartbeatBeforeWaiting(t *testing.T) {
	store := &fakeWriterLeaseStore{blockRenew: make(chan struct{}), renewStarted: make(chan struct{}, 1)}
	guard := newFakeWriterLeaseGuard(t, store)
	session := acquireFakeWriterSession(t, guard, WriterLeaseTaskKindWork)
	stop, _, err := session.StartHeartbeat(context.Background(), nil)
	if err != nil {
		t.Fatalf("start heartbeat: %v", err)
	}
	select {
	case <-store.renewStarted:
	case <-time.After(time.Second):
		t.Fatal("heartbeat did not begin")
	}
	done := make(chan error, 1)
	go func() { done <- session.ReleaseAfterTerminal(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("release: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("release deadlocked behind blocked heartbeat")
	}
	stop()
}

func TestWriterLeaseSession_MutationAndReleaseAreSerialized(t *testing.T) {
	store := &fakeWriterLeaseStore{verifyStarted: make(chan struct{}, 1), blockVerify: make(chan struct{})}
	session := acquireFakeWriterSession(t, newFakeWriterLeaseGuard(t, store), WriterLeaseTaskKindWork)
	verifyDone := make(chan error, 1)
	go func() {
		verifyDone <- session.WithMutation(context.Background(), func(context.Context) error { return nil })
	}()
	select {
	case <-store.verifyStarted:
	case <-time.After(time.Second):
		t.Fatal("verify did not begin")
	}
	releaseDone := make(chan error, 1)
	go func() { releaseDone <- session.ReleaseAfterTerminal(context.Background()) }()
	time.Sleep(10 * time.Millisecond)
	if got := store.releaseCall.Load(); got != 0 {
		t.Fatalf("release calls while verify blocked = %d, want zero", got)
	}
	close(store.blockVerify)
	if err := <-verifyDone; err != nil {
		t.Fatalf("verify: %v", err)
	}
	if err := <-releaseDone; err != nil {
		t.Fatalf("release: %v", err)
	}
}

func TestWriterLeaseSession_LongMutationAllowsHeartbeatAndReleaseWaits(t *testing.T) {
	store := &fakeWriterLeaseStore{}
	session := acquireFakeWriterSession(t, newFakeWriterLeaseGuard(t, store), WriterLeaseTaskKindWork)
	mutationStarted := make(chan struct{})
	allowMutation := make(chan struct{})
	mutationDone := make(chan error, 1)
	go func() {
		mutationDone <- session.WithMutation(context.Background(), func(context.Context) error {
			close(mutationStarted)
			<-allowMutation
			return nil
		})
	}()
	select {
	case <-mutationStarted:
	case <-time.After(time.Second):
		t.Fatal("mutation did not begin")
	}

	heartbeatDone := make(chan error, 1)
	go func() { heartbeatDone <- session.Heartbeat(context.Background()) }()
	select {
	case err := <-heartbeatDone:
		if err != nil {
			t.Fatalf("heartbeat during mutation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("heartbeat blocked behind long mutation")
	}

	releaseDone := make(chan error, 1)
	go func() { releaseDone <- session.ReleaseAfterTerminal(context.Background()) }()
	time.Sleep(10 * time.Millisecond)
	if got := store.releaseCall.Load(); got != 0 {
		t.Fatalf("release calls during mutation = %d, want zero", got)
	}
	close(allowMutation)
	if err := <-mutationDone; err != nil {
		t.Fatalf("mutation: %v", err)
	}
	if err := <-releaseDone; err != nil {
		t.Fatalf("release: %v", err)
	}
}

func TestWriterLeaseSession_ReleaseAfterTerminalIsIdempotent(t *testing.T) {
	store := &fakeWriterLeaseStore{releaseErr: ErrLeaseNotHeld}
	session := acquireFakeWriterSession(t, newFakeWriterLeaseGuard(t, store), WriterLeaseTaskKindRepair)
	if err := session.ReleaseAfterTerminal(context.Background()); err != nil {
		t.Fatalf("release with already-terminal store: %v", err)
	}
	if err := session.ReleaseAfterTerminal(context.Background()); err != nil {
		t.Fatalf("second release: %v", err)
	}
	if got := store.releaseCall.Load(); got != 1 {
		t.Fatalf("release calls = %d, want 1", got)
	}
}

func TestWriterLeaseGuard_ReviewBypassesLease(t *testing.T) {
	store := &fakeWriterLeaseStore{}
	guard := newFakeWriterLeaseGuard(t, store)
	session, err := guard.AcquireForExecution(context.Background(), WriterLeaseTaskKindReview, "", "")
	if err != nil {
		t.Fatalf("review acquire: %v", err)
	}
	if !session.ReadOnly() {
		t.Fatal("review session is not read-only")
	}
	if store.acquireCall.Load() != 0 {
		t.Fatalf("acquire calls = %d, want zero", store.acquireCall.Load())
	}
	mutations := 0
	if err := session.WithMutation(context.Background(), func(context.Context) error {
		mutations++
		return nil
	}); !errors.Is(err, ErrWriterLeaseReadOnly) {
		t.Fatalf("review mutation = %v, want ErrWriterLeaseReadOnly", err)
	}
	if mutations != 0 {
		t.Fatalf("review mutations = %d, want zero", mutations)
	}
	if err := session.ReleaseAfterTerminal(context.Background()); err != nil {
		t.Fatalf("review release: %v", err)
	}
}

func TestWriterLeaseGuard_UnknownKindAndBlankIdentityFailClosed(t *testing.T) {
	store := &fakeWriterLeaseStore{}
	guard := newFakeWriterLeaseGuard(t, store)
	if _, err := guard.AcquireForExecution(context.Background(), WriterLeaseTaskKind("unknown"), "worktree:test", "task:test"); !errors.Is(err, ErrWriterLeaseUnknownTaskKind) {
		t.Fatalf("unknown kind error = %v, want ErrWriterLeaseUnknownTaskKind", err)
	}
	if _, err := guard.AcquireForExecution(context.Background(), WriterLeaseTaskKindWork, "  ", "task:test"); err == nil {
		t.Fatal("blank key unexpectedly acquired")
	}
	if _, err := guard.AcquireForExecution(context.Background(), WriterLeaseTaskKindWork, "worktree:test", "  "); err == nil {
		t.Fatal("blank holder unexpectedly acquired")
	}
	if store.acquireCall.Load() != 0 {
		t.Fatalf("acquire calls = %d, want zero", store.acquireCall.Load())
	}
}

func TestWriterLeaseGuard_InvalidAcquireRowsFailClosedAndCleanupSafeRows(t *testing.T) {
	now := time.Now()
	validToken := uuid.New()
	cases := []struct {
		name string
		row  *WriteLease
		nil  bool
		want error
	}{
		{name: "nil row", nil: true, want: ErrWriterLeaseRecoveryRequired},
		{name: "wrong key", row: &WriteLease{MutexKey: "other", HolderID: "task:test", LeaseToken: validToken, FenceGeneration: 1, Status: WriteLeaseHeld, ExpiresAt: futureLeaseExpiry()}, want: ErrWriterLeaseRecoveryRequired},
		{name: "wrong holder", row: &WriteLease{MutexKey: "worktree:test", HolderID: "other", LeaseToken: validToken, FenceGeneration: 1, Status: WriteLeaseHeld, ExpiresAt: futureLeaseExpiry()}, want: ErrWriterLeaseRecoveryRequired},
		{name: "nil token", row: &WriteLease{MutexKey: "worktree:test", HolderID: "task:test", FenceGeneration: 1, Status: WriteLeaseHeld, ExpiresAt: futureLeaseExpiry()}, want: ErrWriterLeaseRecoveryRequired},
		{name: "zero generation", row: &WriteLease{MutexKey: "worktree:test", HolderID: "task:test", LeaseToken: validToken, Status: WriteLeaseHeld, ExpiresAt: futureLeaseExpiry()}, want: ErrWriterLeaseRecoveryRequired},
		{name: "wrong status", row: &WriteLease{MutexKey: "worktree:test", HolderID: "task:test", LeaseToken: validToken, FenceGeneration: 1, Status: WriteLeaseFree, ExpiresAt: futureLeaseExpiry()}, want: ErrWriterLeaseRecoveryRequired},
		{name: "nil expiry", row: &WriteLease{MutexKey: "worktree:test", HolderID: "task:test", LeaseToken: validToken, FenceGeneration: 1, Status: WriteLeaseHeld}, want: ErrWriterLeaseInvalidRow},
		{name: "expired", row: &WriteLease{MutexKey: "worktree:test", HolderID: "task:test", LeaseToken: validToken, FenceGeneration: 1, Status: WriteLeaseHeld, ExpiresAt: &now}, want: ErrWriterLeaseInvalidRow},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeWriterLeaseStore{acquireRow: tc.row, acquireNil: tc.nil}
			guard := newFakeWriterLeaseGuard(t, store)
			if _, err := guard.AcquireForExecution(context.Background(), WriterLeaseTaskKindWork, "worktree:test", "task:test"); !errors.Is(err, tc.want) {
				t.Fatalf("acquire error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestWriterLeaseGuard_AmbiguousAcquireRequiresRecoveryUnlessExactCleanupSucceeds(t *testing.T) {
	const secret = "backend token=secret"
	exact := &WriteLease{MutexKey: "worktree:test", HolderID: "task:test", LeaseToken: uuid.New(), FenceGeneration: 7, Status: WriteLeaseHeld, ExpiresAt: futureLeaseExpiry()}
	store := &fakeWriterLeaseStore{acquireRow: exact, acquireErr: errors.New(secret)}
	guard := newFakeWriterLeaseGuard(t, store)
	if _, err := guard.AcquireForExecution(context.Background(), WriterLeaseTaskKindWork, "worktree:test", "task:test"); err == nil || errors.Is(err, ErrWriterLeaseRecoveryRequired) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("exact cleanup error = %v, want sanitized original error", err)
	}
	if got := store.releaseCall.Load(); got != 1 {
		t.Fatalf("cleanup release calls = %d, want 1", got)
	}

	store = &fakeWriterLeaseStore{acquireErr: fmt.Errorf("ambiguous backend error")}
	guard = newFakeWriterLeaseGuard(t, store)
	if _, err := guard.AcquireForExecution(context.Background(), WriterLeaseTaskKindWork, "worktree:test", "task:test"); !errors.Is(err, ErrWriterLeaseRecoveryRequired) {
		t.Fatalf("nil-row acquire error = %v, want recovery", err)
	}
	store = &fakeWriterLeaseStore{acquireErr: ErrLeaseBusy, acquireRow: &WriteLease{MutexKey: "other", HolderID: "task:test", LeaseToken: uuid.New(), FenceGeneration: 1, Status: WriteLeaseHeld, ExpiresAt: futureLeaseExpiry()}}
	guard = newFakeWriterLeaseGuard(t, store)
	if _, err := guard.AcquireForExecution(context.Background(), WriterLeaseTaskKindWork, "worktree:test", "task:test"); !errors.Is(err, ErrWriterLeaseRecoveryRequired) {
		t.Fatalf("wrong-row busy error = %v, want recovery", err)
	}
	if got := store.releaseCall.Load(); got != 0 {
		t.Fatalf("busy-row cleanup release calls = %d, want zero", got)
	}

	store = &fakeWriterLeaseStore{acquireErr: ErrLeaseBusy, acquireRow: &WriteLease{MutexKey: "worktree:test", HolderID: "task:test", LeaseToken: uuid.New(), FenceGeneration: 8, Status: WriteLeaseHeld, ExpiresAt: futureLeaseExpiry()}}
	guard = newFakeWriterLeaseGuard(t, store)
	if _, err := guard.AcquireForExecution(context.Background(), WriterLeaseTaskKindWork, "worktree:test", "task:test"); !errors.Is(err, ErrWriterLeaseRecoveryRequired) {
		t.Fatalf("exact-row busy error = %v, want recovery", err)
	}
	if got := store.releaseCall.Load(); got != 0 {
		t.Fatalf("exact busy-row cleanup release calls = %d, want zero", got)
	}
}

func TestWriterLeaseGuard_CanceledAcquireStillCleansExactAmbiguousRow(t *testing.T) {
	exact := &WriteLease{MutexKey: "worktree:test", HolderID: "task:test", LeaseToken: uuid.New(), FenceGeneration: 9, Status: WriteLeaseHeld, ExpiresAt: futureLeaseExpiry()}
	store := &fakeWriterLeaseStore{acquireRow: exact, acquireErr: errors.New("ambiguous backend error")}
	guard := newFakeWriterLeaseGuard(t, store)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := guard.AcquireForExecution(ctx, WriterLeaseTaskKindWork, "worktree:test", "task:test"); err == nil || errors.Is(err, ErrWriterLeaseRecoveryRequired) {
		t.Fatalf("canceled exact acquire error = %v, want sanitized original error", err)
	}
	if got := store.releaseCall.Load(); got != 1 {
		t.Fatalf("canceled exact cleanup release calls = %d, want 1", got)
	}
}

func TestWriterLeaseSession_RenewVerifyReleaseValidateRows(t *testing.T) {
	store := &fakeWriterLeaseStore{}
	session := acquireFakeWriterSession(t, newFakeWriterLeaseGuard(t, store), WriterLeaseTaskKindWork)
	badRenew := *store.lease
	badRenew.HolderID = "other"
	store.renewRow = &badRenew
	if err := session.Heartbeat(context.Background()); !errors.Is(err, ErrWriterLeaseInvalidRow) {
		t.Fatalf("invalid renew row = %v, want ErrWriterLeaseInvalidRow", err)
	}

	store = &fakeWriterLeaseStore{}
	session = acquireFakeWriterSession(t, newFakeWriterLeaseGuard(t, store), WriterLeaseTaskKindWork)
	badVerify := *store.lease
	badVerify.MutexKey = "other"
	store.verifyRow = &badVerify
	if err := session.WithMutation(context.Background(), func(context.Context) error { return nil }); !errors.Is(err, ErrWriterLeaseInvalidRow) {
		t.Fatalf("invalid verify row = %v, want ErrWriterLeaseInvalidRow", err)
	}

	store = &fakeWriterLeaseStore{}
	session = acquireFakeWriterSession(t, newFakeWriterLeaseGuard(t, store), WriterLeaseTaskKindWork)
	store.releaseRow = &WriteLease{MutexKey: "other", FenceGeneration: session.generation, Status: WriteLeaseFree}
	if err := session.ReleaseAfterTerminal(context.Background()); !errors.Is(err, ErrWriterLeaseInvalidRow) {
		t.Fatalf("invalid release row = %v, want ErrWriterLeaseInvalidRow", err)
	}
}

func TestWriterLeaseGuard_UnknownStoreErrorsAreSanitized(t *testing.T) {
	const secret = "token=super-secret"
	store := &fakeWriterLeaseStore{acquireErr: fmt.Errorf("backend leaked %s", secret)}
	guard := newFakeWriterLeaseGuard(t, store)
	if _, err := guard.AcquireForExecution(context.Background(), WriterLeaseTaskKindWork, "worktree:test", "task:test"); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("acquire error = %v, expected sanitized non-secret error", err)
	}

	store = &fakeWriterLeaseStore{renewErr: fmt.Errorf("backend leaked %s", secret)}
	session := acquireFakeWriterSession(t, newFakeWriterLeaseGuard(t, store), WriterLeaseTaskKindWork)
	if err := session.Heartbeat(context.Background()); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("renew error = %v, expected sanitized non-secret error", err)
	}

	store = &fakeWriterLeaseStore{verifyErr: fmt.Errorf("backend leaked %s", secret)}
	session = acquireFakeWriterSession(t, newFakeWriterLeaseGuard(t, store), WriterLeaseTaskKindWork)
	if err := session.WithMutation(context.Background(), func(context.Context) error { return nil }); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("verify error = %v, expected sanitized non-secret error", err)
	}

	store = &fakeWriterLeaseStore{releaseErr: fmt.Errorf("backend leaked %s", secret)}
	session = acquireFakeWriterSession(t, newFakeWriterLeaseGuard(t, store), WriterLeaseTaskKindWork)
	if err := session.ReleaseAfterTerminal(context.Background()); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("release error = %v, expected sanitized non-secret error", err)
	}
}

func TestWriterLeaseSession_WhitespaceIdentityIsTrimmed(t *testing.T) {
	store := &fakeWriterLeaseStore{}
	guard := newFakeWriterLeaseGuard(t, store)
	session, err := guard.AcquireForExecution(context.Background(), WriterLeaseTaskKindWork, " worktree:test ", " task:test ")
	if err != nil {
		t.Fatalf("trimmed acquire: %v", err)
	}
	if session.mutexKey != "worktree:test" || session.holderID != "task:test" {
		t.Fatalf("identity = %q/%q, want trimmed values", session.mutexKey, session.holderID)
	}
}

func TestWriterLeaseSession_StaleReleaseCannotAffectNewGeneration(t *testing.T) {
	store := &fakeWriterLeaseStore{}
	guard := newFakeWriterLeaseGuard(t, store)
	old := acquireFakeWriterSession(t, guard, WriterLeaseTaskKindWork)
	oldToken, oldGeneration := old.leaseToken, old.generation
	if err := old.ReleaseAfterTerminal(context.Background()); err != nil {
		t.Fatalf("old release: %v", err)
	}
	newLease, err := store.Acquire(context.Background(), "worktree:test", "task:new", time.Second)
	if err != nil {
		t.Fatalf("new acquire: %v", err)
	}
	if newLease.FenceGeneration != oldGeneration+1 {
		t.Fatalf("fake store generation = %d, want %d", newLease.FenceGeneration, oldGeneration+1)
	}
	if _, err := store.Release(context.Background(), "worktree:test", oldToken, oldGeneration); !errors.Is(err, ErrLeaseNotHeld) {
		t.Fatalf("stale release = %v, want ErrLeaseNotHeld", err)
	}
	if newLease.Status != WriteLeaseHeld {
		t.Fatalf("new lease status = %s, want held", newLease.Status)
	}
}

func TestWriterLeaseGuard_ConcurrentAcquireHasOneWinner(t *testing.T) {
	store := &fakeWriterLeaseStore{}
	guard := newFakeWriterLeaseGuard(t, store)
	const workers = 16
	start := make(chan struct{})
	var wg sync.WaitGroup
	var winners atomic.Int32
	var busy atomic.Int32
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := guard.AcquireForExecution(context.Background(), WriterLeaseTaskKindWork, "worktree:test", "task:test")
			switch {
			case err == nil:
				winners.Add(1)
			case errors.Is(err, ErrLeaseBusy):
				busy.Add(1)
			default:
				t.Errorf("unexpected acquire error: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if winners.Load() != 1 || busy.Load() != workers-1 {
		t.Fatalf("winners=%d busy=%d, want winners=1 busy=%d", winners.Load(), busy.Load(), workers-1)
	}
}

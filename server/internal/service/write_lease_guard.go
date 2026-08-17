package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// WriterLeaseTaskKind identifies whether an execution may mutate a protected
// worktree. The future server resolver must supply this value; this guard does
// not infer task authority from caller-provided text.
type WriterLeaseTaskKind string

const (
	WriterLeaseTaskKindWork   WriterLeaseTaskKind = "work"
	WriterLeaseTaskKindRepair WriterLeaseTaskKind = "repair"
	WriterLeaseTaskKindReview WriterLeaseTaskKind = "review"
)

const maxWriterLeaseOperationTimeout = 5 * time.Second

var (
	ErrWriterLeaseUnknownTaskKind  = errors.New("writer lease: unknown task kind")
	ErrWriterLeaseReadOnly         = errors.New("writer lease: read-only task does not hold a writer lease")
	ErrWriterLeaseReleased         = errors.New("writer lease: session already released")
	ErrWriterLeaseStale            = errors.New("writer lease: session is stale")
	ErrWriterLeaseClosing          = errors.New("writer lease: session is closing")
	ErrWriterLeaseHeartbeatActive  = errors.New("writer lease: heartbeat already started")
	ErrWriterLeaseInvalidRow       = errors.New("writer lease: store returned an invalid lease row")
	ErrWriterLeaseRecoveryRequired = errors.New("writer lease: lease acquisition requires recovery")
)

// WriterLeaseStore is the transport-neutral seam over migration 262's lease
// primitive. WriteLeaseService satisfies this interface without an adapter.
type WriterLeaseStore interface {
	Acquire(context.Context, string, string, time.Duration) (*WriteLease, error)
	Renew(context.Context, string, uuid.UUID, int64, time.Duration) (*WriteLease, error)
	VerifyHeld(context.Context, string, uuid.UUID, int64) (*WriteLease, error)
	Release(context.Context, string, uuid.UUID, int64) (*WriteLease, error)
}

// WriterLeaseTerminalProof is the daemon-private terminal fence evidence.
// The server intentionally recomputes the mutex key and holder from the task,
// runtime, and project resource relationship; neither value crosses this
// boundary.
type WriterLeaseTerminalProof struct {
	ResourceID       uuid.UUID `json:"resource_id"`
	LeaseToken       uuid.UUID `json:"-"` // in-process legacy compatibility only
	LeaseTokenSHA256 string    `json:"lease_token_sha256,omitempty"`
	FenceGeneration  int64     `json:"fence_generation"`
}

// WriterLeaseGuard owns policy around one execution lease. It deliberately
// does not know about HTTP, tasks, queues, repositories, or agent processes.
// mutexKey and holderID must come from a future server-side resolver; this
// guard only trims and rejects blank values and does not claim canonical-key
// authority.
type WriterLeaseGuard struct {
	store             WriterLeaseStore
	ttl               time.Duration
	heartbeatInterval time.Duration
	operationTimeout  time.Duration
}

// NewWriterLeaseGuard creates a guard backed by a 262-compatible store.
// DefaultLeaseDuration is used when ttl is non-positive. A non-positive
// heartbeat interval is rejected because silently disabling fencing would be
// unsafe for a writer session. Store operations always receive a bounded
// child context.
func NewWriterLeaseGuard(store WriterLeaseStore, ttl, heartbeatInterval time.Duration) (*WriterLeaseGuard, error) {
	if store == nil {
		return nil, errors.New("writer lease: store is required")
	}
	if ttl <= 0 {
		ttl = DefaultLeaseDuration
	}
	if heartbeatInterval <= 0 {
		return nil, errors.New("writer lease: heartbeat interval must be positive")
	}
	if heartbeatInterval >= ttl {
		return nil, fmt.Errorf("writer lease: heartbeat interval %s must be shorter than ttl %s", heartbeatInterval, ttl)
	}
	operationTimeout := ttl / 3
	if operationTimeout > maxWriterLeaseOperationTimeout {
		operationTimeout = maxWriterLeaseOperationTimeout
	}
	if operationTimeout <= 0 {
		operationTimeout = time.Millisecond
	}
	return &WriterLeaseGuard{
		store:             store,
		ttl:               ttl,
		heartbeatInterval: heartbeatInterval,
		operationTimeout:  operationTimeout,
	}, nil
}

// AcquireForExecution acquires a lease for work/repair and returns a read-only
// session for review. Unknown kinds fail closed. A failed acquire returns no
// session, so callers have no lease with which to enter a mutation boundary.
// The key and holder are expected to be server-resolved, not caller self-report.
func (g *WriterLeaseGuard) AcquireForExecution(ctx context.Context, kind WriterLeaseTaskKind, mutexKey, holderID string) (*WriterLeaseSession, error) {
	if g == nil || g.store == nil {
		return nil, errors.New("writer lease: guard is not configured")
	}
	switch kind {
	case WriterLeaseTaskKindReview:
		return &WriterLeaseSession{guard: g, kind: kind, readOnly: true, state: writerLeaseSessionActive}, nil
	case WriterLeaseTaskKindWork, WriterLeaseTaskKindRepair:
		mutexKey = strings.TrimSpace(mutexKey)
		holderID = strings.TrimSpace(holderID)
		if mutexKey == "" {
			return nil, errors.New("writer lease: mutex key is required")
		}
		if holderID == "" {
			return nil, errors.New("writer lease: holder id is required")
		}
		ctx = nonNilWriterLeaseContext(ctx)
		opCtx, cancel := g.operationContext(ctx)
		lease, err := g.store.Acquire(opCtx, mutexKey, holderID, g.ttl)
		cancel()
		if err != nil {
			if errors.Is(err, ErrLeaseBusy) {
				if lease == nil {
					return nil, ErrLeaseBusy
				}
				return nil, ErrWriterLeaseRecoveryRequired
			}
			safe, cleanupErr := g.bestEffortAcquireCleanup(lease, mutexKey, holderID)
			if cleanupErr != nil || !safe {
				return nil, ErrWriterLeaseRecoveryRequired
			}
			return nil, sanitizeWriterLeaseError("acquire", err)
		}
		if err := validateAcquiredLease(lease, mutexKey, holderID, time.Now()); err != nil {
			safe, cleanupErr := g.bestEffortAcquireCleanup(lease, mutexKey, holderID)
			if cleanupErr != nil || !safe {
				return nil, ErrWriterLeaseRecoveryRequired
			}
			return nil, ErrWriterLeaseInvalidRow
		}
		return &WriterLeaseSession{
			guard:      g,
			kind:       kind,
			mutexKey:   mutexKey,
			holderID:   holderID,
			leaseToken: lease.LeaseToken,
			generation: lease.FenceGeneration,
			state:      writerLeaseSessionActive,
		}, nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrWriterLeaseUnknownTaskKind, kind)
	}
}

// WriterLeaseSession is the private token+generation handle for one execution.
// The token is never included in errors or channels. Its only external use is
// through the store methods invoked by this session.
type WriterLeaseSession struct {
	guard      *WriterLeaseGuard
	kind       WriterLeaseTaskKind
	readOnly   bool
	mutexKey   string
	holderID   string
	leaseToken uuid.UUID
	generation int64

	stateMu       sync.Mutex
	useMu         sync.RWMutex
	releaseMu     sync.Mutex
	state         writerLeaseSessionState
	heartbeatStop context.CancelFunc
	heartbeatDone chan struct{}
}

// TerminalProof returns only the token and fencing generation needed by the
// server's atomic completion check. The daemon associates the resource id
// with this proof from its server-provided target bundle.
func (s *WriterLeaseSession) TerminalProof() (uuid.UUID, int64, error) {
	if s == nil {
		return uuid.Nil, 0, ErrWriterLeaseStale
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if err := s.requireActiveLocked(); err != nil {
		return uuid.Nil, 0, err
	}
	return s.leaseToken, s.generation, nil
}

type writerLeaseSessionState uint8

const (
	writerLeaseSessionActive writerLeaseSessionState = iota + 1
	writerLeaseSessionStale
	writerLeaseSessionClosing
	writerLeaseSessionReleased
)

// ReadOnly reports that this session represents a review task and acquired no
// writer lease.
func (s *WriterLeaseSession) ReadOnly() bool {
	return s != nil && s.readOnly
}

// Stale reports whether fencing has been lost or terminal release has begun.
// It is read-only and does not expose the private token or generation.
func (s *WriterLeaseSession) Stale() bool {
	if s == nil {
		return true
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.state == writerLeaseSessionStale || s.state == writerLeaseSessionClosing || s.state == writerLeaseSessionReleased
}

// WithMutation verifies the lease and invokes fn exactly once only when the
// concurrent release cannot invalidate the guarded mutation between verify
// and callback entry. fn receives the execution context, so it must honor
// cancellation (including cancellation caused by heartbeat failure) and must
// not re-enter this session.
func (s *WriterLeaseSession) WithMutation(ctx context.Context, fn func(context.Context) error) error {
	if s == nil {
		return errors.New("writer lease: nil session")
	}
	if s.readOnly {
		return ErrWriterLeaseReadOnly
	}
	if fn == nil {
		return errors.New("writer lease: mutation callback is required")
	}
	ctx = nonNilWriterLeaseContext(ctx)
	releaseUse, err := s.beginUse()
	if err != nil {
		return err
	}
	defer releaseUse()
	opCtx, cancel := s.guard.operationContext(ctx)
	defer cancel()
	if err := s.verifyLocked(ctx, opCtx); err != nil {
		return err
	}
	return fn(ctx)
}

// Heartbeat explicitly renews the current lease. The 262 service keeps the
// same fence generation while extending expiry.
func (s *WriterLeaseSession) Heartbeat(ctx context.Context) error {
	if s == nil {
		return errors.New("writer lease: nil session")
	}
	if s.readOnly {
		return ErrWriterLeaseReadOnly
	}
	ctx = nonNilWriterLeaseContext(ctx)
	releaseUse, err := s.beginUse()
	if err != nil {
		return err
	}
	defer releaseUse()
	opCtx, cancel := s.guard.operationContext(ctx)
	defer cancel()
	row, err := s.guard.store.Renew(opCtx, s.mutexKey, s.leaseToken, s.generation, s.guard.ttl)
	if err != nil {
		return s.recordStoreFailureLocked("renew", err)
	}
	if err := validateOwnedHeldLease(row, s.mutexKey, s.holderID, s.leaseToken, s.generation, time.Now()); err != nil {
		s.stateMu.Lock()
		s.state = writerLeaseSessionStale
		s.stateMu.Unlock()
		return err
	}
	return nil
}

// Renew is an explicit synonym for Heartbeat for callers that use the 262
// service vocabulary.
func (s *WriterLeaseSession) Renew(ctx context.Context) error {
	return s.Heartbeat(ctx)
}

// StartHeartbeat starts a cancellable renew loop and returns a buffered failure
// channel. The execution cancel is invoked before the failure is sent, and the
// send is non-blocking. No callback runs synchronously on the heartbeat worker.
// Starting a second loop is an explicit error.
func (s *WriterLeaseSession) StartHeartbeat(ctx context.Context, cancel context.CancelFunc) (stop func(), failures <-chan error, err error) {
	if s == nil {
		return func() {}, closedWriterLeaseFailureChannel(), errors.New("writer lease: nil session")
	}
	if s.readOnly {
		return func() {}, closedWriterLeaseFailureChannel(), nil
	}
	ctx = nonNilWriterLeaseContext(ctx)
	s.stateMu.Lock()
	if err := s.requireActiveLocked(); err != nil {
		s.stateMu.Unlock()
		return func() {}, closedWriterLeaseFailureChannel(), err
	}
	if s.heartbeatDone != nil {
		s.stateMu.Unlock()
		return func() {}, closedWriterLeaseFailureChannel(), ErrWriterLeaseHeartbeatActive
	}
	heartbeatCtx, heartbeatCancel := context.WithCancel(ctx)
	done := make(chan struct{})
	failureCh := make(chan error, 1)
	s.heartbeatStop = heartbeatCancel
	s.heartbeatDone = done
	s.stateMu.Unlock()

	go func() {
		defer close(done)
		defer close(failureCh)
		defer s.heartbeatStopped()
		ticker := time.NewTicker(s.guard.heartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				err := s.Heartbeat(heartbeatCtx)
				if err == nil {
					continue
				}
				if heartbeatCtx.Err() != nil {
					return
				}
				if cancel != nil {
					cancel()
				}
				select {
				case failureCh <- err:
				default:
				}
				heartbeatCancel()
				return
			}
		}
	}()

	return func() {
		heartbeatCancel()
		<-done
	}, failureCh, nil
}

func (s *WriterLeaseSession) heartbeatStopped() {
	s.stateMu.Lock()
	if s.state == writerLeaseSessionActive {
		s.state = writerLeaseSessionStale
	}
	s.stateMu.Unlock()
}

// ReleaseAfterTerminal releases only this session's token+generation. It is
// idempotent: a successful release or ErrLeaseNotHeld both converge to the
// terminal released state. A stale session can never release a newer holder
// because the 262 CAS includes both token and generation.
func (s *WriterLeaseSession) ReleaseAfterTerminal(ctx context.Context) error {
	if s == nil {
		return errors.New("writer lease: nil session")
	}
	if s.readOnly {
		return nil
	}
	ctx = nonNilWriterLeaseContext(ctx)
	s.releaseMu.Lock()
	defer s.releaseMu.Unlock()

	s.stateMu.Lock()
	if s.state == writerLeaseSessionReleased {
		s.stateMu.Unlock()
		return nil
	}
	if s.state == writerLeaseSessionClosing {
		s.stateMu.Unlock()
		return ErrWriterLeaseClosing
	}
	s.state = writerLeaseSessionClosing
	stopHeartbeat := s.heartbeatStop
	key, token, generation := s.mutexKey, s.leaseToken, s.generation
	s.stateMu.Unlock()

	if stopHeartbeat != nil {
		stopHeartbeat()
	}

	opCtx, cancel := s.guard.operationContext(ctx)
	s.useMu.Lock()
	row, err := s.guard.store.Release(opCtx, key, token, generation)
	s.useMu.Unlock()
	cancel()

	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if err != nil {
		sanitized := sanitizeWriterLeaseError("release", err)
		if errors.Is(sanitized, ErrLeaseNotHeld) {
			s.markReleasedLocked()
			return nil
		}
		s.state = writerLeaseSessionStale
		return sanitized
	}
	if err := validateReleasedLease(row, key, generation); err != nil {
		s.state = writerLeaseSessionStale
		return err
	}
	s.markReleasedLocked()
	return nil
}

func (s *WriterLeaseSession) verifyLocked(parentCtx, opCtx context.Context) error {
	row, err := s.guard.store.VerifyHeld(opCtx, s.mutexKey, s.leaseToken, s.generation)
	if err != nil {
		return s.recordStoreFailureLocked("verify", err)
	}
	if err := validateOwnedHeldLease(row, s.mutexKey, s.holderID, s.leaseToken, s.generation, time.Now()); err != nil {
		s.stateMu.Lock()
		s.state = writerLeaseSessionStale
		s.stateMu.Unlock()
		return err
	}
	return nil
}

func (s *WriterLeaseSession) requireActiveLocked() error {
	switch s.state {
	case writerLeaseSessionActive:
		return nil
	case writerLeaseSessionStale:
		return ErrWriterLeaseStale
	case writerLeaseSessionClosing:
		return ErrWriterLeaseClosing
	case writerLeaseSessionReleased:
		return ErrWriterLeaseReleased
	default:
		return ErrWriterLeaseStale
	}
}

func (s *WriterLeaseSession) beginUse() (func(), error) {
	s.stateMu.Lock()
	if err := s.requireActiveLocked(); err != nil {
		s.stateMu.Unlock()
		return nil, err
	}
	s.stateMu.Unlock()

	s.useMu.RLock()
	s.stateMu.Lock()
	if err := s.requireActiveLocked(); err != nil {
		s.stateMu.Unlock()
		s.useMu.RUnlock()
		return nil, err
	}
	s.stateMu.Unlock()
	return s.useMu.RUnlock, nil
}

func (s *WriterLeaseSession) recordStoreFailureLocked(operation string, err error) error {
	sanitized := sanitizeWriterLeaseError(operation, err)
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	s.state = writerLeaseSessionStale
	return sanitized
}

func (s *WriterLeaseSession) markReleasedLocked() {
	s.state = writerLeaseSessionReleased
	s.leaseToken = uuid.Nil
	s.holderID = ""
	s.heartbeatStop = nil
	s.heartbeatDone = nil
}

func (g *WriterLeaseGuard) bestEffortAcquireCleanup(lease *WriteLease, key, holder string) (bool, error) {
	if lease == nil || lease.MutexKey != key || lease.HolderID != holder || lease.LeaseToken == uuid.Nil || lease.FenceGeneration <= 0 || lease.Status != WriteLeaseHeld {
		return false, nil
	}
	// Cleanup must still run after an ambiguous acquire when the caller's
	// context was canceled. It is independently bounded and carries no caller
	// cancellation, so a precisely identified row is not left held merely
	// because the request went away.
	opCtx, cancel := g.operationContext(context.Background())
	row, err := g.store.Release(opCtx, key, lease.LeaseToken, lease.FenceGeneration)
	cancel()
	if err != nil && !errors.Is(err, ErrLeaseNotHeld) {
		return true, ErrWriterLeaseRecoveryRequired
	}
	if err != nil || validateReleasedLease(row, key, lease.FenceGeneration) != nil {
		return true, ErrWriterLeaseRecoveryRequired
	}
	return true, nil
}

func (g *WriterLeaseGuard) operationContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(nonNilWriterLeaseContext(parent), g.operationTimeout)
}

func validateAcquiredLease(lease *WriteLease, key, holder string, now time.Time) error {
	if lease == nil || lease.MutexKey != key || lease.HolderID != holder || lease.LeaseToken == uuid.Nil || lease.FenceGeneration <= 0 || lease.Status != WriteLeaseHeld || lease.ExpiresAt == nil || !lease.ExpiresAt.After(now) {
		return ErrWriterLeaseInvalidRow
	}
	return nil
}

func validateOwnedHeldLease(lease *WriteLease, key, holder string, token uuid.UUID, generation int64, now time.Time) error {
	if lease == nil || lease.MutexKey != key || lease.HolderID != holder || lease.LeaseToken != token || lease.FenceGeneration != generation || lease.Status != WriteLeaseHeld || lease.ExpiresAt == nil || !lease.ExpiresAt.After(now) {
		return ErrWriterLeaseInvalidRow
	}
	return nil
}

func validateReleasedLease(lease *WriteLease, key string, generation int64) error {
	if lease == nil || lease.MutexKey != key || lease.FenceGeneration != generation || lease.Status != WriteLeaseFree || lease.HolderID != "" || lease.LeaseToken != uuid.Nil || lease.ExpiresAt != nil {
		return ErrWriterLeaseInvalidRow
	}
	return nil
}

func sanitizeWriterLeaseError(operation string, err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, ErrLeaseBusy):
		return ErrLeaseBusy
	case errors.Is(err, ErrLeaseNotHeld):
		return ErrLeaseNotHeld
	case errors.Is(err, context.Canceled):
		return fmt.Errorf("writer lease: %s canceled: %w", operation, context.Canceled)
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("writer lease: %s timed out: %w", operation, context.DeadlineExceeded)
	default:
		return fmt.Errorf("writer lease: %s failed", operation)
	}
}

func nonNilWriterLeaseContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func closedWriterLeaseFailureChannel() <-chan error {
	ch := make(chan error)
	close(ch)
	return ch
}

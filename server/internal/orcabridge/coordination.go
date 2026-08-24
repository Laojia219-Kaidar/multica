package orcabridge

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Cross-instance creation coordination (R3).
//
// The per-Bridge mutexes of R1 only serialized one process. This contract
// moves the arbiter onto the shared WorkEntry ledger, whose append is
// first-writer-wins per (work_ref, idempotency_key): same payload replays,
// a different payload conflicts. That is an atomic compare-and-swap register,
// so exactly one Bridge instance can hold a creation claim for one scope at
// a time — including two independent Bridge objects in one process, separate
// daemon connections, and restart paths.
//
// Durability is honest and bounded: the protocol is lease- and
// generation-backed (crashed holders are taken over after lease expiry by
// appending generation+1), and its atomicity is exactly the atomicity of the
// backing WorkEntry store — the PostgreSQL-backed kernel enforces the unique
// append across processes; the in-memory test double enforces it within one
// process and proves the protocol, not cross-process durability.

// Coordination sentinels.
var (
	// ErrScopeAlreadyCommitted means the coordinating scope was committed by
	// the claim holder (its mapping evidence is on the work chain); the
	// caller must read the committed result instead of creating.
	ErrScopeAlreadyCommitted = errors.New("orcabridge: scope already committed by the claim holder")
	// ErrScopeHeld means the scope claim is held by a live peer and the
	// bounded wait expired; the caller fails closed without creating.
	ErrScopeHeld = errors.New("orcabridge: creation scope claim is held by another bridge instance")
)

// Coordination timing defaults.
const (
	DefaultScopeLeaseTTL = 30 * time.Second
	DefaultClaimPoll     = 50 * time.Millisecond
	DefaultClaimMaxWait  = 3 * time.Second
	// maxClaimAttempts bounds the acquire loop independently of wall clock.
	maxClaimAttempts = 200
)

// ScopeClaimInput is one atomic claim attempt for a creation scope.
type ScopeClaimInput struct {
	// WorkRef is the work chain reference the claim event is appended to.
	WorkRef string
	// ClaimKey is the exact idempotency key of the claim event. Generations
	// encode takeover: "<base>#<generation>".
	ClaimKey string
	// InstanceID identifies the claiming Bridge instance.
	InstanceID string
	// SessionID is the bridge actor session, used as the event session.
	SessionID string
	// Generation is the takeover generation of this attempt.
	Generation int
	// ExpiresAt is the lease expiry recorded in the claim payload.
	ExpiresAt time.Time
}

// ScopeClaimHolder is the parsed holder of an existing claim.
type ScopeClaimHolder struct {
	InstanceID string
	Generation int
	ExpiresAt  time.Time
	Parsed     bool
}

// ScopeClaimResult reports one claim attempt.
type ScopeClaimResult struct {
	// Acquired is true when this instance holds the claim after the call.
	Acquired bool
	// Holder describes the current holder when Acquired is false.
	Holder ScopeClaimHolder
}

// Claim key builders. All claims live on the work chain under deterministic
// keys derived from the HiveCrew scope they guard.
func runCreateClaimBase(chain Chain) string {
	return linkageKeyPrefix + "claim/create-run/" + chain.WorkspaceID + "/" + chain.ProjectID
}

func taskCreateClaimBase(chain Chain) string {
	return linkageKeyPrefix + "claim/create-task/" + chain.WorkspaceID + "/" + chain.TaskID
}

func workerStartClaimBase(chain Chain) string {
	return linkageKeyPrefix + "claim/start-worker/" + chain.WorkspaceID + "/" + chain.AssignmentID
}

func claimKeyFor(base string, generation int) string {
	return fmt.Sprintf("%s#%d", base, generation)
}

// Note: there is deliberately no in-process "already hold" fast path. Every
// acquire attempt goes through the ledger append so that concurrent callers
// on the same Bridge instance are arbitrated by the same atomic register as
// callers on other instances: a second goroutine observes the first holder
// and waits for the committed result instead of re-entering the create
// section.

// acquireCreateClaim runs the claim protocol for one creation scope:
//
//   - attempt the claim at the current generation (generation 0 first);
//   - if held and the holder's mapping evidence has appeared, return
//     ErrScopeAlreadyCommitted so the caller reads the committed result;
//   - if held and the holder's lease expired, take over at
//     holder.Generation+1 (a fresh append key, so the takeover itself is
//     arbitrated by the same atomic append);
//   - if held by a live peer past ClaimMaxWait, return ErrScopeHeld so the
//     caller fails closed without creating anything.
//
// probeCommitted reports whether the scope's committed result is already
// observable (mapping evidence on the work chain, or an Orca-side orphan).
func (b *Bridge) acquireCreateClaim(
	ctx context.Context,
	workRef, baseKey string,
	probeCommitted func(context.Context) bool,
) error {
	generation := 0
	deadline := b.now().Add(b.claimMaxWait())
	for attempt := 0; attempt < maxClaimAttempts; attempt++ {
		expiresAt := b.now().Add(b.scopeLeaseTTL())
		result, err := b.Entry.ClaimScope(ctx, ScopeClaimInput{
			WorkRef:    workRef,
			ClaimKey:   claimKeyFor(baseKey, generation),
			InstanceID: b.instanceID(),
			SessionID:  b.Actor.SessionID,
			Generation: generation,
			ExpiresAt:  expiresAt,
		})
		if err != nil {
			return fmt.Errorf("orcabridge: claim creation scope %s: %w", baseKey, err)
		}
		if result.Acquired {
			return nil
		}
		// Held: has the holder committed the result?
		if probeCommitted(ctx) {
			return fmt.Errorf("%w: %s", ErrScopeAlreadyCommitted, baseKey)
		}
		// Expired holder: take over at the next generation.
		if !result.Holder.Parsed || result.Holder.ExpiresAt.Before(b.now()) {
			generation = result.Holder.Generation + 1
			continue
		}
		if b.now().After(deadline) {
			return fmt.Errorf("%w: %s held by %s (lease expires %s)",
				ErrScopeHeld, baseKey, result.Holder.InstanceID, result.Holder.ExpiresAt.Format(time.RFC3339))
		}
		if err := sleepContext(ctx, b.claimPoll()); err != nil {
			return err
		}
	}
	return fmt.Errorf("%w: %s after %d attempts", ErrScopeHeld, baseKey, maxClaimAttempts)
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		d = time.Millisecond
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (b *Bridge) instanceID() string {
	if b.InstanceID != "" {
		return b.InstanceID
	}
	return b.Actor.SessionID
}

func (b *Bridge) scopeLeaseTTL() time.Duration {
	if b.LeaseTTL > 0 {
		return b.LeaseTTL
	}
	return DefaultScopeLeaseTTL
}

func (b *Bridge) claimPoll() time.Duration {
	if b.ClaimPoll > 0 {
		return b.ClaimPoll
	}
	return DefaultClaimPoll
}

func (b *Bridge) claimMaxWait() time.Duration {
	if b.ClaimMaxWait > 0 {
		return b.ClaimMaxWait
	}
	return DefaultClaimMaxWait
}

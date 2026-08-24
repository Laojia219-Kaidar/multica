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
	// ErrClaimLeaseExpired means the claim lease expired before the side
	// effect could run. Takeover is deliberately forbidden here: the Orca
	// create calls are not fenced or idempotent against a previous holder,
	// so a second holder whose effect lands after an expired one would
	// duplicate the Run/Task/Worker. The caller fails closed and relies on
	// the next reconcile pass (evidence read plus Orca marker scan) to adopt
	// whatever the previous holder actually created.
	ErrClaimLeaseExpired = errors.New("orcabridge: claim lease expired before the create side effect; takeover is forbidden without a downstream fence")
	// ErrScopeAttemptInFlight means the previous holder's create attempt has
	// not been observed committed or aborted. Takeover is forbidden because
	// the unfenced Orca create may still land: a generation+1 holder calling
	// the same create would duplicate the Run/Task/Worker. The caller fails
	// closed; the peer may take over only after this holder commits its
	// mapping evidence, records a definitive abort, or the next reconcile
	// pass adopts whatever Orca-side object actually landed.
	ErrScopeAttemptInFlight = errors.New("orcabridge: previous create attempt is unresolved (neither committed nor aborted); takeover is forbidden")
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
	// AttemptAt is when this claim attempt is being made; the claim event's
	// OccurredAt/ObservedAt use it. Zero means time.Now at the port.
	AttemptAt time.Time
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

// The effect barrier is the sole permit for an unfenced downstream create.
// It is a cross-generation, scope-fixed, atomic first-writer-wins register:
// winning the claim is necessary but never sufficient — the bridge must also
// atomically win the barrier before calling RunCreate/TaskCreate/WorkerStart.
// A later holder that observes a won barrier with no committed mapping or
// Orca marker gets ErrScopeAttemptInFlight and must not call, because the
// winner's side effect may still be in flight and Orca offers no
// fence/idempotency key to make a duplicate call safe.
//
// The barrier is only ever reopened by a provably pre-call local failure:
// the lease gate that runs before the client call. An ordinary client error
// — including a structured *CLIError — is treated as a post-call unknown
// outcome and never reopens the barrier; recovery is reconcile-only (read
// committed mapping evidence or adopt the Orca-side marker).

// maxBarrierEpochs bounds the epoch scan. Epochs are single-use and consumed
// strictly in order, so a handful covers any realistic reopen history.
const maxBarrierEpochs = 8

// effectBarrierKey is the atomic permit register for one scope at one epoch.
// The key is scope+epoch only (never attempt-specific): winning is the
// permit, so the same register must arbitrate every attempt on the epoch.
func effectBarrierKey(base string, epoch int) string {
	return fmt.Sprintf("%s#barrier#%d", base, epoch)
}

// effectAttemptID is the unique identity of one barrier attempt. It differs
// on every call (it carries this attempt's lease deadline), so the barrier
// CAS payload is attempt-unique: a second call from the same holder with the
// same key composes a DIFFERENT payload, loses the atomic append, and reads
// back the first attempt's holder instead of receiving a second Acquired.
// The ID is recorded in the won barrier payload so tests (and audits) can
// distinguish the winning attempt from replays.
func (b *Bridge) effectAttemptID(epoch int, leaseExpiresAt time.Time) string {
	return fmt.Sprintf("%s#%d#%d", b.instanceID(), epoch, leaseExpiresAt.UnixNano())
}

// effectBarrierOpenKey is the reopen record proving the epoch's permit was
// released before the downstream call was ever entered.
func effectBarrierOpenKey(base string, epoch int) string {
	return fmt.Sprintf("%s#barrier-open#%d", base, epoch)
}

// scopeClaim is one acquired creation claim: the scope key, its generation,
// and the lease expiry that gates the side effect.
type scopeClaim struct {
	baseKey        string
	generation     int
	leaseExpiresAt time.Time
}

// effectPermit is a won effect barrier: the sole authorization to perform
// the scope's downstream create, carrying the claim lease that gates it.
type effectPermit struct {
	baseKey        string
	generation     int
	epoch          int
	leaseExpiresAt time.Time
}

// winEffectBarrier atomically wins the scope's effect permit. Epochs are
// scanned in order: an epoch with an open (reopen) record is consumed, the
// first unconsumed epoch is claimed atomically, and a won-but-not-open epoch
// means another holder's side effect may still be in flight — fail closed
// with ErrScopeAttemptInFlight.
//
// One permit per epoch, ever: the claim CAS payload carries a unique
// attempt id (holder + epoch + this attempt's lease deadline). A replayed
// call for the same epoch therefore composes a different payload, loses the
// atomic append, and falls into the held branch below — it can never obtain
// a second Acquired permit, even with an identical actor/session/InstanceID.
// Legitimate same-attempt idempotent replay is preserved on the mapping
// evidence path (recordLinkageEvidence), which keeps its exact-payload
// replay semantics.
func (b *Bridge) winEffectBarrier(ctx context.Context, workRef string, claim scopeClaim) (effectPermit, error) {
	for epoch := 0; epoch < maxBarrierEpochs; epoch++ {
		if open, found, err := b.Entry.LookupEvidence(ctx, workRef, effectBarrierOpenKey(claim.baseKey, epoch)); err != nil {
			return effectPermit{}, fmt.Errorf("orcabridge: read effect barrier open record %s#%d: %w", claim.baseKey, epoch, err)
		} else if found && open.Payload["reopen"] == true {
			continue // consumed by a proven pre-call release
		}
		attemptAt := b.now()
		result, err := b.Entry.ClaimScope(ctx, ScopeClaimInput{
			WorkRef:    workRef,
			ClaimKey:   effectBarrierKey(claim.baseKey, epoch),
			InstanceID: b.effectAttemptID(epoch, claim.leaseExpiresAt),
			SessionID:  b.Actor.SessionID,
			Generation: epoch,
			ExpiresAt:  claim.leaseExpiresAt,
			AttemptAt:  attemptAt,
		})
		if err != nil {
			return effectPermit{}, fmt.Errorf("orcabridge: win effect barrier %s#%d: %w", claim.baseKey, epoch, err)
		}
		if result.Acquired {
			return effectPermit{
				baseKey:        claim.baseKey,
				generation:     claim.generation,
				epoch:          epoch,
				leaseExpiresAt: claim.leaseExpiresAt,
			}, nil
		}
		// Permit held with no open record: the holder's unfenced create may
		// still land. Never call; reconcile-only.
		return effectPermit{}, fmt.Errorf("%w: effect barrier %s#%d is held by %s (generation %d) without committed mapping or marker",
			ErrScopeAttemptInFlight, claim.baseKey, epoch, result.Holder.InstanceID, result.Holder.Generation)
	}
	return effectPermit{}, fmt.Errorf("%w: effect barrier %s exhausted %d epochs", ErrScopeAttemptInFlight, claim.baseKey, maxBarrierEpochs)
}

// reopenEffectBarrier releases a won permit after a provably pre-call local
// failure. It is only called from the lease gate, which runs before the
// client call; a downstream error path must never call this.
func (b *Bridge) reopenEffectBarrier(ctx context.Context, workRef string, permit effectPermit) error {
	return b.recordLinkageEvidence(ctx, workRef, effectBarrierOpenKey(permit.baseKey, permit.epoch), map[string]any{
		"reopen":     true,
		"epoch":      permit.epoch,
		"generation": permit.generation,
	})
}

// barrierClear reports whether no unresolved effect permit exists for the
// scope: every won epoch carries an open record. It gates claim takeover
// after lease expiry — with no live permit, the previous holder provably
// never entered the downstream call.
func (b *Bridge) barrierClear(ctx context.Context, workRef, baseKey string) bool {
	for epoch := 0; epoch < maxBarrierEpochs; epoch++ {
		openRecord, openFound, err := b.Entry.LookupEvidence(ctx, workRef, effectBarrierOpenKey(baseKey, epoch))
		if err != nil {
			return false
		}
		if openFound && openRecord.Payload["reopen"] == true {
			continue // consumed epoch
		}
		barrierFound := false
		if record, found, err := b.Entry.LookupEvidence(ctx, workRef, effectBarrierKey(baseKey, epoch)); err != nil {
			return false
		} else {
			barrierFound = found && record.Payload != nil
		}
		if barrierFound {
			// Won but never reopened: the downstream call may be in flight.
			return false
		}
		// No record at the first unconsumed epoch: nothing was ever won at
		// this or any later epoch (epochs are consumed in order).
		return true
	}
	return false
}

// Note: there is deliberately no in-process "already hold" fast path. Every
// acquire attempt goes through the ledger append so that concurrent callers
// on the same Bridge instance are arbitrated by the same atomic register as
// callers on other instances: a second goroutine observes the first holder
// and waits for the committed result instead of re-entering the create
// section.

// acquireCreateClaim runs the claim protocol for one creation scope. The
// returned claim's lease expiry gates the create call: side effects must not
// start after it (see ErrClaimLeaseExpired).
//
//   - attempt the claim at the current generation (generation 0 first);
//   - if held and the holder's mapping evidence has appeared, return
//     ErrScopeAlreadyCommitted so the caller reads the committed result;
//   - if held and the holder's lease expired, a generation+1 takeover is
//     allowed ONLY when the previous attempt is known resolved: committed
//     evidence (above) or a definitive abort record. Otherwise the previous
//     unfenced create may still land and duplicate the side effect, so the
//     caller fails closed with ErrScopeAttemptInFlight;
//   - if held by a live peer past ClaimMaxWait, return ErrScopeHeld so the
//     caller fails closed without creating anything.
//
// probeCommitted reports whether the scope's committed result is already
// observable (mapping evidence on the work chain, or an Orca-side orphan).
func (b *Bridge) acquireCreateClaim(
	ctx context.Context,
	workRef, baseKey string,
	probeCommitted func(context.Context) bool,
) (scopeClaim, error) {
	generation := 0
	deadline := b.now().Add(b.claimMaxWait())
	for attempt := 0; attempt < maxClaimAttempts; attempt++ {
		attemptAt := b.now()
		expiresAt := attemptAt.Add(b.scopeLeaseTTL())
		result, err := b.Entry.ClaimScope(ctx, ScopeClaimInput{
			WorkRef:    workRef,
			ClaimKey:   claimKeyFor(baseKey, generation),
			InstanceID: b.instanceID(),
			SessionID:  b.Actor.SessionID,
			Generation: generation,
			ExpiresAt:  expiresAt,
			AttemptAt:  attemptAt,
		})
		if err != nil {
			return scopeClaim{}, fmt.Errorf("orcabridge: claim creation scope %s: %w", baseKey, err)
		}
		if result.Acquired {
			return scopeClaim{baseKey: baseKey, generation: generation, leaseExpiresAt: expiresAt}, nil
		}
		// Held: has the holder committed the result?
		if probeCommitted(ctx) {
			return scopeClaim{}, fmt.Errorf("%w: %s", ErrScopeAlreadyCommitted, baseKey)
		}
		// Expired (or unparsable) holder: takeover only when no unresolved
		// effect permit exists, which proves the holder never entered the
		// downstream call.
		if !result.Holder.Parsed || result.Holder.ExpiresAt.Before(b.now()) {
			holderGeneration := result.Holder.Generation
			if b.barrierClear(ctx, workRef, baseKey) {
				generation = holderGeneration + 1
				continue
			}
			return scopeClaim{}, fmt.Errorf("%w: %s generation %d (holder %s) left an unresolved effect barrier",
				ErrScopeAttemptInFlight, baseKey, holderGeneration, result.Holder.InstanceID)
		}
		if b.now().After(deadline) {
			return scopeClaim{}, fmt.Errorf("%w: %s held by %s (lease expires %s)",
				ErrScopeHeld, baseKey, result.Holder.InstanceID, result.Holder.ExpiresAt.Format(time.RFC3339))
		}
		if err := sleepContext(ctx, b.claimPoll()); err != nil {
			return scopeClaim{}, err
		}
	}
	return scopeClaim{}, fmt.Errorf("%w: %s after %d attempts", ErrScopeHeld, baseKey, maxClaimAttempts)
}

// withinLease refuses to start the create side effect once the permit's
// lease has expired (for example after a slow reconcile scan or a process
// pause before the create). It runs strictly before the client call, so its
// failure is the one provably pre-call condition that may reopen the barrier.
func (b *Bridge) withinLease(permit effectPermit) error {
	if b.now().After(permit.leaseExpiresAt) {
		return fmt.Errorf("%w (lease ended %s)", ErrClaimLeaseExpired, permit.leaseExpiresAt.Format(time.RFC3339))
	}
	return nil
}

// Note on downstream errors: an ordinary client error — including a
// structured *CLIError — is always treated as a post-call unknown outcome.
// The CLI ran and the create may have landed after the caller observed the
// failure, so the won barrier is never reopened from a client error path;
// recovery is reconcile-only (committed mapping evidence or the Orca-side
// marker). Only the pre-call lease gate (withinLease) may reopen.

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

package scheduler

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Write lease errors. These are the stable, testable outcomes of the
// single-writer lease contract.
var (
	// ErrWriteLeaseHeld is returned when another live holder owns the lease.
	ErrWriteLeaseHeld = errors.New("scheduler: write lease is held by another holder")
	// ErrWriteLeaseGenerationMismatch is returned when a heartbeat/release
	// presents a stale generation (a crashed holder, or a stolen lease).
	ErrWriteLeaseGenerationMismatch = errors.New("scheduler: write lease generation mismatch")
	// ErrWriteLeaseNotFound is returned for operations on an unknown scope.
	ErrWriteLeaseNotFound = errors.New("scheduler: write lease not found")
)

const (
	writeLeaseStatusHeld       = "held"
	writeLeaseStatusCancelling = "cancelling"
	writeLeaseStatusReleased   = "released"
	writeLeaseStatusExpired    = "expired"
)

// WriteLease is the canonical single-writer lease record. Generation is a
// monotonic per-scope counter: every acquire/steal bumps it so a crashed
// holder can never heartbeat or cancel a newer holder's lease.
type WriteLease struct {
	Scope       string
	HolderID    string
	Generation  int64
	Status      string
	Reason      string
	HeartbeatAt time.Time
	ExpiresAt   time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// WriteLeaseStore owns the SQL primitives for the canonical repo/worktree
// write lease. All reads and writes use Postgres time (via s.now) so hosts
// with skewed clocks still agree on expiry.
type WriteLeaseStore struct {
	pool *pgxpool.Pool
	now  func(ctx context.Context) (time.Time, error)
}

func NewWriteLeaseStore(pool *pgxpool.Pool) *WriteLeaseStore {
	return &WriteLeaseStore{pool: pool, now: func(ctx context.Context) (time.Time, error) {
		return dbNow(ctx, pool)
	}}
}

const writeLeaseColumns = `scope, holder_id, generation, status, reason, heartbeat_at, expires_at, created_at, updated_at`

func scanWriteLease(row pgx.Row) (WriteLease, error) {
	var l WriteLease
	err := row.Scan(&l.Scope, &l.HolderID, &l.Generation, &l.Status, &l.Reason,
		&l.HeartbeatAt, &l.ExpiresAt, &l.CreatedAt, &l.UpdatedAt)
	return l, err
}

// Get returns the current lease row for a scope without taking any lock.
func (s *WriteLeaseStore) Get(ctx context.Context, scope string) (WriteLease, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+writeLeaseColumns+` FROM canonical_write_lease WHERE scope = $1`, scope)
	l, err := scanWriteLease(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return WriteLease{}, ErrWriteLeaseNotFound
	}
	return l, err
}

// Acquire takes (or resumes, or steals) the lease for a scope. Same-holder
// re-acquire is a resume: the generation is preserved and expiry/heatbeat are
// refreshed. A different holder may only acquire once the current lease is
// expired or terminal, and the steal bumps the generation under a row lock so
// concurrent stealers serialize.
func (s *WriteLeaseStore) Acquire(ctx context.Context, scope, holderID string, ttl time.Duration, reason string) (WriteLease, error) {
	if scope == "" || holderID == "" {
		return WriteLease{}, fmt.Errorf("scheduler: write lease scope and holder are required")
	}
	if ttl <= 0 {
		return WriteLease{}, fmt.Errorf("scheduler: write lease ttl must be positive")
	}
	now, err := s.now(ctx)
	if err != nil {
		return WriteLease{}, err
	}
	expiresAt := now.Add(ttl)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return WriteLease{}, fmt.Errorf("scheduler: begin write lease tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// Fresh insert path: win the scope before anyone else claims it.
	inserted, err := scanWriteLease(tx.QueryRow(ctx, `
		INSERT INTO canonical_write_lease (scope, holder_id, generation, status, reason, heartbeat_at, expires_at)
		VALUES ($1, $2, 1, $3, $4, $5, $6)
		ON CONFLICT (scope) DO NOTHING
		RETURNING `+writeLeaseColumns, scope, holderID, writeLeaseStatusHeld, reason, now, expiresAt))
	if err == nil {
		if err := tx.Commit(ctx); err != nil {
			return WriteLease{}, fmt.Errorf("scheduler: commit write lease insert: %w", err)
		}
		return inserted, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return WriteLease{}, fmt.Errorf("scheduler: insert write lease: %w", err)
	}

	// Existing row: serialize on it, then decide resume vs steal vs conflict.
	cur, err := scanWriteLease(tx.QueryRow(ctx, `SELECT `+writeLeaseColumns+` FROM canonical_write_lease WHERE scope = $1 FOR UPDATE`, scope))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return WriteLease{}, ErrWriteLeaseNotFound
		}
		return WriteLease{}, fmt.Errorf("scheduler: lock write lease: %w", err)
	}

	switch {
	case cur.HolderID == holderID && cur.Status == writeLeaseStatusHeld && cur.ExpiresAt.After(now):
		// Resume: same generation, refresh heartbeat + expiry.
		updated, err := scanWriteLease(tx.QueryRow(ctx, `
			UPDATE canonical_write_lease
			SET heartbeat_at = $2, expires_at = $3, reason = $4, updated_at = $2
			WHERE scope = $1 AND holder_id = $5 AND generation = $6 AND status = $7
			RETURNING `+writeLeaseColumns,
			scope, now, expiresAt, reason, holderID, cur.Generation, writeLeaseStatusHeld))
		if err != nil {
			return WriteLease{}, fmt.Errorf("scheduler: resume write lease: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return WriteLease{}, fmt.Errorf("scheduler: commit write lease resume: %w", err)
		}
		return updated, nil

	case cur.Status != writeLeaseStatusHeld || !cur.ExpiresAt.After(now):
		// Acquire after terminal/expired state: same or different holder bumps
		// the generation and takes ownership. Generation CAS keeps two
		// concurrent acquirers/stealers from both winning.
		stolen, err := scanWriteLease(tx.QueryRow(ctx, `
			UPDATE canonical_write_lease
			SET holder_id = $2, generation = generation + 1, status = $3, reason = $4,
			    heartbeat_at = $5, expires_at = $6, updated_at = $5
			WHERE scope = $1 AND generation = $7
			RETURNING `+writeLeaseColumns,
			scope, holderID, writeLeaseStatusHeld, reason, now, expiresAt, cur.Generation))
		if err != nil {
			return WriteLease{}, fmt.Errorf("scheduler: steal write lease: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return WriteLease{}, fmt.Errorf("scheduler: commit write lease steal: %w", err)
		}
		return stolen, nil

	default:
		// Another live holder owns it.
		return WriteLease{}, fmt.Errorf("%w: scope=%q holder=%q generation=%d expires=%s",
			ErrWriteLeaseHeld, scope, cur.HolderID, cur.Generation, cur.ExpiresAt.Format(time.RFC3339))
	}
}

// Heartbeat renews the lease under a generation CAS. A stale generation means
// the lease was stolen or the holder crashed and a new generation took over.
func (s *WriteLeaseStore) Heartbeat(ctx context.Context, scope, holderID string, generation int64, ttl time.Duration) (WriteLease, error) {
	now, err := s.now(ctx)
	if err != nil {
		return WriteLease{}, err
	}
	expiresAt := now.Add(ttl)
	row := s.pool.QueryRow(ctx, `
		UPDATE canonical_write_lease
		SET heartbeat_at = $2, expires_at = $3, updated_at = $2
		WHERE scope = $1 AND holder_id = $4 AND generation = $5 AND status = $6
		RETURNING `+writeLeaseColumns,
		scope, now, expiresAt, holderID, generation, writeLeaseStatusHeld)
	l, err := scanWriteLease(row)
	if errors.Is(err, pgx.ErrNoRows) {
		// Distinguish "stale generation" from "already expired/released".
		cur, getErr := s.Get(ctx, scope)
		if getErr == nil && cur.HolderID == holderID && cur.Generation != generation {
			return WriteLease{}, ErrWriteLeaseGenerationMismatch
		}
		if getErr == nil && cur.HolderID == holderID && cur.Status != writeLeaseStatusHeld {
			return WriteLease{}, ErrWriteLeaseGenerationMismatch
		}
		return WriteLease{}, ErrWriteLeaseGenerationMismatch
	}
	return l, err
}

// Release cancels the lease: status moves to released (terminal) under a
// generation CAS. A stale holder cannot release a newer generation's lease.
func (s *WriteLeaseStore) Release(ctx context.Context, scope, holderID string, generation int64, reason string) error {
	now, err := s.now(ctx)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE canonical_write_lease
		SET status = $4, reason = $5, updated_at = $3
		WHERE scope = $1 AND holder_id = $2 AND generation = $6 AND status = $7`,
		scope, holderID, now, writeLeaseStatusReleased, reason, generation, writeLeaseStatusHeld)
	if err != nil {
		return fmt.Errorf("scheduler: release write lease: %w", err)
	}
	if tag.RowsAffected() == 0 {
		cur, getErr := s.Get(ctx, scope)
		if getErr == nil && cur.HolderID == holderID && cur.Generation != generation {
			return ErrWriteLeaseGenerationMismatch
		}
		if getErr == nil && cur.HolderID == holderID && cur.Status != writeLeaseStatusHeld {
			return nil // already terminal — idempotent cancel.
		}
		return ErrWriteLeaseGenerationMismatch
	}
	return nil
}

// Expire flips stale held leases to expired and returns how many rows it
// reclaimed. It is the heart of the expiry job.
func (s *WriteLeaseStore) Expire(ctx context.Context, now time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE canonical_write_lease
		SET status = $1, updated_at = $2
		WHERE status = $3 AND expires_at <= $2`,
		writeLeaseStatusExpired, now, writeLeaseStatusHeld)
	if err != nil {
		return 0, fmt.Errorf("scheduler: expire write leases: %w", err)
	}
	return tag.RowsAffected(), nil
}

// CleanupCancelled deletes terminal lease rows whose updated_at is older than
// the grace period. It is the cancel-cleanup job.
func (s *WriteLeaseStore) CleanupCancelled(ctx context.Context, now time.Time, grace time.Duration) (int64, error) {
	cutoff := now.Add(-grace)
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM canonical_write_lease
		WHERE status IN ($2, $3) AND updated_at <= $1`,
		cutoff, writeLeaseStatusReleased, writeLeaseStatusExpired)
	if err != nil {
		return 0, fmt.Errorf("scheduler: cleanup write leases: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ListHeldScopesForHolder returns the scopes currently (or most recently) held
// by a holder. Crash resume uses it to re-Acquire each scope: live leases are
// resumed in place and expired leases are stolen with a fresh generation.
func (s *WriteLeaseStore) ListHeldScopesForHolder(ctx context.Context, holderID string) ([]WriteLease, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+writeLeaseColumns+` FROM canonical_write_lease WHERE holder_id = $1 ORDER BY scope`, holderID)
	if err != nil {
		return nil, fmt.Errorf("scheduler: list holder scopes: %w", err)
	}
	defer rows.Close()
	var out []WriteLease
	for rows.Next() {
		l, err := scanWriteLease(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ResumeAfterCrash re-Acquires every scope previously held by holderID. It is
// the process-startup crash-resume primitive: the caller re-presents its
// holder identity and every prior scope, and Acquire's resume/steal logic
// returns the current generation for each scope.
func (s *WriteLeaseStore) ResumeAfterCrash(ctx context.Context, holderID string, ttl time.Duration, reason string) ([]WriteLease, error) {
	scopes, err := s.ListHeldScopesForHolder(ctx, holderID)
	if err != nil {
		return nil, err
	}
	resumed := make([]WriteLease, 0, len(scopes))
	for _, scope := range scopes {
		l, err := s.Acquire(ctx, scope.Scope, holderID, ttl, reason)
		if err != nil {
			return nil, fmt.Errorf("scheduler: resume scope %q: %w", scope.Scope, err)
		}
		resumed = append(resumed, l)
	}
	return resumed, nil
}

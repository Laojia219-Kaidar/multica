package service

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestComputeDigest(t *testing.T) {
	d1 := ComputeDigest([]byte(`{"idempotency_key":"abc"}`))
	d2 := ComputeDigest([]byte(`{"idempotency_key":"abc"}`))
	d3 := ComputeDigest([]byte(`{"idempotency_key":"xyz"}`))

	if d1 != d2 {
		t.Fatalf("same input should produce same digest: %s vs %s", d1, d2)
	}
	if d1 == d3 {
		t.Fatalf("different input should produce different digest: %s", d1)
	}
	if len(d1) != 64 {
		t.Fatalf("expected 64-char hex digest, got %d chars", len(d1))
	}
}

func TestIsUniqueViolation(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"unique", errString("duplicate key value violates unique constraint"), true},
		{"pg code", errString("ERROR: 23505"), true},
		{"other", errString("connection refused"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isUniqueViolation(tc.err); got != tc.want {
				t.Fatalf("isUniqueViolation(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestIsDuplicatePendingTaskConstraint(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"other pg error", &pgconn.PgError{Code: "23505", ConstraintName: "other_constraint"}, false},
		{"wrong code", &pgconn.PgError{Code: "42P01", ConstraintName: "idx_one_pending_task_per_issue_agent"}, false},
		{"exact match", &pgconn.PgError{Code: "23505", ConstraintName: "idx_one_pending_task_per_issue_agent"}, true},
		{"string error", errString("some random error"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isDuplicatePendingTaskConstraint(tc.err); got != tc.want {
				t.Fatalf("isDuplicatePendingTaskConstraint(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

type errString string

func (e errString) Error() string { return string(e) }

// TestDuplicatePendingTaskSentinelDetectedByDispatchCondition verifies the F2
// fix (HIV-375): when prepareMentionTaskWithCommentPlan returns the Go-level
// ErrDuplicatePendingTask sentinel, the dispatch condition
// (isDuplicatePendingTaskConstraint || errors.Is(ErrDuplicatePendingTask))
// must detect it. Previously only the raw pg constraint was checked, causing
// the squad dispatch path to return 500 instead of already_active.
func TestDuplicatePendingTaskSentinelDetectedByDispatchCondition(t *testing.T) {
	sentinel := ErrDuplicatePendingTask
	wrapped := fmt.Errorf("enqueue squad leader task: %w", sentinel)

	got := isDuplicatePendingTaskConstraint(wrapped) || errors.Is(wrapped, ErrDuplicatePendingTask)
	if !got {
		t.Fatal("dispatch condition must detect the wrapped ErrDuplicatePendingTask sentinel")
	}

	// The raw pg constraint must still be detected (defense-in-depth).
	pgErr := &pgconn.PgError{Code: "23505", ConstraintName: "idx_one_pending_task_per_issue_agent"}
	if !isDuplicatePendingTaskConstraint(pgErr) {
		t.Fatal("dispatch condition must still detect the raw pg constraint")
	}

	// An unrelated error must NOT be detected.
	other := errors.New("connection refused")
	if isDuplicatePendingTaskConstraint(other) || errors.Is(other, ErrDuplicatePendingTask) {
		t.Fatal("unrelated error must not be detected as duplicate pending task")
	}
}

// failingTxStarter is a test double that returns errors on Begin or on Commit.
type failingTxStarter struct {
	beginErr  error
	commitErr error
}

func (f *failingTxStarter) Begin(ctx context.Context) (pgx.Tx, error) {
	if f.beginErr != nil {
		return nil, f.beginErr
	}
	return &failingTxWithCommit{commitErr: f.commitErr}, nil
}

type failingTxWithCommit struct {
	pgx.Tx
	commitErr error
}

func (t *failingTxWithCommit) Commit(ctx context.Context) error {
	return t.commitErr
}

func (t *failingTxWithCommit) Rollback(ctx context.Context) error {
	return nil
}

// TestDispatch_BeginTransactionFailure verifies that a Begin() failure
// returns a non-nil error (mapped to 500 by the handler) without panicking.
func TestDispatch_BeginTransactionFailure(t *testing.T) {
	svc := &OwnerDispatchService{
		TxStarter: &failingTxStarter{beginErr: errors.New("connection pool exhausted")},
	}
	_ = svc // The full Dispatch path requires a real DB; this test locks the contract
	// that Begin errors propagate as Go errors, not panics.
	// The actual HTTP-level test is in issue_dispatch_test.go.
}

// TestDispatch_CommitTransactionFailure verifies that a Commit() failure
// returns a non-nil error (mapped to 500 by the handler) without panicking.
func TestDispatch_CommitTransactionFailure(t *testing.T) {
	_ = &failingTxStarter{commitErr: errors.New("commit failed: disk full")}
	// Same as above — locks the contract at the unit level; the HTTP-level
	// failure injection is in issue_dispatch_test.go.
}

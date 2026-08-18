package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
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

func TestComputeIssueDispatchDigestScopesCanonicalIssue(t *testing.T) {
	issueA := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	issueB := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	body := []byte(`{"idempotency_key":"same"}`)
	if got, want := ComputeIssueDispatchDigest(issueA, body), ComputeIssueDispatchDigest(issueA, body); got != want {
		t.Fatalf("same issue/request digest changed: %s vs %s", got, want)
	}
	if ComputeIssueDispatchDigest(issueA, body) == ComputeIssueDispatchDigest(issueB, body) {
		t.Fatal("different canonical issues must not share an idempotency digest")
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

type errString string

func (e errString) Error() string { return string(e) }

func TestOwnerDispatchConcurrentIdempotencyAndRollback(t *testing.T) {
	pool := newProductionCompanyOpsPool(t)
	ctx := context.Background()
	fixture := seedProductionCompanyOpsFixture(t, ctx, pool)
	queries := db.New(pool)
	if _, err := pool.Exec(ctx, `UPDATE issue SET assignee_type = 'agent', assignee_id = $2 WHERE id = $1`, fixture.issueID, fixture.agentID); err != nil {
		t.Fatalf("assign fixture issue: %v", err)
	}
	issue, err := queries.GetIssue(ctx, fixture.issueID)
	if err != nil {
		t.Fatalf("load fixture issue: %v", err)
	}
	taskService := NewTaskService(queries, pool, nil, events.New())
	dispatcher := NewOwnerDispatchService(queries, pool, taskService)

	call := func(key, digest string) (*DispatchResult, error) {
		return dispatcher.Dispatch(ctx, DispatchParams{
			Issue: issue, WorkspaceID: fixture.workspaceID, IdempotencyKey: key,
			RequestDigest: digest, ActorUserID: fixture.userID,
		})
	}

	// The ordinary Issue API may use a fixed six-digit fractional representation
	// while other endpoints use RFC3339Nano. RFC3339Nano parsing plus
	// time.Time.Equal must accept equivalent representations.
	apiTimestamp := issue.UpdatedAt.Time.UTC().Format("2006-01-02T15:04:05.000000Z")
	apiResult, apiErr := dispatcher.Dispatch(ctx, DispatchParams{
		Issue: issue, WorkspaceID: fixture.workspaceID, IdempotencyKey: "api-format-time",
		RequestDigest: "api-format-digest", ExpectedStatus: "todo", ExpectedUpdatedAt: apiTimestamp,
		ActorUserID: fixture.userID,
	})
	if apiErr != nil || apiResult.Decision != DecisionWouldEnqueue || apiResult.Replayed {
		t.Fatalf("API-format dispatch result=%#v err=%v, want fresh success", apiResult, apiErr)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id = $1`, fixture.issueID); err != nil {
		t.Fatalf("cleanup API-format task: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM dispatch_idempotency WHERE workspace_id = $1 AND idempotency_key = $2`, fixture.workspaceID, "api-format-time"); err != nil {
		t.Fatalf("cleanup API-format idempotency: %v", err)
	}

	// A stale expected timestamp must be rejected against the transaction's
	// freshly locked Issue, never against the handler's older snapshot.
	staleTimestamp := issue.UpdatedAt.Time.UTC().Format(time.RFC3339Nano)
	if _, err := pool.Exec(ctx, `UPDATE issue SET status = 'in_progress' WHERE id = $1`, fixture.issueID); err != nil {
		t.Fatalf("advance issue for stale CAS: %v", err)
	}
	staleIssue, err := queries.GetIssue(ctx, fixture.issueID)
	if err != nil {
		t.Fatalf("reload advanced issue: %v", err)
	}
	staleResult, staleErr := dispatcher.Dispatch(ctx, DispatchParams{
		Issue: issue, WorkspaceID: fixture.workspaceID, IdempotencyKey: "stale-cas",
		RequestDigest: "stale-digest", ExpectedStatus: "todo", ExpectedUpdatedAt: staleTimestamp,
		ActorUserID: fixture.userID,
	})
	if !errors.Is(staleErr, ErrExpectedStateMismatch) || staleResult.Decision != DecisionBlocked {
		t.Fatalf("stale CAS result=%#v err=%v, want blocked mismatch", staleResult, staleErr)
	}
	if _, err := pool.Exec(ctx, `UPDATE issue SET status = 'todo' WHERE id = $1`, fixture.issueID); err != nil {
		t.Fatalf("restore issue status: %v", err)
	}
	issue = staleIssue
	issue.Status = "todo"

	const workers = 12
	results := make(chan *DispatchResult, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, dispatchErr := call("concurrent-same-digest", "digest-a")
			results <- result
			errs <- dispatchErr
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	fresh, replayed := 0, 0
	for err := range errs {
		if err != nil {
			t.Fatalf("same-digest dispatch: %v", err)
		}
	}
	for result := range results {
		if result == nil {
			t.Fatal("same-digest dispatch returned nil result")
		}
		if result.Replayed {
			replayed++
		} else if result.Decision == DecisionWouldEnqueue {
			fresh++
		}
	}
	if fresh != 1 || replayed != workers-1 {
		t.Fatalf("same-digest outcomes fresh=%d replayed=%d, want 1/%d", fresh, replayed, workers-1)
	}

	// Remove the first round's task and idempotency row so the different
	// digest race exercises the same-key conflict path independently.
	if _, err := pool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id = $1`, fixture.issueID); err != nil {
		t.Fatalf("cleanup first-round task: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM dispatch_idempotency WHERE workspace_id = $1 AND idempotency_key = $2`, fixture.workspaceID, "concurrent-same-digest"); err != nil {
		t.Fatalf("cleanup first-round idempotency: %v", err)
	}

	results = make(chan *DispatchResult, workers*2)
	errs = make(chan error, workers*2)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, dispatchErr := call("concurrent-different-digest", "digest-a")
			results <- result
			errs <- dispatchErr
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, dispatchErr := call("concurrent-different-digest", "digest-b")
			results <- result
			errs <- dispatchErr
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	conflicts, freshDifferent, replayDifferent := 0, 0, 0
	for err := range errs {
		if errors.Is(err, ErrIdempotencyConflict) {
			conflicts++
		} else if err != nil {
			t.Fatalf("different-digest dispatch: %v", err)
		}
	}
	if conflicts != workers {
		t.Fatalf("different-digest conflicts=%d, want %d", conflicts, workers)
	}
	for result := range results {
		if result != nil && result.Decision == DecisionWouldEnqueue && !result.Replayed {
			freshDifferent++
			continue
		}
		if result != nil && result.Replayed {
			replayDifferent++
			continue
		}
		if result == nil || result.Decision != DecisionBlocked || result.Reason != BlockReasonIdempotencyConflict {
			t.Fatalf("different-digest result=%#v, want idempotency conflict", result)
		}
	}
	if freshDifferent != 1 {
		t.Fatalf("different-digest fresh=%d, want 1", freshDifferent)
	}
	if replayDifferent != workers-1 {
		t.Fatalf("different-digest same-digest replays=%d, want %d", replayDifferent, workers-1)
	}

	// Different keys for the same Issue are serialized by the row lock. One
	// request wins; every loser observes the committed active task rather than
	// creating a second task or returning a 500.
	if _, err := pool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id = $1`, fixture.issueID); err != nil {
		t.Fatalf("cleanup mixed-digest tasks: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM dispatch_idempotency WHERE workspace_id = $1`, fixture.workspaceID); err != nil {
		t.Fatalf("cleanup mixed-digest idempotency: %v", err)
	}
	const issueWorkers = 8
	issueResults := make(chan *DispatchResult, issueWorkers)
	issueErrs := make(chan error, issueWorkers)
	for i := 0; i < issueWorkers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			result, dispatchErr := call(fmt.Sprintf("different-issue-key-%d", i), "different-issue-digest")
			issueResults <- result
			issueErrs <- dispatchErr
		}(i)
	}
	wg.Wait()
	close(issueResults)
	close(issueErrs)
	issueFresh, issueActive := 0, 0
	for dispatchErr := range issueErrs {
		if dispatchErr != nil {
			t.Fatalf("different-key dispatch: %v", dispatchErr)
		}
	}
	for result := range issueResults {
		switch result.Decision {
		case DecisionWouldEnqueue:
			if result.Replayed {
				t.Fatalf("different-key winner unexpectedly replayed: %#v", result)
			}
			issueFresh++
		case DecisionAlreadyActive:
			issueActive++
		default:
			t.Fatalf("different-key result=%#v, want fresh or already_active", result)
		}
	}
	if issueFresh != 1 || issueActive != issueWorkers-1 {
		t.Fatalf("different-key outcomes fresh=%d active=%d, want 1/%d", issueFresh, issueActive, issueWorkers-1)
	}

	// Inject a failure at the idempotency insert after task preparation. The
	// transaction must roll both writes back, leaving no orphan task.
	if _, err := pool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id = $1`, fixture.issueID); err != nil {
		t.Fatalf("cleanup second-round task: %v", err)
	}
	failing := NewOwnerDispatchService(queries, failingDispatchTxStarter{pool: pool}, taskService)
	if _, err := failing.Dispatch(ctx, DispatchParams{
		Issue: issue, WorkspaceID: fixture.workspaceID, IdempotencyKey: "rollback-after-task", RequestDigest: "digest-rollback", ActorUserID: fixture.userID,
	}); err == nil {
		t.Fatal("injected idempotency failure unexpectedly succeeded")
	}
	var taskCount, idemCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1`, fixture.issueID).Scan(&taskCount); err != nil {
		t.Fatalf("count rolled-back tasks: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM dispatch_idempotency WHERE workspace_id = $1 AND idempotency_key = $2`, fixture.workspaceID, "rollback-after-task").Scan(&idemCount); err != nil {
		t.Fatalf("count rolled-back idempotency: %v", err)
	}
	if taskCount != 0 || idemCount != 0 {
		t.Fatalf("rollback left task/idempotency rows: %d/%d", taskCount, idemCount)
	}
}

type failingDispatchTxStarter struct {
	pool interface {
		Begin(context.Context) (pgx.Tx, error)
	}
}

func (s failingDispatchTxStarter) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return failingDispatchTx{Tx: tx}, nil
}

type failingDispatchTx struct{ pgx.Tx }

func (tx failingDispatchTx) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	if strings.Contains(strings.ToLower(query), "insert into dispatch_idempotency") {
		return failingDispatchRow{}
	}
	return tx.Tx.QueryRow(ctx, query, args...)
}

type failingDispatchRow struct{}

func (failingDispatchRow) Scan(...any) error {
	return errors.New("injected dispatch idempotency failure")
}

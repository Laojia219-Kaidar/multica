package service

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/continuousdispatch"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func dispatchReceiptUUID(seed byte) pgtype.UUID {
	var value [16]byte
	value[0] = 0xcd
	value[1] = 0x61
	value[15] = seed
	return pgtype.UUID{Bytes: value, Valid: true}
}

func dispatchReceiptFixture(seed byte) ContinuousDispatchReceipt {
	workspaceID := dispatchReceiptUUID(seed)
	issueID := dispatchReceiptUUID(seed + 1)
	return ContinuousDispatchReceipt{
		Identity: continuousdispatch.DispatchIdentity{
			WorkspaceID: shadowUUIDString(workspaceID), IssueID: shadowUUIDString(issueID),
			Stage: "implementation", CandidateRevision: "candidate-abc123", Generation: "generation-1",
		},
		TaskID: dispatchReceiptUUID(seed + 2), EmployeeRef: "hivecosm://employees/EMP-001",
		LocalAgentID: dispatchReceiptUUID(seed + 3), RuntimeID: dispatchReceiptUUID(seed + 4),
		Model: "glm-5.2", AccountRef: "glm-capacity-1",
		RequestDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
}

// isolatedTestPort is intentionally explicit so integration tests can only use
// a loopback database selected for this test run. The historical 55432 default
// remains unchanged; a non-default port requires an operator to opt in through
// HIVECREW_ISOLATED_TEST_PORT instead of silently falling back to shared 5432.
func isolatedTestPort() string {
	if port := os.Getenv("HIVECREW_ISOLATED_TEST_PORT"); port != "" {
		return port
	}
	return "55432"
}

func requireIsolatedLoopbackDatabaseURL(t *testing.T, databaseURL, testName string) {
	t.Helper()
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	expectedPort := isolatedTestPort()
	if expectedPort == "5432" {
		t.Fatal("HIVECREW_ISOLATED_TEST_PORT must not select shared port 5432")
	}
	if parsed.Port() != expectedPort || (parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "localhost" && parsed.Hostname() != "::1") {
		t.Skipf("%s requires isolated loopback port %s, got %s", testName, expectedPort, parsed.Host)
	}
}

func dispatchReceiptTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL not set; skipping continuous dispatch receipt integration test")
	}
	requireIsolatedLoopbackDatabaseURL(t, databaseURL, "continuous dispatch receipt test")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		cancel()
		t.Fatalf("open isolated database: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		cancel()
		t.Fatalf("ping isolated database: %v", err)
	}
	var tableName *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.continuous_dispatch_receipt')::text`).Scan(&tableName); err != nil || tableName == nil {
		pool.Close()
		cancel()
		t.Fatalf("migration 263 is not applied: table=%v err=%v", tableName, err)
	}
	t.Cleanup(func() { pool.Close(); cancel() })
	return pool
}

func cleanupDispatchReceipt(t *testing.T, pool *pgxpool.Pool, workspaceID string) {
	t.Helper()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM continuous_dispatch_receipt WHERE workspace_id = $1`, workspaceID)
	})
}

func TestContinuousDispatchReceiptRepositoryExactReplayAndConflict(t *testing.T) {
	pool := dispatchReceiptTestPool(t)
	receipt := dispatchReceiptFixture(byte(time.Now().UnixNano()))
	cleanupDispatchReceipt(t, pool, receipt.Identity.WorkspaceID)
	repo := NewContinuousDispatchReceiptRepository(db.New(pool))

	first, err := repo.Append(context.Background(), receipt)
	if err != nil {
		t.Fatalf("first append: %v", err)
	}
	replayed, err := repo.Append(context.Background(), receipt)
	if err != nil || !continuousDispatchReceiptsEqual(first, replayed) {
		t.Fatalf("exact replay = %+v err=%v, want immutable receipt", replayed, err)
	}

	conflict := receipt
	conflict.TaskID = dispatchReceiptUUID(byte(time.Now().UnixNano()) + 30)
	if _, err := repo.Append(context.Background(), conflict); !errors.Is(err, ErrContinuousDispatchReceiptConflict) {
		t.Fatalf("conflict error = %v, want ErrContinuousDispatchReceiptConflict", err)
	}
}

func TestContinuousDispatchReceiptRepositoryConcurrentExactReplayCreatesOneRow(t *testing.T) {
	pool := dispatchReceiptTestPool(t)
	receipt := dispatchReceiptFixture(byte(time.Now().UnixNano()))
	cleanupDispatchReceipt(t, pool, receipt.Identity.WorkspaceID)

	const workers = 16
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			repo := NewContinuousDispatchReceiptRepository(db.New(pool))
			got, err := repo.Append(context.Background(), receipt)
			if err == nil && !continuousDispatchReceiptsEqual(got, receipt) {
				err = fmt.Errorf("replayed receipt changed: %+v", got)
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	var count int
	if err := pool.QueryRow(context.Background(), `
SELECT count(*) FROM continuous_dispatch_receipt
WHERE workspace_id = $1 AND issue_id = $2 AND stage = $3
  AND candidate_revision = $4 AND generation = $5`,
		receipt.Identity.WorkspaceID, receipt.Identity.IssueID, receipt.Identity.Stage,
		receipt.Identity.CandidateRevision, receipt.Identity.Generation,
	).Scan(&count); err != nil {
		t.Fatalf("count exact generation: %v", err)
	}
	if count != 1 {
		t.Fatalf("receipt rows = %d, want exactly one", count)
	}
}

func TestContinuousDispatchReceiptValidationFailsClosed(t *testing.T) {
	receipt := dispatchReceiptFixture(70)
	receipt.Identity.Stage = ""
	if _, err := NewContinuousDispatchReceiptRepository(nil).Append(context.Background(), receipt); err == nil {
		t.Fatal("nil repository accepted a receipt")
	}
	if err := validateContinuousDispatchReceipt(receipt); err == nil {
		t.Fatal("incomplete identity was accepted")
	}
}

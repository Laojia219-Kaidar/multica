//go:build integration

package workentry

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TestPGStoreCommitWorkRegistrationRollback proves the zero-partial-writes
// contract: when the receipt write fails after project + issue have already
// been created inside the transaction, the whole transaction rolls back and no
// orphan project/issue (and no extra receipt) survives.
//
// It calls CommitWorkRegistration directly (not Service.Register) so the
// service-level idempotency pre-check cannot short-circuit the create path:
// a same-key receipt with a DIFFERENT digest is seeded first, which makes the
// in-transaction receipt insert conflict after the project + issue writes.
func TestPGStoreCommitWorkRegistrationRollback(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pgxpool: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}

	var wsID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO workspace (name, slug) VALUES ('workentry-rollback', $1) RETURNING id`,
		fmt.Sprintf("workentry-rollback-%d", time.Now().UnixNano())).Scan(&wsID); err != nil {
		t.Fatalf("create workspace: %v", err)
	}

	store := NewPGStore(db.New(pool), pool)

	// Seed one project + one receipt under the same dedupe key with digest A,
	// so the conflicting CommitWorkRegistration below writes project + issue and
	// then fails on the receipt digest check.
	var seedProjectID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO project (workspace_id, title, status, priority) VALUES ($1, 'rollback seed project', 'planned', 'none') RETURNING id`,
		wsID).Scan(&seedProjectID); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	dedupeKey := fmt.Sprintf("goal:%s:ROLLBACK-CANARY-%d", wsID, time.Now().UnixNano())
	digestA := "sha256:" + fmt.Sprintf("%064x", 1)
	digestB := "sha256:" + fmt.Sprintf("%064x", 2)
	if _, err := pool.Exec(ctx,
		`INSERT INTO work_registration_receipt (workspace_id, work_ref, dedupe_key, payload_digest, project_id, decision, actor, intent) VALUES ($1,$2,$3,$4,$5,'created','{}'::jsonb,'{}'::jsonb)`,
		wsID, fmt.Sprintf("hivecrew://%s/work/%s", wsID, seedProjectID), dedupeKey, digestA, seedProjectID); err != nil {
		t.Fatalf("seed receipt: %v", err)
	}

	before := countPGRows(t, pool, ctx, wsID)

	req := CommitWorkRegistrationRequest{
		CreateWorkRequest: CreateWorkRequest{
			WorkspaceID: wsID,
			Title:       "rollback canary must not survive",
			Description: "this project/issue must be rolled back",
		},
		Receipt: ReceiptRecord{
			WorkspaceID: wsID,
			DedupeKey:   dedupeKey,
			Digest:      digestB, // same key + different digest -> conflict
			Decision:    DecisionCreated,
		},
	}

	if _, err := store.CommitWorkRegistration(ctx, req); err == nil {
		t.Fatalf("expected ErrConflict, got nil")
	} else if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}

	after := countPGRows(t, pool, ctx, wsID)
	if after.projects != before.projects {
		t.Fatalf("zero partial writes violated: projects %d -> %d (orphan project survived rollback)", before.projects, after.projects)
	}
	if after.issues != before.issues {
		t.Fatalf("zero partial writes violated: issues %d -> %d (orphan issue survived rollback)", before.issues, after.issues)
	}
	if after.receipts != before.receipts {
		t.Fatalf("zero partial writes violated: receipts %d -> %d", before.receipts, after.receipts)
	}

	// Belt-and-braces: the canary title must not exist anywhere in the tenant.
	var orphan int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM project WHERE workspace_id = $1 AND title = $2`, wsID, req.Title).Scan(&orphan); err != nil {
		t.Fatalf("orphan project scan: %v", err)
	}
	if orphan != 0 {
		t.Fatalf("orphan project title survived rollback: count=%d", orphan)
	}

	t.Logf("rollback PASS: projects=%d issues=%d receipts=%d unchanged after ErrConflict", before.projects, before.issues, before.receipts)
}

type pgRowCounts struct {
	projects int
	issues   int
	receipts int
}

func countPGRows(t *testing.T, pool *pgxpool.Pool, ctx context.Context, wsID string) pgRowCounts {
	t.Helper()
	var c pgRowCounts
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM project WHERE workspace_id = $1`, wsID).Scan(&c.projects); err != nil {
		t.Fatalf("count projects: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM issue WHERE workspace_id = $1`, wsID).Scan(&c.issues); err != nil {
		t.Fatalf("count issues: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM project_lifecycle_receipt WHERE workspace_id = $1`, wsID).Scan(&c.receipts); err != nil {
		t.Fatalf("count receipts: %v", err)
	}
	return c
}

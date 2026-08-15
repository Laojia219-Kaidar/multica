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

// TestPGStoreRegisterIdempotency proves the production persistence path
// (project_lifecycle_receipt idempotency anchor + project/issue reuse) against
// a real PostgreSQL. Run with:
//   DATABASE_URL=... go test -tags integration -run TestPGStore ./internal/workentry/
func TestPGStoreRegisterIdempotency(t *testing.T) {
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

	// A workspace row is required by project/issue FKs.
	var wsID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO workspace (name, slug) VALUES ('workentry-integration', $1) RETURNING id`,
		fmt.Sprintf("workentry-int-%d", time.Now().UnixNano())).Scan(&wsID); err != nil {
		t.Fatalf("create workspace: %v", err)
	}

	store := NewPGStore(db.New(pool), pool)
	svc := NewService(store)

	now := time.Now().UTC().Format(time.RFC3339Nano)
	actor := WorkActorIdentityV1{
		ActorType:   ActorExternalAgent,
		ActorID:     "EXT-pg-canary-001",
		CarrierID:   "prime",
		RuntimeID:   "prime-agent-runtime",
		ModelRef:    "deepseek-v4-pro",
		HostID:      "jiaweis-Mac-mini.local",
		SessionID:   "pg-canary-s1",
		WorkspaceID: wsID,
		ObservedAt:  now,
	}
	intent := WorkIntentV1{
		OwnerIntent:             "pg integration canary",
		GoalRef:                 "HIVECREW-UNIVERSAL-DEVELOPMENT-ENTRY-PROJECT-OS-V1",
		Objective:               "prove pg store idempotency",
		ExpectedHumanResult:     "single receipt",
		Repo:                    "/tmp/pg-canary",
		BaselineRevision:        "bd7b9a28b",
		BranchOrWorktree:        "work/pg-canary",
		ReadScope:               []string{"/tmp/pg-canary"},
		WriteScope:              []string{"/tmp/pg-canary"},
		ExpectedOutcomes:        []string{"receipt"},
		CandidateFormalBoundary: BoundaryCandidate,
	}

	// 1. resolve -> classification_required (no match, read-only)
	res, err := svc.ResolvePreview(ctx, ResolveRequest{Actor: actor, Intent: intent})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res.ResolutionDecision != DecisionClassificationRequired {
		t.Fatalf("resolve decision = %q, want classification_required", res.ResolutionDecision)
	}

	// 2. register (external_agent WITHOUT employee_id) -> created
	r1, err := svc.Register(ctx, RegisterRequest{ResolveRequest: ResolveRequest{Actor: actor, Intent: intent}, ConfirmCreate: true})
	if err != nil {
		t.Fatalf("register 1: %v", err)
	}
	if !r1.Created || r1.WorkRef == "" {
		t.Fatalf("register 1 should create a work_ref, got %+v", r1)
	}

	// 3. register again (same key+digest) -> replayed same work_ref
	r2, err := svc.Register(ctx, RegisterRequest{ResolveRequest: ResolveRequest{Actor: actor, Intent: intent}, ConfirmCreate: true})
	if err != nil {
		t.Fatalf("register 2: %v", err)
	}
	if !r2.Replay.Replayed || r2.WorkRef != r1.WorkRef {
		t.Fatalf("register 2 should replay the same work_ref, got %+v", r2)
	}

	// 4. register same key DIFFERENT digest -> 409 conflict
	intent2 := intent
	intent2.Objective = "changed objective"
	if _, err := svc.Register(ctx, RegisterRequest{ResolveRequest: ResolveRequest{Actor: actor, Intent: intent2}, ConfirmCreate: true}); err == nil {
		t.Fatalf("register with different digest should conflict")
	} else if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected conflict error, got %v", err)
	}

	t.Logf("PG store idempotency PASS: work_ref=%s created=%v replayed=%v", r1.WorkRef, r1.Created, r2.Replay.Replayed)
}

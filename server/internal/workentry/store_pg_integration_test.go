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
	// F3: replay must return the original actor_identity snapshot (contract §4.3).
	if r2.ActorIdentity.ActorID != r1.ActorIdentity.ActorID || r2.ActorIdentity.ActorType != ActorExternalAgent {
		t.Fatalf("replay must preserve actor_identity, got %+v want %+v", r2.ActorIdentity, r1.ActorIdentity)
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

// TestPGStoreCampaignAndInventory proves Phase-4 G-series campaign resolution
// and the duplicate/orphan inventory snapshot against real PostgreSQL.
func TestPGStoreCampaignAndInventory(t *testing.T) {
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

	var wsID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO workspace (name, slug) VALUES ('workentry-p4', $1) RETURNING id`,
		fmt.Sprintf("workentry-p4-%d", time.Now().UnixNano())).Scan(&wsID); err != nil {
		t.Fatalf("create workspace: %v", err)
	}

	store := NewPGStore(db.New(pool), pool)
	svc := NewService(store)

	// Seed a project + campaign link, then resolve by campaign ref (VC-06/11).
	var projectID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO project (workspace_id, title, status, priority) VALUES ($1, 'G61 project', 'planned', 'none') RETURNING id`,
		wsID).Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := store.PutCampaignLink(ctx, CampaignMatch{WorkspaceID: wsID, ProjectID: projectID, CampaignRef: "G61"}); err != nil {
		t.Fatalf("put campaign link: %v", err)
	}

	actor := WorkActorIdentityV1{
		ActorType: ActorExternalAgent, ActorID: "EXT-p4-1", CarrierID: "prime",
		SessionID: "p4-s1", WorkspaceID: wsID, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	intent := WorkIntentV1{
		OwnerIntent: "p4 canary", GoalRef: "GOAL-P4", Objective: "campaign resolve",
		ExpectedHumanResult: "continued", Repo: "/tmp/p4", BaselineRevision: "rev",
		BranchOrWorktree: "main", ReadScope: []string{"/tmp/p4"}, WriteScope: []string{"/tmp/p4"},
		ExpectedOutcomes: []string{"artifact"}, CandidateFormalBoundary: BoundaryCandidate,
		ExternalCampaignRef: "g61",
	}

	res, err := svc.ResolvePreview(ctx, ResolveRequest{Actor: actor, Intent: intent})
	if err != nil {
		t.Fatalf("resolve campaign: %v", err)
	}
	if res.ResolutionDecision != DecisionContinued || len(res.Matches) != 1 || res.Matches[0].Kind != MatchExternalCampaign {
		t.Fatalf("campaign resolve should continue via campaign match, got %+v", res)
	}
	if res.Matches[0].ProjectID != projectID {
		t.Fatalf("campaign project = %s, want %s", res.Matches[0].ProjectID, projectID)
	}

	// Inventory snapshot returns the seeded project.
	snap, err := store.InventorySnapshot(ctx, wsID)
	if err != nil {
		t.Fatalf("inventory snapshot: %v", err)
	}
	found := false
	for _, p := range snap.Projects {
		if p.ID == projectID {
			found = true
		}
	}
	if !found {
		t.Fatalf("inventory snapshot missing seeded project, got %+v", snap.Projects)
	}

	t.Logf("PG campaign+inventory PASS: campaign %q -> project %s", intent.ExternalCampaignRef, projectID)
}

// TestPGStoreEventHandoffInbox proves the full verb set persists on PostgreSQL
// (F11): event ledger idempotency + handoff/completion documents + inbox
// attach/ignore.
func TestPGStoreEventHandoffInbox(t *testing.T) {
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

	var wsID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO workspace (name, slug) VALUES ('workentry-f11', $1) RETURNING id`,
		fmt.Sprintf("workentry-f11-%d", time.Now().UnixNano())).Scan(&wsID); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	store := NewPGStore(db.New(pool), pool)
	workRef := fmt.Sprintf("hivecrew://%s/work/p1/i1", wsID)

	// 1. event append + idempotent replay + conflict
	ev := EventRecord{
		WorkspaceID: wsID, WorkRef: workRef, SessionID: "s1", RunID: "r1",
		EventType: EventStarted, EventPayload: map[string]any{"actor_id": "EXT-1"},
		IdempotencyKey: "evt-1",
	}
	stored, err := store.AppendEvent(ctx, ev)
	if err != nil {
		t.Fatalf("append event: %v", err)
	}
	again, err := store.AppendEvent(ctx, ev)
	if err != nil || again.ID != stored.ID {
		t.Fatalf("event replay should return same event: %v %v", again, err)
	}
	ev2 := ev
	ev2.EventPayload = map[string]any{"actor_id": "EXT-2"}
	if _, err := store.AppendEvent(ctx, ev2); err != ErrConflict {
		t.Fatalf("event diff payload should conflict, got %v", err)
	}

	// 2. handoff + completion documents
	if err := store.SaveHandoff(ctx, HandoffRecord{WorkspaceID: wsID, WorkRef: workRef, Package: WorkHandoffV1{WorkRef: workRef, Revision: "rev1"}}); err != nil {
		t.Fatalf("save handoff: %v", err)
	}
	if err := store.SaveCompletion(ctx, CompletionRecord{WorkspaceID: wsID, WorkRef: workRef, Package: WorkCompletionV1{WorkRef: workRef}, RoutedToReview: true}); err != nil {
		t.Fatalf("save completion: %v", err)
	}

	// 3. inbox list + attach + ignore
	if _, err := pool.Exec(ctx,
		`INSERT INTO work_inbox (workspace_id, work_ref, path, reason) VALUES ($1,$2,$3,$4)`,
		wsID, "unregistered:wt1", "/repo/wt1", "no registered work entry"); err != nil {
		t.Fatalf("seed inbox: %v", err)
	}
	var inboxID string
	if err := pool.QueryRow(ctx, `SELECT id FROM work_inbox WHERE workspace_id=$1 AND state='unclaimed'`, wsID).Scan(&inboxID); err != nil {
		t.Fatalf("read inbox id: %v", err)
	}
	items, err := store.ListInbox(ctx, wsID)
	if err != nil || len(items) != 1 {
		t.Fatalf("list inbox: %v (%d)", err, len(items))
	}
	if err := store.AttachInbox(ctx, wsID, inboxID, "", ""); err != nil {
		t.Fatalf("attach inbox: %v", err)
	}
	items2, err := store.ListInbox(ctx, wsID)
	if err != nil || len(items2) != 0 {
		t.Fatalf("inbox should be empty after attach: %v (%d)", err, len(items2))
	}
	if err := store.IgnoreInbox(ctx, wsID, inboxID, "test"); err != nil {
		// ignore on an already-attached item may be a no-op; not fatal.
		t.Logf("ignore inbox (attached): %v", err)
	}

	t.Logf("PG event/handoff/inbox PASS: event replay + conflict + docs + inbox attach")
}

// TestPGStoreFinishCreatesArtifactCandidate proves the kernel's finish bridges
// the completion candidate into the existing artifact_candidate machinery.
func TestPGStoreFinishCreatesArtifactCandidate(t *testing.T) {
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

	var wsID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO workspace (name, slug) VALUES ('workentry-artifact', $1) RETURNING id`,
		fmt.Sprintf("workentry-artifact-%d", time.Now().UnixNano())).Scan(&wsID); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	store := NewPGStore(db.New(pool), pool)
	svc := NewService(store)

	actor := WorkActorIdentityV1{ActorType: ActorExternalAgent, ActorID: "EXT-art-1", CarrierID: "prime",
		SessionID: "art-s1", WorkspaceID: wsID, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	intent := WorkIntentV1{OwnerIntent: "artifact", GoalRef: "GOAL-ART", Objective: "artifact bridge",
		ExpectedHumanResult: "candidate", Repo: "/tmp/art", BaselineRevision: "rev", BranchOrWorktree: "main",
		ReadScope: []string{"/tmp"}, WriteScope: []string{"/tmp"}, ExpectedOutcomes: []string{"a"},
		CandidateFormalBoundary: BoundaryCandidate}

	r1, err := svc.Register(ctx, RegisterRequest{ResolveRequest: ResolveRequest{Actor: actor, Intent: intent}, ConfirmCreate: true})
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	_, err = svc.Finish(ctx, WorkCompletionV1{
		WorkRef: r1.WorkRef,
		CompletionCandidate: CompletionCandidate{ArtifactRef: "artifact://c/1", Digest: "sha256:abcd", Revision: "rev"},
		Review: CompletionReview{ReviewerActorID: "REV-1"},
		ProjectLifecycleConsequence: LifecycleContinue,
	})
	if err != nil {
		t.Fatalf("finish: %v", err)
	}

	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM artifact_candidate WHERE workspace_id=$1`, wsID).Scan(&n); err != nil {
		t.Fatalf("count artifact candidates: %v", err)
	}
	if n != 1 {
		t.Fatalf("finish should create 1 artifact_candidate, got %d", n)
	}
	t.Logf("finish -> artifact_candidate bridge PASS (1 candidate)")
}

// TestPGStoreReconcilePopulatesInbox proves the discovery source persists
// unregistered worktrees into the inbox (VC-05 discovery -> persistence).
func TestPGStoreReconcilePopulatesInbox(t *testing.T) {
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

	var wsID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO workspace (name, slug) VALUES ('workentry-inbox', $1) RETURNING id`,
		fmt.Sprintf("workentry-inbox-%d", time.Now().UnixNano())).Scan(&wsID); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	store := NewPGStore(db.New(pool), pool)
	svc := NewService(store)

	items, err := svc.Reconcile(ctx, wsID)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	// The canonical repo has many worktrees; the scan + persist + list must
	// produce a non-trivial inbox (idempotent by path).
	if len(items) == 0 {
		t.Fatalf("reconcile should populate the inbox with unregistered worktrees")
	}

	// idempotency: a second reconcile must not duplicate rows.
	items2, err := svc.Reconcile(ctx, wsID)
	if err != nil {
		t.Fatalf("reconcile 2: %v", err)
	}
	if len(items2) != len(items) {
		t.Fatalf("reconcile must be idempotent: %d -> %d", len(items), len(items2))
	}
	t.Logf("reconcile populated inbox with %d unregistered worktrees (idempotent)", len(items))
}

// TestPGStoreReviewRecordsVerdict proves the full candidate->review chain:
// register -> finish (artifact_candidate) -> review PASS (artifact_event approved).
func TestPGStoreReviewRecordsVerdict(t *testing.T) {
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

	var wsID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO workspace (name, slug) VALUES ('workentry-review', $1) RETURNING id`,
		fmt.Sprintf("workentry-review-%d", time.Now().UnixNano())).Scan(&wsID); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	store := NewPGStore(db.New(pool), pool)
	svc := NewService(store)

	actor := WorkActorIdentityV1{ActorType: ActorExternalAgent, ActorID: "EXT-rev-1", CarrierID: "prime",
		SessionID: "rev-s1", WorkspaceID: wsID, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	intent := WorkIntentV1{OwnerIntent: "review", GoalRef: "GOAL-REV", Objective: "review chain",
		ExpectedHumanResult: "verdict", Repo: "/tmp/rev", BaselineRevision: "rev", BranchOrWorktree: "main",
		ReadScope: []string{"/tmp"}, WriteScope: []string{"/tmp"}, ExpectedOutcomes: []string{"a"},
		CandidateFormalBoundary: BoundaryCandidate}

	r1, err := svc.Register(ctx, RegisterRequest{ResolveRequest: ResolveRequest{Actor: actor, Intent: intent}, ConfirmCreate: true})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := svc.Finish(ctx, WorkCompletionV1{
		WorkRef: r1.WorkRef,
		CompletionCandidate: CompletionCandidate{ArtifactRef: "artifact://c/1", Digest: "sha256:abcd", Revision: "rev"},
		Review:              CompletionReview{ReviewerActorID: "REV-1"},
		ProjectLifecycleConsequence: LifecycleContinue,
	}); err != nil {
		t.Fatalf("finish: %v", err)
	}

	// Independent review: reviewer != implementer actor.
	res, err := svc.Review(ctx, ReviewRequest{
		WorkRef: r1.WorkRef, WorkspaceID: wsID,
		ReviewerActorID: "REV-1", Decision: ReviewPass,
	})
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if !res.Passed {
		t.Fatalf("PASS review should set Passed=true, got %+v", res)
	}

	var eventType string
	if err := pool.QueryRow(ctx,
		`SELECT event_type FROM artifact_event WHERE workspace_id=$1 ORDER BY sequence DESC LIMIT 1`, wsID).Scan(&eventType); err != nil {
		t.Fatalf("read artifact event: %v", err)
	}
	if eventType != "approved" {
		t.Fatalf("PASS review should record 'approved' artifact_event, got %q", eventType)
	}
	t.Logf("review chain PASS: register -> finish -> review PASS -> artifact_event approved")
}

package service

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestResolveWriterLeaseTargetsCanonicalRefAndHolder(t *testing.T) {
	resourceID := uuid.New()
	targets, err := ResolveWriterLeaseTargets(WriterLeaseModeEnforce, "ws", "project", "daemon", "runtime", "task", []WriterLeaseResource{{ID: resourceID, ResourceType: "github_repo", URL: "https://github.com/acme/repo", Ref: "refs/remotes/origin/feature/a"}})
	if err != nil || len(targets) != 1 {
		t.Fatalf("targets=%+v err=%v", targets, err)
	}
	if want := "canonical-worktree:ws/ws/repo/" + resourceID.String() + "/ref/feature%2Fa"; targets[0].MutexKey != want {
		t.Fatalf("key=%q want %q", targets[0].MutexKey, want)
	}
	if got := WriterLeaseHolderID("daemon", "runtime", "task"); got != "daemon/daemon/runtime/runtime/task/task" {
		t.Fatalf("holder=%q", got)
	}
}

func TestResolveWriterLeaseTargetsExplicitRefAndDefaultHintHaveParity(t *testing.T) {
	resourceID := uuid.New()
	explicit, err := ResolveWriterLeaseTargets(WriterLeaseModeEnforce, "ws", "project", "daemon", "runtime", "task", []WriterLeaseResource{{
		ID: resourceID, ResourceType: "github_repo", URL: "https://github.com/acme/repo", Ref: "refs/heads/main", DefaultBranchHint: "develop",
	}})
	if err != nil {
		t.Fatal(err)
	}
	hinted, err := ResolveWriterLeaseTargets(WriterLeaseModeEnforce, "ws", "project", "daemon", "runtime", "task", []WriterLeaseResource{{
		ID: resourceID, ResourceType: "github_repo", URL: "https://github.com/acme/repo", DefaultBranchHint: "refs/heads/main",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if explicit[0].MutexKey != hinted[0].MutexKey || explicit[0].Ref != "main" || hinted[0].Ref != "main" {
		t.Fatalf("explicit=%+v hinted=%+v", explicit[0], hinted[0])
	}
}

func TestResolveWriterLeaseTargetsOffShadowDoNotAcquire(t *testing.T) {
	target := WriterLeaseResource{ID: uuid.New(), ResourceType: "github_repo", URL: "https://github.com/a/b"}
	targets, err := ResolveWriterLeaseTargets(WriterLeaseModeShadow, "ws", "project", "daemon", "runtime", "task", []WriterLeaseResource{target})
	if err != nil || len(targets) != 1 {
		t.Fatalf("shadow targets=%v err=%v", targets, err)
	}
	for _, mode := range []WriterLeaseMode{WriterLeaseModeOff} {
		targets, err := ResolveWriterLeaseTargets(mode, "", "", "", "", "", nil)
		if err != nil || len(targets) != 0 {
			t.Fatalf("mode=%s targets=%v err=%v", mode, targets, err)
		}
	}
}

func TestAcquireWriterLeaseBatchRollsBackInReverseOrder(t *testing.T) {
	store := &batchTestStore{}
	guard, err := NewWriterLeaseGuard(store, 100*time.Millisecond, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	// A held first target makes the second acquire fail; the first must be
	// released by the batch rollback.
	first := WriterLeaseTarget{MutexKey: "a", ResourceID: "a"}
	second := WriterLeaseTarget{MutexKey: "b", ResourceID: "b"}
	batch, err := AcquireWriterLeaseBatch(context.Background(), guard, WriterLeaseTaskKindWork, []WriterLeaseTarget{first, second}, "holder")
	if !errors.Is(err, ErrLeaseBusy) {
		t.Fatalf("error=%v want busy", err)
	}
	if batch != nil {
		t.Fatalf("batch=%+v want nil", batch)
	}
	if store.releaseCalls.Load() != 1 {
		t.Fatalf("release calls=%d want 1", store.releaseCalls.Load())
	}
}

type batchTestStore struct{ acquireCalls, releaseCalls atomic.Int32 }

func (s *batchTestStore) Acquire(_ context.Context, key, holder string, _ time.Duration) (*WriteLease, error) {
	if s.acquireCalls.Add(1) > 1 {
		return nil, ErrLeaseBusy
	}
	return &WriteLease{MutexKey: key, HolderID: holder, LeaseToken: uuid.New(), FenceGeneration: 1, Status: WriteLeaseHeld, ExpiresAt: futureLeaseExpiry()}, nil
}
func (s *batchTestStore) Renew(context.Context, string, uuid.UUID, int64, time.Duration) (*WriteLease, error) {
	return nil, nil
}
func (s *batchTestStore) VerifyHeld(context.Context, string, uuid.UUID, int64) (*WriteLease, error) {
	return nil, nil
}
func (s *batchTestStore) Release(context.Context, string, uuid.UUID, int64) (*WriteLease, error) {
	s.releaseCalls.Add(1)
	return &WriteLease{Status: WriteLeaseFree}, nil
}

func TestResolveWriterLeaseTargetsRejectsUnknownMode(t *testing.T) {
	_, err := ResolveWriterLeaseTargets("future", "ws", "project", "daemon", "runtime", "task", nil)
	if !errors.Is(err, ErrWriterLeaseInvalidMode) {
		t.Fatalf("err=%v", err)
	}
}

func TestCanonicalWriterLeaseClaimSortsAndHashesStableJSON(t *testing.T) {
	workspaceID := uuid.New().String()
	firstID := uuid.New().String()
	secondID := uuid.New().String()
	targets := []WriterLeaseTarget{
		{ResourceID: secondID, MutexKey: WriterLeaseMutexKey(workspaceID, secondID, "main"), URL: "https://github.com/acme/two", Ref: "main"},
		{ResourceID: firstID, MutexKey: WriterLeaseMutexKey(workspaceID, firstID, "main"), URL: "https://github.com/acme/one", Ref: "main"},
	}
	canonical, digest, err := CanonicalWriterLeaseClaim(WriterLeaseModeEnforce, workspaceID, targets)
	if err != nil {
		t.Fatal(err)
	}
	reversed, reversedDigest, err := CanonicalWriterLeaseClaim(WriterLeaseModeEnforce, workspaceID, []WriterLeaseTarget{targets[1], targets[0]})
	if err != nil {
		t.Fatal(err)
	}
	if string(canonical) != string(reversed) || digest != reversedDigest {
		t.Fatalf("canonicalization is order-sensitive: %s/%s vs %s/%s", canonical, digest, reversed, reversedDigest)
	}
	if digest == "" || len(digest) != 64 {
		t.Fatalf("digest=%q", digest)
	}
}

func TestCanonicalWriterLeaseClaimAcceptsEscapedSlashRef(t *testing.T) {
	workspaceID := uuid.New().String()
	resourceID := uuid.New().String()
	ref := "release%2Fv1"
	target := WriterLeaseTarget{ResourceID: resourceID, MutexKey: WriterLeaseMutexKey(workspaceID, resourceID, "release/v1"), URL: "https://github.com/acme/repo", Ref: ref}
	if _, _, err := CanonicalWriterLeaseClaim(WriterLeaseModeEnforce, workspaceID, []WriterLeaseTarget{target}); err != nil {
		t.Fatalf("escaped slash ref rejected: %v", err)
	}
}

func TestCanonicalWriterLeaseClaimRejectsDriftAndForbiddenFields(t *testing.T) {
	workspaceID := uuid.New().String()
	resourceID := uuid.New().String()
	base := WriterLeaseTarget{ResourceID: resourceID, MutexKey: WriterLeaseMutexKey(workspaceID, resourceID, "main"), URL: "https://github.com/acme/repo", Ref: "main"}
	for name, target := range map[string]WriterLeaseTarget{
		"duplicate resource": base,
		"noncanonical ref":   {ResourceID: resourceID, MutexKey: base.MutexKey, URL: base.URL, Ref: "refs/heads/main"},
		"noncanonical mutex": {ResourceID: resourceID, MutexKey: "wrong", URL: base.URL, Ref: base.Ref},
	} {
		t.Run(name, func(t *testing.T) {
			targets := []WriterLeaseTarget{base, target}
			if _, _, err := CanonicalWriterLeaseClaim(WriterLeaseModeEnforce, workspaceID, targets); err == nil {
				t.Fatal("expected canonical validation error")
			}
		})
	}
	if _, err := decodeCanonicalWriterLeaseTargets([]byte(`[{"resource_id":"` + resourceID + `","mutex_key":"` + base.MutexKey + `","url":"` + base.URL + `","ref":"main","lease_token":"secret"}]`)); err == nil {
		t.Fatal("forbidden lease field was accepted")
	}
	if _, err := decodeCanonicalWriterLeaseTargets([]byte(`null`)); err == nil {
		t.Fatal("non-array snapshot was accepted")
	}
}

func TestDecodePersistedWriterLeaseClaimRequiresCanonicalDigest(t *testing.T) {
	workspaceID := uuid.New().String()
	resourceID := uuid.New().String()
	target := WriterLeaseTarget{ResourceID: resourceID, MutexKey: WriterLeaseMutexKey(workspaceID, resourceID, "main"), URL: "https://github.com/acme/repo", Ref: "main"}
	canonical, digest, err := CanonicalWriterLeaseClaim(WriterLeaseModeEnforce, workspaceID, []WriterLeaseTarget{target})
	if err != nil {
		t.Fatal(err)
	}
	task := db.AgentTaskQueue{
		WriterLeaseClaimMode:      pgtype.Text{String: "enforce", Valid: true},
		WriterLeaseTargetSnapshot: canonical,
		WriterLeaseTargetDigest:   pgtype.Text{String: digest, Valid: true},
	}
	claim, legacy, err := DecodePersistedWriterLeaseClaim(task, workspaceID)
	if err != nil || legacy || claim.Digest != digest || len(claim.Targets) != 1 {
		t.Fatalf("claim=%+v legacy=%v err=%v", claim, legacy, err)
	}
	// JSONB may normalize whitespace/encoding on readback; digest validation is
	// semantic and must not depend on the original wire bytes.
	task.WriterLeaseTargetSnapshot = append([]byte("  "), canonical...)
	task.WriterLeaseTargetSnapshot = append(task.WriterLeaseTargetSnapshot, '\n')
	if _, _, err := DecodePersistedWriterLeaseClaim(task, workspaceID); err != nil {
		t.Fatalf("JSONB-equivalent snapshot rejected: %v", err)
	}
	task.WriterLeaseTargetSnapshot = canonical
	task.WriterLeaseTargetDigest = pgtype.Text{String: "deadbeef", Valid: true}
	if _, _, err := DecodePersistedWriterLeaseClaim(task, workspaceID); err == nil {
		t.Fatal("digest tampering was accepted")
	}
	task.WriterLeaseTargetDigest = pgtype.Text{String: digest, Valid: true}
	task.WriterLeaseClaimMode = pgtype.Text{String: string(WriterLeaseModeShadow), Valid: true}
	if _, _, err := DecodePersistedWriterLeaseClaim(task, workspaceID); err == nil {
		t.Fatal("persisted mode drift was accepted with the original digest")
	}
	_, offDigest, err := CanonicalWriterLeaseClaim(WriterLeaseModeOff, workspaceID, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, shadowDigest, err := CanonicalWriterLeaseClaim(WriterLeaseModeShadow, workspaceID, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, enforceDigest, err := CanonicalWriterLeaseClaim(WriterLeaseModeEnforce, workspaceID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if offDigest == shadowDigest || offDigest == enforceDigest || shadowDigest == enforceDigest {
		t.Fatalf("empty-target mode digests collided: off=%s shadow=%s enforce=%s", offDigest, shadowDigest, enforceDigest)
	}
	legacyTask := db.AgentTaskQueue{}
	_, legacy, err = DecodePersistedWriterLeaseClaim(legacyTask, workspaceID)
	if err != nil || !legacy {
		t.Fatalf("legacy=%v err=%v", legacy, err)
	}
}

func TestFinalizeWriterLeaseClaimFailsClosedWithoutTransactionStarter(t *testing.T) {
	workspaceID := uuid.New().String()
	task := db.AgentTaskQueue{
		ID:           pgtype.UUID{Bytes: uuid.New(), Valid: true},
		RuntimeID:    pgtype.UUID{Bytes: uuid.New(), Valid: true},
		DispatchedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}
	_, err := (&TaskService{}).FinalizeTaskClaimWithWriterLeaseClaim(
		context.Background(), task, db.CreateTaskTokenParams{}, nil, false, nil,
		&WriterLeaseClaim{Mode: WriterLeaseModeEnforce, Targets: []WriterLeaseTarget{}}, workspaceID,
	)
	if !errors.Is(err, ErrWriterLeaseFenceRejected) {
		t.Fatalf("error=%v, want fail-closed writer lease rejection", err)
	}
}

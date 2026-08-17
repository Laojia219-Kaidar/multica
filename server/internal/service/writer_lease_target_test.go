package service

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
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

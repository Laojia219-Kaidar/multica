package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/service"
)

func TestRemoteWriterLeaseStoreBindsResourceAndDoesNotSendKeyHolder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if _, ok := body["mutex_key"]; ok {
			t.Fatal("mutex_key must not cross daemon API")
		}
		if _, ok := body["holder_id"]; ok {
			t.Fatal("holder_id must not cross daemon API")
		}
		if body["resource_id"] != "resource-1" {
			t.Fatalf("body=%v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"lease": map[string]any{"mutex_key": "server-key", "holder_id": "server-holder", "status": "held", "fence_generation": 1}})
	}))
	defer server.Close()
	store := NewRemoteWriterLeaseStore(NewClient(server.URL), "runtime-1", "task-1", []WriterLeaseTarget{{ResourceID: "resource-1", MutexKey: "client-key"}})
	lease, err := store.Acquire(context.Background(), "client-key", "caller-holder", 0)
	if err != nil {
		t.Fatal(err)
	}
	if lease.MutexKey != "server-key" || strings.Contains(lease.HolderID, "caller") {
		t.Fatalf("lease=%+v", lease)
	}
	if got := lease.FenceGeneration; got != 1 {
		t.Fatalf("generation=%d", got)
	}
}

func TestWriterLeaseCheckoutSelectsExactTargetAndRejectsUnknownURL(t *testing.T) {
	store := &daemonWriterLeaseStoreTest{}
	guard, err := service.NewWriterLeaseGuard(store, time.Second, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	first, err := guard.AcquireForExecution(context.Background(), service.WriterLeaseTaskKindWork, "key-a", "holder")
	if err != nil {
		t.Fatal(err)
	}
	second, err := guard.AcquireForExecution(context.Background(), service.WriterLeaseTaskKindWork, "key-b", "holder")
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{writerLeaseModes: map[string]string{"task": string(service.WriterLeaseModeEnforce)}, writerLeaseSessions: map[string]*writerLeaseCheckoutSession{
		"task": {sessions: map[string]writerLeaseTargetSession{
			"a": {target: WriterLeaseTarget{ResourceID: "a", URL: "https://a/repo", Ref: "main"}, session: first},
			"b": {target: WriterLeaseTarget{ResourceID: "b", URL: "https://b/repo", Ref: "develop"}, session: second},
		}},
	}}
	called := ""
	if err := d.withWriterLeaseCheckout(context.Background(), "task", "https://b/repo", "refs/heads/develop", func(context.Context) error { called = "b"; return nil }); err != nil {
		t.Fatal(err)
	}
	if called != "b" {
		t.Fatalf("callback target=%q", called)
	}
	if err := d.withWriterLeaseCheckout(context.Background(), "task", "https://a/repo", "", func(context.Context) error { called = "a-default"; return nil }); err != nil {
		t.Fatal(err)
	}
	if called != "a-default" {
		t.Fatalf("default-ref callback target=%q", called)
	}
	if err := d.withWriterLeaseCheckout(context.Background(), "task", "https://unknown/repo", "main", func(context.Context) error { t.Fatal("unknown target callback"); return nil }); !errors.Is(err, service.ErrWriterLeaseStale) {
		t.Fatalf("unknown target error=%v", err)
	}
}

func TestWriterLeaseFinalVerifyRejectsSecondStaleTarget(t *testing.T) {
	store := &daemonWriterLeaseStoreTest{failVerifyKey: "key-b"}
	guard, err := service.NewWriterLeaseGuard(store, time.Second, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	first, err := guard.AcquireForExecution(context.Background(), service.WriterLeaseTaskKindWork, "key-a", "holder")
	if err != nil {
		t.Fatal(err)
	}
	second, err := guard.AcquireForExecution(context.Background(), service.WriterLeaseTaskKindWork, "key-b", "holder")
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{writerLeaseSessions: map[string]*writerLeaseCheckoutSession{"task": {sessions: map[string]writerLeaseTargetSession{
		"a": {target: WriterLeaseTarget{ResourceID: "a", MutexKey: "key-a", URL: "https://a/repo", Ref: "main"}, session: first},
		"b": {target: WriterLeaseTarget{ResourceID: "b", MutexKey: "key-b", URL: "https://b/repo", Ref: "develop"}, session: second},
	}}}}
	if err := d.writerLeaseVerifyAll(context.Background(), "task"); err == nil {
		t.Fatal("final verify succeeded after second target was fenced")
	}
}

func TestWriterLeaseUnknownKindAndStateCleanup(t *testing.T) {
	for _, kind := range []string{"", "unknown", "future"} {
		if writerLeaseTaskKindAllowed(kind) {
			t.Fatalf("kind %q unexpectedly allowed", kind)
		}
	}
	d := &Daemon{writerLeaseModes: map[string]string{"task": string(service.WriterLeaseModeEnforce)}, writerLeaseSessions: map[string]*writerLeaseCheckoutSession{"task": {}}}
	d.clearWriterLeaseState("task")
	if _, ok := d.writerLeaseModes["task"]; ok {
		t.Fatal("writer lease mode retained after cleanup")
	}
	if _, ok := d.writerLeaseSessions["task"]; ok {
		t.Fatal("writer lease sessions retained after cleanup")
	}
}

func TestWriterLeaseEnforceMissingBundleFailsClosedWithoutCallback(t *testing.T) {
	d := &Daemon{writerLeaseModes: map[string]string{"task": string(service.WriterLeaseModeEnforce)}}
	called := 0
	if err := d.withWriterLeaseExecution(context.Background(), "task", func(context.Context) error { called++; return nil }); !errors.Is(err, service.ErrWriterLeaseStale) {
		t.Fatalf("execution error=%v", err)
	}
	if called != 0 {
		t.Fatalf("execution callback called %d times", called)
	}
	if err := d.writerLeaseVerifyAll(context.Background(), "task"); !errors.Is(err, service.ErrWriterLeaseStale) {
		t.Fatalf("final verify error=%v", err)
	}
	if !d.writerLeaseWasLost("task") {
		t.Fatal("missing enforce bundle was not treated as lost")
	}
	d.writerLeaseSessions = map[string]*writerLeaseCheckoutSession{"task": {sessions: map[string]writerLeaseTargetSession{}}}
	if err := d.withWriterLeaseExecution(context.Background(), "task", func(context.Context) error { called++; return nil }); !errors.Is(err, service.ErrWriterLeaseStale) {
		t.Fatalf("empty bundle execution error=%v", err)
	}
	if called != 0 {
		t.Fatalf("empty bundle execution callback called %d times", called)
	}
	if err := d.writerLeaseVerifyAll(context.Background(), "task"); !errors.Is(err, service.ErrWriterLeaseStale) {
		t.Fatalf("empty bundle final verify error=%v", err)
	}
	if !d.writerLeaseWasLost("task") {
		t.Fatal("empty enforce bundle was not treated as lost")
	}
}

func TestWriterLeaseOffAndShadowRegisterAndReleaseLifecycle(t *testing.T) {
	for _, mode := range []string{string(service.WriterLeaseModeOff), string(service.WriterLeaseModeShadow)} {
		d := &Daemon{}
		release, abort := d.acquireWriterLeaseForTask(context.Background(), Task{ID: "task", WriterLeaseMode: mode, TaskKind: string(service.WriterLeaseTaskKindWork), WriterLeaseTargets: []WriterLeaseTarget{{ResourceID: "r", MutexKey: "k"}}}, func() {}, slog.Default())
		if release == nil || abort {
			t.Fatalf("mode=%s release=%v abort=%v", mode, release != nil, abort)
		}
		if got := d.writerLeaseModes["task"]; got != mode {
			t.Fatalf("mode=%s registered=%q", mode, got)
		}
		release()
		if _, ok := d.writerLeaseModes["task"]; ok {
			t.Fatalf("mode=%s registration retained after release", mode)
		}
	}
}

type daemonWriterLeaseStoreTest struct {
	failVerifyKey string
}

func (s *daemonWriterLeaseStoreTest) Acquire(_ context.Context, key, holder string, _ time.Duration) (*service.WriteLease, error) {
	return &service.WriteLease{MutexKey: key, HolderID: holder, LeaseToken: uuid.New(), FenceGeneration: 1, Status: service.WriteLeaseHeld, ExpiresAt: daemonLeaseExpiry()}, nil
}
func (s *daemonWriterLeaseStoreTest) Renew(_ context.Context, key string, token uuid.UUID, generation int64, _ time.Duration) (*service.WriteLease, error) {
	return &service.WriteLease{MutexKey: key, HolderID: "holder", LeaseToken: token, FenceGeneration: generation, Status: service.WriteLeaseHeld, ExpiresAt: daemonLeaseExpiry()}, nil
}
func (s *daemonWriterLeaseStoreTest) VerifyHeld(_ context.Context, key string, token uuid.UUID, generation int64) (*service.WriteLease, error) {
	if key == s.failVerifyKey {
		return nil, service.ErrLeaseNotHeld
	}
	return &service.WriteLease{MutexKey: key, HolderID: "holder", LeaseToken: token, FenceGeneration: generation, Status: service.WriteLeaseHeld, ExpiresAt: daemonLeaseExpiry()}, nil
}

func daemonLeaseExpiry() *time.Time {
	expires := time.Now().Add(time.Minute)
	return &expires
}
func (s *daemonWriterLeaseStoreTest) Release(_ context.Context, key string, token uuid.UUID, generation int64) (*service.WriteLease, error) {
	return &service.WriteLease{MutexKey: key, LeaseToken: token, FenceGeneration: generation, Status: service.WriteLeaseFree}, nil
}

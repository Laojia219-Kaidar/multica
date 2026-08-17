package daemon

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/mutationbroker"
)

func TestMutationHookServeFailureKillSeam(t *testing.T) {
	var mu sync.Mutex
	killed := 0
	h := &codexMutationLaunchHook{kill: func(pid int) error {
		mu.Lock()
		defer mu.Unlock()
		killed = pid
		return nil
	}, startTime: func(int) (uint64, error) { return 7, nil }}
	state := &mutationLaunchState{generation: 4, pid: 1234, rootStartTime: 7}
	h.serveState = map[uint64]*mutationLaunchState{state.generation: state}
	h.handleServeFailure(state, errors.New("accept failed"))
	if killed != 1234 {
		t.Fatalf("unexpected killed pid=%d", killed)
	}
	h.handleServeFailure(state, context.Canceled)
	h.handleServeFailure(state, mutationbroker.ErrNotBound)
	if killed != 1234 {
		t.Fatalf("expected expected-revoke errors not to kill another pid: %d", killed)
	}
	delete(h.serveState, state.generation)
	h.handleServeFailure(state, errors.New("removed generation"))
	if killed != 1234 {
		t.Fatalf("removed generation was killed: %d", killed)
	}
	h.serveState[state.generation] = &mutationLaunchState{generation: state.generation, pid: state.pid, rootStartTime: 7}
	h.handleServeFailure(state, errors.New("replaced generation"))
	if killed != 1234 {
		t.Fatalf("replaced generation was killed: %d", killed)
	}
	h.serveState[state.generation] = state
	h.startTime = func(int) (uint64, error) { return 8, nil }
	h.handleServeFailure(state, errors.New("pid reused"))
	if killed != 1234 {
		t.Fatalf("starttime mismatch was killed: %d", killed)
	}
}

func TestMutationHookJoinStateBoundedKillAndSecondWait(t *testing.T) {
	done := make(chan struct{})
	killed := make(chan struct{})
	h := &codexMutationLaunchHook{
		joinWindow: time.Millisecond,
		startTime:  func(int) (uint64, error) { return 7, nil },
		kill: func(int) error {
			close(killed)
			close(done)
			return nil
		},
	}
	state := &mutationLaunchState{generation: 1, pid: 1234, rootStartTime: 7, done: done}
	h.serveState = map[uint64]*mutationLaunchState{1: state}
	if err := h.joinState(context.Background(), state); err != nil {
		t.Fatalf("join after kill: %v", err)
	}
	select {
	case <-killed:
	default:
		t.Fatal("expected kill after first join window")
	}
}

func TestMutationHookJoinStateCanKillDuringClose(t *testing.T) {
	done := make(chan struct{})
	killed := 0
	state := &mutationLaunchState{generation: 2, pid: 1234, rootStartTime: 7, done: done}
	h := &codexMutationLaunchHook{
		closed:     true,
		joinWindow: time.Millisecond,
		startTime:  func(int) (uint64, error) { return 7, nil },
		kill:       func(int) error { killed++; return nil },
		serveState: map[uint64]*mutationLaunchState{2: state},
	}
	if err := h.joinState(context.Background(), state); err == nil {
		t.Fatal("unjoined close state unexpectedly completed")
	}
	if killed != 1 {
		t.Fatalf("close join kill count=%d, want 1", killed)
	}
}

func TestMutationHookJoinStateReportsUnjoined(t *testing.T) {
	h := &codexMutationLaunchHook{
		joinWindow: time.Millisecond,
		startTime:  func(int) (uint64, error) { return 7, nil },
		kill:       func(int) error { return nil },
	}
	state := &mutationLaunchState{generation: 1, pid: 1234, rootStartTime: 7, done: make(chan struct{})}
	h.serveState = map[uint64]*mutationLaunchState{1: state}
	err := h.joinState(context.Background(), state)
	if err == nil || err.Error() != "mutation transport serve join timeout" {
		t.Fatalf("join error = %v, want bounded join timeout", err)
	}
}

func TestMutationHookJoinStateHonorsContext(t *testing.T) {
	h := &codexMutationLaunchHook{
		joinWindow: time.Second,
		startTime:  func(int) (uint64, error) { return 7, nil },
		kill: func(int) error {
			t.Fatal("canceled join must not kill")
			return nil
		},
	}
	state := &mutationLaunchState{generation: 1, pid: 1234, rootStartTime: 7, done: make(chan struct{})}
	h.serveState = map[uint64]*mutationLaunchState{1: state}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := h.joinState(ctx, state); !errors.Is(err, context.Canceled) {
		t.Fatalf("join error = %v, want context.Canceled", err)
	}
}

func TestMutationHookJoinTimeoutDoesNotKillStaleState(t *testing.T) {
	killed := 0
	state := &mutationLaunchState{generation: 9, pid: 1234, rootStartTime: 7, done: make(chan struct{})}
	h := &codexMutationLaunchHook{
		joinWindow: time.Millisecond,
		startTime:  func(int) (uint64, error) { return 8, nil },
		kill:       func(int) error { killed++; return nil },
		serveState: map[uint64]*mutationLaunchState{state.generation: state},
	}
	if err := h.joinState(context.Background(), state); err == nil {
		t.Fatal("stale PID unexpectedly joined")
	}
	if killed != 0 {
		t.Fatalf("starttime mismatch killed pid: %d", killed)
	}
	delete(h.serveState, state.generation)
	h.startTime = func(int) (uint64, error) { return 7, nil }
	if err := h.joinState(context.Background(), state); err == nil {
		t.Fatal("removed state unexpectedly joined")
	}
	if killed != 0 {
		t.Fatalf("removed state killed pid: %d", killed)
	}
}

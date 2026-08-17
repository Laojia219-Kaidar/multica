package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/mutationbroker"
	"github.com/multica-ai/multica/server/internal/daemon/repocache"
	"github.com/multica-ai/multica/server/pkg/agent"
)

// codexMutationLaunchHook owns one daemon-side endpoint but scopes every
// Prepare/Bind/Cleanup operation to the generation returned by Prepare. This
// matters when Codex's internal retry starts a fresh process before a delayed
// cleanup callback from the failed attempt runs.
type codexMutationLaunchHook struct {
	endpoint   *mutationbroker.Endpoint
	handler    mutationbroker.Handler
	mu         sync.Mutex
	closed     bool
	serveDone  map[uint64]chan struct{}
	serveState map[uint64]*mutationLaunchState
	kill       func(int) error
	startTime  func(int) (uint64, error)
	joinWindow time.Duration
}

type mutationLaunchState struct {
	generation    uint64
	pid           int
	cancel        context.CancelFunc
	done          chan struct{}
	serveErr      error
	rootStartTime uint64
}

var errStaleMutationReplay = errors.New("stale checkout replay")

type checkoutAuthorizer interface {
	Authorize(mutationbroker.CheckoutRequest) (mutationbroker.Decision, error)
	Complete(mutationbroker.CheckoutRequest, []byte) error
	Abort(mutationbroker.CheckoutRequest) error
	InvalidateReplay(mutationbroker.CheckoutRequest) error
}

type capabilityCheckoutAuthorizer struct {
	registry   *mutationbroker.Registry
	capability string
}

func (a capabilityCheckoutAuthorizer) Authorize(req mutationbroker.CheckoutRequest) (mutationbroker.Decision, error) {
	return a.registry.Authorize(a.capability, req)
}
func (a capabilityCheckoutAuthorizer) Complete(req mutationbroker.CheckoutRequest, result []byte) error {
	return a.registry.Complete(a.capability, req, result)
}
func (a capabilityCheckoutAuthorizer) Abort(req mutationbroker.CheckoutRequest) error {
	return a.registry.Abort(a.capability, req)
}
func (a capabilityCheckoutAuthorizer) InvalidateReplay(req mutationbroker.CheckoutRequest) error {
	return a.registry.InvalidateReplay(a.capability, req)
}

var _ agent.LaunchHook = (*codexMutationLaunchHook)(nil)

func (h *codexMutationLaunchHook) Prepare(ctx context.Context) (agent.LaunchHookAttempt, error) {
	if h == nil || h.endpoint == nil || h.handler == nil {
		return agent.LaunchHookAttempt{}, mutationbroker.ErrUnsupported
	}
	h.mu.Lock()
	closed := h.closed
	h.mu.Unlock()
	if closed || ctx == nil || ctx.Err() != nil {
		return agent.LaunchHookAttempt{}, context.Canceled
	}
	prepared, err := h.endpoint.PrepareRunner()
	if err != nil {
		return agent.LaunchHookAttempt{}, err
	}
	return agent.LaunchHookAttempt{
		Generation: prepared.Generation,
		Env: map[string]string{
			"MULTICA_REPO_CHECKOUT_TRANSPORT": "unix-seqpacket-v1",
			"MULTICA_REPO_CHECKOUT_LOCATOR":   prepared.Locator,
		},
		State: &mutationLaunchState{generation: prepared.Generation},
	}, nil
}

func (h *codexMutationLaunchHook) Bind(ctx context.Context, attempt agent.LaunchHookAttempt, pid int) error {
	state, ok := attempt.State.(*mutationLaunchState)
	if !ok || state == nil || state.generation != attempt.Generation || pid <= 0 {
		return mutationbroker.ErrRunnerUnauthorized
	}
	start, err := mutationbroker.ProcessStartTime(pid)
	if err != nil {
		return mutationbroker.ErrRunnerUnauthorized
	}
	if err := h.endpoint.BindPreparedRunner(state.generation, pid, start); err != nil {
		return err
	}
	serveCtx, cancel := context.WithCancel(ctx)
	state.cancel = cancel
	done := make(chan struct{})
	state.pid = pid
	state.done = done
	state.rootStartTime = start
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		cancel()
		_ = h.endpoint.RevokeGeneration(state.generation)
		return mutationbroker.ErrClosed
	}
	if h.serveDone == nil {
		h.serveDone = make(map[uint64]chan struct{})
	}
	if h.serveState == nil {
		h.serveState = make(map[uint64]*mutationLaunchState)
	}
	h.serveDone[state.generation] = done
	h.serveState[state.generation] = state
	h.mu.Unlock()
	go func() {
		defer close(done)
		err := h.endpoint.Serve(state.generation, serveCtx, h.handler)
		state.serveErr = err
		h.handleServeFailure(state, err)
	}()
	return nil
}

func (h *codexMutationLaunchHook) Cleanup(ctx context.Context, attempt agent.LaunchHookAttempt) error {
	state, ok := attempt.State.(*mutationLaunchState)
	if !ok || state == nil || state.generation != attempt.Generation {
		return mutationbroker.ErrNotBound
	}
	if state.cancel != nil {
		state.cancel()
	}
	if ctx == nil {
		ctx = context.Background()
	}
	err := h.endpoint.RevokeGeneration(state.generation)
	joinErr := h.joinState(ctx, state)
	if joinErr == nil {
		h.mu.Lock()
		if h.serveState[state.generation] == state {
			delete(h.serveDone, state.generation)
			delete(h.serveState, state.generation)
		}
		h.mu.Unlock()
	}
	if err != nil {
		return err
	}
	return joinErr
}

func (h *codexMutationLaunchHook) killRunnerIfCurrent(state *mutationLaunchState) error {
	if state == nil || state.pid <= 0 || state.rootStartTime == 0 {
		return nil
	}
	h.mu.Lock()
	if h.serveState[state.generation] != state {
		h.mu.Unlock()
		return nil
	}
	startTime := h.startTime
	kill := h.kill
	expectedStart := state.rootStartTime
	pid := state.pid
	h.mu.Unlock()
	if startTime == nil {
		startTime = mutationbroker.ProcessStartTime
	}
	start, err := startTime(pid)
	if err != nil || start != expectedStart {
		return nil
	}
	// Revalidate ownership and identity immediately before the destructive
	// operation so cleanup/rotation cannot redirect this kill to a later
	// generation or a reused PID.
	h.mu.Lock()
	if h.serveState[state.generation] != state {
		h.mu.Unlock()
		return nil
	}
	kill = h.kill
	startTime = h.startTime
	h.mu.Unlock()
	if startTime == nil {
		startTime = mutationbroker.ProcessStartTime
	}
	start, err = startTime(pid)
	if err != nil || start != expectedStart {
		return nil
	}
	if kill == nil {
		kill = killRunnerProcessGroup
	}
	return kill(pid)
}

func (h *codexMutationLaunchHook) joinState(ctx context.Context, state *mutationLaunchState) error {
	if state == nil || state.done == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	window := h.joinWindow
	if window <= 0 {
		window = 5 * time.Second
	}
	wait := func() error {
		timer := time.NewTimer(window)
		defer timer.Stop()
		select {
		case <-state.done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return errors.New("mutation transport serve join timeout")
		}
	}
	if err := wait(); err == nil {
		return nil
	} else if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && err.Error() != "mutation transport serve join timeout" {
		return err
	} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	_ = h.killRunnerIfCurrent(state)
	return wait()
}

func (h *codexMutationLaunchHook) handleServeFailure(state *mutationLaunchState, err error) {
	if state == nil || err == nil || errors.Is(err, context.Canceled) || errors.Is(err, mutationbroker.ErrNotBound) || errors.Is(err, mutationbroker.ErrClosed) {
		return
	}
	h.mu.Lock()
	current := !h.closed && h.serveState[state.generation] == state
	h.mu.Unlock()
	if !current {
		return
	}
	_ = h.killRunnerIfCurrent(state)
}

func (h *codexMutationLaunchHook) Close() error {
	if h == nil || h.endpoint == nil {
		return nil
	}
	h.mu.Lock()
	h.closed = true
	states := make([]*mutationLaunchState, 0, len(h.serveState))
	for _, state := range h.serveState {
		states = append(states, state)
	}
	h.mu.Unlock()
	var firstErr error
	for _, state := range states {
		if state.cancel != nil {
			state.cancel()
		}
		if err := h.endpoint.RevokeGeneration(state.generation); err != nil && firstErr == nil {
			firstErr = err
		}
		if err := h.joinState(context.Background(), state); err != nil && firstErr == nil {
			firstErr = err
		} else if err == nil {
			h.mu.Lock()
			if h.serveState[state.generation] == state {
				delete(h.serveDone, state.generation)
				delete(h.serveState, state.generation)
			}
			h.mu.Unlock()
		}
	}
	if err := h.endpoint.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// mutationCheckoutHandler is the Unix transport adapter. The task/runtime /
// workspace/workdir/agent identity is supplied by the daemon closure; fields
// supplied by the client are only request data and are checked against the
// daemon-private Authority.
func (d *Daemon) mutationCheckoutHandler(authority *mutationbroker.Authority, taskID, runtimeID, workspaceID, workDir, agentName, checkoutMode string) mutationbroker.Handler {
	return func(ctx context.Context, request mutationbroker.Request) (json.RawMessage, error) {
		if request.Operation != mutationbroker.OperationRepoCheckout || request.RequestID == "" {
			return nil, mutationbroker.ErrProtocol
		}
		var supplied mutationbroker.CheckoutRequest
		decoder := json.NewDecoder(strings.NewReader(string(request.Payload)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&supplied); err != nil {
			return nil, mutationbroker.ErrProtocol
		}
		actual := supplied
		actual.TaskID, actual.RuntimeID, actual.WorkspaceID = taskID, runtimeID, workspaceID
		actual.WorkDir, actual.AgentName = workDir, agentName
		actual.Operation, actual.RequestID, actual.CheckoutMode = request.Operation, request.RequestID, checkoutMode
		checkoutRef := strings.TrimSpace(actual.Ref)
		if checkoutRef == "" {
			checkoutRef = d.taskRepoDefaultRef(workspaceID, taskID, actual.URL)
			actual.Ref = checkoutRef
		}
		return d.executeAuthorizedCheckout(ctx, actual, authority)
	}
}

// executeAuthorizedCheckout is the single capability/Authority checkout
// executor. HTTP only parses and maps errors; Unix only binds task identity.
func (d *Daemon) executeAuthorizedCheckout(ctx context.Context, req mutationbroker.CheckoutRequest, auth checkoutAuthorizer) ([]byte, error) {
	checkoutRef := strings.TrimSpace(req.Ref)
	if checkoutRef == "" {
		checkoutRef = d.taskRepoDefaultRef(req.WorkspaceID, req.TaskID, req.URL)
		req.Ref = checkoutRef
	}
	decision, err := auth.Authorize(req)
	if err != nil {
		return nil, err
	}
	if decision.Replay != nil {
		var replay repocache.WorktreeResult
		if json.Unmarshal(decision.Replay, &replay) != nil || replay.Path == "" || repocache.ValidateWorktreePath(req.WorkDir, replay.Path) != nil {
			_ = auth.InvalidateReplay(req)
			return nil, errStaleMutationReplay
		}
		info, statErr := os.Stat(replay.Path)
		if statErr != nil || !info.IsDir() {
			_ = auth.InvalidateReplay(req)
			return nil, errStaleMutationReplay
		}
		return decision.Replay, nil
	}
	completed := false
	defer func() {
		if decision.Acquired && !completed {
			_ = auth.Abort(req)
		}
	}()
	result, err := d.checkoutWithWriterLease(ctx, req.TaskID, req.URL, checkoutRef, func(mutationCtx context.Context) (*repocache.WorktreeResult, error) {
		if d.writerLeaseMode(req.TaskID) == "enforce" && req.CheckoutMode != "isolated" {
			return nil, errors.New("enforce checkout requires mediated isolated mode")
		}
		if err := d.ensureRepoReady(mutationCtx, req.WorkspaceID, req.URL); err != nil {
			return nil, err
		}
		params := repocache.WorktreeParams{
			WorkspaceID: req.WorkspaceID, RepoURL: req.URL, WorkDir: req.WorkDir,
			Ref: checkoutRef, AgentName: req.AgentName, TaskID: req.TaskID,
			CoAuthoredByEnabled: d.workspaceCoAuthoredByEnabled(req.WorkspaceID),
			IsolatedGitMetadata: req.CheckoutMode == "isolated",
			Mediated:            d.writerLeaseMode(req.TaskID) == "enforce",
			NoPush:              d.writerLeaseMode(req.TaskID) == "enforce",
		}
		if contextual, ok := d.repoCache.(interface {
			CreateWorktreeContext(context.Context, repocache.WorktreeParams) (*repocache.WorktreeResult, error)
		}); ok {
			worktree, createErr := contextual.CreateWorktreeContext(mutationCtx, params)
			if createErr != nil {
				return nil, createErr
			}
			if d.writerLeaseMode(req.TaskID) == "enforce" {
				return d.sanitizeMutationResult(mutationCtx, worktree)
			}
			return worktree, nil
		}
		if d.writerLeaseMode(req.TaskID) == "enforce" {
			return nil, errors.New("enforce checkout requires context-aware repo cache")
		}
		if err := mutationCtx.Err(); err != nil {
			return nil, err
		}
		return d.repoCache.CreateWorktree(params)
	})
	if err != nil {
		return nil, err
	}
	if result == nil || result.Path == "" {
		return nil, errors.New("checkout result unavailable")
	}
	if err := ctx.Err(); err != nil {
		if result.Created {
			_ = removeCheckout(result.Path)
		}
		return nil, err
	}
	if err := d.writerLeaseVerifyAll(ctx, req.TaskID); err != nil {
		if result.Created {
			_ = removeCheckout(result.Path)
		}
		return nil, err
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	if err := auth.Complete(req, encoded); err != nil {
		if result.Created {
			_ = removeCheckout(result.Path)
		}
		return nil, err
	}
	completed = true
	return encoded, nil
}

func (d *Daemon) sanitizeMutationResult(ctx context.Context, result *repocache.WorktreeResult) (*repocache.WorktreeResult, error) {
	if result == nil || result.Path == "" {
		return nil, errors.New("checkout result unavailable")
	}
	if sanitizer, ok := d.repoCache.(interface {
		SanitizeMediatedWorktree(context.Context, string) error
	}); ok {
		if err := sanitizer.SanitizeMediatedWorktree(ctx, result.Path); err != nil {
			if result.Created {
				_ = removeCheckout(result.Path)
			}
			return nil, err
		}
		return result, nil
	}
	if err := disableWorktreePushRemote(ctx, result.Path); err != nil {
		if result.Created {
			_ = removeCheckout(result.Path)
		}
		return nil, err
	}
	return result, nil
}

func removeCheckout(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("empty checkout path")
	}
	return os.RemoveAll(path)
}

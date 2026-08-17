package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/service"
)

// remoteWriterLeaseStore binds migration-262 lease operations to one claimed
// runtime/task and project-resource target set. It deliberately does not send
// mutex keys or holder ids: the server recomputes both from authenticated
// daemon identity and the task/resource relationship.
type remoteWriterLeaseStore struct {
	client    *Client
	runtimeID string
	taskID    string
	targets   map[string]string // server-resolved mutex key -> resource id
}

type writerLeaseCheckoutSession struct {
	sessions map[string]writerLeaseTargetSession
	stale    atomic.Bool
}

type writerLeaseTargetSession struct {
	target  WriterLeaseTarget
	session *service.WriterLeaseSession
}

func (d *Daemon) clearWriterLeaseState(taskID string) {
	d.mu.Lock()
	delete(d.writerLeaseSessions, taskID)
	delete(d.writerLeaseModes, taskID)
	d.mu.Unlock()
}

func (b *writerLeaseCheckoutSession) allStale() bool {
	if b == nil {
		return true
	}
	if b.stale.Load() || len(b.sessions) == 0 {
		return true
	}
	for _, target := range b.sessions {
		if target.session == nil || target.session.Stale() {
			return true
		}
	}
	return false
}

func (b *writerLeaseCheckoutSession) sessionFor(url, ref string) (*service.WriterLeaseSession, error) {
	if b == nil || b.allStale() {
		return nil, service.ErrWriterLeaseStale
	}
	url = strings.TrimSpace(url)
	ref = strings.TrimSpace(ref)
	var found *service.WriterLeaseSession
	for _, target := range b.sessions {
		if strings.TrimSpace(target.target.URL) != url {
			continue
		}
		// A single authoritative target can supply its server-resolved default
		// ref when the checkout caller omitted one. Same-URL multi-target tasks
		// must provide a ref to avoid ambiguous session selection.
		if ref != "" && target.target.Ref != service.NormalizeWriterLeaseRef(ref, "") {
			continue
		}
		if found != nil {
			return nil, service.ErrWriterLeaseStale
		}
		found = target.session
	}
	if found == nil || found.Stale() {
		return nil, service.ErrWriterLeaseStale
	}
	return found, nil
}

func writerLeaseTaskKindAllowed(kind string) bool {
	switch service.WriterLeaseTaskKind(strings.TrimSpace(kind)) {
	case service.WriterLeaseTaskKindWork, service.WriterLeaseTaskKindRepair, service.WriterLeaseTaskKindReview:
		return true
	default:
		return false
	}
}

func (d *Daemon) writerLeaseWasLost(taskID string) bool {
	d.mu.Lock()
	mode := d.writerLeaseModes[taskID]
	bundle := d.writerLeaseSessions[taskID]
	d.mu.Unlock()
	return mode == string(service.WriterLeaseModeEnforce) && (bundle == nil || bundle.allStale())
}

// writerLeaseVerifyAll performs a final synchronous fence check immediately
// before CompleteTask. It narrows the stale window for every claimed target,
// but does not atomically couple filesystem mutation or CompleteTask to the
// database generation: force-cancel/expiry between this check and completion
// remains an explicit VC04 P0 boundary.
func (d *Daemon) writerLeaseVerifyAll(ctx context.Context, taskID string) error {
	d.mu.Lock()
	mode := d.writerLeaseModes[taskID]
	bundle := d.writerLeaseSessions[taskID]
	d.mu.Unlock()
	if mode == string(service.WriterLeaseModeEnforce) && (bundle == nil || bundle.allStale()) {
		return service.ErrWriterLeaseStale
	}
	if bundle == nil {
		return nil
	}
	if bundle.allStale() {
		return service.ErrWriterLeaseStale
	}
	keys := make([]string, 0, len(bundle.sessions))
	for key := range bundle.sessions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		target := bundle.sessions[key]
		if target.session == nil {
			return service.ErrWriterLeaseStale
		}
		if err := target.session.WithMutation(ctx, func(context.Context) error { return nil }); err != nil {
			return err
		}
	}
	return nil
}

// writerLeaseTerminalProof snapshots the daemon-private token/generation pair
// for every claimed resource after a final local check. The server remains the
// authority: it re-resolves the target set and validates these values under
// the task/lease-row transaction lock.
func (d *Daemon) writerLeaseTerminalProof(ctx context.Context, taskID string) ([]WriterLeaseTerminalProof, error) {
	d.mu.Lock()
	mode := d.writerLeaseModes[taskID]
	bundle := d.writerLeaseSessions[taskID]
	d.mu.Unlock()
	if mode != string(service.WriterLeaseModeEnforce) {
		return nil, nil
	}
	if bundle == nil || bundle.allStale() {
		return nil, service.ErrWriterLeaseStale
	}
	resourceIDs := make([]string, 0, len(bundle.sessions))
	for resourceID := range bundle.sessions {
		resourceIDs = append(resourceIDs, resourceID)
	}
	sort.Strings(resourceIDs)
	proof := make([]WriterLeaseTerminalProof, 0, len(resourceIDs))
	for _, resourceID := range resourceIDs {
		target := bundle.sessions[resourceID]
		if target.session == nil {
			return nil, service.ErrWriterLeaseStale
		}
		if err := target.session.WithMutation(ctx, func(context.Context) error { return nil }); err != nil {
			return nil, err
		}
		token, generation, err := target.session.TerminalProof()
		if err != nil {
			return nil, err
		}
		proof = append(proof, WriterLeaseTerminalProof{ResourceID: resourceID, LeaseToken: token, FenceGeneration: generation})
	}
	return proof, nil
}

func (d *Daemon) withWriterLeaseCheckout(ctx context.Context, taskID, repoURL, ref string, fn func(context.Context) error) error {
	d.mu.Lock()
	mode := d.writerLeaseModes[taskID]
	bundle := d.writerLeaseSessions[taskID]
	d.mu.Unlock()
	if mode != string(service.WriterLeaseModeEnforce) {
		return fn(ctx)
	}
	session, err := bundle.sessionFor(repoURL, ref)
	if err != nil {
		return err
	}
	return session.WithMutation(ctx, fn)
}

// withWriterLeaseExecution holds every claimed target's mutation boundary for
// the real runner lifetime. Heartbeats share the session read-use lock, so
// they can renew while the runner is active; terminal release waits for this
// boundary to exit before releasing any token.
func (d *Daemon) withWriterLeaseExecution(ctx context.Context, taskID string, fn func(context.Context) error) error {
	d.mu.Lock()
	mode := d.writerLeaseModes[taskID]
	bundle := d.writerLeaseSessions[taskID]
	d.mu.Unlock()
	if mode != string(service.WriterLeaseModeEnforce) {
		return fn(ctx)
	}
	if bundle == nil || bundle.allStale() {
		return service.ErrWriterLeaseStale
	}
	keys := make([]string, 0, len(bundle.sessions))
	for key := range bundle.sessions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var enter func(int, context.Context) error
	enter = func(index int, execCtx context.Context) error {
		if index >= len(keys) {
			return fn(execCtx)
		}
		session := bundle.sessions[keys[index]].session
		if session == nil {
			return service.ErrWriterLeaseStale
		}
		return session.WithMutation(execCtx, func(nextCtx context.Context) error {
			return enter(index+1, nextCtx)
		})
	}
	return enter(0, ctx)
}

func (d *Daemon) acquireWriterLeaseForTask(ctx context.Context, task Task, cancel context.CancelFunc, taskLog *slog.Logger) (func(), bool) {
	if task.WriterLeaseMode != string(service.WriterLeaseModeEnforce) {
		return nil, false
	}
	d.mu.Lock()
	if d.writerLeaseModes == nil {
		d.writerLeaseModes = make(map[string]string)
	}
	d.writerLeaseModes[task.ID] = task.WriterLeaseMode
	d.mu.Unlock()
	cleanupState := true
	defer func() {
		if cleanupState {
			d.clearWriterLeaseState(task.ID)
		}
	}()
	kind := service.WriterLeaseTaskKind(task.TaskKind)
	if !writerLeaseTaskKindAllowed(task.TaskKind) {
		taskLog.Error("writer lease: unknown task kind", "task_kind", task.TaskKind)
		_ = d.reportTerminalTask(ctx, terminalTaskReport{kind: terminalTaskReportFail, taskID: task.ID, errorMessage: "writer lease task kind unavailable", failureReason: "writer_lease_unavailable"})
		return nil, true
	}
	if task.TaskKind == string(service.WriterLeaseTaskKindReview) {
		taskLog.Error("writer lease: review task cannot enter mutable execution")
		_ = d.reportTerminalTask(ctx, terminalTaskReport{kind: terminalTaskReportFail, taskID: task.ID, errorMessage: "review task is read-only under writer lease enforcement", failureReason: "writer_lease_read_only"})
		return nil, true
	}
	if len(task.WriterLeaseTargets) == 0 {
		if task.ProjectID != "" && len(task.Repos) > 0 {
			taskLog.Error("writer lease: enforced project task has no resolved targets")
			_ = d.reportTerminalTask(ctx, terminalTaskReport{kind: terminalTaskReportFail, taskID: task.ID, errorMessage: "writer lease targets unavailable", failureReason: "writer_lease_unavailable"})
			return nil, true
		}
		d.clearWriterLeaseState(task.ID)
		return nil, false
	}
	store := NewRemoteWriterLeaseStore(d.client, task.RuntimeID, task.ID, task.WriterLeaseTargets)
	guard, err := service.NewWriterLeaseGuard(store, service.DefaultLeaseDuration, service.DefaultLeaseDuration/3)
	if err != nil {
		taskLog.Error("writer lease: guard unavailable", "error", err)
		_ = d.reportTerminalTask(ctx, terminalTaskReport{kind: terminalTaskReportFail, taskID: task.ID, errorMessage: "writer lease unavailable", failureReason: "writer_lease_unavailable"})
		return nil, true
	}
	targets := make([]service.WriterLeaseTarget, 0, len(task.WriterLeaseTargets))
	for _, target := range task.WriterLeaseTargets {
		targets = append(targets, service.WriterLeaseTarget{ResourceID: target.ResourceID, MutexKey: target.MutexKey, URL: target.URL, Ref: target.Ref})
	}
	batch, err := service.AcquireWriterLeaseBatch(ctx, guard, kind, targets, service.WriterLeaseHolderID(d.cfg.DaemonID, task.RuntimeID, task.ID))
	if err != nil {
		taskLog.Error("writer lease: acquire failed", "error", err)
		_ = d.reportTerminalTask(ctx, terminalTaskReport{kind: terminalTaskReportFail, taskID: task.ID, errorMessage: "writer lease unavailable", failureReason: "writer_lease_unavailable"})
		return nil, true
	}
	stops := make([]func(), 0, len(batch.Sessions))
	failureChannels := make([]<-chan error, 0, len(batch.Sessions))
	for _, session := range batch.Sessions {
		stop, failures, heartbeatErr := session.StartHeartbeat(ctx, cancel)
		if heartbeatErr != nil {
			for i := len(stops) - 1; i >= 0; i-- {
				stops[i]()
			}
			_ = batch.Release(context.WithoutCancel(ctx))
			taskLog.Error("writer lease: heartbeat start failed", "error", heartbeatErr)
			_ = d.reportTerminalTask(ctx, terminalTaskReport{kind: terminalTaskReportFail, taskID: task.ID, errorMessage: "writer lease unavailable", failureReason: "writer_lease_unavailable"})
			return nil, true
		}
		stops = append(stops, stop)
		failureChannels = append(failureChannels, failures)
	}
	d.mu.Lock()
	if d.writerLeaseSessions == nil {
		d.writerLeaseSessions = make(map[string]*writerLeaseCheckoutSession)
	}
	ordered := append([]service.WriterLeaseTarget(nil), targets...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].MutexKey < ordered[j].MutexKey })
	bundle := &writerLeaseCheckoutSession{sessions: make(map[string]writerLeaseTargetSession, len(batch.Sessions))}
	for i, session := range batch.Sessions {
		if i >= len(ordered) {
			break
		}
		bundle.sessions[ordered[i].ResourceID] = writerLeaseTargetSession{target: WriterLeaseTarget{ResourceID: ordered[i].ResourceID, MutexKey: ordered[i].MutexKey, URL: ordered[i].URL, Ref: ordered[i].Ref}, session: session}
	}
	d.writerLeaseSessions[task.ID] = bundle
	d.mu.Unlock()
	for _, failures := range failureChannels {
		go func(ch <-chan error, gate *writerLeaseCheckoutSession) {
			for heartbeatErr := range ch {
				if heartbeatErr != nil {
					gate.stale.Store(true)
				}
			}
		}(failures, bundle)
	}
	cleanupState = false
	return func() {
		for i := len(stops) - 1; i >= 0; i-- {
			stops[i]()
		}
		if err := batch.Release(context.WithoutCancel(ctx)); err != nil {
			taskLog.Warn("writer lease: release failed", "error", err)
		}
		d.clearWriterLeaseState(task.ID)
		cleanupState = false
	}, false
}

func NewRemoteWriterLeaseStore(client *Client, runtimeID, taskID string, targets []WriterLeaseTarget) service.WriterLeaseStore {
	byKey := make(map[string]string, len(targets))
	for _, target := range targets {
		if target.MutexKey != "" && target.ResourceID != "" {
			byKey[target.MutexKey] = target.ResourceID
		}
	}
	return &remoteWriterLeaseStore{client: client, runtimeID: runtimeID, taskID: taskID, targets: byKey}
}

func (s *remoteWriterLeaseStore) resourceID(key string) (string, error) {
	if s == nil || s.client == nil {
		return "", errors.New("writer lease: remote store is not configured")
	}
	id := s.targets[key]
	if id == "" {
		return "", errors.New("writer lease: target is not part of claimed task")
	}
	return id, nil
}

func (s *remoteWriterLeaseStore) call(ctx context.Context, action, key string, token uuid.UUID, generation int64, ttl time.Duration) (*service.WriteLease, error) {
	resourceID, err := s.resourceID(key)
	if err != nil {
		return nil, err
	}
	body := map[string]any{"resource_id": resourceID}
	if token != uuid.Nil {
		body["lease_token"] = token
	}
	if generation != 0 {
		body["fence_generation"] = generation
	}
	if ttl > 0 {
		body["ttl_ms"] = ttl.Milliseconds()
	}
	var resp struct {
		Lease *service.WriteLease `json:"lease"`
	}
	path := fmt.Sprintf("/api/daemon/runtimes/%s/tasks/%s/writer-lease/%s", s.runtimeID, s.taskID, action)
	if err := s.client.postJSON(ctx, path, body, &resp); err != nil {
		return nil, err
	}
	if resp.Lease == nil {
		return nil, errors.New("writer lease: server returned no lease")
	}
	return resp.Lease, nil
}

func (s *remoteWriterLeaseStore) Acquire(ctx context.Context, key, _ string, ttl time.Duration) (*service.WriteLease, error) {
	return s.call(ctx, "acquire", key, uuid.Nil, 0, ttl)
}

func (s *remoteWriterLeaseStore) Renew(ctx context.Context, key string, token uuid.UUID, generation int64, ttl time.Duration) (*service.WriteLease, error) {
	return s.call(ctx, "renew", key, token, generation, ttl)
}

func (s *remoteWriterLeaseStore) VerifyHeld(ctx context.Context, key string, token uuid.UUID, generation int64) (*service.WriteLease, error) {
	return s.call(ctx, "verify", key, token, generation, 0)
}

func (s *remoteWriterLeaseStore) Release(ctx context.Context, key string, token uuid.UUID, generation int64) (*service.WriteLease, error) {
	return s.call(ctx, "release", key, token, generation, 0)
}

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/mutationbroker"
	"github.com/multica-ai/multica/server/internal/daemon/repocache"
)

// HealthResponse is returned by the daemon's local health endpoint.
type HealthResponse struct {
	Status string `json:"status"`
	PID    int    `json:"pid"`
	// OS is the daemon's runtime.GOOS. The desktop app compares it against its
	// own host OS to detect a daemon it cannot manage — e.g. a Windows desktop
	// reaching a Linux daemon inside WSL2 over localhost forwarding. The
	// lifecycle CLI (`daemon start/stop`) acts on the host process namespace,
	// so a foreign-OS daemon can't be started/stopped by the app even though
	// /health is reachable. See #3916.
	OS              string   `json:"os"`
	Uptime          string   `json:"uptime"`
	DaemonID        string   `json:"daemon_id"`
	DeviceName      string   `json:"device_name"`
	ServerURL       string   `json:"server_url"`
	CLIVersion      string   `json:"cli_version"`
	ActiveTaskCount int64    `json:"active_task_count"`
	Agents          []string `json:"agents"`
	// SkippedAgents maps a provider that WAS discovered on this machine to the
	// reason the last registration round dropped it (version undetectable,
	// below the minimum supported version). Purely diagnostic, and omitted when
	// empty so older consumers see no change.
	//
	// Without it, "CLI not installed" and "CLI installed but rejected" both
	// render as an absent runtime, which is what made GH #6077 unactionable for
	// the reporter (MUL-5439).
	SkippedAgents map[string]string `json:"skipped_agents,omitempty"`
	Workspaces    []healthWorkspace `json:"workspaces"`
}

type healthWorkspace struct {
	ID       string   `json:"id"`
	Runtimes []string `json:"runtimes"`
}

// listenHealth binds the health port. Returns the listener or an error if
// another daemon is already running (port taken).
func (d *Daemon) listenHealth() (net.Listener, error) {
	addr := fmt.Sprintf("127.0.0.1:%d", d.cfg.HealthPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("another daemon is already running on %s: %w", addr, err)
	}
	return ln, nil
}

// repoCheckoutRequest is the body of a POST /repo/checkout request.
type repoCheckoutRequest struct {
	URL          string `json:"url"`
	WorkspaceID  string `json:"workspace_id"`
	WorkDir      string `json:"workdir"`
	Ref          string `json:"ref,omitempty"`
	AgentName    string `json:"agent_name"`
	TaskID       string `json:"task_id"`
	RuntimeID    string `json:"runtime_id"`
	CheckoutMode string `json:"checkout_mode,omitempty"`
}

// healthHandler returns the /health HTTP handler. Extracted from serveHealth
// so tests can exercise it without spinning up a listener.
func (d *Daemon) healthHandler(startedAt time.Time) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		var wsList []healthWorkspace
		for id, ws := range d.workspaces {
			wsList = append(wsList, healthWorkspace{
				ID:       id,
				Runtimes: ws.runtimeIDs,
			})
		}
		d.mu.Unlock()

		agents := make([]string, 0, len(d.agents()))
		for name := range d.agents() {
			agents = append(agents, name)
		}

		// "starting" until preflight (PAT renew + initial workspace sync +
		// runtime registration) completes; "running" once the daemon can
		// actually claim tasks. The health port is bound before preflight for
		// liveness/diagnostics, so callers must not treat a reachable endpoint
		// as ready — they gate on this status. Consumers that only know
		// "running" (older CLI/desktop) safely treat "starting" as not-ready.
		status := "starting"
		if d.ready.Load() {
			status = "running"
		}

		resp := HealthResponse{
			Status:          status,
			PID:             os.Getpid(),
			OS:              runtime.GOOS,
			Uptime:          time.Since(startedAt).Truncate(time.Second).String(),
			DaemonID:        d.cfg.DaemonID,
			DeviceName:      d.cfg.DeviceName,
			ServerURL:       d.cfg.ServerBaseURL,
			CLIVersion:      d.cfg.CLIVersion,
			ActiveTaskCount: d.activeTasks.Load(),
			Agents:          agents,
			SkippedAgents:   d.skippedAgentsSnapshot(),
			Workspaces:      wsList,
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

// shutdownHandler triggers a graceful daemon shutdown by cancelling the
// top-level context. Used by `multica daemon stop` so we don't depend on
// OS-signal delivery, which is unreliable on Windows once the daemon is
// spawned with DETACHED_PROCESS (no shared console with the stop caller).
// The listener is bound to 127.0.0.1 only, so only local processes can hit
// this endpoint.
func (d *Daemon) shutdownHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "shutting down"})
		if d.cancelFunc != nil {
			// Cancel asynchronously so the response flushes first; otherwise
			// srv.Close() races with the writer.
			go d.cancelFunc()
		}
	}
}

// serveHealth runs the health HTTP server on the given listener.
// Blocks until ctx is cancelled.
func (d *Daemon) serveHealth(ctx context.Context, ln net.Listener, startedAt time.Time) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", d.healthHandler(startedAt))
	mux.HandleFunc("/shutdown", d.shutdownHandler())
	mux.HandleFunc("/repo/checkout", d.repoCheckoutHandler())

	srv := &http.Server{Handler: mux}

	go func() {
		<-ctx.Done()
		srv.Close()
	}()

	d.logger.Info("health server listening", "addr", ln.Addr().String())
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		d.logger.Warn("health server error", "error", err)
	}
}

func (d *Daemon) repoCheckoutHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req repoCheckoutRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		req.URL = strings.TrimSpace(req.URL)
		if req.URL == "" {
			http.Error(w, "url is required", http.StatusBadRequest)
			return
		}
		if req.WorkspaceID == "" {
			http.Error(w, "workspace_id is required", http.StatusBadRequest)
			return
		}
		if req.WorkDir == "" {
			http.Error(w, "workdir is required", http.StatusBadRequest)
			return
		}
		if req.CheckoutMode != "" && req.CheckoutMode != repoCheckoutModeIsolated {
			http.Error(w, "invalid checkout_mode", http.StatusBadRequest)
			return
		}
		// Enforced mediated tasks must arrive through the daemon-owned Unix
		// transport. Reject before touching repoCache or replay state, even if a
		// caller presents a capability left by an older daemon generation.
		if d.writerLeaseMode(req.TaskID) == "enforce" {
			http.Error(w, "mediated checkout transport required", http.StatusForbidden)
			return
		}

		if d.repoCache == nil {
			http.Error(w, "repo cache not initialized", http.StatusInternalServerError)
			return
		}
		capability := r.Header.Get(mutationbroker.CapabilityHeader)
		requestID := strings.TrimSpace(r.Header.Get(mutationbroker.RequestIDHeader))
		if d.mutationBroker == nil || requestID == "" {
			http.Error(w, "task mutation capability required", http.StatusUnauthorized)
			return
		}
		checkoutRef := strings.TrimSpace(req.Ref)
		if checkoutRef == "" {
			checkoutRef = d.taskRepoDefaultRef(req.WorkspaceID, req.TaskID, req.URL)
		}
		brokerReq := mutationbroker.CheckoutRequest{TaskID: req.TaskID, RuntimeID: req.RuntimeID, WorkspaceID: req.WorkspaceID, WorkDir: req.WorkDir, AgentName: req.AgentName, URL: req.URL, Ref: checkoutRef, Operation: mutationbroker.OperationRepoCheckout, RequestID: requestID}
		brokerReq.CheckoutMode = req.CheckoutMode
		encoded, err := d.executeAuthorizedCheckout(r.Context(), brokerReq, capabilityCheckoutAuthorizer{registry: d.mutationBroker, capability: capability})
		if err != nil {
			status := http.StatusInternalServerError
			switch {
			case errors.Is(err, mutationbroker.ErrInvalidCapability), errors.Is(err, mutationbroker.ErrExpiredCapability):
				status = http.StatusUnauthorized
			case errors.Is(err, mutationbroker.ErrUnauthorized):
				status = http.StatusForbidden
			case errors.Is(err, mutationbroker.ErrReplayDrift), errors.Is(err, mutationbroker.ErrReplayInProgress), errors.Is(err, mutationbroker.ErrRequestLimit), errors.Is(err, errStaleMutationReplay):
				status = http.StatusConflict
			}
			d.logger.Error("repo checkout failed", "url", req.URL, "error", err)
			http.Error(w, "task mutation checkout rejected", status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(encoded)
	}
}

func (d *Daemon) writerLeaseMode(taskID string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.writerLeaseModes[taskID]
}

// disableWorktreePushRemote protects the agent checkout while leaving the
// daemon-owned bare cache's fetch remote untouched.
func disableWorktreePushRemote(ctx context.Context, worktreePath string) error {
	if strings.TrimSpace(worktreePath) == "" {
		return errors.New("checkout path unavailable")
	}
	cmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "config", "--local", "remote.origin.pushurl", "no_push://multica-mediated/disabled")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("disable checkout push remote: %s", strings.TrimSpace(string(output)))
	}
	return nil
}

func (d *Daemon) checkoutWithWriterLease(ctx context.Context, taskID, repoURL, ref string, fn func(context.Context) (*repocache.WorktreeResult, error)) (*repocache.WorktreeResult, error) {
	var result *repocache.WorktreeResult
	err := d.withWriterLeaseCheckout(ctx, taskID, repoURL, ref, func(mutationCtx context.Context) error {
		var err error
		result, err = fn(mutationCtx)
		return err
	})
	return result, err
}

package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/mutationbroker"
	"github.com/multica-ai/multica/server/internal/daemon/repocache"
)

func TestHealthHandlerReportsCLIVersionAndActiveTaskCount(t *testing.T) {
	t.Parallel()

	d := &Daemon{
		cfg: Config{
			CLIVersion:    "v9.9.9",
			DaemonID:      "daemon-test",
			DeviceName:    "dev",
			ServerBaseURL: "http://localhost:8080",
		},
		workspaces: map[string]*workspaceState{},
		logger:     slog.Default(),
	}
	d.activeTasks.Store(3)
	d.ready.Store(true) // preflight done -> status should be "running"

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	d.healthHandler(time.Now()).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	// Decode into a raw map so the test locks in the exact wire-level JSON
	// keys — the desktop TS client depends on snake_case (cli_version,
	// active_task_count), so a silent struct-tag rename must fail here.
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw response: %v", err)
	}
	if got, want := raw["cli_version"], "v9.9.9"; got != want {
		t.Errorf("cli_version key: got %v, want %q", got, want)
	}
	// JSON numbers decode to float64 through map[string]any.
	if got, want := raw["active_task_count"], float64(3); got != want {
		t.Errorf("active_task_count key: got %v, want %v", got, want)
	}
	if got, want := raw["status"], "running"; got != want {
		t.Errorf("status key: got %v, want %q", got, want)
	}
	// The desktop relies on the `os` key (runtime.GOOS) to detect a daemon it
	// can't manage (e.g. Linux-in-WSL behind a Windows desktop). A rename or
	// drop would silently re-break #3916, so lock both the key and its value.
	if got, want := raw["os"], runtime.GOOS; got != want {
		t.Errorf("os key: got %v, want %q", got, want)
	}

	// Also round-trip into the typed struct as a separate check that the
	// field values match, independent of key naming.
	var resp HealthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode typed response: %v", err)
	}
	if resp.CLIVersion != "v9.9.9" {
		t.Errorf("CLIVersion: got %q, want %q", resp.CLIVersion, "v9.9.9")
	}
	if resp.ActiveTaskCount != 3 {
		t.Errorf("ActiveTaskCount: got %d, want 3", resp.ActiveTaskCount)
	}
}

// TestHealthHandlerReportsStartingUntilReady pins the liveness/readiness split:
// the health server binds and answers before preflight finishes, but it must
// report "starting" until d.ready is set, and only then "running". Otherwise a
// slow or failing preflight would be misreported to `daemon start` (and the
// desktop) as a fully started daemon.
func TestHealthHandlerReportsStartingUntilReady(t *testing.T) {
	t.Parallel()

	d := &Daemon{
		cfg:        Config{CLIVersion: "v1.0.0"},
		workspaces: map[string]*workspaceState{},
		logger:     slog.Default(),
	}
	handler := d.healthHandler(time.Now())

	readStatus := func() string {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
		var resp HealthResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		return resp.Status
	}

	if got := readStatus(); got != "starting" {
		t.Fatalf("status before ready: got %q, want \"starting\"", got)
	}

	d.ready.Store(true)

	if got := readStatus(); got != "running" {
		t.Fatalf("status after ready: got %q, want \"running\"", got)
	}
}

func TestHealthHandlerActiveTaskCountTracksCounter(t *testing.T) {
	t.Parallel()

	d := &Daemon{
		cfg:        Config{CLIVersion: "v1.0.0"},
		workspaces: map[string]*workspaceState{},
		logger:     slog.Default(),
	}
	handler := d.healthHandler(time.Now())

	// Simulate the pollLoop increment/decrement protocol.
	d.activeTasks.Add(1)
	d.activeTasks.Add(1)
	assertActiveTaskCount(t, handler, 2)

	d.activeTasks.Add(-1)
	assertActiveTaskCount(t, handler, 1)

	d.activeTasks.Add(-1)
	assertActiveTaskCount(t, handler, 0)
}

func TestShutdownHandlerPostCancelsDaemonContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := &Daemon{cancelFunc: cancel}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/shutdown", nil)
	d.shutdownHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("daemon context was not cancelled after POST /shutdown")
	}
}

func TestShutdownHandlerRejectsNonPost(t *testing.T) {
	t.Parallel()

	cancelled := false
	d := &Daemon{cancelFunc: func() { cancelled = true }}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/shutdown", nil)
	d.shutdownHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
	// Give the handler's deferred cancel goroutine a moment to fire
	// in case a bug causes it to run anyway.
	time.Sleep(10 * time.Millisecond)
	if cancelled {
		t.Fatal("GET request should not trigger cancellation")
	}
}

func TestHealthHandlerRespondsWhileTaskRepoLookupWaits(t *testing.T) {
	const workspaceID = "ws-health"
	const repoURL = "https://github.com/org/repo.git"
	cache := newBlockingLookupRepoCache("/cache/org/repo.git")
	d := &Daemon{
		cfg: Config{CLIVersion: "v1.0.0"},
		workspaces: map[string]*workspaceState{
			workspaceID: {
				workspaceID:     workspaceID,
				runtimeIDs:      []string{"rt-1"},
				allowedRepoURLs: map[string]struct{}{repoURL: {}},
				taskRepoURLs:    map[string]struct{}{},
			},
		},
		repoCache: cache,
		logger:    slog.Default(),
	}
	defer cache.release()

	registerDone := make(chan struct{})
	go func() {
		d.registerTaskRepos(workspaceID, "task-health", []RepoData{{URL: repoURL}})
		close(registerDone)
	}()
	cache.waitForLookup(t)

	rec := httptest.NewRecorder()
	healthDone := make(chan struct{})
	go func() {
		d.healthHandler(time.Now()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
		close(healthDone)
	}()

	select {
	case <-healthDone:
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
	case <-time.After(time.Second):
		t.Fatal("/health blocked behind task repo cache lookup")
	}

	cache.release()
	select {
	case <-registerDone:
	case <-time.After(time.Second):
		t.Fatal("registerTaskRepos did not unblock after repo lookup finished")
	}
}

func TestRepoCheckoutUsesTaskScopedProjectRefByDefault(t *testing.T) {
	t.Parallel()

	const workspaceID = "ws-checkout"
	const repoURL = "https://github.com/org/repo.git"
	cache := &recordingRepoCache{lookupPath: "/cache/org/repo.git"}
	d := newRepoCheckoutTestDaemon(t, workspaceID, repoURL, cache)
	d.registerTaskRepos(workspaceID, "task-1", []RepoData{{URL: repoURL, Ref: "release/v2"}})

	rec := httptest.NewRecorder()
	body := `{"url":"` + repoURL + `","workspace_id":"` + workspaceID + `","workdir":"` + t.TempDir() + `","task_id":"task-1","runtime_id":"rt-1"}`
	d.repoCheckoutHandler().ServeHTTP(rec, authorizedCheckoutRequest(t, d, body))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := cache.lastCreateParams().Ref; got != "release/v2" {
		t.Fatalf("CreateWorktree Ref = %q, want release/v2", got)
	}
}

func TestRepoCheckoutExplicitRefOverridesProjectDefault(t *testing.T) {
	t.Parallel()

	const workspaceID = "ws-checkout"
	const repoURL = "https://github.com/org/repo.git"
	cache := &recordingRepoCache{lookupPath: "/cache/org/repo.git"}
	d := newRepoCheckoutTestDaemon(t, workspaceID, repoURL, cache)
	d.registerTaskRepos(workspaceID, "task-1", []RepoData{{URL: repoURL, Ref: "release/v2"}})

	rec := httptest.NewRecorder()
	body := `{"url":"` + repoURL + `","workspace_id":"` + workspaceID + `","workdir":"` + t.TempDir() + `","task_id":"task-1","runtime_id":"rt-1","ref":"hotfix"}`
	d.repoCheckoutHandler().ServeHTTP(rec, authorizedCheckoutRequest(t, d, body))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := cache.lastCreateParams().Ref; got != "hotfix" {
		t.Fatalf("CreateWorktree Ref = %q, want explicit hotfix", got)
	}
}

func TestRepoCheckoutForwardsIsolatedMode(t *testing.T) {
	t.Parallel()

	const workspaceID = "ws-checkout"
	const repoURL = "https://github.com/org/repo.git"
	cache := &recordingRepoCache{lookupPath: "/cache/org/repo.git"}
	d := newRepoCheckoutTestDaemon(t, workspaceID, repoURL, cache)

	rec := httptest.NewRecorder()
	body := `{"url":"` + repoURL + `","workspace_id":"` + workspaceID + `","workdir":"` + t.TempDir() + `","task_id":"task-1","runtime_id":"rt-1","checkout_mode":"isolated"}`
	d.repoCheckoutHandler().ServeHTTP(rec, authorizedCheckoutRequest(t, d, body))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !cache.lastCreateParams().IsolatedGitMetadata {
		t.Fatal("isolated checkout_mode was not forwarded to repo cache")
	}
}

func TestRepoCheckoutRejectsUnknownMode(t *testing.T) {
	t.Parallel()

	const workspaceID = "ws-checkout"
	const repoURL = "https://github.com/org/repo.git"
	cache := &recordingRepoCache{lookupPath: "/cache/org/repo.git"}
	d := newRepoCheckoutTestDaemon(t, workspaceID, repoURL, cache)

	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"url":"` + repoURL + `","workspace_id":"` + workspaceID + `","workdir":"/tmp/work","task_id":"task-1","checkout_mode":"unsafe"}`)
	d.repoCheckoutHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/repo/checkout", body))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := cache.lastCreateParams(); got != (repocache.WorktreeParams{}) {
		t.Fatalf("invalid checkout mode reached repo cache: %+v", got)
	}
}

func TestRepoCheckoutInvalidReplayIsDroppedWithoutCacheCallback(t *testing.T) {
	const workspaceID = "ws-checkout"
	const repoURL = "https://github.com/org/repo.git"
	cases := []struct {
		name   string
		result func(string) []byte
	}{
		{name: "malformed", result: func(string) []byte { return []byte("{malformed") }},
		{name: "missing", result: func(workDir string) []byte { return []byte(`{"path":"` + filepath.Join(workDir, "missing") + `"}`) }},
		{name: "outside", result: func(workDir string) []byte { return []byte(`{"path":"` + filepath.Dir(workDir) + `"}`) }},
		{name: "symlink", result: func(workDir string) []byte { return []byte(`{"path":"` + filepath.Join(workDir, "repo") + `"}`) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cache := &recordingRepoCache{lookupPath: "/cache/org/repo.git"}
			d := newRepoCheckoutTestDaemon(t, workspaceID, repoURL, cache)
			workDir := t.TempDir()
			if tc.name == "symlink" {
				if err := os.Symlink(t.TempDir(), filepath.Join(workDir, "repo")); err != nil {
					t.Fatal(err)
				}
			}
			body := `{"url":"` + repoURL + `","workspace_id":"` + workspaceID + `","workdir":"` + workDir + `","task_id":"task-1","runtime_id":"rt-1"}`
			httpReq := authorizedCheckoutRequest(t, d, body)
			var req repoCheckoutRequest
			if err := json.Unmarshal([]byte(body), &req); err != nil {
				t.Fatal(err)
			}
			brokerReq := mutationbroker.CheckoutRequest{TaskID: req.TaskID, RuntimeID: req.RuntimeID, WorkspaceID: req.WorkspaceID, WorkDir: req.WorkDir, AgentName: req.AgentName, URL: req.URL, Operation: mutationbroker.OperationRepoCheckout, RequestID: httpReq.Header.Get(mutationbroker.RequestIDHeader)}
			capability := httpReq.Header.Get(mutationbroker.CapabilityHeader)
			decision, err := d.mutationBroker.Authorize(capability, brokerReq)
			if err != nil || !decision.Acquired {
				t.Fatalf("authorize replay fixture = %+v, err=%v", decision, err)
			}
			if err := d.mutationBroker.Complete(capability, brokerReq, tc.result(workDir)); err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			d.repoCheckoutHandler().ServeHTTP(rec, httpReq)
			if rec.Code != http.StatusConflict {
				t.Fatalf("invalid replay status=%d body=%s", rec.Code, rec.Body.String())
			}
			if got := cache.lastCreateParams(); got != (repocache.WorktreeParams{}) {
				t.Fatalf("invalid replay reached repo cache: %+v", got)
			}
			if decision, err := d.mutationBroker.Authorize(capability, brokerReq); err != nil || !decision.Acquired {
				t.Fatalf("invalid replay was not dropped: %+v, err=%v", decision, err)
			} else if err := d.mutationBroker.Abort(capability, brokerReq); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRepoCheckoutCapabilityDriftHasZeroRepoCacheCallbacks(t *testing.T) {
	const workspaceID = "ws-checkout"
	const repoURL = "https://github.com/org/repo.git"
	cases := []struct {
		name   string
		mutate func(*http.Request, *repoCheckoutRequest)
	}{
		{name: "task", mutate: func(_ *http.Request, req *repoCheckoutRequest) { req.TaskID = "task-other" }},
		{name: "runtime", mutate: func(_ *http.Request, req *repoCheckoutRequest) { req.RuntimeID = "rt-other" }},
		{name: "workspace", mutate: func(_ *http.Request, req *repoCheckoutRequest) { req.WorkspaceID = "ws-other" }},
		{name: "workdir", mutate: func(_ *http.Request, req *repoCheckoutRequest) { req.WorkDir = filepath.Join(req.WorkDir, "other") }},
		{name: "url", mutate: func(_ *http.Request, req *repoCheckoutRequest) { req.URL = "https://github.com/org/other.git" }},
		{name: "ref", mutate: func(_ *http.Request, req *repoCheckoutRequest) { req.Ref = "unbound-ref" }},
		{name: "mode", mutate: func(_ *http.Request, req *repoCheckoutRequest) { req.CheckoutMode = repoCheckoutModeIsolated }},
		{name: "agent", mutate: func(_ *http.Request, req *repoCheckoutRequest) { req.AgentName = "agent-other" }},
		{name: "missing-capability", mutate: func(r *http.Request, _ *repoCheckoutRequest) { r.Header.Del(mutationbroker.CapabilityHeader) }},
		{name: "unknown-capability", mutate: func(r *http.Request, _ *repoCheckoutRequest) {
			r.Header.Set(mutationbroker.CapabilityHeader, "unknown-capability")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cache := &recordingRepoCache{lookupPath: "/cache/org/repo.git"}
			d := newRepoCheckoutTestDaemon(t, workspaceID, repoURL, cache)
			workDir := t.TempDir()
			body := `{"url":"` + repoURL + `","workspace_id":"` + workspaceID + `","workdir":"` + workDir + `","task_id":"task-1","runtime_id":"rt-1","agent_name":"agent-1","ref":"release/v2"}`
			req := authorizedCheckoutRequest(t, d, body)
			var payload repoCheckoutRequest
			if err := json.Unmarshal([]byte(body), &payload); err != nil {
				t.Fatal(err)
			}
			tc.mutate(req, &payload)
			encoded, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			req.Body = io.NopCloser(bytes.NewReader(encoded))
			req.ContentLength = int64(len(encoded))
			rec := httptest.NewRecorder()
			d.repoCheckoutHandler().ServeHTTP(rec, req)
			if rec.Code < http.StatusBadRequest || rec.Code >= http.StatusInternalServerError {
				t.Fatalf("drift status=%d body=%s", rec.Code, rec.Body.String())
			}
			if got := cache.callbackCalls(); got != 0 {
				t.Fatalf("drift reached repo cache/ensure callbacks %d times", got)
			}
		})
	}
}

func TestDisableWorktreePushRemoteLeavesNoGitHubPushURL(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command("git", "-C", dir, "init")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %s: %v", output, err)
	}
	if output, err := exec.Command("git", "-C", dir, "remote", "add", "origin", "https://github.com/acme/repo.git").CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %s: %v", output, err)
	}
	if err := disableWorktreePushRemote(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command("git", "-C", dir, "config", "--local", "--get", "remote.origin.pushurl").CombinedOutput()
	if err != nil {
		t.Fatalf("read pushurl: %s: %v", output, err)
	}
	if got := strings.TrimSpace(string(output)); got == "" || strings.Contains(got, "github.com") {
		t.Fatalf("pushurl = %q, want mediated non-GitHub URL", got)
	}
}

func newRepoCheckoutTestDaemon(t *testing.T, workspaceID, repoURL string, cache *recordingRepoCache) *Daemon {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/daemon/workspaces/"+workspaceID+"/repos" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(WorkspaceReposResponse{
			WorkspaceID:  workspaceID,
			Repos:        []RepoData{{URL: repoURL}},
			ReposVersion: "v1",
		})
	}))
	t.Cleanup(srv.Close)
	return &Daemon{
		cfg:       Config{CLIVersion: "v1.0.0"},
		client:    NewClient(srv.URL),
		repoCache: cache,
		workspaces: map[string]*workspaceState{
			workspaceID: newWorkspaceState(workspaceID, nil, "", []RepoData{{URL: repoURL}}, nil),
		},
		logger:           slog.Default(),
		mutationBroker:   mutationbroker.New(),
		writerLeaseModes: map[string]string{"task-1": "off"},
	}
}

func authorizedCheckoutRequest(t *testing.T, d *Daemon, body string) *http.Request {
	t.Helper()
	var req repoCheckoutRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(req.WorkDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path, _, err := d.mutationBroker.Issue(mutationbroker.IssueRequest{TaskID: req.TaskID, RuntimeID: req.RuntimeID, WorkspaceID: req.WorkspaceID, WorkDir: req.WorkDir, Operation: mutationbroker.OperationRepoCheckout, CheckoutMode: req.CheckoutMode, Targets: []mutationbroker.Target{{URL: req.URL, Ref: ""}, {URL: req.URL, Ref: "release/v2"}, {URL: req.URL, Ref: "hotfix"}}}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.mutationBroker.Revoke(path) })
	httpReq := httptest.NewRequest(http.MethodPost, "/repo/checkout", strings.NewReader(body))
	httpReq.Header.Set(mutationbroker.CapabilityHeader, readTestCapability(t, path))
	httpReq.Header.Set(mutationbroker.RequestIDHeader, "req-"+req.Ref+req.WorkDir)
	return httpReq
}

func readTestCapability(t *testing.T, path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

type blockingLookupRepoCache struct {
	path          string
	lookupSeen    chan struct{}
	releaseLookup chan struct{}
	releaseOnce   sync.Once
}

func newBlockingLookupRepoCache(path string) *blockingLookupRepoCache {
	return &blockingLookupRepoCache{
		path:          path,
		lookupSeen:    make(chan struct{}),
		releaseLookup: make(chan struct{}),
	}
}

func (c *blockingLookupRepoCache) Lookup(_, _ string) string {
	select {
	case <-c.lookupSeen:
	default:
		close(c.lookupSeen)
	}
	<-c.releaseLookup
	return c.path
}

func (c *blockingLookupRepoCache) Sync(string, []repocache.RepoInfo) error {
	return nil
}

func (c *blockingLookupRepoCache) WithRepoLock(_ string, fn func() error) error {
	return fn()
}

func (c *blockingLookupRepoCache) CreateWorktree(repocache.WorktreeParams) (*repocache.WorktreeResult, error) {
	return nil, nil
}

type recordingRepoCache struct {
	lookupPath string
	mu         sync.Mutex
	params     []repocache.WorktreeParams
	calls      int
}

func (c *recordingRepoCache) Lookup(_, _ string) string {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return c.lookupPath
}

func (c *recordingRepoCache) Sync(string, []repocache.RepoInfo) error {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return nil
}

func (c *recordingRepoCache) WithRepoLock(_ string, fn func() error) error {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return fn()
}

func (c *recordingRepoCache) CreateWorktree(params repocache.WorktreeParams) (*repocache.WorktreeResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	c.params = append(c.params, params)
	return &repocache.WorktreeResult{Path: params.WorkDir, BranchName: "agent/test"}, nil
}

func (c *recordingRepoCache) lastCreateParams() repocache.WorktreeParams {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.params) == 0 {
		return repocache.WorktreeParams{}
	}
	return c.params[len(c.params)-1]
}

func (c *recordingRepoCache) createCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.params)
}

func (c *recordingRepoCache) callbackCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func (c *blockingLookupRepoCache) waitForLookup(t *testing.T) {
	t.Helper()
	select {
	case <-c.lookupSeen:
	case <-time.After(time.Second):
		t.Fatal("registerTaskRepos did not call repo lookup")
	}
}

func (c *blockingLookupRepoCache) release() {
	c.releaseOnce.Do(func() {
		close(c.releaseLookup)
	})
}

func assertActiveTaskCount(t *testing.T, h http.HandlerFunc, want int64) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	var resp HealthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ActiveTaskCount != want {
		t.Errorf("active_task_count: got %d, want %d", resp.ActiveTaskCount, want)
	}
}

package daemon

import (
	"crypto/sha256"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The constants here are dummy test values only — never a real token,
// capability, env, Keychain or log entry (HIV-806 test rule).
const (
	testTaskToken         = "mat_test_task_token_0123456789"
	testOtherTaskToken    = "mat_test_other_task_token_9876"
	testRegisteredTaskID  = "task-token-test-1"
	testRegisteredTaskID2 = "task-token-test-2"
)

func newTaskTokenTestDaemon() *Daemon {
	d := &Daemon{
		cfg:    Config{CLIVersion: "v1.0.0", HealthPort: 0},
		logger: slog.Default(),
	}
	d.taskTokens = newTaskTokenRegistry()
	return d
}

func taskTokenFixture(t *testing.T, d *Daemon, taskID, token string, ttl time.Duration) (string, string) {
	t.Helper()
	path, err := d.taskTokens.Register(taskID, token, filepath.Join(t.TempDir(), ".multica"), ttl)
	if err != nil {
		t.Fatalf("Register(%q): %v", taskID, err)
	}
	t.Cleanup(func() { _ = d.taskTokens.Revoke(taskID) })
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read capability file: %v", err)
	}
	return path, strings.TrimSpace(string(raw))
}

// newTaskTokenHTTPRequest builds a well-formed request for the handler. It
// must not be named taskTokenRequest: that is the production body type.
func newTaskTokenHTTPRequest(method, target, taskID, capability string) (*http.Request, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(method, target, strings.NewReader(`{"task_id":"`+taskID+`"}`))
	req.Header.Set("Content-Type", "application/json")
	if capability != "" {
		req.Header.Set(TaskTokenCapabilityHeader, capability)
	}
	return req, httptest.NewRecorder()
}

// TestTaskTokenRegisterWritesCapabilityOnlyFile pins the on-disk contract:
// the capability file exists, is owner-readable only (0400, no write bits),
// and contains the opaque capability — never the task token.
func TestTaskTokenRegisterWritesCapabilityOnlyFile(t *testing.T) {
	d := newTaskTokenTestDaemon()
	path, capability := taskTokenFixture(t, d, testRegisteredTaskID, testTaskToken, time.Hour)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat capability file: %v", err)
	}
	if perm := info.Mode().Perm(); perm&0o222 != 0 || perm&0o400 == 0 {
		t.Fatalf("capability file mode = %o, want read-only owner permissions (0400)", perm)
	}
	if len(capability) != 64 {
		t.Fatalf("capability length = %d, want 64 hex chars", len(capability))
	}
	if strings.Contains(capability, testTaskToken) || strings.Contains(testTaskToken, capability) {
		t.Fatal("capability file must not contain the task token")
	}
	if !strings.HasSuffix(filepath.Base(path), taskTokenCapabilityFile) {
		t.Fatalf("capability file name = %q", filepath.Base(path))
	}
}

// TestTaskTokenRegisterValidatesInputs pins the fail-closed register rules.
func TestTaskTokenRegisterValidatesInputs(t *testing.T) {
	d := newTaskTokenTestDaemon()
	cases := []struct {
		name    string
		taskID  string
		token   string
		dir     string
		ttl     time.Duration
		wantErr bool
	}{
		{name: "empty task id", taskID: "", token: testTaskToken, dir: t.TempDir(), ttl: time.Hour, wantErr: true},
		{name: "member PAT refused", taskID: testRegisteredTaskID, token: "mul_member_pat", dir: t.TempDir(), ttl: time.Hour, wantErr: true},
		{name: "jwt refused", taskID: testRegisteredTaskID, token: "eyJhbGciOi", dir: t.TempDir(), ttl: time.Hour, wantErr: true},
		{name: "empty dir", taskID: testRegisteredTaskID, token: testTaskToken, dir: " ", ttl: time.Hour, wantErr: true},
		{name: "non-positive ttl", taskID: testRegisteredTaskID, token: testTaskToken, dir: t.TempDir(), ttl: 0, wantErr: true},
		{name: "valid", taskID: testRegisteredTaskID, token: testTaskToken, dir: t.TempDir(), ttl: time.Hour},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := d.taskTokens.Register(tc.taskID, tc.token, tc.dir, tc.ttl)
			if tc.wantErr && err == nil {
				t.Fatalf("Register() = nil error, want refusal")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("Register() = %v, want nil", err)
			}
		})
	}
}

// TestTaskTokenRegistryResolveGatesOnExactTaskAndCapability covers wrong
// task, wrong capability, and empty inputs — all refused indistinguishably.
func TestTaskTokenRegistryResolveGatesOnExactTaskAndCapability(t *testing.T) {
	d := newTaskTokenTestDaemon()
	_, capability := taskTokenFixture(t, d, testRegisteredTaskID, testTaskToken, time.Hour)
	_, _ = taskTokenFixture(t, d, testRegisteredTaskID2, testOtherTaskToken, time.Hour)
	now := time.Now()

	cases := []struct {
		name       string
		taskID     string
		capability string
		wantToken  string
		wantOK     bool
	}{
		{name: "exact task and capability", taskID: testRegisteredTaskID, capability: capability, wantToken: testTaskToken, wantOK: true},
		{name: "wrong task id", taskID: "task-other", capability: capability},
		{name: "empty task id", taskID: "", capability: capability},
		{name: "empty capability", taskID: testRegisteredTaskID, capability: ""},
		{name: "wrong capability", taskID: testRegisteredTaskID, capability: strings.Repeat("deadbeef", 8)},
		{name: "capability of another task", taskID: testRegisteredTaskID, capability: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := d.taskTokens.Resolve(tc.taskID, tc.capability, now)
			if ok != tc.wantOK {
				t.Fatalf("Resolve() ok = %v, want %v", ok, tc.wantOK)
			}
			if got != tc.wantToken {
				t.Fatalf("Resolve() token = %q, want %q", got, tc.wantToken)
			}
		})
	}
}

func TestTaskTokenRegistryExpiredRefused(t *testing.T) {
	d := newTaskTokenTestDaemon()
	_, capability := taskTokenFixture(t, d, testRegisteredTaskID, testTaskToken, time.Nanosecond)

	if _, ok := d.taskTokens.Resolve(testRegisteredTaskID, capability, time.Now().Add(-time.Hour)); !ok {
		t.Fatal("Resolve() before expiry refused, want ok")
	}
	if got, ok := d.taskTokens.Resolve(testRegisteredTaskID, capability, time.Now().Add(time.Hour)); ok {
		t.Fatalf("Resolve() after expiry = %q, want refusal", got)
	}
}

func TestTaskTokenRegistryRevokeRemovesFileAndRecord(t *testing.T) {
	d := newTaskTokenTestDaemon()
	path, capability := taskTokenFixture(t, d, testRegisteredTaskID, testTaskToken, time.Hour)

	if err := d.taskTokens.Revoke(testRegisteredTaskID); err != nil {
		t.Fatalf("Revoke(): %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("capability file still present after revoke: %v", err)
	}
	if got, ok := d.taskTokens.Resolve(testRegisteredTaskID, capability, time.Now()); ok {
		t.Fatalf("Resolve() after revoke = %q, want refusal", got)
	}
	// Idempotent: a second revoke must not error.
	if err := d.taskTokens.Revoke(testRegisteredTaskID); err != nil {
		t.Fatalf("second Revoke(): %v", err)
	}
}

// TestTaskTokenRegistryResolveRevokeLinearized proves that the token copy and
// revocation share one critical section. Revoke must not complete while an
// in-flight Resolve is validating/copying the record, and every Resolve after
// Revoke completes must fail closed.
func TestTaskTokenRegistryResolveRevokeLinearized(t *testing.T) {
	d := newTaskTokenTestDaemon()
	_, capability := taskTokenFixture(t, d, testRegisteredTaskID, testTaskToken, time.Hour)
	digest := sha256.Sum256([]byte(capability))

	d.taskTokens.mu.Lock()
	got, ok := d.taskTokens.resolveLocked(testRegisteredTaskID, digest, time.Now())
	if !ok || got != testTaskToken {
		d.taskTokens.mu.Unlock()
		t.Fatalf("resolveLocked() = %q, ok=%v; want live task token", got, ok)
	}
	revokeDone := make(chan error, 1)
	go func() { revokeDone <- d.taskTokens.Revoke(testRegisteredTaskID) }()
	select {
	case err := <-revokeDone:
		d.taskTokens.mu.Unlock()
		t.Fatalf("Revoke completed before Resolve critical section ended: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	d.taskTokens.mu.Unlock()
	if err := <-revokeDone; err != nil {
		t.Fatalf("Revoke(): %v", err)
	}
	if token, ok := d.taskTokens.Resolve(testRegisteredTaskID, capability, time.Now()); ok {
		t.Fatalf("Resolve() after completed Revoke = %q, want refusal", token)
	}
}

// TestTaskTokenRegistryReregisterReplacesCapability pins that a replaced
// registration invalidates the old capability.
func TestTaskTokenRegistryReregisterReplacesCapability(t *testing.T) {
	d := newTaskTokenTestDaemon()
	dir := filepath.Join(t.TempDir(), ".multica")
	firstPath, err := d.taskTokens.Register(testRegisteredTaskID, testTaskToken, dir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	firstCap := strings.TrimSpace(mustReadTaskTokenFile(t, firstPath))

	secondPath, err := d.taskTokens.Register(testRegisteredTaskID, testOtherTaskToken, dir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	secondCap := strings.TrimSpace(mustReadTaskTokenFile(t, secondPath))

	if firstCap == secondCap {
		t.Fatal("re-register produced the same capability")
	}
	if _, ok := d.taskTokens.Resolve(testRegisteredTaskID, firstCap, time.Now()); ok {
		t.Fatal("old capability still valid after re-register")
	}
	if got, ok := d.taskTokens.Resolve(testRegisteredTaskID, secondCap, time.Now()); !ok || got != testOtherTaskToken {
		t.Fatalf("new capability refused: got %q ok=%v", got, ok)
	}
	_ = d.taskTokens.Revoke(testRegisteredTaskID)
}

func mustReadTaskTokenFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestTaskTokenHandlerHappyPath(t *testing.T) {
	d := newTaskTokenTestDaemon()
	_, capability := taskTokenFixture(t, d, testRegisteredTaskID, testTaskToken, time.Hour)

	req, rec := newTaskTokenHTTPRequest(http.MethodPost, TaskTokenPath, testRegisteredTaskID, capability)
	d.taskTokenHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	var resp taskTokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Token != testTaskToken {
		t.Fatalf("token = %q, want task-scoped dummy", resp.Token)
	}
}

// TestTaskTokenHandlerRejections pins the failure surface: wrong, expired,
// revoked, and non-running tasks all refuse identically, and no response
// ever echoes the capability or the token.
func TestTaskTokenHandlerRejections(t *testing.T) {
	type setup func(t *testing.T, d *Daemon) (taskID, capability string)

	withFixture := func(taskID, token string, ttl time.Duration) setup {
		return func(t *testing.T, d *Daemon) (string, string) {
			_, capability := taskTokenFixture(t, d, taskID, token, ttl)
			return taskID, capability
		}
	}

	cases := []struct {
		name       string
		setup      setup
		prepare    func(t *testing.T, d *Daemon)
		method     string
		target     string
		body       string // when empty, built from taskID as {"task_id":...}
		taskID     string
		capability func(fixtureTaskID, fixtureCap string) string
		wantStatus int
	}{
		{
			name:       "wrong capability",
			setup:      withFixture(testRegisteredTaskID, testTaskToken, time.Hour),
			method:     http.MethodPost,
			target:     TaskTokenPath,
			taskID:     testRegisteredTaskID,
			capability: func(_, _ string) string { return strings.Repeat("deadbeef", 8) },
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "missing capability header",
			setup:      withFixture(testRegisteredTaskID, testTaskToken, time.Hour),
			method:     http.MethodPost,
			target:     TaskTokenPath,
			taskID:     testRegisteredTaskID,
			capability: func(_, _ string) string { return "" },
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "capability presented for another task",
			setup:      withFixture(testRegisteredTaskID, testTaskToken, time.Hour),
			method:     http.MethodPost,
			target:     TaskTokenPath,
			taskID:     testRegisteredTaskID2,
			capability: func(_, cap string) string { return cap },
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "unknown task (never registered / not running)",
			setup:      withFixture(testRegisteredTaskID, testTaskToken, time.Hour),
			method:     http.MethodPost,
			target:     TaskTokenPath,
			taskID:     "task-never-registered",
			capability: func(_, cap string) string { return cap },
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:  "revoked task (task no longer running)",
			setup: withFixture(testRegisteredTaskID, testTaskToken, time.Hour),
			prepare: func(t *testing.T, d *Daemon) {
				if err := d.taskTokens.Revoke(testRegisteredTaskID); err != nil {
					t.Fatal(err)
				}
			},
			method:     http.MethodPost,
			target:     TaskTokenPath,
			taskID:     testRegisteredTaskID,
			capability: func(_, cap string) string { return cap },
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "expired registration",
			setup:      withFixture(testRegisteredTaskID, testTaskToken, time.Nanosecond),
			method:     http.MethodPost,
			target:     TaskTokenPath,
			taskID:     testRegisteredTaskID,
			capability: func(_, cap string) string { return cap },
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "GET refused",
			setup:      withFixture(testRegisteredTaskID, testTaskToken, time.Hour),
			method:     http.MethodGet,
			target:     TaskTokenPath,
			taskID:     testRegisteredTaskID,
			capability: func(_, cap string) string { return cap },
			wantStatus: http.StatusMethodNotAllowed,
		},
		{
			name:       "query selectors refused",
			setup:      withFixture(testRegisteredTaskID, testTaskToken, time.Hour),
			method:     http.MethodPost,
			target:     TaskTokenPath + "?task_id=" + testRegisteredTaskID,
			taskID:     testRegisteredTaskID,
			capability: func(_, cap string) string { return cap },
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "empty task id refused",
			setup:      withFixture(testRegisteredTaskID, testTaskToken, time.Hour),
			method:     http.MethodPost,
			target:     TaskTokenPath,
			taskID:     "",
			capability: func(_, cap string) string { return cap },
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "malformed body refused",
			setup:      withFixture(testRegisteredTaskID, testTaskToken, time.Hour),
			method:     http.MethodPost,
			target:     TaskTokenPath,
			body:       "{malformed",
			capability: func(_, cap string) string { return cap },
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "unknown field refused",
			setup:      withFixture(testRegisteredTaskID, testTaskToken, time.Hour),
			method:     http.MethodPost,
			target:     TaskTokenPath,
			body:       `{"task_id":"` + testRegisteredTaskID + `","runtime_id":"not-accepted"}`,
			capability: func(_, cap string) string { return cap },
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "second JSON value refused",
			setup:      withFixture(testRegisteredTaskID, testTaskToken, time.Hour),
			method:     http.MethodPost,
			target:     TaskTokenPath,
			body:       `{"task_id":"` + testRegisteredTaskID + `"}{"task_id":"` + testRegisteredTaskID + `"}`,
			capability: func(_, cap string) string { return cap },
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "oversized body refused",
			setup:      withFixture(testRegisteredTaskID, testTaskToken, time.Hour),
			method:     http.MethodPost,
			target:     TaskTokenPath,
			body:       `{"task_id":"` + testRegisteredTaskID + `","padding":"` + strings.Repeat("x", taskTokenRequestLimit) + `"}`,
			capability: func(_, cap string) string { return cap },
			wantStatus: http.StatusBadRequest,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newTaskTokenTestDaemon()
			fixtureTaskID, fixtureCap := tc.setup(t, d)
			if tc.prepare != nil {
				tc.prepare(t, d)
			}
			body := tc.body
			if body == "" {
				body = `{"task_id":"` + tc.taskID + `"}`
			}
			req := httptest.NewRequest(tc.method, tc.target, strings.NewReader(body))
			if cap := tc.capability(fixtureTaskID, fixtureCap); cap != "" {
				req.Header.Set(TaskTokenCapabilityHeader, cap)
			}
			rec := httptest.NewRecorder()
			d.taskTokenHandler().ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Fatalf("Cache-Control = %q, want no-store", got)
			}
			respBody := rec.Body.String()
			for _, secret := range []string{testTaskToken, testOtherTaskToken, fixtureCap, "mat_"} {
				if strings.Contains(respBody, secret) {
					t.Fatalf("error response echoes protected value: %q", respBody)
				}
			}
		})
	}
}

// TestTaskTokenHandlerFailsClosedWithoutRegistry covers hand-built daemon
// fixtures whose registry was never initialized.
func TestTaskTokenHandlerFailsClosedWithoutRegistry(t *testing.T) {
	d := &Daemon{cfg: Config{CLIVersion: "v1.0.0"}, logger: slog.Default()}
	req, rec := newTaskTokenHTTPRequest(http.MethodPost, TaskTokenPath, testRegisteredTaskID, "whatever")
	d.taskTokenHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (fail closed on nil registry)", rec.Code)
	}
}

// TestTaskTokenCapabilityEnvNameSurvivesCredentialEnvScrub pins the naming
// contract that makes the whole feature work: the pointer env name must not
// contain any credential-shaped substring the DeepSeek Harness scrub
// (KEY|PASSWORD|SECRET|TOKEN) or Codex's default secret guard removes.
func TestTaskTokenCapabilityEnvNameSurvivesCredentialEnvScrub(t *testing.T) {
	name := strings.ToUpper(TaskTokenCapabilityFileEnv)
	for _, banned := range []string{"KEY", "PASSWORD", "SECRET", "TOKEN"} {
		if strings.Contains(name, banned) {
			t.Fatalf("env name %s contains credential-shaped substring %q; env scrubbers would strip it", TaskTokenCapabilityFileEnv, banned)
		}
	}
	if !strings.HasPrefix(name, "MULTICA_") {
		t.Fatalf("env name %s should stay in the MULTICA_ namespace", TaskTokenCapabilityFileEnv)
	}
}

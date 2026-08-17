package mutationbroker

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestRegistryAuthorizesExactTaskTargetAndReplays(t *testing.T) {
	workDir := t.TempDir()
	r := New()
	path, _, err := r.Issue(IssueRequest{TaskID: "task", RuntimeID: "runtime", WorkspaceID: "workspace", WorkDir: workDir, Operation: OperationRepoCheckout, Targets: []Target{{ResourceID: "resource", URL: "https://github.com/acme/repo.git", Ref: "main"}}}, filepath.Join(t.TempDir(), "cap"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Revoke(path)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o400 {
		t.Fatalf("capability mode = %o, want 400", got)
	}
	req := CheckoutRequest{TaskID: "task", RuntimeID: "runtime", WorkspaceID: "workspace", WorkDir: workDir, URL: "https://github.com/acme/repo.git", Ref: "main", Operation: OperationRepoCheckout, RequestID: "request-1"}
	capBytes, _ := os.ReadFile(path)
	if _, err := r.Authorize(string(capBytes), req); err != nil {
		t.Fatal(err)
	}
	result := []byte(`{"path":"/work/repo"}`)
	if err := r.Complete(string(capBytes), req, result); err != nil {
		t.Fatal(err)
	}
	decision, err := r.Authorize(string(capBytes), req)
	if err != nil || string(decision.Replay) != string(result) {
		t.Fatalf("replay = %q, err=%v", decision.Replay, err)
	}
	if _, err := r.Authorize(string(capBytes), CheckoutRequest{TaskID: "other", RuntimeID: req.RuntimeID, WorkspaceID: req.WorkspaceID, WorkDir: workDir, URL: req.URL, Ref: req.Ref, Operation: req.Operation, RequestID: "request-2"}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("cross-task err=%v, want unauthorized", err)
	}
	if _, err := r.Authorize(string(capBytes), CheckoutRequest{TaskID: req.TaskID, RuntimeID: req.RuntimeID, WorkspaceID: req.WorkspaceID, WorkDir: workDir, URL: req.URL, Ref: "release", Operation: req.Operation, RequestID: "request-1"}); !errors.Is(err, ErrReplayDrift) {
		t.Fatalf("replay drift err=%v, want replay drift", err)
	}
}

func TestRegistryModeBindingAndAbortAllowsRetry(t *testing.T) {
	workDir := t.TempDir()
	r := New()
	path, capValue, err := r.Issue(IssueRequest{TaskID: "task", RuntimeID: "runtime", WorkspaceID: "workspace", WorkDir: workDir, AgentName: "codex", Operation: OperationRepoCheckout, CheckoutMode: "isolated", Targets: []Target{{URL: "https://github.com/acme/repo.git"}}}, filepath.Join(t.TempDir(), "cap"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Revoke(path)
	req := CheckoutRequest{TaskID: "task", RuntimeID: "runtime", WorkspaceID: "workspace", WorkDir: workDir, AgentName: "codex", URL: "https://github.com/acme/repo.git", Operation: OperationRepoCheckout, CheckoutMode: "", RequestID: "r1"}
	if _, err := r.Authorize(capValue, req); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("mode drift err=%v", err)
	}
	req.CheckoutMode = "isolated"
	req.AgentName = "other-agent"
	if _, err := r.Authorize(capValue, req); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("agent drift err=%v", err)
	}
	req.AgentName = "codex"
	if _, err := r.Authorize(capValue, req); err != nil {
		t.Fatal(err)
	}
	if err := r.Abort(capValue, req); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Authorize(capValue, req); err != nil {
		t.Fatalf("retry after abort: %v", err)
	}
}

func TestRegistryRejectsExpiredAndRevokedCapability(t *testing.T) {
	workDir := t.TempDir()
	r := New()
	path, _, err := r.Issue(IssueRequest{TaskID: "task", RuntimeID: "runtime", WorkspaceID: "workspace", WorkDir: workDir, Operation: OperationRepoCheckout, Targets: []Target{{URL: "https://github.com/acme/repo.git"}}, TTL: time.Nanosecond}, filepath.Join(t.TempDir(), "cap"))
	if err != nil {
		t.Fatal(err)
	}
	capBytes, _ := os.ReadFile(path)
	time.Sleep(time.Millisecond)
	req := CheckoutRequest{TaskID: "task", RuntimeID: "runtime", WorkspaceID: "workspace", WorkDir: workDir, URL: "https://github.com/acme/repo.git", Operation: OperationRepoCheckout, RequestID: "request"}
	if _, err := r.Authorize(string(capBytes), req); !errors.Is(err, ErrExpiredCapability) {
		t.Fatalf("expired err=%v", err)
	}
	if err := r.Revoke(path); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Authorize(string(capBytes), req); !errors.Is(err, ErrInvalidCapability) {
		t.Fatalf("revoked err=%v", err)
	}
}

func TestRegistryConcurrentSameRequestAbortOwnership(t *testing.T) {
	workDir := t.TempDir()
	r := New()
	path, capValue, err := r.Issue(IssueRequest{TaskID: "task", RuntimeID: "runtime", WorkspaceID: "workspace", WorkDir: workDir, AgentName: "codex", Operation: OperationRepoCheckout, Targets: []Target{{URL: "https://github.com/acme/repo.git"}}}, filepath.Join(t.TempDir(), "cap"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Revoke(path)
	req := CheckoutRequest{TaskID: "task", RuntimeID: "runtime", WorkspaceID: "workspace", WorkDir: workDir, AgentName: "codex", URL: "https://github.com/acme/repo.git", Operation: OperationRepoCheckout, RequestID: "same"}
	first, err := r.Authorize(capValue, req)
	if err != nil || !first.Acquired {
		t.Fatalf("first authorize = %+v, err=%v", first, err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	var secondErr error
	go func() {
		defer wg.Done()
		_, secondErr = r.Authorize(capValue, req)
	}()
	wg.Wait()
	if !errors.Is(secondErr, ErrReplayInProgress) {
		t.Fatalf("second authorize err=%v, want replay in progress", secondErr)
	}
	if err := r.Complete(capValue, req, []byte(`{"path":"/work/repo"}`)); err != nil {
		t.Fatal(err)
	}
	replay, err := r.Authorize(capValue, req)
	if err != nil || string(replay.Replay) != `{"path":"/work/repo"}` {
		t.Fatalf("replay = %+v, err=%v", replay, err)
	}
}

func TestRegistryInvalidateReplayOnlyDropsCompletedExactRequest(t *testing.T) {
	workDir := t.TempDir()
	r := New()
	path, capValue, err := r.Issue(IssueRequest{TaskID: "task", RuntimeID: "runtime", WorkspaceID: "workspace", WorkDir: workDir, Operation: OperationRepoCheckout, Targets: []Target{{URL: "https://github.com/acme/repo.git"}}}, filepath.Join(t.TempDir(), "cap"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Revoke(path)
	req := CheckoutRequest{TaskID: "task", RuntimeID: "runtime", WorkspaceID: "workspace", WorkDir: workDir, URL: "https://github.com/acme/repo.git", Operation: OperationRepoCheckout, RequestID: "replay"}
	if decision, err := r.Authorize(capValue, req); err != nil || !decision.Acquired {
		t.Fatalf("authorize = %+v, err=%v", decision, err)
	}
	if err := r.Complete(capValue, req, []byte(`{"path":"/work/repo"}`)); err != nil {
		t.Fatal(err)
	}
	if err := r.InvalidateReplay(capValue, req); err != nil {
		t.Fatal(err)
	}
	decision, err := r.Authorize(capValue, req)
	if err != nil || !decision.Acquired {
		t.Fatalf("same request did not reacquire after invalidation: %+v, err=%v", decision, err)
	}
	if err := r.Abort(capValue, req); err != nil {
		t.Fatal(err)
	}

	if decision, err := r.Authorize(capValue, req); err != nil || !decision.Acquired {
		t.Fatalf("second acquisition = %+v, err=%v", decision, err)
	}
	if err := r.InvalidateReplay(capValue, req); !errors.Is(err, ErrReplayInProgress) {
		t.Fatalf("in-flight invalidation err=%v, want replay in progress", err)
	}
	if err := r.Abort(capValue, req); err != nil {
		t.Fatal(err)
	}

	other := req
	other.RequestID = "other"
	if decision, err := r.Authorize(capValue, other); err != nil || !decision.Acquired {
		t.Fatalf("other acquisition = %+v, err=%v", decision, err)
	}
	if err := r.InvalidateReplay(capValue, req); !errors.Is(err, ErrReplayDrift) {
		t.Fatalf("different digest invalidation err=%v, want replay drift", err)
	}
	if err := r.Abort(capValue, other); err != nil {
		t.Fatal(err)
	}
}

func TestIssueRejectsCapabilityPathOutsideOwnedRoot(t *testing.T) {
	workDir := t.TempDir()
	r := New()
	root := t.TempDir()
	_, _, err := r.Issue(IssueRequest{TaskID: "task", RuntimeID: "runtime", WorkspaceID: "workspace", WorkDir: workDir, OwnedRoot: root, Operation: OperationRepoCheckout, Targets: []Target{{URL: "https://github.com/acme/repo.git"}}}, filepath.Join(t.TempDir(), "outside", "cap"))
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("outside owned root err=%v, want unauthorized", err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	_, _, err = r.Issue(IssueRequest{TaskID: "task", RuntimeID: "runtime", WorkspaceID: "workspace", WorkDir: workDir, OwnedRoot: root, Operation: OperationRepoCheckout, Targets: []Target{{URL: "https://github.com/acme/repo.git"}}}, filepath.Join(root, "link", "cap"))
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("symlink owned root path err=%v, want unauthorized", err)
	}
}

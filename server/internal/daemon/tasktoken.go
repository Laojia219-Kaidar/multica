package daemon

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Task-token recovery over the daemon's loopback health listener (HIV-806).
//
// Credential-shaped env scrubbers (the DeepSeek Harness removes env names
// matching KEY|PASSWORD|SECRET|TOKEN from tool subprocesses; Codex's default
// secret guard behaves the same way) strip MULTICA_TOKEN from the agent's
// shell tools even though the daemon injected it. The task-scoped token stays
// in daemon memory; what crosses into the scrubbed environment is only a
// pointer env var (surviving every such scrub) to a task-owned 0400 file
// holding a random recovery capability. Presenting that capability plus the
// exact task id at POST /task-token releases the mat_ token — but only while
// that exact task is still running in this daemon.
const (
	// TaskTokenCapabilityFileEnv is the env pointer whose value is the path of
	// the task-local recovery capability file. The name deliberately contains
	// none of KEY/PASSWORD/SECRET/TOKEN so credential-shaped env scrubbers
	// leave it in place while stripping MULTICA_TOKEN.
	TaskTokenCapabilityFileEnv = "MULTICA_LOCAL_AUTH_CAPABILITY_FILE"

	// TaskTokenCapabilityHeader carries the opaque recovery capability on
	// POST /task-token requests.
	TaskTokenCapabilityHeader = "X-Multica-Task-Token-Capability"

	// TaskTokenPath is the recovery route, served only on the loopback-bound
	// health listener. Exported so the CLI recovery client references the
	// same wire contract.
	TaskTokenPath = "/task-token"

	// taskTokenCapabilityFile is the file name of the plaintext capability
	// inside the task-owned .multica capability directory.
	taskTokenCapabilityFile = "task-token-capability"

	// taskTokenCapabilityTTL bounds how long a recovery capability stays
	// usable. The registration is revoked when the task exits, so this only
	// caps the damage of a leak from a daemon that lost its cleanup path
	// (crash, kill -9): the token itself is short-lived server-side anyway.
	taskTokenCapabilityTTL = 24 * time.Hour

	// taskTokenRequestLimit bounds the accepted POST /task-token body size.
	taskTokenRequestLimit = 4096
)

// taskTokenRecord is one live per-task grant. The plaintext capability is
// never retained here — only its SHA-256 digest — and the task-scoped token
// never leaves daemon memory through this registry.
type taskTokenRecord struct {
	capabilityDigest [sha256.Size]byte
	taskID           string
	token            string
	file             string
	expires          time.Time
}

// taskTokenRegistry maps exact task id -> its live recovery grant. A record
// exists only between Register (before the agent child launch) and Revoke
// (task exit), so a live record also proves the task is currently running in
// this daemon.
type taskTokenRegistry struct {
	mu      sync.Mutex
	records map[string]*taskTokenRecord
}

func newTaskTokenRegistry() *taskTokenRegistry {
	return &taskTokenRegistry{records: make(map[string]*taskTokenRecord)}
}

// Register creates a random 256-bit capability, writes its only plaintext
// copy into a task-owned 0400 file under dir, and stores the digest plus the
// task-scoped token and an expiry. The token itself is never written to disk.
func (r *taskTokenRegistry) Register(taskID, token, dir string, ttl time.Duration) (string, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return "", errors.New("task token recovery: task id is empty")
	}
	if !strings.HasPrefix(strings.TrimSpace(token), "mat_") {
		return "", errors.New("task token recovery: token is not task-scoped")
	}
	if ttl <= 0 {
		return "", errors.New("task token recovery: ttl must be positive")
	}
	if strings.TrimSpace(dir) == "" {
		return "", errors.New("task token recovery: capability dir is empty")
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("task token recovery: random capability: %w", err)
	}
	capability := hex.EncodeToString(raw[:])
	digest := sha256.Sum256([]byte(capability))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("task token recovery: capability dir: %w", err)
	}
	path := filepath.Join(dir, taskTokenCapabilityFile)
	// A leftover regular file from a replaced registration for the same task
	// is removed; a symlink at the target is rejected outright so the
	// capability can never be written through a task-planted link.
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("task token recovery: capability path is a symlink")
		}
		if err := os.Remove(path); err != nil {
			return "", fmt.Errorf("task token recovery: stale capability file: %w", err)
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o400)
	if err != nil {
		return "", fmt.Errorf("task token recovery: capability file: %w", err)
	}
	if _, err := f.WriteString(capability); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("task token recovery: capability file write: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("task token recovery: capability file close: %w", err)
	}
	r.mu.Lock()
	r.records[taskID] = &taskTokenRecord{
		capabilityDigest: digest,
		taskID:           taskID,
		token:            token,
		file:             path,
		expires:          time.Now().Add(ttl),
	}
	r.mu.Unlock()
	return path, nil
}

// Resolve releases the task-scoped token only for the exact task id plus its
// opaque, unexpired capability. Every mismatch — unknown/expired/revoked
// task, wrong or empty capability — returns the same indistinguishable
// refusal.
func (r *taskTokenRegistry) Resolve(taskID, capability string, now time.Time) (string, bool) {
	if r == nil {
		return "", false
	}
	taskID = strings.TrimSpace(taskID)
	capability = strings.TrimSpace(capability)
	if taskID == "" || capability == "" {
		return "", false
	}
	digest := sha256.Sum256([]byte(capability))
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.resolveLocked(taskID, digest, now)
}

// resolveLocked validates and copies the token while the registry lock is
// held. This is the linearization point shared with Revoke: once Revoke has
// completed, no concurrent Resolve can still return a token from the removed
// record.
func (r *taskTokenRegistry) resolveLocked(taskID string, digest [sha256.Size]byte, now time.Time) (string, bool) {
	rec := r.records[taskID]
	if rec == nil {
		return "", false
	}
	if subtle.ConstantTimeCompare(rec.capabilityDigest[:], digest[:]) != 1 {
		return "", false
	}
	if now.After(rec.expires) {
		return "", false
	}
	return rec.token, true
}

// Revoke drops a task's registration and removes its capability file.
// Idempotent: revoking an unknown task is a no-op.
func (r *taskTokenRegistry) Revoke(taskID string) error {
	if r == nil {
		return nil
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil
	}
	r.mu.Lock()
	rec := r.records[taskID]
	delete(r.records, taskID)
	r.mu.Unlock()
	if rec == nil {
		return nil
	}
	if err := os.Remove(rec.file); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("task token recovery: remove capability file: %w", err)
	}
	return nil
}

// taskTokenRequest is the body of a POST /task-token request.
type taskTokenRequest struct {
	TaskID string `json:"task_id"`
}

// taskTokenResponse is the success body of POST /task-token.
type taskTokenResponse struct {
	Token string `json:"token"`
}

// taskTokenHandler returns the POST /task-token handler for the loopback
// health listener. Wrong, expired, revoked, or non-running tasks are all
// refused with the same status and body; neither the capability nor the
// token is ever echoed or logged, and every response is no-store.
func (d *Daemon) taskTokenHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// The task id and capability travel in the body and an opaque header
		// respectively; query selectors are refused outright.
		if r.URL.RawQuery != "" {
			http.Error(w, "query parameters are not accepted", http.StatusBadRequest)
			return
		}
		var req taskTokenRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, taskTokenRequestLimit))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		taskID := strings.TrimSpace(req.TaskID)
		if taskID == "" {
			http.Error(w, "task_id is required", http.StatusBadRequest)
			return
		}
		token, ok := d.taskTokens.Resolve(taskID, r.Header.Get(TaskTokenCapabilityHeader), time.Now())
		if !ok || !strings.HasPrefix(token, "mat_") {
			http.Error(w, "task token unavailable", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(taskTokenResponse{Token: token})
	}
}

// ensureTaskTokenRegistry lazily initializes the registry for hand-built
// daemon fixtures that did not go through New().
func (d *Daemon) ensureTaskTokenRegistry() *taskTokenRegistry {
	if d.taskTokens == nil {
		d.taskTokens = newTaskTokenRegistry()
	}
	return d.taskTokens
}

// registerTaskTokenRecovery issues the per-task recovery capability before
// the agent child is launched. Failure is non-fatal and only warns: the
// direct MULTICA_TOKEN injection still works, so without the recovery path
// the task behaves exactly as before this feature. Only the task id and the
// error are logged — never the token or the capability.
func (d *Daemon) registerTaskTokenRecovery(taskID, token, dir string, taskLog *slog.Logger) string {
	path, err := d.ensureTaskTokenRegistry().Register(taskID, token, dir, taskTokenCapabilityTTL)
	if err != nil {
		if taskLog != nil {
			taskLog.Warn("task token recovery registration failed; MULTICA_TOKEN env remains the only credential path",
				"task_id", taskID, "error", err)
		}
		return ""
	}
	return path
}

// Package mutationbroker contains the daemon-local authority for task-scoped
// repository mutations. Capability values are deliberately never retained in
// memory after issuance; only their SHA-256 digest is kept.
package mutationbroker

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	OperationRepoCheckout = "repo.checkout"
	CapabilityHeader      = "X-Multica-Mutation-Capability"
	RequestIDHeader       = "X-Multica-Mutation-Request-ID"
)

var (
	ErrInvalidCapability = errors.New("mutation capability invalid")
	ErrExpiredCapability = errors.New("mutation capability expired")
	ErrUnauthorized      = errors.New("mutation not authorized")
	ErrReplayDrift       = errors.New("mutation request replay drift")
	ErrReplayInProgress  = errors.New("mutation request already in progress")
	ErrRequestLimit      = errors.New("mutation request limit exceeded")
	ErrResultTooLarge    = errors.New("mutation result too large")
)

const (
	maxRequestsPerCapability = 128
	maxResultBytes           = 64 << 10
)

// Target is the immutable authority granted to one task. URL/ref are matched
// exactly after trimming; ResourceID is optional for legacy/off tasks.
type Target struct {
	ResourceID string
	URL        string
	Ref        string
}

type IssueRequest struct {
	TaskID, RuntimeID, WorkspaceID, WorkDir, AgentName string
	OwnedRoot                                          string
	Operation                                          string
	CheckoutMode                                       string
	Targets                                            []Target
	TTL                                                time.Duration
}

type CheckoutRequest struct {
	TaskID       string `json:"task_id"`
	RuntimeID    string `json:"runtime_id"`
	WorkspaceID  string `json:"workspace_id"`
	WorkDir      string `json:"workdir"`
	AgentName    string `json:"agent_name"`
	URL          string `json:"url"`
	Ref          string `json:"ref"`
	Operation    string `json:"operation"`
	RequestID    string `json:"request_id"`
	CheckoutMode string `json:"checkout_mode"`
}

// Authority is a daemon-private grant for transports that cannot safely hand
// a bearer capability to the task. It stores only the registry digest; the
// plaintext capability is never retained after Grant returns.
type Authority struct {
	registry *Registry
	digest   [32]byte
	mu       sync.Mutex
}

func (a *Authority) snapshot() (*Registry, [32]byte, bool) {
	if a == nil {
		return nil, [32]byte{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.registry, a.digest, a.registry != nil && a.digest != ([32]byte{})
}

func (a *Authority) Authorize(req CheckoutRequest) (Decision, error) {
	registry, digest, ok := a.snapshot()
	if !ok {
		return Decision{}, ErrInvalidCapability
	}
	return registry.authorizeDigest(digest, req)
}

func (a *Authority) Complete(req CheckoutRequest, result []byte) error {
	registry, digest, ok := a.snapshot()
	if !ok {
		return ErrInvalidCapability
	}
	return registry.completeDigest(digest, req, result)
}

func (a *Authority) Abort(req CheckoutRequest) error {
	registry, digest, ok := a.snapshot()
	if !ok {
		return ErrInvalidCapability
	}
	return registry.abortDigest(digest, req)
}

func (a *Authority) InvalidateReplay(req CheckoutRequest) error {
	registry, digest, ok := a.snapshot()
	if !ok {
		return ErrInvalidCapability
	}
	return registry.invalidateReplayDigest(digest, req)
}

type record struct {
	digest      [32]byte
	request     map[string]requestRecord
	expires     time.Time
	requestBase IssueRequest
	realWorkDir string
}
type requestRecord struct {
	digest   [32]byte
	result   []byte
	inFlight bool
}

// Decision is returned by Authorize. Replay is non-nil only for a completed
// exact replay and is a copy owned by the caller. Acquired is true only when
// this call created the request's in-flight record and therefore owns Abort.
type Decision struct {
	Replay   []byte
	Acquired bool
}

type Registry struct {
	mu       sync.Mutex
	byDigest map[[32]byte]*record
	files    map[string][32]byte
}

func New() *Registry {
	return &Registry{byDigest: make(map[[32]byte]*record), files: make(map[string][32]byte)}
}

func hashCapability(capability string) [32]byte { return sha256.Sum256([]byte(capability)) }

func canonicalTargets(targets []Target) []Target {
	out := append([]Target(nil), targets...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].URL != out[j].URL {
			return out[i].URL < out[j].URL
		}
		if out[i].Ref != out[j].Ref {
			return out[i].Ref < out[j].Ref
		}
		return out[i].ResourceID < out[j].ResourceID
	})
	return out
}

func canonicalRef(ref string) string {
	decoded, err := url.PathUnescape(strings.TrimSpace(ref))
	if err != nil {
		return strings.TrimSpace(ref)
	}
	return decoded
}

func normalizeIssue(in IssueRequest) (IssueRequest, error) {
	in.TaskID, in.RuntimeID, in.WorkspaceID, in.WorkDir, in.AgentName, in.OwnedRoot, in.Operation = strings.TrimSpace(in.TaskID), strings.TrimSpace(in.RuntimeID), strings.TrimSpace(in.WorkspaceID), strings.TrimSpace(in.WorkDir), strings.TrimSpace(in.AgentName), strings.TrimSpace(in.OwnedRoot), strings.TrimSpace(in.Operation)
	if in.TaskID == "" || in.RuntimeID == "" || in.WorkspaceID == "" || in.WorkDir == "" || in.Operation == "" {
		return IssueRequest{}, ErrUnauthorized
	}
	if in.CheckoutMode != "" && in.CheckoutMode != "isolated" {
		return IssueRequest{}, ErrUnauthorized
	}
	abs, err := filepath.Abs(in.WorkDir)
	if err != nil {
		return IssueRequest{}, ErrUnauthorized
	}
	in.WorkDir = filepath.Clean(abs)
	if in.TTL <= 0 {
		in.TTL = 2 * time.Hour
	}
	seen := make(map[string]struct{}, len(in.Targets))
	for i := range in.Targets {
		in.Targets[i].URL, in.Targets[i].Ref, in.Targets[i].ResourceID = strings.TrimSpace(in.Targets[i].URL), strings.TrimSpace(in.Targets[i].Ref), strings.TrimSpace(in.Targets[i].ResourceID)
		if in.Targets[i].URL == "" {
			return IssueRequest{}, ErrUnauthorized
		}
		key := in.Targets[i].URL + "\x00" + canonicalRef(in.Targets[i].Ref)
		if _, ok := seen[key]; ok {
			return IssueRequest{}, ErrUnauthorized
		}
		seen[key] = struct{}{}
	}
	in.Targets = canonicalTargets(in.Targets)
	return in, nil
}

// Issue creates a random 256-bit capability and writes its only plaintext copy
// into a task-owned file. The registry keeps only the digest.
func (r *Registry) Issue(in IssueRequest, dir string) (string, string, error) {
	in, err := normalizeIssue(in)
	if err != nil {
		return "", "", err
	}
	realWorkDir, err := filepath.EvalSymlinks(in.WorkDir)
	if err != nil {
		return "", "", ErrUnauthorized
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", "", fmt.Errorf("random capability: %w", err)
	}
	capability := hex.EncodeToString(raw[:])
	digest := hashCapability(capability)
	if dir == "" {
		return "", "", ErrUnauthorized
	}
	if err := rejectSymlinkComponents(dir); err != nil {
		return "", "", err
	}
	if in.OwnedRoot != "" {
		root, rootErr := filepath.Abs(in.OwnedRoot)
		if rootErr != nil || !pathWithin(root, dir) {
			return "", "", ErrUnauthorized
		}
		if err := rejectSymlinkComponentsWithin(root, dir); err != nil {
			return "", "", err
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", fmt.Errorf("capability directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", "", fmt.Errorf("capability directory mode: %w", err)
	}
	path := filepath.Join(dir, "mutation-capability")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o400)
	if err != nil {
		return "", "", fmt.Errorf("capability file: %w", err)
	}
	if _, err = f.WriteString(capability); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", "", fmt.Errorf("capability file write: %w", err)
	}
	if err = f.Close(); err != nil {
		_ = os.Remove(path)
		return "", "", fmt.Errorf("capability file close: %w", err)
	}
	r.mu.Lock()
	r.byDigest[digest] = &record{digest: digest, expires: time.Now().Add(in.TTL), request: make(map[string]requestRecord), requestBase: in, realWorkDir: realWorkDir}
	r.files[path] = digest
	r.mu.Unlock()
	return path, capability, nil
}

// Grant creates the same exact-replay registry record as Issue without
// creating a task-readable file. It is used only by the daemon-owned Unix
// transport; the returned Authority never crosses the process boundary.
func (r *Registry) Grant(in IssueRequest) (*Authority, error) {
	in, err := normalizeIssue(in)
	if err != nil {
		return nil, err
	}
	realWorkDir, err := filepath.EvalSymlinks(in.WorkDir)
	if err != nil {
		return nil, ErrUnauthorized
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return nil, fmt.Errorf("random grant: %w", err)
	}
	capability := hex.EncodeToString(raw[:])
	digest := hashCapability(capability)
	r.mu.Lock()
	r.byDigest[digest] = &record{digest: digest, expires: time.Now().Add(in.TTL), request: make(map[string]requestRecord), requestBase: in, realWorkDir: realWorkDir}
	r.mu.Unlock()
	// The plaintext is used only to derive the digest and is not retained in
	// the Authority or Registry record.
	return &Authority{registry: r, digest: digest}, nil
}

// RevokeAuthority drops a daemon-private grant without exposing its bearer.
func (r *Registry) RevokeAuthority(a *Authority) error {
	if a == nil {
		return ErrInvalidCapability
	}
	a.mu.Lock()
	if a.registry != r || a.digest == ([32]byte{}) {
		a.mu.Unlock()
		return ErrInvalidCapability
	}
	digest := a.digest
	a.digest = [32]byte{}
	a.registry = nil
	a.mu.Unlock()
	r.mu.Lock()
	delete(r.byDigest, digest)
	r.mu.Unlock()
	return nil
}

func rejectSymlinkComponents(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return ErrUnauthorized
	}
	info, statErr := os.Lstat(abs)
	if statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return ErrUnauthorized
	}
	if os.IsNotExist(statErr) {
		parent := filepath.Dir(abs)
		if info, parentErr := os.Lstat(parent); parentErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return ErrUnauthorized
		}
	}
	return nil
}

func rejectSymlinkComponentsWithin(root, path string) error {
	rootAbs, rootErr := filepath.Abs(root)
	pathAbs, pathErr := filepath.Abs(path)
	if rootErr != nil || pathErr != nil {
		return ErrUnauthorized
	}
	for current := filepath.Clean(pathAbs); ; current = filepath.Dir(current) {
		info, statErr := os.Lstat(current)
		if statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return ErrUnauthorized
		}
		if statErr != nil && !os.IsNotExist(statErr) {
			return ErrUnauthorized
		}
		if current == filepath.Clean(rootAbs) {
			return nil
		}
		parent := filepath.Dir(current)
		if parent == current || !pathWithin(rootAbs, parent) {
			return ErrUnauthorized
		}
	}
}

func pathWithin(root, path string) bool {
	rootAbs, rootErr := filepath.Abs(root)
	pathAbs, pathErr := filepath.Abs(path)
	if rootErr != nil || pathErr != nil {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(rootAbs), filepath.Clean(pathAbs))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func requestDigest(req CheckoutRequest) [32]byte {
	s := strings.Join([]string{strings.TrimSpace(req.TaskID), strings.TrimSpace(req.RuntimeID), strings.TrimSpace(req.WorkspaceID), filepath.Clean(strings.TrimSpace(req.WorkDir)), strings.TrimSpace(req.AgentName), strings.TrimSpace(req.URL), strings.TrimSpace(req.Ref), strings.TrimSpace(req.Operation), strings.TrimSpace(req.CheckoutMode), strings.TrimSpace(req.RequestID)}, "\x00")
	return sha256.Sum256([]byte(s))
}

func (r *Registry) match(rec *record, req CheckoutRequest) bool {
	base := rec.requestBase
	if strings.TrimSpace(req.TaskID) != base.TaskID || strings.TrimSpace(req.RuntimeID) != base.RuntimeID || strings.TrimSpace(req.WorkspaceID) != base.WorkspaceID || filepath.Clean(strings.TrimSpace(req.WorkDir)) != base.WorkDir || strings.TrimSpace(req.AgentName) != base.AgentName || strings.TrimSpace(req.Operation) != base.Operation || strings.TrimSpace(req.CheckoutMode) != base.CheckoutMode {
		return false
	}
	if _, err := os.Stat(base.WorkDir); err != nil {
		return false
	}
	realWorkDir, err := filepath.EvalSymlinks(strings.TrimSpace(req.WorkDir))
	if err != nil || realWorkDir != rec.realWorkDir {
		return false
	}
	for _, t := range base.Targets {
		if t.URL == strings.TrimSpace(req.URL) && canonicalRef(t.Ref) == canonicalRef(req.Ref) {
			return true
		}
	}
	return false
}

// Authorize validates the opaque capability and exact task/workdir/target
// authority before any repository cache operation. Empty/unknown/replayed
// drifted requests fail closed.
func (r *Registry) Authorize(capability string, req CheckoutRequest) (Decision, error) {
	if strings.TrimSpace(capability) == "" || strings.TrimSpace(req.RequestID) == "" {
		return Decision{}, ErrInvalidCapability
	}
	digest := hashCapability(strings.TrimSpace(capability))
	return r.authorizeDigest(digest, req)
}

func (r *Registry) authorizeDigest(digest [32]byte, req CheckoutRequest) (Decision, error) {
	if digest == ([32]byte{}) || strings.TrimSpace(req.RequestID) == "" {
		return Decision{}, ErrInvalidCapability
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	rec := r.byDigest[digest]
	if rec == nil {
		return Decision{}, ErrInvalidCapability
	}
	if time.Now().After(rec.expires) {
		return Decision{}, ErrExpiredCapability
	}
	if prior, ok := rec.request[req.RequestID]; ok {
		if prior.digest != requestDigest(req) {
			return Decision{}, ErrReplayDrift
		}
	}
	if !r.match(rec, req) {
		return Decision{}, ErrUnauthorized
	}
	rd := requestDigest(req)
	if prior, ok := rec.request[req.RequestID]; ok {
		if prior.digest != rd {
			return Decision{}, ErrReplayDrift
		}
		if prior.inFlight {
			return Decision{}, ErrReplayInProgress
		}
		return Decision{Replay: append([]byte(nil), prior.result...)}, nil
	}
	if len(rec.request) >= maxRequestsPerCapability {
		return Decision{}, ErrRequestLimit
	}
	rec.request[req.RequestID] = requestRecord{digest: rd, inFlight: true}
	return Decision{Acquired: true}, nil
}

func (r *Registry) Complete(capability string, req CheckoutRequest, result []byte) error {
	if strings.TrimSpace(capability) == "" {
		return ErrInvalidCapability
	}
	digest := hashCapability(strings.TrimSpace(capability))
	return r.completeDigest(digest, req, result)
}

func (r *Registry) completeDigest(digest [32]byte, req CheckoutRequest, result []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec := r.byDigest[digest]
	if rec == nil {
		return ErrInvalidCapability
	}
	if time.Now().After(rec.expires) {
		return ErrExpiredCapability
	}
	if !r.match(rec, req) {
		return ErrUnauthorized
	}
	prior, ok := rec.request[req.RequestID]
	if !ok || prior.digest != requestDigest(req) {
		return ErrReplayDrift
	}
	if !prior.inFlight {
		return ErrReplayInProgress
	}
	if len(result) > maxResultBytes {
		return ErrResultTooLarge
	}
	prior.inFlight = false
	prior.result = append([]byte(nil), result...)
	rec.request[req.RequestID] = prior
	return nil
}

// Abort releases an in-flight request after any pre-completion failure so the
// same request ID can be retried without weakening replay binding.
func (r *Registry) Abort(capability string, req CheckoutRequest) error {
	if strings.TrimSpace(capability) == "" {
		return ErrInvalidCapability
	}
	digest := hashCapability(strings.TrimSpace(capability))
	return r.abortDigest(digest, req)
}

func (r *Registry) abortDigest(digest [32]byte, req CheckoutRequest) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec := r.byDigest[digest]
	if rec == nil {
		return ErrInvalidCapability
	}
	prior, ok := rec.request[req.RequestID]
	if !ok || prior.digest != requestDigest(req) {
		return ErrReplayDrift
	}
	if !prior.inFlight {
		return ErrReplayInProgress
	}
	delete(rec.request, req.RequestID)
	return nil
}

// InvalidateReplay drops only a completed replay record owned by this exact
// capability and request digest. In-flight work and drifted requests remain
// untouched so a concurrent owner cannot be canceled by a stale replay.
func (r *Registry) InvalidateReplay(capability string, req CheckoutRequest) error {
	if strings.TrimSpace(capability) == "" {
		return ErrInvalidCapability
	}
	digest := hashCapability(strings.TrimSpace(capability))
	return r.invalidateReplayDigest(digest, req)
}

func (r *Registry) invalidateReplayDigest(digest [32]byte, req CheckoutRequest) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec := r.byDigest[digest]
	if rec == nil {
		return ErrInvalidCapability
	}
	prior, ok := rec.request[req.RequestID]
	if !ok || prior.digest != requestDigest(req) {
		return ErrReplayDrift
	}
	if prior.inFlight {
		return ErrReplayInProgress
	}
	delete(rec.request, req.RequestID)
	return nil
}

// Sweep expires capability records and removes their owned files. It is safe
// to call periodically from the daemon and never removes unregistered paths.
func (r *Registry) Sweep(now time.Time) {
	r.mu.Lock()
	var paths []string
	for digest, rec := range r.byDigest {
		if now.After(rec.expires) {
			delete(r.byDigest, digest)
			for path, d := range r.files {
				if d == digest {
					delete(r.files, path)
					paths = append(paths, path)
				}
			}
		}
	}
	r.mu.Unlock()
	for _, path := range paths {
		_ = os.Remove(path)
	}
}

// Revoke forgets the hash and removes only the task-owned capability file.
func (r *Registry) Revoke(capabilityFile string) error {
	r.mu.Lock()
	digest, ok := r.files[capabilityFile]
	if !ok {
		r.mu.Unlock()
		return nil
	}
	delete(r.byDigest, digest)
	delete(r.files, capabilityFile)
	r.mu.Unlock()
	if capabilityFile == "" {
		return nil
	}
	if err := os.Remove(capabilityFile); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

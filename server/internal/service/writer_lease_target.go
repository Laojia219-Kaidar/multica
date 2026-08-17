package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"

	"github.com/google/uuid"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// WriterLeaseMode is the server-resolved rollout mode for the migration-262
// writer lease. Unknown values are intentionally rejected by ResolveTargets.
type WriterLeaseMode string

const (
	WriterLeaseModeOff     WriterLeaseMode = "off"
	WriterLeaseModeShadow  WriterLeaseMode = "shadow"
	WriterLeaseModeEnforce WriterLeaseMode = "enforce"
)

const WriterLeaseCapabilityV1 = "writer-lease-v1"

var (
	ErrWriterLeaseInvalidMode   = errors.New("writer lease: invalid mode")
	ErrWriterLeaseNoProject     = errors.New("writer lease: project is required")
	ErrWriterLeaseNoGithubRepo  = errors.New("writer lease: no project github_repo target")
	ErrWriterLeaseInvalidTarget = errors.New("writer lease: invalid project resource")
	ErrWriterLeaseFenceRejected = errors.New("writer lease: terminal fence rejected")
)

// WriterLeaseResource is the narrow, server-loaded project resource shape used
// by the resolver. Callers cannot provide a mutex key or holder.
type WriterLeaseResource struct {
	ID                uuid.UUID
	ResourceType      string
	URL               string
	Ref               string
	DefaultBranchHint string
}

// WriterLeaseTarget is safe to send to a daemon. The daemon never receives
// the lease token or fencing generation in a claim response.
type WriterLeaseTarget struct {
	ResourceID string `json:"resource_id"`
	MutexKey   string `json:"mutex_key"`
	URL        string `json:"url"`
	Ref        string `json:"ref"`
}

// WriterLeaseClaim is the immutable, task-scoped authority captured when a
// daemon claims a task. It deliberately contains no token, generation, or
// holder: those remain properties of migration-262 lease rows and sessions.
type WriterLeaseClaim struct {
	Mode    WriterLeaseMode
	Targets []WriterLeaseTarget
	Digest  string
}

// canonicalWriterLeaseTarget is intentionally private so the persisted JSON
// shape cannot grow token/fencing fields by accident.
type canonicalWriterLeaseTarget struct {
	ResourceID string `json:"resource_id"`
	MutexKey   string `json:"mutex_key"`
	URL        string `json:"url"`
	Ref        string `json:"ref"`
}

// CanonicalWriterLeaseClaim serializes the persisted target array and hashes a
// stable mode+targets envelope. The mode is deliberately absent from the
// persisted JSON array, but remains cryptographically bound to its digest.
// The workspace is part of the canonical mutex key, so callers must provide
// the exact runtime workspace that owns the task.
func CanonicalWriterLeaseClaim(mode WriterLeaseMode, workspaceID string, targets []WriterLeaseTarget) (canonical []byte, digest string, err error) {
	normalizedMode, err := NormalizeWriterLeaseMode(string(mode))
	if err != nil {
		return nil, "", err
	}
	if _, err := writerLeaseCanonicalUUID(workspaceID, "workspace"); err != nil {
		return nil, "", err
	}
	if normalizedMode == WriterLeaseModeOff && len(targets) != 0 {
		return nil, "", fmt.Errorf("%w: off claim cannot carry targets", ErrWriterLeaseInvalidTarget)
	}
	ordered := make([]canonicalWriterLeaseTarget, 0, len(targets))
	seenResources := make(map[string]struct{}, len(targets))
	seenMutexes := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		resourceID, err := writerLeaseCanonicalUUID(target.ResourceID, "resource_id")
		if err != nil {
			return nil, "", err
		}
		if target.URL == "" || target.URL != strings.TrimSpace(target.URL) {
			return nil, "", fmt.Errorf("%w: empty or noncanonical URL", ErrWriterLeaseInvalidTarget)
		}
		decodedRef, decodeErr := url.PathUnescape(target.Ref)
		if decodeErr != nil || target.Ref == "" || target.Ref != NormalizeWriterLeaseRef(decodedRef, "") {
			return nil, "", fmt.Errorf("%w: noncanonical ref", ErrWriterLeaseInvalidTarget)
		}
		expectedMutex := writerLeaseMutexKeyNormalized(workspaceID, resourceID, target.Ref)
		if target.MutexKey == "" || target.MutexKey != expectedMutex {
			return nil, "", fmt.Errorf("%w: noncanonical mutex key", ErrWriterLeaseInvalidTarget)
		}
		if _, exists := seenResources[resourceID]; exists {
			return nil, "", fmt.Errorf("%w: duplicate resource", ErrWriterLeaseInvalidTarget)
		}
		if _, exists := seenMutexes[target.MutexKey]; exists {
			return nil, "", fmt.Errorf("%w: duplicate mutex", ErrWriterLeaseInvalidTarget)
		}
		seenResources[resourceID] = struct{}{}
		seenMutexes[target.MutexKey] = struct{}{}
		ordered = append(ordered, canonicalWriterLeaseTarget{
			ResourceID: resourceID,
			MutexKey:   target.MutexKey,
			URL:        target.URL,
			Ref:        target.Ref,
		})
	}
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].MutexKey < ordered[j].MutexKey })
	canonical, err = json.Marshal(ordered)
	if err != nil {
		return nil, "", fmt.Errorf("marshal writer lease claim: %w", err)
	}
	digestInput, err := json.Marshal(struct {
		Mode    WriterLeaseMode              `json:"mode"`
		Targets []canonicalWriterLeaseTarget `json:"targets"`
	}{Mode: normalizedMode, Targets: ordered})
	if err != nil {
		return nil, "", fmt.Errorf("marshal writer lease digest envelope: %w", err)
	}
	hash := sha256.Sum256(digestInput)
	return canonical, hex.EncodeToString(hash[:]), nil
}

// DecodePersistedWriterLeaseClaim validates the all-or-none database fields
// and requires the stored JSON and digest to be exactly canonical. legacy is
// true only for the pre-406 all-NULL row; partial/corrupt state fails closed.
func DecodePersistedWriterLeaseClaim(task db.AgentTaskQueue, workspaceID string) (claim WriterLeaseClaim, legacy bool, err error) {
	modeSet := task.WriterLeaseClaimMode.Valid
	snapshotSet := len(task.WriterLeaseTargetSnapshot) != 0
	digestSet := task.WriterLeaseTargetDigest.Valid
	if !modeSet && !snapshotSet && !digestSet {
		return WriterLeaseClaim{}, true, nil
	}
	if !modeSet || !snapshotSet || !digestSet {
		return WriterLeaseClaim{}, false, fmt.Errorf("%w: partial persisted claim", ErrWriterLeaseFenceRejected)
	}
	mode, err := NormalizeWriterLeaseMode(task.WriterLeaseClaimMode.String)
	if err != nil {
		return WriterLeaseClaim{}, false, err
	}
	targets, err := decodeCanonicalWriterLeaseTargets(task.WriterLeaseTargetSnapshot)
	if err != nil {
		return WriterLeaseClaim{}, false, err
	}
	_, digest, err := CanonicalWriterLeaseClaim(mode, workspaceID, targets)
	if err != nil {
		return WriterLeaseClaim{}, false, err
	}
	// PostgreSQL JSONB canonicalizes object/array representation on storage and
	// readback, so raw bytes are not an authority. The strict decoder rejects
	// unknown fields, while the canonical re-encoding and digest validate the
	// semantic target set and its canonical hash.
	if task.WriterLeaseTargetDigest.String != digest {
		return WriterLeaseClaim{}, false, fmt.Errorf("%w: persisted claim digest or encoding mismatch", ErrWriterLeaseFenceRejected)
	}
	return WriterLeaseClaim{Mode: mode, Targets: targets, Digest: digest}, false, nil
}

func decodeCanonicalWriterLeaseTargets(raw []byte) ([]WriterLeaseTarget, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, fmt.Errorf("%w: target snapshot must be a JSON array", ErrWriterLeaseFenceRejected)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var encoded []canonicalWriterLeaseTarget
	if err := decoder.Decode(&encoded); err != nil {
		return nil, fmt.Errorf("%w: invalid target snapshot: %v", ErrWriterLeaseFenceRejected, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("%w: target snapshot has trailing data", ErrWriterLeaseFenceRejected)
	}
	targets := make([]WriterLeaseTarget, 0, len(encoded))
	for _, target := range encoded {
		targets = append(targets, WriterLeaseTarget{
			ResourceID: target.ResourceID,
			MutexKey:   target.MutexKey,
			URL:        target.URL,
			Ref:        target.Ref,
		})
	}
	return targets, nil
}

func writerLeaseCanonicalUUID(raw, field string) (string, error) {
	if raw == "" || raw != strings.TrimSpace(raw) {
		return "", fmt.Errorf("%w: invalid %s", ErrWriterLeaseInvalidTarget, field)
	}
	parsed, err := uuid.Parse(raw)
	if err != nil || parsed.String() != raw {
		return "", fmt.Errorf("%w: noncanonical %s", ErrWriterLeaseInvalidTarget, field)
	}
	return raw, nil
}

func NormalizeWriterLeaseMode(raw string) (WriterLeaseMode, error) {
	switch WriterLeaseMode(strings.ToLower(strings.TrimSpace(raw))) {
	case WriterLeaseModeOff:
		return WriterLeaseModeOff, nil
	case WriterLeaseModeShadow:
		return WriterLeaseModeShadow, nil
	case WriterLeaseModeEnforce:
		return WriterLeaseModeEnforce, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrWriterLeaseInvalidMode, raw)
	}
}

// NormalizeWriterLeaseRef applies the ref precedence contract and encodes it
// as one path segment, preventing caller/path ambiguity in a canonical key.
func NormalizeWriterLeaseRef(ref, defaultBranchHint string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		ref = strings.TrimSpace(defaultBranchHint)
	}
	if ref == "" {
		ref = "default"
	}
	for _, prefix := range []string{"refs/heads/", "heads/", "refs/remotes/origin/", "remotes/origin/", "refs/remotes/", "remotes/", "refs/tags/", "tags/", "origin/"} {
		ref = strings.TrimPrefix(ref, prefix)
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		ref = "default"
	}
	return url.PathEscape(ref)
}

func WriterLeaseMutexKey(workspaceID, projectResourceID, ref string) string {
	return "canonical-worktree:ws/" + url.PathEscape(strings.TrimSpace(workspaceID)) + "/repo/" + url.PathEscape(strings.TrimSpace(projectResourceID)) + "/ref/" + NormalizeWriterLeaseRef(ref, "")
}

func writerLeaseMutexKeyNormalized(workspaceID, projectResourceID, normalizedRef string) string {
	return "canonical-worktree:ws/" + url.PathEscape(strings.TrimSpace(workspaceID)) + "/repo/" + url.PathEscape(strings.TrimSpace(projectResourceID)) + "/ref/" + normalizedRef
}

func WriterLeaseHolderID(daemonID, runtimeID, taskID string) string {
	return "daemon/" + url.PathEscape(strings.TrimSpace(daemonID)) + "/runtime/" + url.PathEscape(strings.TrimSpace(runtimeID)) + "/task/" + url.PathEscape(strings.TrimSpace(taskID))
}

// ResolveWriterLeaseTargets resolves only project-level, persisted
// github_repo rows. Workspace repos and caller-supplied URLs are intentionally
// not accepted as lease authority.
func ResolveWriterLeaseTargets(mode WriterLeaseMode, workspaceID, projectID, daemonID, runtimeID, taskID string, resources []WriterLeaseResource) ([]WriterLeaseTarget, error) {
	if _, err := NormalizeWriterLeaseMode(string(mode)); err != nil {
		return nil, err
	}
	if mode == WriterLeaseModeOff {
		return []WriterLeaseTarget{}, nil
	}
	if strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(projectID) == "" {
		return nil, ErrWriterLeaseNoProject
	}
	if strings.TrimSpace(taskID) == "" || strings.TrimSpace(runtimeID) == "" {
		return nil, ErrWriterLeaseInvalidTarget
	}
	targets := make([]WriterLeaseTarget, 0, len(resources))
	for _, resource := range resources {
		if resource.ResourceType != "github_repo" {
			continue
		}
		if resource.ID == uuid.Nil || strings.TrimSpace(resource.URL) == "" {
			return nil, ErrWriterLeaseInvalidTarget
		}
		id := resource.ID.String()
		ref := NormalizeWriterLeaseRef(resource.Ref, resource.DefaultBranchHint)
		targets = append(targets, WriterLeaseTarget{
			ResourceID: id,
			MutexKey:   writerLeaseMutexKeyNormalized(workspaceID, id, ref),
			URL:        strings.TrimSpace(resource.URL),
			Ref:        ref,
		})
	}
	if len(targets) == 0 {
		return nil, ErrWriterLeaseNoGithubRepo
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].MutexKey < targets[j].MutexKey })
	return targets, nil
}

// WriterLeaseBatch acquires targets in canonical order and releases acquired
// sessions in reverse order if any acquire fails.
type WriterLeaseBatch struct{ Sessions []*WriterLeaseSession }

func AcquireWriterLeaseBatch(ctx context.Context, guard *WriterLeaseGuard, kind WriterLeaseTaskKind, targets []WriterLeaseTarget, holder string) (*WriterLeaseBatch, error) {
	if len(targets) == 0 {
		return &WriterLeaseBatch{}, nil
	}
	ordered := append([]WriterLeaseTarget(nil), targets...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].MutexKey < ordered[j].MutexKey })
	b := &WriterLeaseBatch{Sessions: make([]*WriterLeaseSession, 0, len(targets))}
	for _, target := range ordered {
		session, err := guard.AcquireForExecution(ctx, kind, target.MutexKey, holder)
		if err != nil {
			_ = b.Release(context.WithoutCancel(ctx))
			return nil, err
		}
		b.Sessions = append(b.Sessions, session)
	}
	return b, nil
}

func (b *WriterLeaseBatch) Release(ctx context.Context) error {
	if b == nil {
		return nil
	}
	var first error
	for i := len(b.Sessions) - 1; i >= 0; i-- {
		if err := b.Sessions[i].ReleaseAfterTerminal(ctx); err != nil && first == nil {
			first = err
		}
	}
	return first
}

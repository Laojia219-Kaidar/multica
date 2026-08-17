package service

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/google/uuid"
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

package workentry

import (
	"context"
	"sort"
	"strings"
)

// UnregisteredWork is one discovered-but-unclaimed development action. It is
// the material the unregistered-work inbox surfaces for attach/ignore (VC-05).
type UnregisteredWork struct {
	Path     string `json:"path"`
	Branch   string `json:"branch,omitempty"`
	HEAD     string `json:"head"`
	Detached bool   `json:"detached"`
	Prunable bool   `json:"prunable"`
	Reason   string `json:"reason"` // why it is treated as unregistered
}

// ReconcileWorktrees scans the canonical repo's worktrees and returns those
// whose branch/revision has no registered work entry. It never writes; the
// caller feeds the results into the inbox surface (attach/ignore).
func (s *Service) ReconcileWorktrees(ctx context.Context, workspaceID, repoPath string) ([]UnregisteredWork, error) {
	if strings.TrimSpace(repoPath) == "" {
		return nil, ErrInvalidRequest
	}
	observed, err := ScanGitWorktrees(repoPath)
	if err != nil {
		return nil, err
	}
	// No registered worktree→work_ref mapping exists yet on the persistence
	// path, so every observed worktree is a candidate. The registered-key set
	// is empty here; callers can pass a richer set through ReconcileUnregistered
	// once receipts persist repo+branch+revision lineage.
	return ReconcileUnregistered(observed, nil), nil
}

// ReconcileUnregistered is the pure registered-vs-observed computation. A
// worktree is "unregistered" when no registered project title/repo identity
// matches its branch or path. Prunable entries are reported with that reason.
func ReconcileUnregistered(observed []ObservedWorktree, registered map[string]bool) []UnregisteredWork {
	out := []UnregisteredWork{}
	for _, w := range observed {
		reason := "no registered work entry matches this branch/worktree"
		if w.Prunable {
			reason = "prunable gitdir (stale worktree)"
		} else if w.Branch == "" {
			reason = "detached HEAD worktree with no registered work entry"
		}
		// A registered project title that appears in the branch or path counts
		// as a (loose) match; otherwise it is unregistered.
		matched := false
		for key := range registered {
			if strings.Contains(w.Branch, key) || strings.Contains(w.Path, key) {
				matched = true
				break
			}
		}
		if !matched {
			out = append(out, UnregisteredWork{
				Path: w.Path, Branch: w.Branch, HEAD: w.HEAD,
				Detached: w.Detached, Prunable: w.Prunable, Reason: reason,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

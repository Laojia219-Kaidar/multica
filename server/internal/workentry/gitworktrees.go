package workentry

import (
	"os/exec"
	"sort"
	"strings"
)

// ObservedWorktree is one worktree discovered by the reconcile source. It is
// the raw filesystem/git observation — the "unregistered work" signal the
// Portfolio Reconciler turns into inbox items (VC-05).
type ObservedWorktree struct {
	Path     string `json:"path"`
	HEAD     string `json:"head"`
	Branch   string `json:"branch,omitempty"` // empty when detached
	Detached bool   `json:"detached"`
	Prunable bool   `json:"prunable"`
}

// ScanGitWorktrees runs `git worktree list --porcelain` in the canonical repo
// and returns the parsed worktrees. Read-only; never mutates the repository.
func ScanGitWorktrees(repoPath string) ([]ObservedWorktree, error) {
	cmd := exec.Command("git", "-C", repoPath, "worktree", "list", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return ParseWorktreePorcelain(string(out)), nil
}

// ParseWorktreePorcelain parses `git worktree list --porcelain` output into a
// deterministic, sorted list. Detached worktrees and prunable markers are
// preserved; unknown lines are ignored (forward compatible).
func ParseWorktreePorcelain(out string) []ObservedWorktree {
	var worktrees []ObservedWorktree
	var cur *ObservedWorktree
	flush := func() {
		if cur != nil {
			worktrees = append(worktrees, *cur)
			cur = nil
		}
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "worktree "):
			flush()
			cur = &ObservedWorktree{Path: strings.TrimSpace(strings.TrimPrefix(line, "worktree "))}
		case strings.HasPrefix(line, "HEAD "):
			if cur != nil {
				cur.HEAD = strings.TrimSpace(strings.TrimPrefix(line, "HEAD "))
			}
		case strings.HasPrefix(line, "branch "):
			if cur != nil {
				cur.Branch = strings.TrimSpace(strings.TrimPrefix(line, "branch refs/heads/"))
				cur.Branch = strings.TrimPrefix(cur.Branch, "refs/heads/")
			}
		case strings.HasPrefix(line, "detached"):
			if cur != nil {
				cur.Detached = true
			}
		case strings.HasPrefix(line, "prunable"):
			if cur != nil {
				cur.Prunable = true
			}
		}
	}
	flush()
	sort.Slice(worktrees, func(i, j int) bool { return worktrees[i].Path < worktrees[j].Path })
	return worktrees
}

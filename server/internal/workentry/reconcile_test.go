package workentry

import (
	"context"
	"testing"
)

func TestParseWorktreePorcelain(t *testing.T) {
	out := `worktree /repo/main
HEAD aaa111
branch refs/heads/main

worktree /repo/wt1
HEAD bbb222
branch refs/heads/work/feature-1

worktree /repo/wt2
HEAD ccc333
detached

worktree /repo/wt3
HEAD ddd444
detached
prunable gitdir file points to non-existent location
`
	got := ParseWorktreePorcelain(out)
	if len(got) != 4 {
		t.Fatalf("want 4 worktrees, got %d: %+v", len(got), got)
	}
	if got[0].Branch != "main" || got[0].Detached {
		t.Fatalf("main worktree parse wrong: %+v", got[0])
	}
	if got[1].Branch != "work/feature-1" {
		t.Fatalf("feature branch parse wrong: %+v", got[1])
	}
	if !got[2].Detached || got[2].Branch != "" {
		t.Fatalf("detached parse wrong: %+v", got[2])
	}
	if !got[3].Prunable {
		t.Fatalf("prunable parse wrong: %+v", got[3])
	}
}

func TestReconcileUnregistered(t *testing.T) {
	observed := []ObservedWorktree{
		{Path: "/repo/main", Branch: "main", HEAD: "aaa"},
		{Path: "/repo/wt1", Branch: "work/feature-1", HEAD: "bbb"},
		{Path: "/repo/wt2", Detached: true, HEAD: "ccc"},
		{Path: "/repo/wt3", Branch: "work/known", HEAD: "ddd", Prunable: true},
	}
	registered := map[string]bool{"feature-1": true}
	got := ReconcileUnregistered(observed, registered)
	if len(got) != 3 { // main, wt2, wt3 are unregistered; wt1 matches "feature-1"
		t.Fatalf("want 3 unregistered, got %d: %+v", len(got), got)
	}
}

func TestReconcileWorktreesReadOnly(t *testing.T) {
	// Scanning a real repo (the canonical multica repo) must succeed and return
	// a non-trivial list without mutating anything.
	svc := NewService(NewMemoryStore())
	got, err := svc.ReconcileWorktrees(context.Background(), "ws-1",
		"/Volumes/HiveData/hivecosm/HQ-50-代码仓库/01-源码下载/multica")
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(got) == 0 {
		t.Fatalf("expected some observed worktrees")
	}
	for _, w := range got {
		if w.Path == "" {
			t.Fatalf("empty path in %+v", w)
		}
	}
	t.Logf("reconcile observed %d unregistered worktrees", len(got))
}

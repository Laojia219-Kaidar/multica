package workentry

import (
	"context"
	"testing"
)

func TestInventoryEmpty(t *testing.T) {
	store := NewMemoryStore()
	svc := NewService(store)
	res, err := svc.Inventory(context.Background(), "ws-1")
	if err != nil {
		t.Fatalf("inventory: %v", err)
	}
	if len(res.Duplicates) != 0 || len(res.Orphans) != 0 {
		t.Fatalf("empty store must produce empty diagnostic: %+v", res)
	}
}

func TestInventoryNonEmpty(t *testing.T) {
	store := NewMemoryStore()
	svc := NewService(store)
	ctx := context.Background()
	const ws = "ws-1"

	store.SeedProject(ProjectRef{ID: "p-own", WorkspaceID: ws, Title: "Owned Project", Status: "in_progress"})
	store.SeedProject(ProjectRef{ID: "p-orphan", WorkspaceID: ws, Title: "Orphan Project", Status: "planned"})
	store.SeedIssue(IssueRef{ID: "i-owned", WorkspaceID: ws, Title: "owned work", Status: "todo", ProjectID: "p-own"})
	store.SeedIssue(IssueRef{ID: "i-orphan", WorkspaceID: ws, Title: "orphan work", Status: "todo", ProjectID: "p-orphan"})
	store.SeedWorkOrderLink(ExternalWorkOrderLink{WorkspaceID: ws, WorkOrderRef: "goal-1", IssueID: "i-owned"})

	// duplicate candidates: same repo + similar titles
	store.SeedIssue(IssueRef{ID: "i-dup-a", WorkspaceID: ws, Title: "implement user login", Status: "todo", ProjectID: "p-own"})
	store.SeedIssue(IssueRef{ID: "i-dup-b", WorkspaceID: ws, Title: "implement user login flow", Status: "todo", ProjectID: "p-own"})
	store.SeedRepo(RepoRef{WorkspaceID: ws, OwnerKind: "issue", OwnerID: "i-owned", Repo: "/repo/x"})
	store.SeedRepo(RepoRef{WorkspaceID: ws, OwnerKind: "issue", OwnerID: "i-dup-a", Repo: "/repo/x"})
	store.SeedRepo(RepoRef{WorkspaceID: ws, OwnerKind: "issue", OwnerID: "i-dup-b", Repo: "/repo/x"})

	res, err := svc.Inventory(ctx, ws)
	if err != nil {
		t.Fatalf("inventory: %v", err)
	}
	if len(res.Duplicates) != 1 {
		t.Fatalf("expected 1 duplicate, got %d (%+v)", len(res.Duplicates), res.Duplicates)
	}
	dup := res.Duplicates[0]
	if dup.Repo != "/repo/x" {
		t.Fatalf("duplicate repo = %q, want /repo/x", dup.Repo)
	}
	if !((dup.RefA == "i-dup-a" && dup.RefB == "i-dup-b") || (dup.RefA == "i-dup-b" && dup.RefB == "i-dup-a")) {
		t.Fatalf("unexpected duplicate pair: %+v", dup)
	}
	if dup.Similarity < inventorySimilarityThreshold {
		t.Fatalf("similarity %f below threshold", dup.Similarity)
	}

	orphanProjects := 0
	orphanIssues := 0
	for _, o := range res.Orphans {
		switch {
		case o.Kind == "project" && o.RefID == "p-orphan":
			orphanProjects++
		case o.Kind == "issue" && o.RefID == "i-orphan":
			orphanIssues++
		}
	}
	if orphanProjects != 1 {
		t.Fatalf("expected p-orphan orphan project once, got %d (%+v)", orphanProjects, res.Orphans)
	}
	if orphanIssues != 1 {
		t.Fatalf("expected i-orphan orphan issue once, got %d (%+v)", orphanIssues, res.Orphans)
	}
}

func TestInventoryUnavailableWithoutSource(t *testing.T) {
	svc := NewService(nil)
	if _, err := svc.Inventory(context.Background(), "ws-1"); err != ErrUnavailable {
		t.Fatalf("expected ErrUnavailable for nil store, got %v", err)
	}
}

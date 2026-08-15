package workentry

import (
	"context"
	"testing"
)

func TestStewardDiagnostics(t *testing.T) {
	store := NewMemoryStore()
	svc := NewService(store)
	ctx := context.Background()
	const ws = "ws-1"

	// no-owner + no-next-action + orphan project
	store.SeedProject(ProjectRef{ID: "p-bad", WorkspaceID: ws, Title: "Unmanaged Project", Status: "planned"})
	// healthy project: has lead + open issue + goal link
	store.SeedProject(ProjectRef{ID: "p-good", WorkspaceID: ws, Title: "Managed Project", Status: "in_progress"})
	store.SeedLead(ProjectLead{ProjectID: "p-good", LeadType: "agent", LeadID: "agent-1"})
	store.SeedIssue(IssueRef{ID: "i-good", WorkspaceID: ws, Title: "open work", Status: "todo", ProjectID: "p-good"})
	store.SeedWorkOrderLink(ExternalWorkOrderLink{WorkspaceID: ws, WorkOrderRef: "goal-1", IssueID: "i-good"})

	res, err := svc.StewardDiagnostics(ctx, ws)
	if err != nil {
		t.Fatalf("steward: %v", err)
	}

	kinds := map[StewardDiagnosticKind]int{}
	byRef := map[string]StewardDiagnostic{}
	for _, d := range res {
		kinds[d.Kind]++
		byRef[d.RefID] = d
	}
	// p-bad should surface no_owner + no_next_action + orphan.
	if d, ok := byRef["p-bad"]; !ok {
		t.Fatalf("p-bad missing from diagnostics: %+v", res)
	} else {
		_ = d
	}
	if kinds[StewardNoOwner] == 0 {
		t.Fatalf("expected no_owner diagnostic, got %+v", res)
	}
	if kinds[StewardNoNextAction] == 0 {
		t.Fatalf("expected no_next_action diagnostic, got %+v", res)
	}
	if kinds[StewardOrphan] == 0 {
		t.Fatalf("expected orphan diagnostic, got %+v", res)
	}
	// p-good should not surface no_owner/no_next_action/orphan.
	for _, d := range res {
		if d.RefID == "p-good" && (d.Kind == StewardNoOwner || d.Kind == StewardNoNextAction || d.Kind == StewardOrphan) {
			t.Fatalf("healthy project flagged %s: %+v", d.Kind, d)
		}
	}
}

func TestStewardUnavailableWithoutSource(t *testing.T) {
	svc := NewService(&storeWithoutSteward{})
	if _, err := svc.StewardDiagnostics(context.Background(), "ws-1"); err == nil {
		t.Fatal("expected ErrUnavailable")
	}
}

type storeWithoutSteward struct{ MemoryStore }

func (s *storeWithoutSteward) StewardSnapshot(context.Context, string) (*StewardSnapshot, error) { return nil, ErrUnavailable }

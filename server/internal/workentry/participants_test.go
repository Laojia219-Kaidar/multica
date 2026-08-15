package workentry

import (
	"context"
	"errors"
	"testing"
)

func TestProjectParticipantsEndToEnd(t *testing.T) {
	store := NewMemoryStore()
	svc := NewService(store)
	ctx := context.Background()

	intent := fixtureIntent()
	intent.GoalRef = "VC04-PARTICIPANTS-E2E"

	a := fixtureActor(ActorExternalAgent)
	a.ActorID = "EXT-A"
	a.CarrierID = "claude"
	a.HostID = "mac-mini.local"
	a.SessionID = "sess-A"
	a.ModelRef = "sonnet-4.6"
	a.WorkspaceID = "ws-1"

	b := fixtureActor(ActorExternalAgent)
	b.ActorID = "EXT-B"
	b.CarrierID = "codex"
	b.HostID = "dgx-spark-b398"
	b.SessionID = "sess-B"
	b.ModelRef = "gpt-5.2"
	b.WorkspaceID = "ws-1"

	ra, err := svc.Register(ctx, RegisterRequest{ResolveRequest: ResolveRequest{Actor: a, Intent: intent}, ConfirmCreate: true})
	if err != nil {
		t.Fatalf("register A: %v", err)
	}
	rb, err := svc.Register(ctx, RegisterRequest{ResolveRequest: ResolveRequest{Actor: b, Intent: intent}, ConfirmCreate: true})
	if err != nil {
		t.Fatalf("register B: %v", err)
	}
	if ra.ProjectID == "" || ra.ProjectID != rb.ProjectID {
		t.Fatalf("two actors with same goal must share one project: A=%q B=%q", ra.ProjectID, rb.ProjectID)
	}

	res, err := svc.ProjectParticipants(ctx, "ws-1", ra.ProjectID)
	if err != nil {
		t.Fatalf("project participants: %v", err)
	}
	if res.Source != "work_entry_participants" {
		t.Fatalf("unexpected source %q", res.Source)
	}
	if len(res.Participants) != 2 {
		t.Fatalf("expected 2 participants, got %d: %+v", len(res.Participants), res.Participants)
	}
	byID := map[string]ProjectParticipant{}
	for _, p := range res.Participants {
		byID[p.ActorID] = p
		if p.ActorType != ActorExternalAgent {
			t.Fatalf("expected external_agent, got %q", p.ActorType)
		}
		if p.EmployeeID != "" {
			t.Fatalf("external_agent must not carry employee_id (VC-02), got %q", p.EmployeeID)
		}
	}
	if byID["EXT-A"].CarrierID != "claude" || byID["EXT-A"].HostID != "mac-mini.local" || byID["EXT-A"].ModelRef != "sonnet-4.6" {
		t.Fatalf("EXT-A dimensions not projected: %+v", byID["EXT-A"])
	}
	if byID["EXT-B"].CarrierID != "codex" || byID["EXT-B"].HostID != "dgx-spark-b398" || byID["EXT-B"].ModelRef != "gpt-5.2" {
		t.Fatalf("EXT-B dimensions not projected: %+v", byID["EXT-B"])
	}
}

func TestProjectParticipantsTenantAndDedupe(t *testing.T) {
	store := NewMemoryStore()
	svc := NewService(store)
	ctx := context.Background()

	mk := func(ws, proj, actorID string) ReceiptRecord {
		return ReceiptRecord{
			WorkspaceID: ws, ProjectID: proj, DedupeKey: "k-" + ws + "-" + actorID,
			WorkRef: "hivecrew://" + ws + "/work/" + proj + "/issue-" + actorID,
			Actor: WorkActorIdentityV1{
				ActorType: ActorExternalAgent, ActorID: actorID, CarrierID: "claude",
				SessionID: "s-" + actorID, WorkspaceID: ws, ObservedAt: "2026-08-15T22:00:00Z",
			},
		}
	}
	// same project, two actors + one duplicate actor (different dedupe key)
	if err := store.PutReceipt(ctx, mk("ws-1", "proj-1", "A")); err != nil {
		t.Fatal(err)
	}
	if err := store.PutReceipt(ctx, mk("ws-1", "proj-1", "B")); err != nil {
		t.Fatal(err)
	}
	if err := store.PutReceipt(ctx, mk("ws-1", "proj-1", "A")); err != nil {
		// same dedupe key with same digest: PutReceipt returns nil (idempotent)
		t.Fatal(err)
	}
	// different project + different workspace must not leak
	if err := store.PutReceipt(ctx, mk("ws-1", "proj-2", "C")); err != nil {
		t.Fatal(err)
	}
	if err := store.PutReceipt(ctx, mk("ws-2", "proj-1", "D")); err != nil {
		t.Fatal(err)
	}

	res, err := svc.ProjectParticipants(ctx, "ws-1", "proj-1")
	if err != nil {
		t.Fatalf("project participants: %v", err)
	}
	if len(res.Participants) != 2 {
		t.Fatalf("expected 2 participants (A,B) after tenant+dedupe, got %d: %+v", len(res.Participants), res.Participants)
	}
	ids := map[string]bool{}
	for _, p := range res.Participants {
		ids[p.ActorID] = true
	}
	if !ids["A"] || !ids["B"] || ids["C"] || ids["D"] {
		t.Fatalf("tenant/dedupe isolation failed: %+v", res.Participants)
	}

	if _, err := svc.ProjectParticipants(ctx, "ws-1", ""); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("empty project_id must be invalid request, got %v", err)
	}
	if _, err := svc.ProjectParticipants(ctx, "", "proj-1"); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("empty workspace must be invalid request, got %v", err)
	}
}

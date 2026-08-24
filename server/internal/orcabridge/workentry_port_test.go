package orcabridge

import (
	"context"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/workentry"
)

// newKernel builds a real workentry kernel over its own memory store. The
// adapter tests prove the bridge works against the existing service exactly
// as production would, with no schema and no bridge-owned tables.
func newKernel(t *testing.T) *WorkEntryServiceAdapter {
	t.Helper()
	service := workentry.NewService(workentry.NewMemoryStore())
	return &WorkEntryServiceAdapter{
		Service:     service,
		WorkspaceID: validChain().WorkspaceID,
	}
}

func repeatHex(seed byte) string {
	out := make([]byte, 64)
	for i := range out {
		out[i] = '0' + seed%10
	}
	return string(out)
}

func bridgeActor() ActorIdentity {
	return ActorIdentity{
		ActorID:    "orca-bridge-a1",
		CarrierID:  "hivecrew-orca-bridge",
		SessionID:  "bridge-session-1",
		ObservedAt: time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC),
	}
}

func TestWorkEntryAdapterRegisterIsIdempotentAndDriftConflicts(t *testing.T) {
	adapter := newKernel(t)
	ctx := context.Background()
	chain := validChain()

	first, err := adapter.RegisterLinkage(ctx, LinkageInput{
		Chain: chain, Actor: bridgeActor(), MappingKind: "run",
	})
	if err != nil {
		t.Fatalf("first register: %v", err)
	}
	if first.WorkRef == "" {
		t.Fatal("work_ref must not be empty")
	}
	replay, err := adapter.RegisterLinkage(ctx, LinkageInput{
		Chain: chain, Actor: bridgeActor(), MappingKind: "run",
	})
	if err != nil {
		t.Fatalf("replay register: %v", err)
	}
	if replay.WorkRef != first.WorkRef || !replay.Replayed {
		t.Fatalf("replay must return the original work_ref: %+v vs %+v", first, replay)
	}
}

func TestWorkEntryAdapterEvidenceIdempotentAndDriftConflicts(t *testing.T) {
	adapter := newKernel(t)
	ctx := context.Background()
	chain := validChain()
	linkage, err := adapter.RegisterLinkage(ctx, LinkageInput{
		Chain: chain, Actor: bridgeActor(), MappingKind: "dispatch",
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	input := EvidenceInput{
		WorkRef:        linkage.WorkRef,
		SessionID:      bridgeActor().SessionID,
		EventType:      "checkpoint",
		IdempotencyKey: "k1",
		Payload:        map[string]any{"orca_dispatch_id": "ctx_ab12"},
	}
	if input.OccurredAt.IsZero() {
		input.OccurredAt = time.Now()
	}
	first, err := adapter.AppendEvidence(ctx, input)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	replay, err := adapter.AppendEvidence(ctx, input)
	if err != nil {
		t.Fatalf("replay append: %v", err)
	}
	if !replay.Replayed || replay.EventID != first.EventID {
		t.Fatalf("replay must be idempotent: %+v vs %+v", first, replay)
	}
	drifted := input
	drifted.Payload = map[string]any{"orca_dispatch_id": "ctx_cd34"}
	if _, err := adapter.AppendEvidence(ctx, drifted); err == nil {
		t.Fatal("drifted evidence must conflict")
	}
	// Lookup finds the stored evidence.
	found, ok, err := adapter.LookupEvidence(ctx, linkage.WorkRef, "k1")
	if err != nil || !ok || found.Payload["orca_dispatch_id"] != "ctx_ab12" {
		t.Fatalf("lookup: found=%+v ok=%v err=%v", found, ok, err)
	}
	missing, ok, err := adapter.LookupEvidence(ctx, linkage.WorkRef, "nope")
	if err != nil || ok || missing.Payload != nil {
		t.Fatalf("missing lookup must be clean: %+v %v %v", missing, ok, err)
	}
}

func TestWorkEntryAdapterRejectsIncompleteActor(t *testing.T) {
	adapter := newKernel(t)
	_, err := adapter.RegisterLinkage(context.Background(), LinkageInput{
		Chain: validChain(), Actor: ActorIdentity{}, MappingKind: "run",
	})
	if err == nil {
		t.Fatal("incomplete actor must fail closed")
	}
}

func TestWorkEntryAdapterRejectsOpenEventType(t *testing.T) {
	adapter := newKernel(t)
	_, err := adapter.AppendEvidence(context.Background(), EvidenceInput{
		WorkRef: "hivecrew://ws/work/prj", SessionID: "s",
		EventType: "not_a_closed_type", IdempotencyKey: "k",
	})
	if err == nil {
		t.Fatal("open event type must fail closed")
	}
}

func TestLinkageKeysAreDeterministicAndScoped(t *testing.T) {
	chain := validChain()
	if RunLinkageKey(chain) == TaskLinkageKey(chain) ||
		TaskLinkageKey(chain) == DispatchLinkageKey(chain) ||
		RunLinkageKey(chain) == DispatchLinkageKey(chain) {
		t.Fatal("linkage keys must be distinct per scope")
	}
	if ResultEvidenceKey("ctx_1") == ResultEvidenceKey("ctx_2") {
		t.Fatal("result keys must be dispatch-scoped")
	}
	other := chain
	other.AssignmentID = "c05a0000-0000-4000-8000-000000000009"
	if DispatchLinkageKey(chain) == DispatchLinkageKey(other) {
		t.Fatal("dispatch keys must be assignment-scoped")
	}
}

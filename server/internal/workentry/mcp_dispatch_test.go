package workentry

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// TestWorkMCPToolsSchemas verifies the manifest carries real argument schemas
// (not empty property bags) for every exported verb.
func TestWorkMCPToolsSchemas(t *testing.T) {
	tools := WorkMCPTools()
	want := map[string]bool{
		"work.resolve": true, "work.register": true, "work.start": true,
		"work.status": true, "work.heartbeat": true, "work.event": true,
		"work.handoff": true, "work.finish": true, "work.sync": true,
		"work.doctor": true,
	}
	if len(tools) != len(want) {
		t.Fatalf("got %d tools, want %d", len(tools), len(want))
	}
	for _, tool := range tools {
		if !want[tool.Name] {
			t.Fatalf("unexpected tool %q", tool.Name)
		}
		if tool.InputSchema.Type != "object" {
			t.Fatalf("tool %q inputSchema.type = %q", tool.Name, tool.InputSchema.Type)
		}
		if len(tool.InputSchema.Properties) == 0 {
			t.Fatalf("tool %q has no argument properties", tool.Name)
		}
		if tool.Name == "work.resolve" || tool.Name == "work.register" {
			if _, ok := tool.InputSchema.Properties["actor_identity"]; !ok {
				t.Fatalf("tool %q missing actor_identity", tool.Name)
			}
			if _, ok := tool.InputSchema.Properties["intent"]; !ok {
				t.Fatalf("tool %q missing intent", tool.Name)
			}
		}
	}
}

// TestCallMCPTool exercises the dispatcher end to end against the in-memory
// store: resolve classification, register create, start idempotency, event,
// finish-to-review, doctor, and unknown-tool failure.
func TestCallMCPTool(t *testing.T) {
	svc := NewService(NewMemoryStore())
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339Nano)

	actor := map[string]any{
		"actor_type": "external_agent", "actor_id": "EXT-canary-1",
		"carrier_id": "claude-code", "session_id": "s1", "workspace_id": "ws-canary",
		"observed_at": now,
	}
	intent := map[string]any{
		"owner_intent": "canary", "goal_ref": "GOAL-CANARY-1", "objective": "demo",
		"expected_human_result": "pass", "repo": "/tmp/canary", "baseline_revision": "abc123",
		"branch_or_worktree": "main", "read_scope": []any{"/tmp/canary"},
		"write_scope": []any{"/tmp/canary"}, "expected_outcomes": []any{"artifact"},
		"candidate_formal_boundary": "candidate",
	}

	// resolve -> classification_required (fresh store, no match)
	res, err := svc.CallMCPTool(ctx, "work.resolve", map[string]any{"actor_identity": actor, "intent": intent})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res.(ResolveResult).ResolutionDecision != DecisionClassificationRequired {
		t.Fatalf("resolve decision = %q", res.(ResolveResult).ResolutionDecision)
	}

	// register with confirm_create -> created work_ref (external_agent without employee_id)
	res, err = svc.CallMCPTool(ctx, "work.register", map[string]any{
		"actor_identity": actor, "intent": intent, "confirm_create": true,
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	receipt := res.(WorkRegistrationReceiptV1)
	if !receipt.Created || receipt.WorkRef == "" {
		t.Fatalf("register should create and return work_ref: %+v", receipt)
	}
	workRef := receipt.WorkRef

	// register again with the same key+digest -> idempotent replay of the SAME work_ref.
	res, err = svc.CallMCPTool(ctx, "work.register", map[string]any{
		"actor_identity": actor, "intent": intent, "confirm_create": true,
	})
	if err != nil {
		t.Fatalf("register replay: %v", err)
	}
	replayed := res.(WorkRegistrationReceiptV1)
	if !replayed.Replay.Replayed || replayed.WorkRef != workRef {
		t.Fatalf("register replay should return the same work_ref: %+v", replayed)
	}

	// start -> append a started event for the work_ref.
	res, err = svc.CallMCPTool(ctx, "work.start", map[string]any{
		"work_ref": workRef, "session_id": "s1", "run_id": "r1", "actor_id": "EXT-canary-1",
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if res.(EventResult).EventID == "" {
		t.Fatalf("start should append a started event")
	}

	// event -> progress
	res, err = svc.CallMCPTool(ctx, "work.event", map[string]any{
		"work_ref": workRef, "session_id": "s1", "event_type": "progress",
		"event_payload": map[string]any{"step": "verify"},
		"idempotency_key": "evt-1", "occurred_at": now, "observed_at": now,
	})
	if err != nil {
		t.Fatalf("event: %v", err)
	}
	if res.(EventResult).EventID == "" {
		t.Fatalf("event id empty")
	}

	// heartbeat -> accepted
	res, err = svc.CallMCPTool(ctx, "work.heartbeat", map[string]any{
		"workspace_id": "ws-canary", "actor_id": "EXT-canary-1", "session_id": "s1",
		"window_index": float64(1), "pane_index": float64(2), "host": "mac-mini",
	})
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if !res.(HeartbeatResult).Accepted {
		t.Fatalf("heartbeat should be accepted")
	}

	// finish -> review routed, never auto-pass
	res, err = svc.CallMCPTool(ctx, "work.finish", map[string]any{
		"work_ref": workRef,
		"completion_candidate": map[string]any{"artifact_ref": "artifact://c/1", "digest": "sha256:abcd", "revision": "r1"},
		"review":               map[string]any{"reviewer_actor_id": "REV-1", "decision": ""},
		"project_lifecycle_consequence": "continue",
	})
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	cr := res.(CompletionResult)
	if !cr.ReviewRouted || cr.AutoPassed {
		t.Fatalf("finish should route to review and never auto-pass: %+v", cr)
	}

	// sync -> empty spool
	res, err = svc.CallMCPTool(ctx, "work.sync", map[string]any{"entries": []any{}})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if res.(SyncResult).Synced != 0 {
		t.Fatalf("empty sync should be 0 synced")
	}

	// doctor -> reconcile discovers + persists real unregistered worktrees.
	res, err = svc.CallMCPTool(ctx, "work.doctor", map[string]any{"workspace_id": "ws-canary"})
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if len(res.([]InboxItem)) == 0 {
		t.Fatalf("doctor should discover unregistered worktrees")
	}

	// status -> found
	res, err = svc.CallMCPTool(ctx, "work.status", map[string]any{"work_ref": workRef, "workspace_id": "ws-canary"})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !res.(StatusResult).Found {
		t.Fatalf("status should be found")
	}

	// unknown tool
	if _, err := svc.CallMCPTool(ctx, "work.nope", map[string]any{}); err == nil {
		t.Fatalf("unknown tool should error")
	}

	// manifest serializes to valid JSON
	if _, err := json.Marshal(WorkMCPTools()); err != nil {
		t.Fatalf("manifest marshal: %v", err)
	}
}

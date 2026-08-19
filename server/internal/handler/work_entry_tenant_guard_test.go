package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/workentry"
)

func TestWorkEntryEventAndReplayPreserveTypedRunID(t *testing.T) {
	h, _ := newWorkEntryGuardHandler()
	event := guardEvent(guardTenantWS)
	event["run_id"] = "run-guard-1"
	body, _ := json.Marshal(event)

	w := httptest.NewRecorder()
	h.WorkEntryEvent(w, newWorkEntryGuardRequest(http.MethodPost, "/api/work/event", string(body)))
	if w.Code != http.StatusOK {
		t.Fatalf("event write: %d %s", w.Code, w.Body.String())
	}

	workRef := event["work_ref"].(string)
	path := "/api/work/replay?kind=event&key=" + url.QueryEscape("evt-tenant-guard") + "&work_ref=" + url.QueryEscape(workRef)
	w = httptest.NewRecorder()
	h.WorkEntryReplay(w, newWorkEntryGuardRequest(http.MethodGet, path, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("event replay: %d %s", w.Code, w.Body.String())
	}
	var replay workentry.ReplayResult
	if err := json.Unmarshal(w.Body.Bytes(), &replay); err != nil {
		t.Fatalf("decode replay: %v", err)
	}
	if replay.Event == nil || replay.Event.RunID != "run-guard-1" {
		t.Fatalf("replay event = %+v", replay.Event)
	}
}

func TestWorkEntryEventRejectsNestedRunID(t *testing.T) {
	h, _ := newWorkEntryGuardHandler()
	event := guardEvent(guardTenantWS)
	event["run_id"] = "run-guard-1"
	event["event_payload"] = map[string]any{"run_id": "forged"}
	body, _ := json.Marshal(event)
	w := httptest.NewRecorder()
	h.WorkEntryEvent(w, newWorkEntryGuardRequest(http.MethodPost, "/api/work/event", string(body)))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "forbidden_proof_field") {
		t.Fatalf("nested run_id: %d %s", w.Code, w.Body.String())
	}
}

const (
	guardTenantWS  = "ws-tenant-a"
	guardForeignWS = "ws-tenant-b"
)

func guardNow() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

func newWorkEntryGuardHandler() (*Handler, *workentry.MemoryStore) {
	store := workentry.NewMemoryStore()
	return &Handler{WorkEntry: workentry.NewService(store)}, store
}

func newWorkEntryGuardRequest(method, path, body string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Workspace-ID", guardTenantWS)
	return req
}

func guardActor(ws string) map[string]any {
	return map[string]any{
		"actor_type":   "external_agent",
		"actor_id":     "EXT-tenant-guard",
		"carrier_id":   "claude-code",
		"session_id":   "sess-guard",
		"workspace_id": ws,
		"observed_at":  guardNow(),
	}
}

func guardIntent() map[string]any {
	return map[string]any{
		"owner_intent":              "tenant guard test",
		"goal_ref":                  "GOAL-TENANT-GUARD-1",
		"objective":                 "verify tenant isolation",
		"expected_human_result":     "pass",
		"repo":                      "/tmp/tenant-guard",
		"baseline_revision":         "abc123",
		"branch_or_worktree":        "main",
		"read_scope":                []any{"/tmp/tenant-guard"},
		"write_scope":               []any{"/tmp/tenant-guard"},
		"expected_outcomes":         []any{"artifact"},
		"candidate_formal_boundary": "candidate",
	}
}

func guardEvent(ws string) map[string]any {
	return map[string]any{
		"work_ref":        workentry.FormatWorkRef(ws, "proj", "issue", ""),
		"session_id":      "sess-guard",
		"event_type":      "progress",
		"event_payload":   map[string]any{"step": "verify"},
		"idempotency_key": "evt-tenant-guard",
		"occurred_at":     guardNow(),
		"observed_at":     guardNow(),
	}
}

func guardEventWithRunID(ws string) map[string]any {
	event := guardEvent(ws)
	event["run_id"] = "run-guard-1"
	return event
}

func assertGuardForbidden(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if code, _ := body["reason_code"].(string); code != "forbidden" {
		t.Fatalf("expected reason_code=forbidden, got %q", code)
	}
}

// ---------------------------------------------------------------------------
// POST /api/work/sync
// ---------------------------------------------------------------------------

func TestWorkEntrySyncRejectsCrossTenantRegister(t *testing.T) {
	h, store := newWorkEntryGuardHandler()

	body := map[string]any{
		"entries": []any{
			map[string]any{
				"verb":            "register",
				"idempotency_key": "reg-cross-tenant",
				"payload_digest":  "sha256:0000000000000000000000000000000000000000000000000000000000000000",
				"canonical_payload": map[string]any{
					"actor_identity": guardActor(guardForeignWS),
					"intent":         guardIntent(),
					"confirm_create": true,
				},
			},
		},
	}
	b, _ := json.Marshal(body)

	w := httptest.NewRecorder()
	h.WorkEntrySync(w, newWorkEntryGuardRequest(http.MethodPost, "/api/work/sync", string(b)))
	assertGuardForbidden(t, w)

	// Nothing may land in the foreign tenant.
	rcpt, err := store.GetReceipt(context.Background(), guardForeignWS,
		workentry.DedupeKey(guardForeignWS, "EXT-tenant-guard", "GOAL-TENANT-GUARD-1", "/tmp/tenant-guard", "abc123", "main"))
	if err != nil {
		t.Fatalf("get receipt: %v", err)
	}
	if rcpt != nil {
		t.Fatalf("cross-tenant register wrote a receipt: %+v", rcpt)
	}
}

func TestWorkEntrySyncRejectsCrossTenantEvent(t *testing.T) {
	h, store := newWorkEntryGuardHandler()

	body := map[string]any{
		"entries": []any{
			map[string]any{
				"verb":            "event",
				"idempotency_key": "evt-cross-tenant",
				"payload_digest":  "sha256:0000000000000000000000000000000000000000000000000000000000000000",
				"canonical_payload": map[string]any{
					"event": guardEvent(guardForeignWS),
				},
			},
		},
	}
	b, _ := json.Marshal(body)

	w := httptest.NewRecorder()
	h.WorkEntrySync(w, newWorkEntryGuardRequest(http.MethodPost, "/api/work/sync", string(b)))
	assertGuardForbidden(t, w)

	ev, err := store.GetEvent(context.Background(), guardForeignWS,
		workentry.FormatWorkRef(guardForeignWS, "proj", "issue", ""), "evt-tenant-guard")
	if err != nil {
		t.Fatalf("get event: %v", err)
	}
	if ev != nil {
		t.Fatalf("cross-tenant event wrote an event record: %+v", ev)
	}
}

func TestWorkEntrySyncAllowsSameTenant(t *testing.T) {
	h, _ := newWorkEntryGuardHandler()

	body := map[string]any{
		"entries": []any{
			map[string]any{
				"verb":            "register",
				"idempotency_key": "reg-same-tenant",
				"payload_digest":  "sha256:0000000000000000000000000000000000000000000000000000000000000000",
				"canonical_payload": map[string]any{
					"actor_identity": guardActor(guardTenantWS),
					"intent":         guardIntent(),
					"confirm_create": true,
				},
			},
			map[string]any{
				"verb":            "event",
				"idempotency_key": "evt-same-tenant",
				"payload_digest":  "sha256:0000000000000000000000000000000000000000000000000000000000000000",
				"canonical_payload": map[string]any{
					"event": guardEventWithRunID(guardTenantWS),
				},
			},
		},
	}
	b, _ := json.Marshal(body)

	w := httptest.NewRecorder()
	h.WorkEntrySync(w, newWorkEntryGuardRequest(http.MethodPost, "/api/work/sync", string(b)))
	if w.Code != http.StatusOK {
		t.Fatalf("same-tenant sync: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var res workentry.SyncResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode sync result: %v", err)
	}
	if res.Synced != 2 {
		t.Fatalf("same-tenant sync: expected 2 synced entries, got %+v", res)
	}
}

func TestWorkEntryMCPCallAllowsSameTenantEventRunID(t *testing.T) {
	h, _ := newWorkEntryGuardHandler()
	body := map[string]any{"name": "work.event", "arguments": guardEventWithRunID(guardTenantWS)}
	b, _ := json.Marshal(body)

	w := httptest.NewRecorder()
	h.WorkEntryMCPCall(w, newWorkEntryGuardRequest(http.MethodPost, "/api/work/mcp/call", string(b)))
	if w.Code != http.StatusOK {
		t.Fatalf("same-tenant MCP event: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var result workentry.EventResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode event result: %v", err)
	}
	if result.EventID == "" {
		t.Fatal("same-tenant MCP event returned no event id")
	}
}

// ---------------------------------------------------------------------------
// POST /api/work/mcp/call
// ---------------------------------------------------------------------------

func TestWorkEntryMCPCallRejectsCrossTenantWrites(t *testing.T) {
	cases := []struct {
		name      string
		tool      string
		arguments map[string]any
	}{
		{
			name: "register",
			tool: "work.register",
			arguments: map[string]any{
				"actor_identity": guardActor(guardForeignWS),
				"intent":         guardIntent(),
				"confirm_create": true,
			},
		},
		{
			name: "start",
			tool: "work.start",
			arguments: map[string]any{
				"work_ref":   workentry.FormatWorkRef(guardForeignWS, "proj", "issue", ""),
				"session_id": "sess-guard",
				"actor_id":   "EXT-tenant-guard",
			},
		},
		{
			name: "event",
			tool: "work.event",
			arguments: map[string]any{
				"event": guardEvent(guardForeignWS),
			},
		},
		{
			name: "handoff",
			tool: "work.handoff",
			arguments: map[string]any{
				"work_ref": workentry.FormatWorkRef(guardForeignWS, "proj", "issue", ""),
			},
		},
		{
			name: "finish",
			tool: "work.finish",
			arguments: map[string]any{
				"work_ref": workentry.FormatWorkRef(guardForeignWS, "proj", "issue", ""),
			},
		},
		{
			name: "heartbeat",
			tool: "work.heartbeat",
			arguments: map[string]any{
				"workspace_id": guardForeignWS,
				"actor_id":     "EXT-tenant-guard",
				"session_id":   "sess-guard",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := newWorkEntryGuardHandler()
			b, _ := json.Marshal(map[string]any{"name": tc.tool, "arguments": tc.arguments})
			w := httptest.NewRecorder()
			h.WorkEntryMCPCall(w, newWorkEntryGuardRequest(http.MethodPost, "/api/work/mcp/call", string(b)))
			assertGuardForbidden(t, w)
		})
	}
}

func TestWorkEntryMCPCallRejectsCrossTenantSync(t *testing.T) {
	h, store := newWorkEntryGuardHandler()

	body := map[string]any{
		"name": "work.sync",
		"arguments": map[string]any{
			"entries": []any{
				map[string]any{
					"verb":            "event",
					"idempotency_key": "evt-cross-tenant",
					"canonical_payload": map[string]any{
						"event": guardEvent(guardForeignWS),
					},
				},
			},
		},
	}
	b, _ := json.Marshal(body)

	w := httptest.NewRecorder()
	h.WorkEntryMCPCall(w, newWorkEntryGuardRequest(http.MethodPost, "/api/work/mcp/call", string(b)))
	assertGuardForbidden(t, w)

	ev, err := store.GetEvent(context.Background(), guardForeignWS,
		workentry.FormatWorkRef(guardForeignWS, "proj", "issue", ""), "evt-tenant-guard")
	if err != nil {
		t.Fatalf("get event: %v", err)
	}
	if ev != nil {
		t.Fatalf("cross-tenant MCP sync wrote an event record: %+v", ev)
	}
}

func TestWorkEntryMCPCallAllowsSameTenantRegister(t *testing.T) {
	h, _ := newWorkEntryGuardHandler()

	body := map[string]any{
		"name": "work.register",
		"arguments": map[string]any{
			"actor_identity": guardActor(guardTenantWS),
			"intent":         guardIntent(),
			"confirm_create": true,
		},
	}
	b, _ := json.Marshal(body)

	w := httptest.NewRecorder()
	h.WorkEntryMCPCall(w, newWorkEntryGuardRequest(http.MethodPost, "/api/work/mcp/call", string(b)))
	if w.Code != http.StatusOK {
		t.Fatalf("same-tenant MCP register: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var receipt workentry.WorkRegistrationReceiptV1
	if err := json.Unmarshal(w.Body.Bytes(), &receipt); err != nil {
		t.Fatalf("decode receipt: %v", err)
	}
	if !strings.HasPrefix(receipt.WorkRef, "hivecrew://"+guardTenantWS+"/") {
		t.Fatalf("same-tenant MCP register wrote a foreign work_ref: %q", receipt.WorkRef)
	}
}

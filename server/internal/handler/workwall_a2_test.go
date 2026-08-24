package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/workwall"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// A2 Work Wall handler tests — all DB-free. The pure helpers (strict
// parsing, cursor, filter, envelope, SSE frames, poll decision) are covered
// directly; the handler-level tests exercise only the validation paths that
// return before any database access, using a bare &Handler{} and a
// middleware-injected member context. Dedicated-DB integration coverage of
// the full assembly path remains a tracked gap (see the issue).

const (
	a2TestWorkspaceID = "11111111-1111-1111-1111-111111111111"
	a2TestUserID      = "22222222-2222-2222-2222-222222222222"
	a2TestEmployeeID  = "33333333-3333-3333-3333-333333333333"
)

func a2TestPane(employeeID string) workwall.A2PaneV1 {
	return workwall.A2PaneV1{
		SchemaVersion:  workwall.A2PaneSchemaV1,
		WorkspaceID:    a2TestWorkspaceID,
		WorkRef:        "hive://hivecosm/work/" + employeeID,
		EmployeeID:     employeeID,
		EmployeeName:   "Employee-" + employeeID,
		ExecutionState: workwall.A2ExecutionActive,
		SurfaceKind:    workwall.A2SurfaceEventConsole,
		SourceRefs:     []string{"work_event://44444444-4444-4444-4444-444444444444"},
	}
}

func newA2TestRequest(t *testing.T, target, workspaceID string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if workspaceID != "" {
		req.Header.Set("X-Workspace-ID", workspaceID)
	}
	req.Header.Set("X-User-ID", a2TestUserID)
	// Inject the member the workspace middleware would have set, so the
	// membership gate passes without a database.
	return req.WithContext(middleware.SetMemberContext(req.Context(), workspaceID, db.Member{Role: "owner"}))
}

func assertA2JSONError(t *testing.T, rec *httptest.ResponseRecorder, wantStatus int, wantMsg string) {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, wantStatus, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not a JSON object: %v (body %s)", err, rec.Body.String())
	}
	if body["error"] != wantMsg {
		t.Fatalf("error = %q, want %q", body["error"], wantMsg)
	}
}

// TestWorkWallA2ParseEventLimit pins the strict decimal contract of
// event_limit: absent -> default window, otherwise pure ASCII digits in
// [1, 1000]. Everything else — signs, whitespace, exponent/hex/float forms,
// non-ASCII digit lookalikes, 0, over-cap, overflow — is rejected.
func TestWorkWallA2ParseEventLimit(t *testing.T) {
	cases := []struct {
		raw  string
		want int32
		ok   bool
	}{
		{raw: "", want: a2DefaultEventLimit, ok: true},
		{raw: "1", want: 1, ok: true},
		{raw: "200", want: 200, ok: true},
		{raw: "1000", want: 1000, ok: true},
		{raw: "007", want: 7, ok: true},
		{raw: "0", ok: false},
		{raw: "1001", ok: false},
		{raw: "-1", ok: false},
		{raw: "+1", ok: false},
		{raw: " 1", ok: false},
		{raw: "1 ", ok: false},
		{raw: "1.5", ok: false},
		{raw: "1e3", ok: false},
		{raw: "0x10", ok: false},
		{raw: "1_000", ok: false},
		{raw: "12,000", ok: false},
		// Arabic-Indic THREE and fullwidth ONE: decimal lookalikes that
		// must not pass an ASCII-decimal-only contract.
		{raw: "\u0663", ok: false},
		{raw: "\uff11", ok: false},
		{raw: "99999999999999999999", ok: false},
	}
	for _, tc := range cases {
		got, ok := parseA2EventLimit(tc.raw)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("parseA2EventLimit(%q) = (%d, %v), want (%d, %v)", tc.raw, got, ok, tc.want, tc.ok)
		}
	}
}

// TestWorkWallA2CursorValidation pins the strict Last-Event-ID form:
// exactly 64 lowercase hex characters.
func TestWorkWallA2CursorValidation(t *testing.T) {
	cases := []struct {
		id string
		ok bool
	}{
		{id: strings.Repeat("a", 64), ok: true},
		{id: strings.Repeat("0", 64), ok: true},
		{id: "deadbeef" + strings.Repeat("f", 56), ok: true},
		{id: strings.Repeat("A", 64)},             // uppercase rejected
		{id: "aB" + strings.Repeat("a", 62)},      // mixed case rejected
		{id: strings.Repeat("a", 63)},             // too short
		{id: strings.Repeat("a", 65)},             // too long
		{id: "g" + strings.Repeat("a", 63)},       // not hex
		{id: strings.Repeat("z", 64)},             // not hex
		{id: "[" + strings.Repeat("a", 63) + "]"}, // wrapped
		{id: ""},                                 // empty (call site treats as absent)
		{id: "\u0663" + strings.Repeat("a", 63)}, // non-ASCII
	}
	for _, tc := range cases {
		if got := isValidA2Cursor(tc.id); got != tc.ok {
			t.Errorf("isValidA2Cursor(%q) = %v, want %v", tc.id, got, tc.ok)
		}
	}
}

var a2CursorRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// TestWorkWallA2CursorStability pins the cursor contract: lowercase sha256
// hex, a pure function of pane content (the per-snapshot observed_at stamp
// excluded), deterministic, and different for different content, workspace
// or pane count.
func TestWorkWallA2CursorStability(t *testing.T) {
	t1 := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	t2 := t1.Add(90 * time.Second)

	base := []workwall.A2PaneV1{a2TestPane(a2TestEmployeeID)}
	base[0].ObservedAt = t1

	later := []workwall.A2PaneV1{a2TestPane(a2TestEmployeeID)}
	later[0].ObservedAt = t2

	c1 := a2SnapshotCursor(a2TestWorkspaceID, base)
	c2 := a2SnapshotCursor(a2TestWorkspaceID, later)
	if c1 == "" || c2 == "" {
		t.Fatal("cursor must not be empty")
	}
	if !a2CursorRE.MatchString(c1) || !a2CursorRE.MatchString(c2) {
		t.Fatalf("cursor must be 64-char lowercase hex, got %q / %q", c1, c2)
	}
	if c1 != c2 {
		t.Fatal("identical pane content must share one cursor regardless of the observed_at stamp")
	}
	if a2SnapshotCursor(a2TestWorkspaceID, base) != c1 {
		t.Fatal("cursor must be deterministic")
	}

	// Same panes under a different workspace are a different answer.
	otherWS := a2SnapshotCursor("99999999-9999-9999-9999-999999999999", base)
	if otherWS == c1 {
		t.Fatal("cursor must bind to the workspace id")
	}

	// Any semantic pane change moves the cursor.
	renamed := []workwall.A2PaneV1{a2TestPane(a2TestEmployeeID)}
	renamed[0].ObservedAt = t1
	renamed[0].EmployeeName = "Someone Else"
	if a2SnapshotCursor(a2TestWorkspaceID, renamed) == c1 {
		t.Fatal("changed pane content must change the cursor")
	}

	empty := a2SnapshotCursor(a2TestWorkspaceID, []workwall.A2PaneV1{})
	if !a2CursorRE.MatchString(empty) {
		t.Fatalf("empty pane set must still yield a valid cursor, got %q", empty)
	}
}

// TestWorkWallA2FilterVisiblePanes pins the visibility filter: only panes
// with a non-empty employee_id the caller may see leave the server, order
// preserved, fail closed on an empty allowed set.
func TestWorkWallA2FilterVisiblePanes(t *testing.T) {
	other := "55555555-5555-5555-5555-555555555555"
	panes := []workwall.A2PaneV1{
		a2TestPane(a2TestEmployeeID),
		{},                // no employee ownership: dropped
		a2TestPane(other), // employee not visible: dropped
		a2TestPane(a2TestEmployeeID),
	}
	allowed := map[string]struct{}{a2TestEmployeeID: {}}

	got := filterA2VisiblePanes(panes, allowed)
	if len(got) != 2 {
		t.Fatalf("kept %d panes, want 2", len(got))
	}
	for i, p := range got {
		if p.EmployeeID != a2TestEmployeeID {
			t.Fatalf("pane %d employee = %q, want visible employee only", i, p.EmployeeID)
		}
	}
	if got[0].WorkRef != panes[0].WorkRef || got[1].WorkRef != panes[3].WorkRef {
		t.Fatal("filter must preserve pane order")
	}

	if all := filterA2VisiblePanes(panes, nil); len(all) != 0 {
		t.Fatal("empty allowed set must keep nothing (fail closed)")
	}
	if all := filterA2VisiblePanes(nil, allowed); len(all) != 0 || all == nil {
		t.Fatal("nil input must yield a non-nil empty slice so the envelope emits []")
	}
}

// TestWorkWallA2EnvelopeShape pins the wire envelope: schema id, workspace,
// cursor, observed_at, event_limit echo and the untouched pane schema.
func TestWorkWallA2EnvelopeShape(t *testing.T) {
	panes := filterA2VisiblePanes([]workwall.A2PaneV1{a2TestPane(a2TestEmployeeID)}, map[string]struct{}{a2TestEmployeeID: {}})
	observed := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	env := newA2SnapshotEnvelope(a2TestWorkspaceID, 250, panes, observed)

	body, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("envelope is not a JSON object: %v", err)
	}
	if decoded["schema_version"] != A2SnapshotSchemaV1 {
		t.Fatalf("schema_version = %v, want %q", decoded["schema_version"], A2SnapshotSchemaV1)
	}
	if decoded["workspace_id"] != a2TestWorkspaceID {
		t.Fatalf("workspace_id = %v, want %q", decoded["workspace_id"], a2TestWorkspaceID)
	}
	cursor, _ := decoded["cursor"].(string)
	if !a2CursorRE.MatchString(cursor) {
		t.Fatalf("cursor = %q, want 64-char lowercase hex", cursor)
	}
	if _, ok := decoded["observed_at"]; !ok {
		t.Fatal("envelope must carry observed_at")
	}
	if decoded["event_limit"] != float64(250) {
		t.Fatalf("event_limit = %v, want 250", decoded["event_limit"])
	}
	panesField, ok := decoded["panes"].([]any)
	if !ok || len(panesField) != 1 {
		t.Fatalf("panes = %v, want one pane", decoded["panes"])
	}
	pane := panesField[0].(map[string]any)
	if pane["schema_version"] != workwall.A2PaneSchemaV1 {
		t.Fatalf("pane schema_version = %v, want %q", pane["schema_version"], workwall.A2PaneSchemaV1)
	}
	if pane["employee_id"] != a2TestEmployeeID {
		t.Fatalf("pane employee_id = %v, want %q", pane["employee_id"], a2TestEmployeeID)
	}

	// An empty (filtered-to-nothing) pane set must serialize as [], not null.
	empty := newA2SnapshotEnvelope(a2TestWorkspaceID, 200, filterA2VisiblePanes(nil, nil), observed)
	eb, _ := json.Marshal(empty)
	if !bytes.Contains(eb, []byte(`"panes":[]`)) {
		t.Fatalf("empty pane set must emit [], got %s", eb)
	}
}

// TestWorkWallA2SSEFrames pins the exact wire format of every frame kind:
// retry hint, named event with cursor id, keepalive comment, error frame.
func TestWorkWallA2SSEFrames(t *testing.T) {
	var buf bytes.Buffer

	writeA2SSERetry(&buf, 5000)
	if got := buf.String(); got != "retry: 5000\n\n" {
		t.Fatalf("retry frame = %q", got)
	}

	buf.Reset()
	writeA2SSEEvent(&buf, "snapshot", "abc123", []byte(`{"x":1}`))
	if got := buf.String(); got != "id: abc123\nevent: snapshot\ndata: {\"x\":1}\n\n" {
		t.Fatalf("event frame = %q", got)
	}

	buf.Reset()
	writeA2SSEEvent(&buf, "error", "", []byte(`{}`))
	if got := buf.String(); got != "event: error\ndata: {}\n\n" {
		t.Fatalf("event frame without id = %q", got)
	}

	buf.Reset()
	writeA2SSEComment(&buf, "keepalive")
	if got := buf.String(); got != ": keepalive\n\n" {
		t.Fatalf("keepalive frame = %q", got)
	}

	buf.Reset()
	writeA2SSEError(&buf, a2SSECodeSnapshotFailed)
	if got := buf.String(); got != "event: error\ndata: {\"error\":\""+a2SSECodeSnapshotFailed+"\"}\n\n" {
		t.Fatalf("error frame = %q", got)
	}
}

// TestWorkWallA2StreamPoll pins the per-poll decision table of the stream:
// changed cursor -> full snapshot frame carrying the new id; unchanged
// cursor -> keepalive comment; visibility revocation -> error frame and
// close; transient assembly failure -> error frame, stream stays open.
func TestWorkWallA2StreamPoll(t *testing.T) {
	panes := []workwall.A2PaneV1{a2TestPane(a2TestEmployeeID)}
	env := newA2SnapshotEnvelope(a2TestWorkspaceID, 200, panes, time.Now().UTC())
	var buf bytes.Buffer

	// Changed (or first) snapshot: full frame with the cursor id.
	buf.Reset()
	next, open := emitA2StreamPoll(&buf, env, nil, "")
	if !open {
		t.Fatal("a successful poll must keep the stream open")
	}
	if next != env.Cursor {
		t.Fatalf("next cursor = %q, want %q", next, env.Cursor)
	}
	if !strings.Contains(buf.String(), "id: "+env.Cursor+"\n") ||
		!strings.Contains(buf.String(), "event: snapshot\n") ||
		!strings.Contains(buf.String(), A2SnapshotSchemaV1) {
		t.Fatalf("snapshot frame missing id/event/envelope: %q", buf.String())
	}

	// Unchanged content: keepalive, cursor carried forward.
	buf.Reset()
	next, open = emitA2StreamPoll(&buf, env, nil, env.Cursor)
	if !open || next != env.Cursor {
		t.Fatalf("keepalive poll: open=%v next=%q", open, next)
	}
	if got := buf.String(); got != ": keepalive\n\n" {
		t.Fatalf("unchanged poll must emit only the keepalive comment, got %q", got)
	}

	// Visibility revocation: terminal error frame, stream closes.
	buf.Reset()
	next, open = emitA2StreamPoll(&buf, nil, errA2Access, env.Cursor)
	if open {
		t.Fatal("visibility revocation must close the stream")
	}
	if next != env.Cursor {
		t.Fatalf("closed poll must not advance the cursor, got %q", next)
	}
	if got := buf.String(); got != "event: error\ndata: {\"error\":\""+a2SSECodeAccessRevoked+"\"}\n\n" {
		t.Fatalf("revocation frame = %q", got)
	}

	// Transient assembly failure: reported, stream stays open.
	buf.Reset()
	next, open = emitA2StreamPoll(&buf, nil, errA2Assemble, env.Cursor)
	if !open || next != env.Cursor {
		t.Fatalf("transient failure must keep the stream open and cursor: open=%v next=%q", open, next)
	}
	if got := buf.String(); got != "event: error\ndata: {\"error\":\""+a2SSECodeSnapshotFailed+"\"}\n\n" {
		t.Fatalf("transient failure frame = %q", got)
	}
}

// TestWorkWallA2SnapshotBadRequest exercises the snapshot endpoint's
// validation paths that return before any database access.
func TestWorkWallA2SnapshotBadRequest(t *testing.T) {
	h := &Handler{}

	// No workspace identifier at all.
	req := httptest.NewRequest(http.MethodGet, "/api/work-wall/a2/snapshot", nil)
	rec := httptest.NewRecorder()
	h.GetWorkWallA2Snapshot(rec, req)
	assertA2JSONError(t, rec, http.StatusBadRequest, "workspace_id is required")

	// Strict event_limit.
	for _, q := range []string{"event_limit=1001", "event_limit=0", "event_limit=1e3", "event_limit=-1", "event_limit=1.5", "event_limit=%2B1"} {
		req := newA2TestRequest(t, "/api/work-wall/a2/snapshot?"+q, a2TestWorkspaceID)
		rec := httptest.NewRecorder()
		h.GetWorkWallA2Snapshot(rec, req)
		assertA2JSONError(t, rec, http.StatusBadRequest, "invalid event_limit")
	}

	// Unvalidated workspace identifier (e.g. via header/query) must be
	// rejected, never reach the panicking UUID wrapper.
	req = newA2TestRequest(t, "/api/work-wall/a2/snapshot", "not-a-uuid")
	rec = httptest.NewRecorder()
	h.GetWorkWallA2Snapshot(rec, req)
	assertA2JSONError(t, rec, http.StatusBadRequest, "invalid workspace_id")
}

// TestWorkWallA2StreamBadRequest exercises the stream endpoint's validation
// paths that return before any database access or stream start.
func TestWorkWallA2StreamBadRequest(t *testing.T) {
	h := &Handler{}

	// No workspace identifier at all.
	req := httptest.NewRequest(http.MethodGet, "/api/work-wall/a2/stream", nil)
	rec := httptest.NewRecorder()
	h.GetWorkWallA2Stream(rec, req)
	assertA2JSONError(t, rec, http.StatusBadRequest, "workspace_id is required")

	// Strict Last-Event-ID: exactly the emitted cursor form, else 400 —
	// before the stream starts, so the client gets a real status code.
	for _, id := range []string{
		strings.Repeat("A", 64),       // uppercase
		strings.Repeat("a", 63),       // too short
		"g" + strings.Repeat("a", 63), // not hex
		"[reconnect:1]",               // foreign id space
	} {
		req := newA2TestRequest(t, "/api/work-wall/a2/stream", a2TestWorkspaceID)
		req.Header.Set("Last-Event-ID", id)
		rec := httptest.NewRecorder()
		h.GetWorkWallA2Stream(rec, req)
		assertA2JSONError(t, rec, http.StatusBadRequest, "invalid last_event_id")
	}

	// Strict event_limit on the stream too.
	req = newA2TestRequest(t, "/api/work-wall/a2/stream?event_limit=0", a2TestWorkspaceID)
	rec = httptest.NewRecorder()
	h.GetWorkWallA2Stream(rec, req)
	assertA2JSONError(t, rec, http.StatusBadRequest, "invalid event_limit")

	// Unvalidated workspace identifier.
	req = newA2TestRequest(t, "/api/work-wall/a2/stream", "not-a-uuid")
	rec = httptest.NewRecorder()
	h.GetWorkWallA2Stream(rec, req)
	assertA2JSONError(t, rec, http.StatusBadRequest, "invalid workspace_id")
}

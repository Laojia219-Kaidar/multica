package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSanitizeTail_StripsControlCharsAndKeepsNewlineTab(t *testing.T) {
	in := "hello\x00\x01\x1b[31mworld\n\tnext"
	got := sanitizeTail(in, 1000)
	if strings.Contains(got, "\x00") || strings.Contains(got, "\x01") {
		t.Fatalf("control chars not stripped: %q", got)
	}
	if !strings.Contains(got, "\n") || !strings.Contains(got, "\t") {
		t.Fatalf("newline/tab should be preserved: %q", got)
	}
	if strings.Contains(got, "\x1b[") {
		t.Fatalf("ANSI escape not stripped: %q", got)
	}
	if !strings.Contains(got, "hello") || !strings.Contains(got, "world") {
		t.Fatalf("visible text should survive: %q", got)
	}
}

func TestSanitizeTail_StripsOSCSequences(t *testing.T) {
	in := "before\x1b]0;secret-shaped-window-title\x07middle\x1b]2;other-title\x1b\\after"
	got := sanitizeTail(in, 1000)
	if got != "beforemiddleafter" {
		t.Fatalf("OSC escapes should be removed completely, got %q", got)
	}
}

func TestSanitizeTail_TruncatesFromEnd(t *testing.T) {
	in := strings.Repeat("a", 50)
	got := sanitizeTail(in, 10)
	if len(got) != 10 {
		t.Fatalf("expected length 10, got %d: %q", len(got), got)
	}
	// tail-only keeps the last 10 chars
	if got != "aaaaaaaaaa" {
		t.Fatalf("expected last 10 a's, got %q", got)
	}
}

func TestSanitizeTail_RedactsObviousSecretShapes(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"sk-prefix", "export OPENAI_API_KEY=sk-abcdef1234567890 done"},
		{"gla-prefix", "token gla-ABCDEF1234567890 tail"},
		{"mul-prefix", "pat mul-0123456789abcdef tail"},
		{"mul-task-token", "pat mul_0123456789abcdef tail"},
		{"mdt-daemon-token", "pat mdt_0123456789abcdef tail"},
		{"mat-agent-token", "pat mat_0123456789abcdef tail"},
		{"aws-akia", "AWS_KEY=AKIAIOSFODNN7EXAMPLE more"},
		{"bearer", "Authorization: Bearer abcdefghijklmnopqrst"},
		{"lowercase-bearer", "authorization: bearer abcdefghijklmnopqrst"},
		{"password-kv", `password=hunter2 things`},
		{"secret-colon", `client_secret: abcdef0123456789 trailing`},
		{"token-uppercase", `TOKEN=supersecretvalue9999 trailing`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeTail(tc.in, 1000)
			if !strings.Contains(got, "[REDACTED]") {
				t.Fatalf("expected [REDACTED] in output for %q, got %q", tc.in, got)
			}
			// Make sure the actual secret didn't survive.
			bad := []string{
				"sk-abcdef1234567890",
				"gla-ABCDEF1234567890",
				"mul-0123456789abcdef",
				"mul_0123456789abcdef",
				"mdt_0123456789abcdef",
				"mat_0123456789abcdef",
				"AKIAIOSFODNN7EXAMPLE",
				"abcdefghijklmnopqrst",
				"hunter2",
				"abcdef0123456789",
				"supersecretvalue9999",
			}
			for _, b := range bad {
				if strings.Contains(got, b) {
					t.Fatalf("secret %q survived sanitization: %q", b, got)
				}
			}
		})
	}
}

func TestSanitizeTail_PlainTextUnchanged(t *testing.T) {
	in := "go test ./... 2>&1 | tee test.log\nPASS\nok"
	got := sanitizeTail(in, 1000)
	if got != in {
		t.Fatalf("plain text should be unchanged, got %q", got)
	}
}

func TestSanitizeTail_TruncationAfterRedaction(t *testing.T) {
	// Long secret near end must be fully redacted before truncation,
	// not leaked by a tail slice that cuts through the [REDACTED] token.
	in := strings.Repeat("x", 50) + " sk-" + strings.Repeat("Z", 40)
	got := sanitizeTail(in, 30)
	if strings.Contains(got, "sk-") || strings.Contains(got, "ZZZZ") {
		t.Fatalf("secret leaked through truncation: %q", got)
	}
}

// TestReportTerminalPresence_IgnoresBodyWorkspaceSlug verifies the POST handler
// writes rows under the AUTHENTICATED workspace (resolved from middleware/header),
// not the slug supplied in the JSON body. This is the cross-workspace write fix.
func TestReportTerminalPresence_IgnoresBodyWorkspaceSlug(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	// Create a second workspace the test user is NOT a member of.
	var foreignWorkspaceID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ('Foreign WS', 'foreign-terminal-presence', 'foreign', 'FOR')
		RETURNING id
	`).Scan(&foreignWorkspaceID); err != nil {
		t.Fatalf("seed foreign workspace: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM workspace WHERE id = $1`, foreignWorkspaceID)
	})

	// Seed a terminal presence row owned by the foreign workspace on a
	// DISTINCT host so the global (host, session, window, pane) conflict
	// key cannot collide with the POST we are about to make. We want to
	// prove the body slug cannot redirect writes to this foreign row.
	const foreignHost = "test-host-foreign-presence"
	const session = "session-cross-ws"
	if _, err := testPool.Exec(ctx, `
		INSERT INTO terminal_presence
			(workspace_id, host, session_name, window_index, pane_index, pane_pid,
			 current_command, agent_hint, tail_text, heartbeat_at)
		VALUES ($1, $2, $3, 0, 0, 0, 'foreign-cmd', '', 'foreign-tail', now())
	`, foreignWorkspaceID, foreignHost, session); err != nil {
		t.Fatalf("seed foreign presence row: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM terminal_presence WHERE host IN ($1, $2)`, foreignHost, "test-host-auth-presence")
	})

	// POST claiming a foreign slug in the JSON body. The authenticated
	// workspace (X-Workspace-ID) must win; the foreign slug must be ignored.
	const authHost = "test-host-auth-presence"
	body := map[string]any{
		"workspace_slug": "foreign-terminal-presence",
		"host":           authHost,
		"sessions": []map[string]any{
			{
				"session_name":    session,
				"window_index":    0,
				"pane_index":      0,
				"pane_pid":        12345,
				"current_command": "my-cmd",
				"agent_hint":      "codex",
				"tail_text":       "my-tail sk-aaaaaaaaaaaaaaaa",
			},
		},
	}
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/work-wall/terminal-presence", body)
	testHandler.ReportTerminalPresence(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Foreign row must be untouched (its tail must still say "foreign-tail",
	// and it must not have been overwritten with "my-tail").
	var foreignTail string
	var foreignCmd string
	if err := testPool.QueryRow(ctx, `
		SELECT tail_text, current_command FROM terminal_presence
		WHERE workspace_id = $1 AND host = $2 AND session_name = $3
	`, foreignWorkspaceID, foreignHost, session).Scan(&foreignTail, &foreignCmd); err != nil {
		t.Fatalf("query foreign row: %v", err)
	}
	if foreignTail != "foreign-tail" || foreignCmd != "foreign-cmd" {
		t.Fatalf("foreign row was overwritten by cross-workspace POST: tail=%q cmd=%q", foreignTail, foreignCmd)
	}

	// The authenticated workspace must have the new row with redacted tail.
	var gotTail string
	var gotCmd string
	var wsID string
	if err := testPool.QueryRow(ctx, `
		SELECT workspace_id, tail_text, current_command FROM terminal_presence
		WHERE host = $1 AND session_name = $2
		ORDER BY heartbeat_at DESC LIMIT 1
	`, authHost, session).Scan(&wsID, &gotTail, &gotCmd); err != nil {
		t.Fatalf("query authenticated row: %v", err)
	}
	if wsID != testWorkspaceID {
		t.Fatalf("row written to workspace %s, want authenticated workspace %s", wsID, testWorkspaceID)
	}
	if strings.Contains(gotTail, "sk-") {
		t.Fatalf("secret was not redacted before storage: %q", gotTail)
	}
	if !strings.Contains(gotTail, "[REDACTED]") {
		t.Fatalf("expected [REDACTED] in stored tail, got %q", gotTail)
	}
}

func TestReportTerminalPresence_RejectsCrossWorkspacePaneCollision(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	var foreignWorkspaceID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ('Foreign Collision WS', 'foreign-terminal-collision', 'foreign', 'FTC')
		RETURNING id
	`).Scan(&foreignWorkspaceID); err != nil {
		t.Fatalf("seed foreign workspace: %v", err)
	}
	const host = "test-host-cross-workspace-collision"
	const session = "session-cross-workspace-collision"
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM terminal_presence WHERE host = $1`, host)
		testPool.Exec(ctx, `DELETE FROM workspace WHERE id = $1`, foreignWorkspaceID)
	})
	if _, err := testPool.Exec(ctx, `
		INSERT INTO terminal_presence
			(workspace_id, host, session_name, window_index, pane_index, pane_pid,
			 current_command, agent_hint, tail_text, heartbeat_at)
		VALUES ($1, $2, $3, 0, 0, 7, 'foreign-cmd', '', 'foreign-tail', now())
	`, foreignWorkspaceID, host, session); err != nil {
		t.Fatalf("seed foreign presence row: %v", err)
	}

	body := map[string]any{
		"host": host,
		"sessions": []map[string]any{{
			"session_name": session, "window_index": 0, "pane_index": 0,
			"pane_pid": 8, "current_command": "attacker-cmd", "tail_text": "attacker-tail",
		}},
	}
	w := httptest.NewRecorder()
	testHandler.ReportTerminalPresence(w, newRequest("POST", "/api/work-wall/terminal-presence", body))
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 for cross-workspace pane collision, got %d: %s", w.Code, w.Body.String())
	}
	var gotWorkspace, gotTail string
	if err := testPool.QueryRow(ctx, `
		SELECT workspace_id, tail_text FROM terminal_presence
		WHERE host = $1 AND session_name = $2 AND window_index = 0 AND pane_index = 0
	`, host, session).Scan(&gotWorkspace, &gotTail); err != nil {
		t.Fatalf("query foreign row: %v", err)
	}
	if gotWorkspace != foreignWorkspaceID || gotTail != "foreign-tail" {
		t.Fatalf("foreign pane was reassigned: workspace=%q tail=%q", gotWorkspace, gotTail)
	}
}

func TestReportTerminalPresence_SanitizesHostAndSessionName(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	const cleanHost = "test-host-redacted"
	const cleanSession = "session-redacted"
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM terminal_presence WHERE host = $1`, cleanHost)
	})
	body := map[string]any{
		"host": cleanHost + "\x1b]0;mul_0123456789abcdef\x07",
		"sessions": []map[string]any{{
			"session_name": cleanSession + "\x1b]0;mdt_0123456789abcdef\x07",
			"window_index": 0, "pane_index": 0, "pane_pid": 9,
			"current_command": "go test", "tail_text": "PASS",
		}},
	}
	w := httptest.NewRecorder()
	testHandler.ReportTerminalPresence(w, newRequest("POST", "/api/work-wall/terminal-presence", body))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var host, session string
	if err := testPool.QueryRow(ctx, `
		SELECT host, session_name FROM terminal_presence
		WHERE workspace_id = $1 AND host = $2
	`, testWorkspaceID, cleanHost).Scan(&host, &session); err != nil {
		t.Fatalf("query sanitized pane: %v", err)
	}
	if host != cleanHost || session != cleanSession {
		t.Fatalf("host/session not sanitized: host=%q session=%q", host, session)
	}
}

func TestReportTerminalPresence_RejectsMissingWorkspace(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	body := map[string]any{
		"host":     "no-ws-host",
		"sessions": []map[string]any{},
	}
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/work-wall/terminal-presence", body)
	// Strip the workspace identifier so resolveWorkspaceID returns "".
	req.Header.Del("X-Workspace-ID")
	testHandler.ReportTerminalPresence(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when workspace is missing, got %d: %s", w.Code, w.Body.String())
	}
}

func TestListTerminalPresence_RejectsMissingWorkspace(t *testing.T) {
	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/work-wall/terminal-presence", nil)
	req.Header.Del("X-Workspace-ID")
	testHandler.ListTerminalPresence(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when workspace is missing, got %d: %s", w.Code, w.Body.String())
	}
}

func TestListTerminalPresence_ReturnsOnlyAuthenticatedWorkspace(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	const host = "test-host-list-presence"
	const session = "session-list-ws"
	// Seed a row in the authenticated workspace.
	if _, err := testPool.Exec(ctx, `
		INSERT INTO terminal_presence
			(workspace_id, host, session_name, window_index, pane_index, pane_pid,
			 current_command, agent_hint, tail_text, heartbeat_at)
		VALUES ($1, $2, $3, 0, 0, 0, 'cmd', '', 'tail', now())
	`, testWorkspaceID, host, session); err != nil {
		t.Fatalf("seed presence: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM terminal_presence WHERE host = $1`, host)
	})

	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/work-wall/terminal-presence?workspace_slug=foreign-terminal-presence", nil)
	testHandler.ListTerminalPresence(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var rows []terminalPresenceDTO
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, r := range rows {
		if r.Host == host && r.SessionName == session {
			return // success: our row is visible to the authenticated workspace
		}
	}
	t.Fatalf("authenticated workspace's row was not returned (body=%s)", w.Body.String())
}

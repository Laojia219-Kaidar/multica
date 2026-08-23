package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewReturnsKimiBackend(t *testing.T) {
	t.Parallel()
	b, err := New("kimi", Config{ExecutablePath: "/nonexistent/kimi"})
	if err != nil {
		t.Fatalf("New(kimi) error: %v", err)
	}
	if _, ok := b.(*kimiBackend); !ok {
		t.Fatalf("expected *kimiBackend, got %T", b)
	}
}

func TestKimiToolNameFromTitle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		title string
		want  string
	}{
		{"Read file: /tmp/foo.go", "read_file"},
		{"read", "read_file"},
		{"Write: /tmp/bar.go", "write_file"},
		{"Edit", "edit_file"},
		{"Patch: /tmp/x", "edit_file"},
		{"Shell: ls -la", "terminal"},
		{"Bash", "terminal"},
		{"Run command: pwd", "terminal"},
		{"Search: foo", "search_files"},
		{"Glob: *.go", "glob"},
		{"Web search: golang acp", "web_search"},
		{"Fetch: https://example.com", "web_fetch"},
		{"Todo Write", "todo_write"},
		// Fallback: snake_case the title.
		{"Custom Thing", "custom_thing"},
		// Empty input returns empty — caller decides how to react.
		{"", ""},
	}
	for _, tt := range tests {
		got := kimiToolNameFromTitle(tt.title)
		if got != tt.want {
			t.Errorf("kimiToolNameFromTitle(%q) = %q, want %q", tt.title, got, tt.want)
		}
	}
}

// fakeKimiACPScript returns a POSIX-sh script that impersonates
// `kimi acp` for a single short ACP session: it acks initialize /
// session/new and then replies to session/set_model with a JSON-RPC
// error — the scenario the kimiBackend must propagate as a failed
// task rather than silently falling back to the default model.
func fakeKimiACPScript() string {
	return `#!/bin/sh
# Fake ` + "`kimi`" + ` binary — used by TestKimiBackendSetModelFailureFailsTask
# and TestKimiBackendPassesYoloFlag.
#
# Writes the full argv (one arg per line) to $KIMI_ARGS_FILE if that env
# var is set, so tests can assert that the daemon invokes us with the
# right flags (` + "`--yolo acp`" + `, not bare ` + "`acp`" + `).
#
# Then reads one JSON-RPC request per line from stdin, matches on the
# method name, and writes back a canned response. Exits after set_model
# so the kimiBackend cleanup path can run.
if [ -n "$KIMI_ARGS_FILE" ]; then
  for arg in "$@"; do
    printf '%s\n' "$arg" >> "$KIMI_ARGS_FILE"
  done
fi
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  case "$line" in
    *'"method":"initialize"'*)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1,"agentCapabilities":{}}}\n' "$id"
      ;;
    *'"method":"session/new"'*)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"ses_fake"}}\n' "$id"
      ;;
    *'"method":"session/set_model"'*)
      printf '{"jsonrpc":"2.0","id":%s,"error":{"code":-32602,"message":"model not available: bogus-model"}}\n' "$id"
      exit 0
      ;;
  esac
done
`
}

// TestKimiBackendSetModelFailureFailsTask pins the "don't silently
// fall back" behaviour that landed in this PR: when kimi rejects the
// caller-selected model via session/set_model, the task result must
// report status=failed with a message that names the model and the
// upstream error — not claim success while actually running on the
// default model.
func TestKimiBackendSetModelFailureFailsTask(t *testing.T) {
	t.Parallel()

	fakePath := filepath.Join(t.TempDir(), "kimi")
	writeTestExecutable(t, fakePath, []byte(fakeKimiACPScript()))

	backend, err := New("kimi", Config{ExecutablePath: fakePath, Logger: slog.Default()})
	if err != nil {
		t.Fatalf("new kimi backend: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	session, err := backend.Execute(ctx, "prompt-ignored", ExecOptions{
		Model:   "bogus-model",
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	// Drain message stream so the lifecycle goroutine can progress.
	go func() {
		for range session.Messages {
		}
	}()

	select {
	case result, ok := <-session.Result:
		if !ok {
			t.Fatal("result channel closed without a value")
		}
		if result.Status != "failed" {
			t.Fatalf("expected status=failed, got %q (error=%q)", result.Status, result.Error)
		}
		if !strings.Contains(result.Error, `could not switch to model "bogus-model"`) {
			t.Errorf("expected error to name the requested model, got %q", result.Error)
		}
		if !strings.Contains(result.Error, "model not available") {
			t.Errorf("expected error to surface upstream message, got %q", result.Error)
		}
		if result.SessionID != "ses_fake" {
			t.Errorf("expected session id to be preserved on failure, got %q", result.SessionID)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for result")
	}
}

// fakeKimiACPStaleResumeSetModelScript impersonates kimi-cli when a
// resumed session is gone and the caller picked a model:
// session/resume echoes the requested sessionId back, then
// session/set_model rejects the unknown session the way kimi-cli
// actually does — RequestError.invalid_params (-32602) with
// {"session_id": "Session not found"} in data
// (src/kimi_cli/acp/server.py, set_session_model).
func fakeKimiACPStaleResumeSetModelScript() string {
	return `#!/bin/sh
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  case "$line" in
    *'"method":"initialize"'*)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1,"agentCapabilities":{}}}\n' "$id"
      ;;
    *'"method":"session/resume"'*)
      sid=$(printf '%s' "$line" | sed -n 's/.*"sessionId":"\([^"]*\)".*/\1/p')
      printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"%s"}}\n' "$id" "$sid"
      ;;
    *'"method":"session/set_model"'*)
      printf '{"jsonrpc":"2.0","id":%s,"error":{"code":-32602,"message":"Invalid params","data":{"session_id":"Session not found"}}}\n' "$id"
      exit 0
      ;;
  esac
done
`
}

// TestKimiBackendClearsSessionIDWhenSetModelSessionNotFound pins the
// set_model sibling of the resumed-session fix: with a model override,
// session/set_model runs before session/prompt, so a dead resumed
// session surfaces there. The Result must carry an empty SessionID so
// the daemon's fresh-session retry (gated on SessionID == "") fires.
func TestKimiBackendClearsSessionIDWhenSetModelSessionNotFound(t *testing.T) {
	t.Parallel()

	fakePath := filepath.Join(t.TempDir(), "kimi")
	writeTestExecutable(t, fakePath, []byte(fakeKimiACPStaleResumeSetModelScript()))

	backend, err := New("kimi", Config{ExecutablePath: fakePath, Logger: slog.Default()})
	if err != nil {
		t.Fatalf("new kimi backend: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	session, err := backend.Execute(ctx, "prompt-ignored", ExecOptions{
		Timeout:         5 * time.Second,
		ResumeSessionID: "ses_stale",
		Model:           "kimi-for-coding",
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	go func() {
		for range session.Messages {
		}
	}()

	select {
	case result, ok := <-session.Result:
		if !ok {
			t.Fatal("result channel closed without a value")
		}
		if result.Status != "failed" {
			t.Fatalf("expected status=failed, got %q (error=%q)", result.Status, result.Error)
		}
		if !strings.Contains(result.Error, `could not switch to model "kimi-for-coding"`) {
			t.Errorf("expected error to name the requested model, got %q", result.Error)
		}
		if result.SessionID != "" {
			t.Errorf("expected empty session id so the daemon's fresh-session retry fires, got %q", result.SessionID)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for result")
	}
}

// TestKimiBackendInvokesACPSubcommand pins the argv for `kimi`. An
// earlier fix tried passing `--yolo` to bypass per-tool approval
// prompts, but the `acp` subcommand in kimi-cli takes no options
// (see cli/__init__.py @cli.command def acp()), so `--yolo` was a
// no-op and the daemon still hung for 5 min on the first Shell call.
// The actual bypass is in hermesClient.handleAgentRequest, which
// auto-approves session/request_permission. This test catches
// accidental re-introduction of the dead flag.
func TestKimiBackendInvokesACPSubcommand(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	argsFile := filepath.Join(tempDir, "argv.txt")
	fakePath := filepath.Join(tempDir, "kimi")
	writeTestExecutable(t, fakePath, []byte(fakeKimiACPScript()))

	backend, err := New("kimi", Config{
		ExecutablePath: fakePath,
		Logger:         slog.Default(),
		Env:            map[string]string{"KIMI_ARGS_FILE": argsFile},
	})
	if err != nil {
		t.Fatalf("new kimi backend: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Set Model so the fake binary exits on set_model and we don't
	// have to wait for the prompt branch. We only care about argv here.
	session, err := backend.Execute(ctx, "prompt-ignored", ExecOptions{
		Model:   "bogus-model",
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	go func() {
		for range session.Messages {
		}
	}()
	<-session.Result

	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read args file: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) < 1 {
		t.Fatalf("expected at least 1 arg (acp), got %d: %q", len(lines), lines)
	}
	if lines[0] != "acp" {
		t.Errorf("expected first arg to be acp, got %q (full: %q)", lines[0], lines)
	}
	for _, l := range lines {
		switch l {
		case "--yolo", "--auto-approve", "--yes", "-y":
			t.Errorf("kimi acp doesn't accept %q; auto-approval is handled in hermesClient.handleAgentRequest", l)
		}
	}
}

// TestKimiResumeIncludesMcpServers pins the same contract as the matching
// Hermes test: session/resume must carry the managed MCP set so a resumed
// Kimi task has the same MCP tools as a fresh one.
func TestKimiResumeIncludesMcpServers(t *testing.T) {
	t.Parallel()

	recordPath := filepath.Join(t.TempDir(), "frames.jsonl")
	fakePath := filepath.Join(t.TempDir(), "kimi")
	writeTestExecutable(t, fakePath, []byte(fakeACPRecordingScript(recordPath, "ses_resume", `{}`)))

	backend, err := New("kimi", Config{ExecutablePath: fakePath, Logger: slog.Default()})
	if err != nil {
		t.Fatalf("new kimi backend: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := backend.Execute(ctx, "prompt-ignored", ExecOptions{
		Timeout:         5 * time.Second,
		ResumeSessionID: "ses_resume",
		McpConfig:       json.RawMessage(`{"mcpServers":{"fetch":{"command":"uvx"}}}`),
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	go func() {
		for range session.Messages {
		}
	}()
	select {
	case <-session.Result:
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for result")
	}

	frame := findRecordedFrame(t, recordPath, "session/resume")
	params := frame["params"].(map[string]any)
	servers, ok := params["mcpServers"].([]any)
	if !ok {
		t.Fatalf("session/resume.mcpServers: got %T, want []any", params["mcpServers"])
	}
	if len(servers) != 1 || servers[0].(map[string]any)["name"] != "fetch" {
		t.Fatalf("session/resume.mcpServers: got %v, want one entry named fetch", servers)
	}
}

// ── ACP terminal bridge (HIV-862) ──
//
// Kimi Code CLI 0.37.2 executes its native Bash tool by asking the ACP
// client (this daemon) to spawn terminals. These tests pin both halves of
// the fix: the initialize advertisement that makes kimi-cli consider the
// terminal capability available, and the terminal/* request serving the
// fake CLI exercises end-to-end.

// fakeKimiACPTerminalScript impersonates `kimi acp` driving one Bash-tool
// terminal through its full ACP lifecycle: terminal/create → two
// terminal/output polls (before/after the child's second line) →
// terminal/wait_for_exit → terminal/kill (idempotent after exit) →
// terminal/release, then answers session/prompt and exits. Every
// daemon→agent reply is recorded to recordPath with a tag so the Go test
// can assert the exact wire shapes.
func fakeKimiACPTerminalScript(recordPath string) string {
	return `#!/bin/sh
RECORD=` + recordPath + `
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  case "$line" in
    *'"method":"initialize"'*)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1,"agentCapabilities":{}}}\n' "$id"
      ;;
    *'"method":"session/new"'*)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"ses_term"}}\n' "$id"
      ;;
    *'"method":"session/prompt"'*)
      printf '{"jsonrpc":"2.0","id":900,"method":"terminal/create","params":{"sessionId":"ses_term","command":"/bin/sh","args":["-c","echo first-line; echo stderr-line 1>&2; sleep 1; echo second-line; exit 7"],"env":[{"name":"NO_COLOR","value":"1"},{"name":"TERM","value":"dumb"}],"cwd":"'"$PWD"'","outputByteLimit":65536}}\n'
      IFS= read -r resp; printf 'CREATE-RESP %s\n' "$resp" >> "$RECORD"
      tid=$(printf '%s' "$resp" | sed -n 's/.*"terminalId":"\([^"]*\)".*/\1/p')
      sleep 0.3
      printf '{"jsonrpc":"2.0","id":901,"method":"terminal/output","params":{"sessionId":"ses_term","terminalId":"%s"}}\n' "$tid"
      IFS= read -r resp; printf 'OUTPUT1-RESP %s\n' "$resp" >> "$RECORD"
      sleep 2
      printf '{"jsonrpc":"2.0","id":902,"method":"terminal/output","params":{"sessionId":"ses_term","terminalId":"%s"}}\n' "$tid"
      IFS= read -r resp; printf 'OUTPUT2-RESP %s\n' "$resp" >> "$RECORD"
      printf '{"jsonrpc":"2.0","id":903,"method":"terminal/wait_for_exit","params":{"sessionId":"ses_term","terminalId":"%s"}}\n' "$tid"
      IFS= read -r resp; printf 'WAIT-RESP %s\n' "$resp" >> "$RECORD"
      printf '{"jsonrpc":"2.0","id":904,"method":"terminal/kill","params":{"sessionId":"ses_term","terminalId":"%s"}}\n' "$tid"
      IFS= read -r resp; printf 'KILL-RESP %s\n' "$resp" >> "$RECORD"
      printf '{"jsonrpc":"2.0","id":905,"method":"terminal/release","params":{"sessionId":"ses_term","terminalId":"%s"}}\n' "$tid"
      IFS= read -r resp; printf 'RELEASE-RESP %s\n' "$resp" >> "$RECORD"
      printf '{"jsonrpc":"2.0","id":%s,"result":{"stopReason":"end_turn"}}\n' "$id"
      exit 0
      ;;
  esac
done
`
}

// runFakeKimiBackend executes one prompt against a fake kimi binary and
// returns the final Result.
func runFakeKimiBackend(t *testing.T, script string, opts ExecOptions) Result {
	t.Helper()
	fakePath := filepath.Join(t.TempDir(), "kimi")
	writeTestExecutable(t, fakePath, []byte(script))

	backend, err := New("kimi", Config{ExecutablePath: fakePath, Logger: slog.Default()})
	if err != nil {
		t.Fatalf("new kimi backend: %v", err)
	}
	if opts.Timeout == 0 {
		opts.Timeout = 20 * time.Second
	}
	session, err := backend.Execute(context.Background(), "prompt-ignored", opts)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	go func() {
		for range session.Messages {
		}
	}()
	select {
	case result, ok := <-session.Result:
		if !ok {
			t.Fatal("result channel closed without a value")
		}
		return result
	case <-time.After(30 * time.Second):
		t.Fatal("timeout waiting for result")
		return Result{}
	}
}

// recordedTaggedFrame finds the recorded daemon→agent reply tagged with
// tag and returns its decoded JSON.
func recordedTaggedFrame(t *testing.T, recordPath, tag string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("read record file: %v", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, tag+" ") {
			continue
		}
		var frame map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, tag+" ")), &frame); err != nil {
			t.Fatalf("tagged frame %s not valid JSON: %q err=%v", tag, line, err)
		}
		return frame
	}
	t.Fatalf("no recorded frame tagged %s in %s", tag, data)
	return nil
}

// TestKimiBackendAdvertisesTerminalCapability pins the initialize half of
// HIV-862: `kimi acp` refuses every terminal request ("ACP terminal
// capability is unavailable") unless the client advertises
// clientCapabilities.terminal=true, so the kimi backend must send it.
func TestKimiBackendAdvertisesTerminalCapability(t *testing.T) {
	t.Parallel()

	recordPath := filepath.Join(t.TempDir(), "frames.jsonl")
	fakePath := filepath.Join(t.TempDir(), "kimi")
	writeTestExecutable(t, fakePath, []byte(fakeACPRecordingScript(recordPath, "ses_caps", `{}`)))

	backend, err := New("kimi", Config{ExecutablePath: fakePath, Logger: slog.Default()})
	if err != nil {
		t.Fatalf("new kimi backend: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := backend.Execute(ctx, "prompt-ignored", ExecOptions{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	go func() {
		for range session.Messages {
		}
	}()
	select {
	case <-session.Result:
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for result")
	}

	frame := findRecordedFrame(t, recordPath, "initialize")
	params, ok := frame["params"].(map[string]any)
	if !ok {
		t.Fatalf("initialize params: got %T, want map", frame["params"])
	}
	caps, _ := params["clientCapabilities"].(map[string]any)
	if caps["terminal"] != true {
		t.Errorf("kimi initialize clientCapabilities = %v, want terminal=true (kimi-cli gates its Bash tool on it)", caps)
	}
}

// TestKimiBackendServesNativeTerminalRequests drives the full terminal
// lifecycle a real kimi-cli Bash tool performs: create with command/env/
// cwd/outputByteLimit, incremental output polls, wait_for_exit with the
// child's real exit code, idempotent kill after exit, and release. The
// task must still complete normally afterwards.
func TestKimiBackendServesNativeTerminalRequests(t *testing.T) {
	t.Parallel()

	recordPath := filepath.Join(t.TempDir(), "replies.jsonl")
	result := runFakeKimiBackend(t, fakeKimiACPTerminalScript(recordPath), ExecOptions{
		Cwd: t.TempDir(),
	})
	if result.Status != "completed" {
		t.Fatalf("task status = %q (error=%q), want completed", result.Status, result.Error)
	}

	create := recordedTaggedFrame(t, recordPath, "CREATE-RESP")
	createResult, _ := create["result"].(map[string]any)
	terminalID, _ := createResult["terminalId"].(string)
	if terminalID == "" {
		t.Fatalf("terminal/create reply has no terminalId: %#v", create)
	}

	out1 := recordedTaggedFrame(t, recordPath, "OUTPUT1-RESP")
	r1, _ := out1["result"].(map[string]any)
	output1, _ := r1["output"].(string)
	if !strings.Contains(output1, "first-line") || !strings.Contains(output1, "stderr-line") {
		t.Errorf("early output poll = %q, want both streams' first lines", output1)
	}
	if strings.Contains(output1, "second-line") {
		t.Errorf("early output poll saw second-line before it was produced: %q", output1)
	}
	if r1["truncated"] != false {
		t.Errorf("early output truncated = %v, want false", r1["truncated"])
	}

	out2 := recordedTaggedFrame(t, recordPath, "OUTPUT2-RESP")
	r2, _ := out2["result"].(map[string]any)
	output2, _ := r2["output"].(string)
	if !strings.Contains(output2, "second-line") || !strings.Contains(output2, "first-line") {
		t.Errorf("late output poll = %q, want full combined output", output2)
	}

	wait := recordedTaggedFrame(t, recordPath, "WAIT-RESP")
	waitResult, _ := wait["result"].(map[string]any)
	if code, _ := waitResult["exitCode"].(float64); code != 7 {
		t.Errorf("wait_for_exit exitCode = %v (%#v), want 7", waitResult["exitCode"], wait)
	}

	kill := recordedTaggedFrame(t, recordPath, "KILL-RESP")
	if _, hasErr := kill["error"]; hasErr {
		t.Errorf("terminal/kill after exit returned an error: %#v", kill)
	}
	release := recordedTaggedFrame(t, recordPath, "RELEASE-RESP")
	if _, hasErr := release["error"]; hasErr {
		t.Errorf("terminal/release returned an error: %#v", release)
	}
}

// fakeKimiACPTerminalUnknownIDScript impersonates `kimi acp` asking about
// a terminal id the daemon never issued, then finishing the prompt.
func fakeKimiACPTerminalUnknownIDScript(recordPath string) string {
	return `#!/bin/sh
RECORD=` + recordPath + `
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  case "$line" in
    *'"method":"initialize"'*)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1,"agentCapabilities":{}}}\n' "$id"
      ;;
    *'"method":"session/new"'*)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"ses_term"}}\n' "$id"
      ;;
    *'"method":"session/prompt"'*)
      printf '{"jsonrpc":"2.0","id":900,"method":"terminal/output","params":{"sessionId":"ses_term","terminalId":"term-never-issued"}}\n'
      IFS= read -r resp; printf 'UNKNOWN-RESP %s\n' "$resp" >> "$RECORD"
      printf '{"jsonrpc":"2.0","id":%s,"result":{"stopReason":"end_turn"}}\n' "$id"
      exit 0
      ;;
  esac
done
`
}

// TestKimiBackendTerminalUnknownIDReturnsStructuredError pins the
// fail-closed path: an unknown terminal id gets a structured -32002
// resource-not-found JSON-RPC error (the code kimi-cli recognises) and
// the Run keeps flowing instead of hanging.
func TestKimiBackendTerminalUnknownIDReturnsStructuredError(t *testing.T) {
	t.Parallel()

	recordPath := filepath.Join(t.TempDir(), "replies.jsonl")
	result := runFakeKimiBackend(t, fakeKimiACPTerminalUnknownIDScript(recordPath), ExecOptions{})
	if result.Status != "completed" {
		t.Fatalf("task status = %q (error=%q), want completed — an unknown terminal id must not hang or fail the run", result.Status, result.Error)
	}

	unknown := recordedTaggedFrame(t, recordPath, "UNKNOWN-RESP")
	if _, hasResult := unknown["result"]; hasResult {
		t.Fatalf("unknown-id reply carried a result: %#v", unknown)
	}
	rpcErr, _ := unknown["error"].(map[string]any)
	if code, _ := rpcErr["code"].(float64); code != -32002 {
		t.Errorf("unknown-id error code = %v, want -32002 (kimi-cli resource-not-found)", rpcErr["code"])
	}
	if unknown["id"] != float64(900) {
		t.Errorf("unknown-id reply id = %v, want 900", unknown["id"])
	}
}

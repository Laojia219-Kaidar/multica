//go:build unix

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// newTestTerminalManager builds a manager over a cancellable context that
// tests must be able to cancel (which also reaps every spawned process).
func newTestTerminalManager(t *testing.T) (*acpTerminalManager, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		time.Sleep(20 * time.Millisecond) // let ctx-driven kills land before the test ends
	})
	return newACPTerminalManager(ctx, t.TempDir(), slog.Default()), cancel
}

// createTestTerminal drives terminal/create through dispatch and fails the
// test unless it succeeds, returning the new terminal id.
func createTestTerminal(t *testing.T, m *acpTerminalManager, params string) string {
	t.Helper()
	res, terr := m.dispatch("terminal/create", json.RawMessage(params))
	if terr != nil {
		t.Fatalf("terminal/create failed: code=%d msg=%q", terr.code, terr.message)
	}
	id, _ := res.(map[string]any)["terminalId"].(string)
	if id == "" {
		t.Fatalf("terminal/create returned no terminalId: %#v", res)
	}
	return id
}

// outputTestTerminal drives terminal/output and returns the response.
func outputTestTerminal(t *testing.T, m *acpTerminalManager, sessionID, id string) acpTerminalOutputResponse {
	t.Helper()
	res, terr := m.dispatch("terminal/output", terminalIDParams(sessionID, id))
	if terr != nil {
		t.Fatalf("terminal/output failed: code=%d msg=%q", terr.code, terr.message)
	}
	o, ok := res.(acpTerminalOutputResponse)
	if !ok {
		t.Fatalf("terminal/output returned %T, want acpTerminalOutputResponse", res)
	}
	return o
}

// waitTestTerminalExit drives terminal/wait_for_exit and fails unless it
// returns an exit status.
func waitTestTerminalExit(t *testing.T, m *acpTerminalManager, sessionID, id string) acpTerminalExitStatus {
	t.Helper()
	res, terr := m.dispatch("terminal/wait_for_exit", terminalIDParams(sessionID, id))
	if terr != nil {
		t.Fatalf("terminal/wait_for_exit failed: code=%d msg=%q", terr.code, terr.message)
	}
	status, ok := res.(acpTerminalExitStatus)
	if !ok {
		t.Fatalf("terminal/wait_for_exit returned %T, want acpTerminalExitStatus", res)
	}
	return status
}

func terminalIDParams(sessionID, terminalID string) json.RawMessage {
	return json.RawMessage(`{"sessionId":"` + sessionID + `","terminalId":"` + terminalID + `"}`)
}

func testJSONString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// TestACPTerminalCreateRejectsInvalidRequests pins the fail-closed
// validation of terminal/create: empty command, missing session, invalid
// output limits, malformed env names and credential-like env names are
// all rejected with a structured -32602 before any process is spawned.
func TestACPTerminalCreateRejectsInvalidRequests(t *testing.T) {
	t.Parallel()
	m, _ := newTestTerminalManager(t)

	badRequests := map[string]string{
		"empty command":    `{"sessionId":"ses_1","command":""}`,
		"missing command":  `{"sessionId":"ses_1"}`,
		"missing session":  `{"command":"/bin/sh"}`,
		"zero limit":       `{"sessionId":"ses_1","command":"/bin/sh","outputByteLimit":0}`,
		"negative limit":   `{"sessionId":"ses_1","command":"/bin/sh","outputByteLimit":-5}`,
		"malformed env":    `{"sessionId":"ses_1","command":"/bin/sh","env":[{"name":"BAD=NAME","value":"x"}]}`,
		"empty env name":   `{"sessionId":"ses_1","command":"/bin/sh","env":[{"name":"","value":"x"}]}`,
		"non-string env":   `{"sessionId":"ses_1","command":"/bin/sh","env":"PATH=/bin"}`,
		"provider key":     `{"sessionId":"ses_1","command":"/bin/sh","env":[{"name":"KIMI_API_KEY","value":"x"}]}`,
		"daemon token":     `{"sessionId":"ses_1","command":"/bin/sh","env":[{"name":"MULTICA_TOKEN","value":"x"}]}`,
		"auth token":       `{"sessionId":"ses_1","command":"/bin/sh","env":[{"name":"ANTHROPIC_AUTH_TOKEN","value":"x"}]}`,
		"aws session":      `{"sessionId":"ses_1","command":"/bin/sh","env":[{"name":"AWS_SESSION_TOKEN","value":"x"}]}`,
		"generic secret":   `{"sessionId":"ses_1","command":"/bin/sh","env":[{"name":"MY_SECRET","value":"x"}]}`,
		"malformed params": `{"sessionId":`,
	}
	for name, params := range badRequests {
		_, terr := m.dispatch("terminal/create", json.RawMessage(params))
		if terr == nil {
			t.Errorf("%s: expected terminal/create to be rejected", name)
			continue
		}
		if terr.code != -32602 {
			t.Errorf("%s: error code = %d, want -32602 (msg=%q)", name, terr.code, terr.message)
		}
	}

	// Safe request-scoped names kimi's Bash tool actually sends must pass.
	res, terr := m.dispatch("terminal/create", json.RawMessage(`{"sessionId":"ses_1","command":"/bin/echo","args":["ok"],"env":[{"name":"NO_COLOR","value":"1"},{"name":"TERM","value":"dumb"},{"name":"GIT_TERMINAL_PROMPT","value":"0"},{"name":"SHELL","value":"/bin/sh"}]}`))
	if terr != nil {
		t.Fatalf("terminal/create rejected kimi-style env: code=%d msg=%q", terr.code, terr.message)
	}
	if id, _ := res.(map[string]any)["terminalId"].(string); id == "" {
		t.Fatal("terminal/create returned no terminalId for a valid request")
	}
}

// TestACPTerminalExecutesCommandCombinedOutputAndExitCode pins the core
// happy path: the spawned process runs in the requested cwd, stdout and
// stderr are combined into one buffer, and wait_for_exit reports the real
// exit code (non-zero included) once the process is reaped.
func TestACPTerminalExecutesCommandCombinedOutputAndExitCode(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	m, _ := newTestTerminalManager(t)

	id := createTestTerminal(t, m, `{"sessionId":"ses_1","command":"/bin/sh","args":["-c","pwd; echo stdout-line; echo stderr-line 1>&2; exit 7"],"cwd":`+testJSONString(dir)+`}`)

	waitTestTerminalExit(t, m, "ses_1", id)
	out := outputTestTerminal(t, m, "ses_1", id)
	if !strings.Contains(out.Output, "stdout-line") || !strings.Contains(out.Output, "stderr-line") {
		t.Errorf("combined output missing streams: %q", out.Output)
	}
	if !strings.Contains(out.Output, dir) {
		t.Errorf("terminal did not run in requested cwd %s, output=%q", dir, out.Output)
	}
	if out.ExitStatus == nil || out.ExitStatus.ExitCode == nil || *out.ExitStatus.ExitCode != 7 {
		t.Errorf("output exitStatus = %+v, want exitCode 7", out.ExitStatus)
	}

	status := waitTestTerminalExit(t, m, "ses_1", id)
	if status.ExitCode == nil || *status.ExitCode != 7 {
		t.Errorf("wait_for_exit = %+v, want exitCode 7", status)
	}
	if status.Signal != "" {
		t.Errorf("wait_for_exit signal = %q, want empty for a normal exit", status.Signal)
	}
}

// TestACPTerminalOutputIsIncremental pins that output is captured as it
// is produced, not buffered until exit: an early poll sees the first
// line while a later poll sees the rest.
func TestACPTerminalOutputIsIncremental(t *testing.T) {
	t.Parallel()
	m, _ := newTestTerminalManager(t)

	id := createTestTerminal(t, m, `{"sessionId":"ses_1","command":"/bin/sh","args":["-c","echo first-line; sleep 5; echo second-line"]}`)

	// Wait for the first line (appears within milliseconds); the 5s sleep
	// guarantees the second line cannot exist yet.
	var early string
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		early = outputTestTerminal(t, m, "ses_1", id).Output
		if strings.Contains(early, "first-line") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(early, "first-line") {
		t.Fatalf("early poll never saw first-line: %q", early)
	}
	if strings.Contains(early, "second-line") {
		t.Fatalf("early poll saw second-line before it was produced: %q", early)
	}

	// Kill instead of waiting out the 5s sleep; the exit must be by signal.
	if _, terr := m.dispatch("terminal/kill", terminalIDParams("ses_1", id)); terr != nil {
		t.Fatalf("terminal/kill: code=%d msg=%q", terr.code, terr.message)
	}
	status := waitTestTerminalExit(t, m, "ses_1", id)
	if status.Signal == "" {
		t.Errorf("killed terminal wait_for_exit = %+v, want a signal", status)
	}
}

// TestACPTerminalOutputTruncationHonorsRequestLimit pins the bounded
// buffer: a smaller positive outputByteLimit keeps only the newest bytes,
// sets truncated=true, and never exceeds the requested bound.
func TestACPTerminalOutputTruncationHonorsRequestLimit(t *testing.T) {
	t.Parallel()
	m, _ := newTestTerminalManager(t)

	id := createTestTerminal(t, m, `{"sessionId":"ses_1","command":"/bin/sh","args":["-c","yes HEADxxxx | head -n 20; echo TAIL-MARKER"],"outputByteLimit":64}`)
	waitTestTerminalExit(t, m, "ses_1", id)

	out := outputTestTerminal(t, m, "ses_1", id)
	if len(out.Output) > 64 {
		t.Errorf("output length = %d, want <= 64 (request limit)", len(out.Output))
	}
	if !out.Truncated {
		t.Errorf("truncated = false, want true after dropping head bytes")
	}
	if !strings.Contains(out.Output, "TAIL-MARKER") {
		t.Errorf("bounded buffer should keep the newest bytes, got %q", out.Output)
	}
	if strings.Count(out.Output, "HEAD") > 5 {
		t.Errorf("expected head bytes dropped, got %q", out.Output)
	}
}

// TestACPTerminalDaemonMaximumClampsLargeRequestLimit pins the daemon-side
// ceiling: kimi asks for 4 MiB, but the daemon keeps at most
// acpTerminalMaxOutputBytes and reports truncation.
func TestACPTerminalDaemonMaximumClampsLargeRequestLimit(t *testing.T) {
	t.Parallel()
	m, _ := newTestTerminalManager(t)

	id := createTestTerminal(t, m, `{"sessionId":"ses_1","command":"/bin/sh","args":["-c","seq 1 400000"],"outputByteLimit":16777216}`)
	waitTestTerminalExit(t, m, "ses_1", id)

	out := outputTestTerminal(t, m, "ses_1", id)
	if len(out.Output) > acpTerminalMaxOutputBytes {
		t.Errorf("output length = %d, want <= daemon max %d", len(out.Output), acpTerminalMaxOutputBytes)
	}
	if !out.Truncated {
		t.Errorf("truncated = false, want true after clamping to the daemon maximum")
	}
}

// TestACPTerminalReleaseInvalidatesID pins that a released terminal can
// never be touched again through any terminal method: every later access
// fails closed with -32002 (kimi-cli's resource-not-found code).
func TestACPTerminalReleaseInvalidatesID(t *testing.T) {
	t.Parallel()
	m, _ := newTestTerminalManager(t)

	id := createTestTerminal(t, m, `{"sessionId":"ses_1","command":"/bin/echo","args":["done"]}`)
	waitTestTerminalExit(t, m, "ses_1", id)

	if _, terr := m.dispatch("terminal/release", terminalIDParams("ses_1", id)); terr != nil {
		t.Fatalf("terminal/release: code=%d msg=%q", terr.code, terr.message)
	}
	for _, method := range []string{"terminal/output", "terminal/wait_for_exit", "terminal/kill", "terminal/release"} {
		_, terr := m.dispatch(method, terminalIDParams("ses_1", id))
		if terr == nil {
			t.Errorf("%s after release: expected error, got none", method)
			continue
		}
		if terr.code != -32002 {
			t.Errorf("%s after release: code = %d, want -32002 (msg=%q)", method, terr.code, terr.message)
		}
	}
}

// TestACPTerminalUnknownIDAndMalformedParamsFailClosed pins that unknown
// terminal ids and malformed requests return structured errors promptly
// instead of hanging or crashing the Run.
func TestACPTerminalUnknownIDAndMalformedParamsFailClosed(t *testing.T) {
	t.Parallel()
	m, _ := newTestTerminalManager(t)

	for _, method := range []string{"terminal/output", "terminal/wait_for_exit", "terminal/kill", "terminal/release"} {
		done := make(chan *acpTerminalError, 1)
		go func(method string) {
			_, terr := m.dispatch(method, terminalIDParams("ses_1", "term-does-not-exist"))
			done <- terr
		}(method)
		select {
		case terr := <-done:
			if terr == nil || terr.code != -32002 {
				t.Errorf("%s unknown id: got %+v, want code -32002", method, terr)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("%s unknown id: dispatch hung", method)
		}
	}

	for _, params := range []string{
		`{"sessionId":"ses_1"}`,            // no terminalId
		`{"sessionId":"ses_1","terminalId`, // malformed JSON
	} {
		_, terr := m.dispatch("terminal/output", json.RawMessage(params))
		if terr == nil || terr.code != -32602 {
			t.Errorf("malformed params %s: got %+v, want code -32602", params, terr)
		}
	}
	// A request with no sessionId addresses no session the terminal could
	// belong to; the ownership lookup must fail closed (-32002), never
	// fall through to a sessionless match.
	if _, terr := m.dispatch("terminal/output", json.RawMessage(`{"terminalId":"term-x"}`)); terr == nil || terr.code != -32002 {
		t.Errorf("no-session access: got %+v, want code -32002", terr)
	}
}

// TestACPTerminalSessionOwnershipEnforced pins that a terminal belongs to
// exactly one session of the owning client: an id addressed from another
// session fails closed exactly like an unknown id (no existence oracle),
// and — the review P1 scenario — a foreign terminal/release must be
// rejected WITHOUT invalidating the owner's terminal, so afterwards the
// owner can still terminal/output and then terminal/release successfully.
func TestACPTerminalSessionOwnershipEnforced(t *testing.T) {
	t.Parallel()
	m, _ := newTestTerminalManager(t)

	id := createTestTerminal(t, m, `{"sessionId":"ses_owner","command":"/bin/echo","args":["hi"]}`)
	waitTestTerminalExit(t, m, "ses_owner", id)

	if _, terr := m.dispatch("terminal/output", terminalIDParams("ses_other", id)); terr == nil || terr.code != -32002 {
		t.Errorf("foreign session access: got %+v, want code -32002", terr)
	}
	if _, terr := m.dispatch("terminal/output", terminalIDParams("ses_owner", id)); terr != nil {
		t.Errorf("owning session access should succeed, got code=%d msg=%q", terr.code, terr.message)
	}
	if _, terr := m.dispatch("terminal/kill", terminalIDParams("ses_other", id)); terr == nil || terr.code != -32002 {
		t.Errorf("foreign session kill: got %+v, want code -32002", terr)
	}
	if _, terr := m.dispatch("terminal/release", terminalIDParams("ses_other", id)); terr == nil || terr.code != -32002 {
		t.Errorf("foreign session must not be able to release a terminal: got %+v, want code -32002", terr)
	}

	// P1: the rejected foreign release must have left the owner's
	// terminal fully intact — still readable…
	out := outputTestTerminal(t, m, "ses_owner", id)
	if !strings.Contains(out.Output, "hi") {
		t.Errorf("owner output after rejected foreign release = %q, want the echoed line", out.Output)
	}
	// …and still releasable by its actual owner.
	if _, terr := m.dispatch("terminal/release", terminalIDParams("ses_owner", id)); terr != nil {
		t.Fatalf("owner release after rejected foreign release: code=%d msg=%q", terr.code, terr.message)
	}
	// Only the owner's own release invalidates the id.
	if _, terr := m.dispatch("terminal/output", terminalIDParams("ses_owner", id)); terr == nil || terr.code != -32002 {
		t.Errorf("output after owner release: got %+v, want code -32002", terr)
	}
}

// waitForChildPID reads a pid file written by a terminal's descendant.
func waitForChildPID(t *testing.T, pidFile string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("terminal child never reported its pid via %s", pidFile)
	return 0
}

// assertProcessGone polls signal-0 until the pid is reaped.
func assertProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pid %d survived the cleanup it was tied to", pid)
}

// TestACPTerminalCancellationKillsProcesses pins that spawned processes
// are tied to the parent Task context: cancelling the run context
// terminates the terminal process AND its descendants (whole process
// group), and a blocked wait_for_exit still returns afterwards.
func TestACPTerminalCancellationKillsProcesses(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	m, cancel := newTestTerminalManager(t)

	id := createTestTerminal(t, m, `{"sessionId":"ses_1","command":"/bin/sh","args":["-c","sleep 600 & echo $! > `+pidFile+`; wait"]}`)
	childPID := waitForChildPID(t, pidFile)
	if err := syscall.Kill(childPID, 0); err != nil {
		t.Fatalf("pre-cancel liveness check failed: %v", err)
	}

	cancel()

	// The blocked wait must return (kill + acpTerminalWaitGrace bound it),
	// reporting death by signal.
	status := waitTestTerminalExit(t, m, "ses_1", id)
	if status.Signal == "" && status.ExitCode == nil {
		t.Errorf("wait_for_exit after cancel = %+v, want a terminal outcome", status)
	}
	assertProcessGone(t, childPID)
}

// TestACPTerminalCloseAllKillsRemaining pins client-shutdown cleanup:
// terminals the agent never released die with the bridge, descendants
// included, and later creates are refused.
func TestACPTerminalCloseAllKillsRemaining(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	m, _ := newTestTerminalManager(t)

	createTestTerminal(t, m, `{"sessionId":"ses_1","command":"/bin/sh","args":["-c","sleep 600 & echo $! > `+pidFile+`; wait"]}`)
	childPID := waitForChildPID(t, pidFile)

	m.closeAll()

	assertProcessGone(t, childPID)
	if _, terr := m.dispatch("terminal/create", json.RawMessage(`{"sessionId":"ses_1","command":"/bin/echo"}`)); terr == nil {
		t.Error("terminal/create after closeAll should be refused")
	}
}

// TestACPTerminalSecretEnvNotInherited proves the terminal child does not
// inherit provider/API credentials or daemon task tokens: only the
// allowlisted host environment plus the (screened) request-scoped
// variables are present, and representative secret names and values are
// absent from the child's environment.
func TestACPTerminalSecretEnvNotInherited(t *testing.T) {
	// t.Setenv forbids t.Parallel().
	secrets := map[string]string{
		"KIMI_API_KEY":          "sk-kimi-leak-123",
		"MOONSHOT_API_KEY":      "sk-moonshot-leak-123",
		"ANTHROPIC_AUTH_TOKEN":  "sk-ant-leak-123",
		"OPENAI_API_KEY":        "sk-openai-leak-123",
		"AWS_SECRET_ACCESS_KEY": "aws-secret-leak-123",
		"MULTICA_TOKEN":         "mat-task-token-leak-123",
	}
	for k, v := range secrets {
		t.Setenv(k, v)
	}

	m, _ := newTestTerminalManager(t)

	id := createTestTerminal(t, m, `{"sessionId":"ses_1","command":"/usr/bin/env","env":[{"name":"FOO","value":"bar"},{"name":"NO_COLOR","value":"1"}]}`)
	waitTestTerminalExit(t, m, "ses_1", id)

	envDump := outputTestTerminal(t, m, "ses_1", id).Output
	for name := range secrets {
		if strings.Contains(envDump, name+"=") {
			t.Errorf("terminal child inherited credential env %s", name)
		}
	}
	if strings.Contains(envDump, "leak-123") {
		t.Error("terminal child environment contains a secret value")
	}
	for _, want := range []string{"FOO=bar", "NO_COLOR=1", "PATH=", "HOME="} {
		if !strings.Contains(envDump, want) {
			t.Errorf("terminal child env missing allowlisted/request entry %q", want)
		}
	}
}

// TestACPTerminalIDsUniqueAndUnguessable pins that ids across many
// terminals never repeat and carry no sequential structure.
func TestACPTerminalIDsUniqueAndUnguessable(t *testing.T) {
	t.Parallel()
	m, _ := newTestTerminalManager(t)
	seen := make(map[string]bool)
	for i := 0; i < 8; i++ {
		id := createTestTerminal(t, m, `{"sessionId":"ses_1","command":"/bin/echo","args":["x"]}`)
		if seen[id] {
			t.Fatalf("duplicate terminal id %s", id)
		}
		seen[id] = true
		if id == "term-"+strconv.Itoa(i) || id == "term-"+strconv.Itoa(i+1) {
			t.Fatalf("terminal id %s is sequentially guessable", id)
		}
	}
}

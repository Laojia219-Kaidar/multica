package agent

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func governedQwenFixture(t *testing.T, secretMode os.FileMode, omitOwner, omitLandlock bool) (*qwenBackend, string, string) {
	t.Helper()
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(repoRoot, "ops", "dgx-runtime-foundation", "bin", "qwen-hive-qwen")
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	bin := filepath.Join(tmp, "bin")
	work := filepath.Join(tmp, "work")
	for _, dir := range []string{filepath.Join(home, ".qwen"), bin, work} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	secret := filepath.Join(home, ".qwen", ".env")
	if err := os.WriteFile(secret, []byte("placeholder-reference-only\n"), secretMode); err != nil {
		t.Fatal(err)
	}
	argsFile := filepath.Join(tmp, "argv")
	traceFile := filepath.Join(tmp, "trace")
	qwen := filepath.Join(bin, "qwen")
	writeTestExecutable(t, qwen, []byte(`#!/bin/sh
set -eu
if [ "${1:-}" = --version ]; then printf 'qwen 0.21.14\n'; exit 0; fi
printf '%s\n' "$@" > "$QWEN_ARGS_FILE"
printf '%s\n' '{"type":"system","subtype":"init","session_id":"governed-1","model":"qwen3.7-plus"}'
printf '%s\n' '{"type":"assistant","session_id":"governed-1","message":{"role":"assistant","model":"qwen3.7-plus","content":[{"type":"text","text":"PONG"}]}}'
printf '%s\n' '{"type":"result","subtype":"success","session_id":"governed-1","is_error":false,"result":"PONG"}'
`))
	landlock := filepath.Join(bin, "landlock")
	if !omitLandlock {
		writeTestExecutable(t, landlock, []byte(`#!/bin/sh
set -eu
while [ "$#" -gt 0 ]; do
  if [ "$1" = -- ]; then shift; exec "$@"; fi
  shift
done
exit 1
`))
	}
	env := map[string]string{
		"PATH":                            bin + ":" + os.Getenv("PATH"),
		"HIVECREW_CANARY_MODE":            "1",
		"HIVECREW_AUTH_TOKEN_REF":         "owner-reference-only",
		"HIVECREW_WORK_ORDER":             "WO-TEST",
		"HIVECREW_QWEN_REAL_HOME":         home,
		"HIVECREW_QWEN_BIN":               qwen,
		"HIVECREW_QWEN_SECRET_FILE":       secret,
		"HIVECREW_LANDLOCK_EXEC":          landlock,
		"HIVECREW_QWEN_CHAIN_TRACE":       traceFile,
		"QWEN_ARGS_FILE":                  argsFile,
		"HIVECREW_QWEN_PROFILE_ID":        "qwen-hive-qwen",
		"HIVECREW_QWEN_LANDLOCK_REQUIRED": "1",
		"HIVECREW_RUNTIME_PREFIX":         filepath.Join(repoRoot, "ops", "dgx-runtime-foundation"),
	}
	if omitOwner {
		delete(env, "HIVECREW_AUTH_TOKEN_REF")
	}
	return &qwenBackend{cfg: Config{ExecutablePath: wrapper, Logger: slog.Default(), Env: env}}, argsFile, traceFile
}

func TestQwenNoToolUsesGovernedEntrypointEndToEnd(t *testing.T) {
	backend, argsFile, traceFile := governedQwenFixture(t, 0o600, false, false)
	session, err := backend.Execute(context.Background(), "reply PONG", ExecOptions{
		Cwd: t.TempDir(), Model: "qwen3.7-plus", ToolPolicy: "deny", SandboxRequired: true, Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	_, result := awaitQwenResult(t, session)
	if result.Status != "completed" || result.Output != "PONG" {
		t.Fatalf("result = %+v", result)
	}
	trace, err := os.ReadFile(traceFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(strings.Fields(string(trace)), ","); got != "go-entrypoint,resolver,runtime-wrapper,qwen-preflight,landlock-launcher" {
		t.Fatalf("trace = %q", got)
	}
	argv, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(strings.Fields(string(argv)), " ")
	for _, want := range []string{"--model qwen3.7-plus", "--approval-mode plan", "--max-tool-calls 0", "--sandbox", "-p reply PONG", "--output-format stream-json"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("argv missing %q: %s", want, joined)
		}
	}
	if strings.Contains(joined, "owner-reference-only") || strings.Contains(joined, "placeholder-reference-only") {
		t.Fatalf("secret reference leaked into argv: %s", joined)
	}
}

func TestQwenNoToolRejectsRawExecutable(t *testing.T) {
	raw := filepath.Join(t.TempDir(), "qwen")
	writeTestExecutable(t, raw, []byte("#!/bin/sh\nexit 0\n"))
	backend := &qwenBackend{cfg: Config{ExecutablePath: raw, Logger: slog.Default()}}
	if _, err := backend.Execute(context.Background(), "task", ExecOptions{Model: "qwen3.7-plus", ToolPolicy: "deny", SandboxRequired: true}); err == nil || !strings.Contains(err.Error(), "governed wrapper") {
		t.Fatalf("raw executable error = %v", err)
	}
}

func TestQwenNoToolRejectsSameNameOutsideTrustedRoot(t *testing.T) {
	trustedRoot := filepath.Join(t.TempDir(), "trusted")
	if err := os.MkdirAll(filepath.Join(trustedRoot, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	forged := filepath.Join(t.TempDir(), "qwen-hive-qwen")
	writeTestExecutable(t, forged, []byte("#!/bin/sh\nexit 0\n"))
	backend := &qwenBackend{cfg: Config{
		ExecutablePath: forged,
		Logger:         slog.Default(),
		Env:            map[string]string{"HIVECREW_RUNTIME_PREFIX": trustedRoot},
	}}
	if _, err := backend.Execute(context.Background(), "task", ExecOptions{Model: "qwen3.7-plus", ToolPolicy: "deny", SandboxRequired: true}); err == nil || !strings.Contains(err.Error(), "outside trusted runtime prefix") {
		t.Fatalf("same-name forged executable error = %v", err)
	}
}

func TestQwenGovernedEntrypointFailsClosedBeforeProvider(t *testing.T) {
	for _, tc := range []struct {
		name         string
		mode         os.FileMode
		omitOwner    bool
		omitLandlock bool
	}{
		{name: "unsafe secret mode", mode: 0o644},
		{name: "missing owner context", mode: 0o600, omitOwner: true},
		{name: "missing landlock", mode: 0o600, omitLandlock: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend, argsFile, _ := governedQwenFixture(t, tc.mode, tc.omitOwner, tc.omitLandlock)
			session, err := backend.Execute(context.Background(), "task", ExecOptions{Cwd: t.TempDir(), Model: "qwen3.7-plus", ToolPolicy: "deny", SandboxRequired: true, Timeout: 5 * time.Second})
			if err != nil {
				t.Fatalf("Execute setup: %v", err)
			}
			_, result := awaitQwenResult(t, session)
			if result.Status != "failed" {
				t.Fatalf("result = %+v", result)
			}
			if _, err := os.Stat(argsFile); !os.IsNotExist(err) {
				t.Fatalf("provider executed, args file stat = %v", err)
			}
		})
	}
}

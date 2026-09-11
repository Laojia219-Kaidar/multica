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

func TestNewReturnsMimoBackend(t *testing.T) {
	t.Parallel()
	b, err := New("mimo", Config{ExecutablePath: "/nonexistent/mimo"})
	if err != nil {
		t.Fatalf("New(mimo) error: %v", err)
	}
	if _, ok := b.(*mimoBackend); !ok {
		t.Fatalf("expected *mimoBackend, got %T", b)
	}
}

func TestMimoExecute_NotFound(t *testing.T) {
	t.Parallel()

	b := &mimoBackend{cfg: Config{ExecutablePath: "/nonexistent/path/mimo", Logger: slog.Default()}}

	ctx := context.Background()
	_, err := b.Execute(ctx, "prompt", ExecOptions{})
	if err == nil {
		t.Fatal("expected error for missing executable")
	}
	if !strings.Contains(err.Error(), "mimo executable not found") {
		t.Fatalf("expected 'mimo executable not found' in error, got %q", err.Error())
	}
}

func TestMimoToolNameFromTitle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		title string
		want  string
	}{
		{"Read file: /tmp/foo.go", "read_file"},
		{"Shell: ls -la", "terminal"},
		{"Custom Thing", "custom_thing"},
		{"", ""},
	}
	for _, tt := range tests {
		got := mimoToolNameFromTitle(tt.title)
		if got != tt.want {
			t.Errorf("mimoToolNameFromTitle(%q) = %q, want %q", tt.title, got, tt.want)
		}
	}
}

func TestMimoLaunchInvokesAcpSubcommand(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fakePath := filepath.Join(dir, "mimo")
	argsFile := filepath.Join(dir, "args.txt")
	script := "#!/bin/sh\n" +
		"if [ -n \"$MIMO_ARGS_FILE\" ]; then\n" +
		"  for arg in \"$@\"; do\n" +
		"    printf '%s\\n' \"$arg\" >> \"$MIMO_ARGS_FILE\"\n" +
		"  done\n" +
		"fi\n" +
		"while IFS= read -r line; do\n" +
		"  id=$(printf '%s' \"$line\" | sed -n 's/.*\"id\":\\([0-9]*\\).*/\\1/p')\n" +
		"  case \"$line\" in\n" +
		"    *'\"method\":\"initialize\"'*)\n" +
		"      printf '{\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"protocolVersion\":1,\"agentCapabilities\":{}}}\\n' \"$id\"\n" +
		"      ;;\n" +
		"    *'\"method\":\"session/new\"'*)\n" +
		"      printf '{\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"sessionId\":\"ses_mimo_fake\"}}\\n' \"$id\"\n" +
		"      ;;\n" +
		"    *'\"method\":\"session/prompt\"'*)\n" +
		"      printf '{\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"stopReason\":\"end_turn\"}}\\n' \"$id\"\n" +
		"      exit 0\n" +
		"      ;;\n" +
		"  esac\n" +
		"done\n"
	writeTestExecutable(t, fakePath, []byte(script))

	backend, err := New("mimo", Config{
		ExecutablePath: fakePath,
		Logger:         slog.Default(),
		Env:            map[string]string{"MIMO_ARGS_FILE": argsFile},
	})
	if err != nil {
		t.Fatalf("new mimo backend: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	session, err := backend.Execute(ctx, "hello", ExecOptions{Timeout: 5 * time.Second})
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
		if result.Status != "completed" {
			t.Fatalf("expected status=completed, got %q (error=%q)", result.Status, result.Error)
		}
		if result.SessionID != "ses_mimo_fake" {
			t.Fatalf("expected session id ses_mimo_fake, got %q", result.SessionID)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for result")
	}

	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read args file: %v", err)
	}
	args := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(args) == 0 || args[0] != "acp" {
		t.Fatalf("expected first arg acp, got %#v", args)
	}
}

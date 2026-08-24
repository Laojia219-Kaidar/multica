package orcabridge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeFakeOrca(t *testing.T, responses map[string]fakeResponse) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "orca")
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString(`printf '%s\n' "$@" > "` + filepath.Join(dir, "last-args") + `"` + "\n")
	// Route on the first two args: `orchestration <verb>`.
	b.WriteString(`
verb="$2"
`)
	// Each verb writes its canned stdout; behaviors needing dynamics use env.
	for verb, response := range responses {
		fmt.Fprintf(&b, "if [ \"$verb\" = \"%s\" ]; then cat <<'JSON'\n%s\nJSON\nexit %d\nfi\n", verb, response.body, response.exit)
	}
	b.WriteString("echo '{\"id\":\"x\",\"ok\":false,\"error\":{\"code\":\"unknown_verb\",\"message\":\"no canned response\"}}'\nexit 1\n")
	if err := os.WriteFile(script, []byte(b.String()), 0o755); err != nil {
		t.Fatalf("write fake orca executable: %v", err)
	}
	return script
}

type fakeResponse struct {
	body string
	exit int
}

func lastArgs(t *testing.T, executable string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(executable), "last-args"))
	if err != nil {
		t.Fatalf("read last args: %v", err)
	}
	return strings.Fields(strings.TrimSpace(string(raw)))
}

func ok(body string) fakeResponse { return fakeResponse{body: body, exit: 0} }

func TestOrcaCLIRunCreateParsesRunID(t *testing.T) {
	script := writeFakeOrca(t, map[string]fakeResponse{
		"run-create": ok(`{"id":"req1","ok":true,"result":{"run":{"id":"run_abc123"}}}`),
	})
	cli := NewOrcaCLI(script)
	runID, err := cli.RunCreate(context.Background(), "objective [hivecrew-orca-bridge/v1 ws=a prj=b]")
	if err != nil {
		t.Fatalf("RunCreate: %v", err)
	}
	if runID != "run_abc123" {
		t.Fatalf("run id = %q", runID)
	}
	args := lastArgs(t, script)
	if args[0] != "orchestration" || args[1] != "run-create" {
		t.Fatalf("unexpected argv head: %v", args)
	}
	if args[2] != "--objective" || args[len(args)-1] != "--json" {
		t.Fatalf("expected --objective as the first flag and --json last, got %v", args)
	}
}

func TestOrcaCLIRunCreateFlatResultKey(t *testing.T) {
	script := writeFakeOrca(t, map[string]fakeResponse{
		"run-create": ok(`{"id":"req1","ok":true,"result":{"runId":"run_def456"}}`),
	})
	cli := NewOrcaCLI(script)
	runID, err := cli.RunCreate(context.Background(), "objective")
	if err != nil || runID != "run_def456" {
		t.Fatalf("RunCreate flat key: runID=%q err=%v", runID, err)
	}
}

func TestOrcaCLIRunCreateRejectsNonRunID(t *testing.T) {
	script := writeFakeOrca(t, map[string]fakeResponse{
		"run-create": ok(`{"id":"req1","ok":true,"result":{"run":{"id":"evil --flag"}}}`),
	})
	cli := NewOrcaCLI(script)
	if _, err := cli.RunCreate(context.Background(), "objective"); err == nil {
		t.Fatal("expected handle-grammar rejection")
	}
}

func TestOrcaCLIRunListDecodes(t *testing.T) {
	script := writeFakeOrca(t, map[string]fakeResponse{
		"run-list": ok(`{"id":"r","ok":true,"result":{"runs":[{"id":"run_1","objective":"a"},{"id":"run_legacy_local","objective":"legacy"}]}}`),
	})
	cli := NewOrcaCLI(script)
	runs, err := cli.RunList(context.Background())
	if err != nil {
		t.Fatalf("RunList: %v", err)
	}
	if len(runs) != 2 || runs[0].ID != "run_1" {
		t.Fatalf("runs = %+v", runs)
	}
}

func TestOrcaCLITaskCreatePassesRunAndTitle(t *testing.T) {
	script := writeFakeOrca(t, map[string]fakeResponse{
		"task-create": ok(`{"id":"t","ok":true,"result":{"task":{"id":"task_9"}}}`),
	})
	cli := NewOrcaCLI(script)
	taskID, err := cli.TaskCreate(context.Background(), TaskCreateInput{RunID: "run_1", Spec: "spec body", Title: "title"})
	if err != nil || taskID != "task_9" {
		t.Fatalf("TaskCreate: taskID=%q err=%v", taskID, err)
	}
	args := lastArgs(t, script)
	joined := strings.Join(args, " ")
	for _, want := range []string{"--run run_1", "--task-title title", "--spec spec body"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("argv %v missing %q", args, want)
		}
	}
	if strings.Count(joined, "--json") != 1 {
		t.Fatalf("argv must contain exactly one --json: %v", args)
	}
}

func TestOrcaCLITaskListScopesToRun(t *testing.T) {
	script := writeFakeOrca(t, map[string]fakeResponse{
		"task-list": ok(`{"id":"t","ok":true,"result":{"tasks":[{"id":"task_1","spec":"[hivecrew-orca-bridge/v1 ws=w prj=p issue=i task=t]\nbody"}]}}`),
	})
	cli := NewOrcaCLI(script)
	tasks, err := cli.TaskList(context.Background(), "run_1")
	if err != nil || len(tasks) != 1 {
		t.Fatalf("TaskList: tasks=%+v err=%v", tasks, err)
	}
	if _, _, _, _, ok := TaskMarkerScan(tasks[0].Spec); !ok {
		t.Fatalf("spec marker not scannable: %q", tasks[0].Spec)
	}
	args := lastArgs(t, script)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--run run_1") {
		t.Fatalf("task-list not scoped to run: %v", args)
	}
}

func TestOrcaCLIWorkerStartReady(t *testing.T) {
	script := writeFakeOrca(t, map[string]fakeResponse{
		"worker-start": ok(`{"id":"w","ok":true,"result":{"state":"ready","dispatch":{"id":"ctx_1"},"task":{"id":"task_9"},"run":{"id":"run_1"},"worker":{"agentTerminalHandle":"term_328ff727-9a71-4047-b8d8-c2f5c34c0f7e"},"resource":{"worktreeId":"wtr_1","worktreePath":"/tmp/wt"}}}`),
	})
	cli := NewOrcaCLI(script)
	receipt, err := cli.WorkerStart(context.Background(), WorkerStartInput{
		TaskID:       "task_9",
		RunID:        "run_1",
		WorktreeMode: "new-child",
		WorktreeName: "hivecrew-task-9",
		RepoSelector: "path:/repo",
		Agent:        "codex",
		Model:        "gpt-5.6",
	})
	if err != nil {
		t.Fatalf("WorkerStart: %v", err)
	}
	if receipt.State != "ready" || receipt.DispatchID != "ctx_1" ||
		receipt.AgentTerminalHandle != "term_328ff727-9a71-4047-b8d8-c2f5c34c0f7e" ||
		receipt.WorktreeID != "wtr_1" {
		t.Fatalf("receipt = %+v", receipt)
	}
	args := lastArgs(t, script)
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"--task task_9", "--worktree new-child", "--name hivecrew-task-9",
		"--repo path:/repo", "--agent codex", "--model gpt-5.6", "--run run_1",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("argv %v missing %q", args, want)
		}
	}
}

func TestOrcaCLIWorkerStartFailedReceipt(t *testing.T) {
	script := writeFakeOrca(t, map[string]fakeResponse{
		"worker-start": {body: `{"id":"w","ok":true,"result":{"state":"failed","failedStage":"setup","dispatch":{"id":"ctx_2"}}}`, exit: 1},
	})
	cli := NewOrcaCLI(script)
	receipt, err := cli.WorkerStart(context.Background(), WorkerStartInput{
		TaskID: "task_9", WorktreeMode: "current", Agent: "claude",
	})
	if err != nil {
		t.Fatalf("failed receipt must not be a hard error: %v", err)
	}
	if receipt.State != "failed" || receipt.DispatchID != "ctx_2" {
		t.Fatalf("receipt = %+v", receipt)
	}
}

func TestOrcaCLIWorkerStartStructuredError(t *testing.T) {
	script := writeFakeOrca(t, map[string]fakeResponse{
		"worker-start": {body: `{"id":"w","ok":false,"error":{"code":"worker_start_failed","message":"setup failed"}}`, exit: 1},
	})
	cli := NewOrcaCLI(script)
	_, err := cli.WorkerStart(context.Background(), WorkerStartInput{
		TaskID: "task_9", WorktreeMode: "current", Agent: "claude",
	})
	var cliErr *CLIError
	if !errors.As(err, &cliErr) || cliErr.Code != "worker_start_failed" {
		t.Fatalf("expected structured CLI error, got %v", err)
	}
}

func TestOrcaCLIWorkerStartUnavailable(t *testing.T) {
	script := writeFakeOrca(t, map[string]fakeResponse{
		"worker-start": {body: `garbage`, exit: 1},
	})
	cli := NewOrcaCLI(script)
	cli.Timeout = 2 * time.Second
	_, err := cli.WorkerStart(context.Background(), WorkerStartInput{
		TaskID: "task_9", WorktreeMode: "current", Agent: "claude",
	})
	if err == nil {
		t.Fatal("expected error for unusable CLI output")
	}
}

func TestOrcaCLITimeoutIsOrcaUnavailable(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "orca-slow")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 5\n"), 0o755); err != nil {
		t.Fatalf("write slow fake: %v", err)
	}
	cli := NewOrcaCLI(script)
	cli.Timeout = 200 * time.Millisecond
	_, err := cli.RunCreate(context.Background(), "objective")
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestOrcaCLIMissingExecutableIsOrcaUnavailable(t *testing.T) {
	cli := NewOrcaCLI(filepath.Join(t.TempDir(), "missing-orca"))
	if _, err := cli.RunList(context.Background()); err == nil {
		t.Fatal("expected error for missing executable")
	}
}

func TestOrcaCLIStructuredErrorEnvelope(t *testing.T) {
	script := writeFakeOrca(t, map[string]fakeResponse{
		"dispatch-show": {body: `{"id":"d","ok":false,"error":{"code":"not_found","message":"no dispatch for task"}}`, exit: 1},
	})
	cli := NewOrcaCLI(script)
	_, err := cli.DispatchShow(context.Background(), "task_9")
	var cliErr *CLIError
	if err == nil {
		t.Fatal("expected CLIError")
	}
	if !asCLIError(err, &cliErr) || cliErr.Code != "not_found" {
		t.Fatalf("err = %v", err)
	}
}

func TestOrcaCLIDispatchShowDecodes(t *testing.T) {
	script := writeFakeOrca(t, map[string]fakeResponse{
		"dispatch-show": ok(`{"id":"d","ok":true,"result":{"dispatch":{"id":"ctx_9ca8c0c81a71","run_id":"run_1","task_id":"task_6cc7b1e49533","assignee_handle":"term_328ff727-9a71-4047-b8d8-c2f5c34c0f7e","status":"dispatched","failure_count":0}}}`),
	})
	cli := NewOrcaCLI(script)
	dispatch, err := cli.DispatchShow(context.Background(), "task_6cc7b1e49533")
	if err != nil {
		t.Fatalf("DispatchShow: %v", err)
	}
	if dispatch.ID != "ctx_9ca8c0c81a71" || dispatch.AssigneeHandle != "term_328ff727-9a71-4047-b8d8-c2f5c34c0f7e" {
		t.Fatalf("dispatch = %+v", dispatch)
	}
}

func TestOrcaCLIInboxDecodesObservedShape(t *testing.T) {
	script := writeFakeOrca(t, map[string]fakeResponse{
		"inbox": ok(`{"id":"i","ok":true,"result":{"messages":[` +
			`{"id":"msg_577b8448d366","run_id":"run_1","from_handle":"term_328ff727-9a71-4047-b8d8-c2f5c34c0f7e","to_handle":"run:run_1","subject":"alive","body":"","type":"heartbeat","payload":"{\"taskId\":\"task_1\",\"dispatchId\":\"ctx_1\",\"phase\":\"implementing\"}"}` +
			`]}}`),
	})
	cli := NewOrcaCLI(script)
	messages, err := cli.InboxMessages(context.Background())
	if err != nil || len(messages) != 1 {
		t.Fatalf("InboxMessages: messages=%+v err=%v", messages, err)
	}
	if messages[0].Type != "heartbeat" || messages[0].Payload == "" {
		t.Fatalf("message = %+v", messages[0])
	}
}

func asCLIError(err error, target **CLIError) bool {
	if e, ok := err.(*CLIError); ok {
		*target = e
		return true
	}
	return false
}

package orcabridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// ErrOrcaUnavailable means the Orca CLI could not be executed at all (bad
// path, signal, timeout). Callers must treat execution as unknown, never as
// "not created", and may reconcile with list scans afterwards.
var ErrOrcaUnavailable = errors.New("orcabridge: orca CLI execution failed or timed out")

// CLIError carries a structured Orca error envelope (ok:false).
type CLIError struct {
	Code    string
	Message string
	Stderr  string
}

func (e *CLIError) Error() string {
	return fmt.Sprintf("orcabridge: orca CLI error %s: %s", e.Code, e.Message)
}

// OrcaCLI executes the local Orca CLI in `orchestration` mode. The executable
// path is always explicit: production wiring resolves it from configuration,
// tests pass a fake executable, and ambient PATH lookup is never used.
type OrcaCLI struct {
	// ExecutablePath is the absolute or relative path of the orca binary.
	ExecutablePath string
	// Environ optionally overrides the child environment. Nil keeps the
	// current process environment.
	Environ []string
	// Timeout bounds each CLI invocation. Zero defaults to 30 seconds;
	// worker starts may need longer and set their own.
	Timeout time.Duration
}

// NewOrcaCLI builds an adapter for one explicit executable path.
func NewOrcaCLI(executablePath string) *OrcaCLI {
	return &OrcaCLI{ExecutablePath: executablePath}
}

// envelope is the common `{id, ok, result|error}` CLI JSON envelope.
type envelope struct {
	ID     string          `json:"id"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  *envelopeError  `json:"error"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// OrcaRun is one orchestration Run namespace row from `run-list`.
type OrcaRun struct {
	ID          string `json:"id"`
	Objective   string `json:"objective"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	Coordinator string `json:"coordinator_handle"`
}

// OrcaTask is one orchestration task row from `task-list`.
type OrcaTask struct {
	ID        string `json:"id"`
	RunID     string `json:"run_id"`
	Title     string `json:"title"`
	Spec      string `json:"spec"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
}

// TaskCreateInput is the payload for `task-create`.
type TaskCreateInput struct {
	RunID string
	Spec  string
	Title string
}

// WorkerStartInput is the payload for `worker-start`.
type WorkerStartInput struct {
	TaskID        string
	RunID         string
	WorktreeMode  string // new-child | new-top-level | current
	WorktreeName  string
	RepoSelector  string
	BaseBranch    string
	Agent         string
	Model         string
	Effort        string
	SetupPolicy   string
	TimeoutMillis int64
}

// WorkerReceipt is the normalized `worker-start` result the bridge persists.
type WorkerReceipt struct {
	State               string // ready | failed | outcome_unknown
	Stage               string
	SetupState          string
	DispatchID          string
	TaskID              string
	RunID               string
	AgentTerminalHandle string
	WorktreeID          string
	WorktreePath        string
}

// OrcaDispatch is one dispatch row from `dispatch-show`.
type OrcaDispatch struct {
	ID             string  `json:"id"`
	RunID          string  `json:"run_id"`
	TaskID         string  `json:"task_id"`
	AssigneeHandle string  `json:"assignee_handle"`
	Status         string  `json:"status"`
	FailureCount   int     `json:"failure_count"`
	DispatchedAt   *string `json:"dispatched_at"`
	CompletedAt    *string `json:"completed_at"`
}

// OrcaMessage is one structured orchestration message from `inbox`/`check`.
type OrcaMessage struct {
	ID         string `json:"id"`
	RunID      string `json:"run_id"`
	FromHandle string `json:"from_handle"`
	ToHandle   string `json:"to_handle"`
	Type       string `json:"type"`
	Subject    string `json:"subject"`
	Body       string `json:"body"`
	Payload    string `json:"payload"`
	CreatedAt  string `json:"created_at"`
}

// RunCreate creates one orchestration Run and returns its id.
func (c *OrcaCLI) RunCreate(ctx context.Context, objective string) (string, error) {
	result, err := c.call(ctx, c.timeout(), "orchestration", "run-create", "--objective", objective, "--json")
	if err != nil {
		return "", err
	}
	runID, ok := firstString(result, "run.id", "runId", "id")
	if !ok {
		return "", fmt.Errorf("orcabridge: run-create result carried no run id: %s", truncateJSON(result))
	}
	if err := ValidateOrcaRunID(runID); err != nil {
		return "", err
	}
	return runID, nil
}

// RunList lists orchestration Runs for marker reconciliation.
func (c *OrcaCLI) RunList(ctx context.Context) ([]OrcaRun, error) {
	result, err := c.call(ctx, c.timeout(), "orchestration", "run-list", "--json")
	if err != nil {
		return nil, err
	}
	var payload struct {
		Runs []OrcaRun `json:"runs"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return nil, fmt.Errorf("orcabridge: decode run-list result: %w", err)
	}
	return payload.Runs, nil
}

// TaskCreate creates one orchestration task inside a Run.
func (c *OrcaCLI) TaskCreate(ctx context.Context, input TaskCreateInput) (string, error) {
	if err := ValidateOrcaRunID(input.RunID); err != nil {
		return "", err
	}
	args := []string{
		"orchestration", "task-create",
		"--spec", input.Spec,
		"--run", input.RunID,
		"--json",
	}
	if input.Title != "" {
		args = append(args[:len(args)-1], "--task-title", input.Title, "--json")
	}
	result, err := c.call(ctx, c.timeout(), args...)
	if err != nil {
		return "", err
	}
	taskID, ok := firstString(result, "task.id", "taskId", "id")
	if !ok {
		return "", fmt.Errorf("orcabridge: task-create result carried no task id: %s", truncateJSON(result))
	}
	if err := ValidateOrcaTaskID(taskID); err != nil {
		return "", err
	}
	return taskID, nil
}

// TaskList lists orchestration tasks of one Run for marker reconciliation.
func (c *OrcaCLI) TaskList(ctx context.Context, runID string) ([]OrcaTask, error) {
	if err := ValidateOrcaRunID(runID); err != nil {
		return nil, err
	}
	result, err := c.call(ctx, c.timeout(), "orchestration", "task-list", "--run", runID, "--json")
	if err != nil {
		return nil, err
	}
	var payload struct {
		Tasks []OrcaTask `json:"tasks"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return nil, fmt.Errorf("orcabridge: decode task-list result: %w", err)
	}
	return payload.Tasks, nil
}

// WorkerStart starts one supervised worker. A non-ready outcome is returned
// as a receipt with State failed/outcome_unknown and no error, because the
// Orca receipt is the authority for what actually happened; every other
// failure path returns an error.
func (c *OrcaCLI) WorkerStart(ctx context.Context, input WorkerStartInput) (*WorkerReceipt, error) {
	if err := ValidateOrcaTaskID(input.TaskID); err != nil {
		return nil, err
	}
	timeout := c.timeout()
	if input.TimeoutMillis > 0 {
		timeout = time.Duration(input.TimeoutMillis) * time.Millisecond
	} else {
		input.TimeoutMillis = int64(timeout / time.Millisecond)
	}
	args := []string{
		"orchestration", "worker-start",
		"--task", input.TaskID,
		"--worktree", input.WorktreeMode,
		"--agent", input.Agent,
	}
	if input.RunID != "" {
		if err := ValidateOrcaRunID(input.RunID); err != nil {
			return nil, err
		}
		args = append(args, "--run", input.RunID)
	}
	switch input.WorktreeMode {
	case "new-child", "new-top-level":
		args = append(args, "--name", input.WorktreeName, "--repo", input.RepoSelector)
		if input.BaseBranch != "" {
			args = append(args, "--base-branch", input.BaseBranch)
		}
	case "current":
		// current worktree: fresh agent terminal, no creation flags.
	default:
		return nil, fmt.Errorf("%w: unsupported worktree mode %q", ErrInvalidChain, input.WorktreeMode)
	}
	if input.SetupPolicy != "" {
		args = append(args, "--setup", input.SetupPolicy)
	}
	if input.Model != "" {
		args = append(args, "--model", input.Model)
	}
	if input.Effort != "" {
		args = append(args, "--effort", input.Effort)
	}
	args = append(args, "--json")

	raw, exitErr := c.run(ctx, timeout, args...)
	if exitErr != nil {
		// worker-start exits 1 for failed/outcome_unknown but still emits a
		// JSON receipt; decode it before deciding this is a hard error.
		receipt, decodeErr := decodeWorkerReceipt(raw)
		if decodeErr == nil && (receipt.State == "failed" || receipt.State == "outcome_unknown") {
			return receipt, nil
		}
		return nil, exitErr
	}
	receipt, err := decodeWorkerReceipt(raw)
	if err != nil {
		return nil, err
	}
	return receipt, nil
}

// DispatchShow reads one dispatch row by task id.
func (c *OrcaCLI) DispatchShow(ctx context.Context, taskID string) (*OrcaDispatch, error) {
	if err := ValidateOrcaTaskID(taskID); err != nil {
		return nil, err
	}
	result, err := c.call(ctx, c.timeout(), "orchestration", "dispatch-show", "--task", taskID, "--json")
	if err != nil {
		return nil, err
	}
	var payload struct {
		Dispatch OrcaDispatch `json:"dispatch"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return nil, fmt.Errorf("orcabridge: decode dispatch-show result: %w", err)
	}
	return &payload.Dispatch, nil
}

// InboxMessages reads every undelivered orchestration message visible to this
// Orca server. The bridge filters for worker_done; other message types are
// ignored by the writeback contract.
func (c *OrcaCLI) InboxMessages(ctx context.Context) ([]OrcaMessage, error) {
	result, err := c.call(ctx, c.timeout(), "orchestration", "inbox", "--json")
	if err != nil {
		return nil, err
	}
	var payload struct {
		Messages []OrcaMessage `json:"messages"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return nil, fmt.Errorf("orcabridge: decode inbox result: %w", err)
	}
	return payload.Messages, nil
}

func (c *OrcaCLI) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return 30 * time.Second
}

// call runs one CLI invocation that must succeed with ok:true.
func (c *OrcaCLI) call(ctx context.Context, timeout time.Duration, args ...string) (json.RawMessage, error) {
	raw, err := c.run(ctx, timeout, args...)
	if err != nil {
		return nil, err
	}
	env, err := decodeEnvelope(raw)
	if err != nil {
		return nil, err
	}
	return env.Result, nil
}

// run executes the CLI and captures stdout. Non-zero exits with a decodable
// ok:false envelope become *CLIError; other non-zero exits become
// ErrOrcaUnavailable so callers treat the effect as unknown.
func (c *OrcaCLI) run(ctx context.Context, timeout time.Duration, args ...string) ([]byte, error) {
	if c.ExecutablePath == "" {
		return nil, fmt.Errorf("orcabridge: orca executable path is empty")
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, c.ExecutablePath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if c.Environ != nil {
		cmd.Env = c.Environ
	} else {
		cmd.Env = os.Environ()
	}
	err := cmd.Run()
	raw := stdout.Bytes()
	if ctxErr := runCtx.Err(); ctxErr != nil {
		return raw, fmt.Errorf("%w: %v %s: %v", ErrOrcaUnavailable, c.ExecutablePath, args[0], ctxErr)
	}
	if err != nil {
		var env envelope
		if decodeErr := json.Unmarshal(raw, &env); decodeErr == nil && env.Error != nil {
			code := env.Error.Code
			if code == "" {
				code = "unknown"
			}
			return raw, &CLIError{Code: code, Message: env.Error.Message, Stderr: stderr.String()}
		}
		return raw, fmt.Errorf("%w: %v %s: %v (stderr: %s)", ErrOrcaUnavailable, c.ExecutablePath, args[0], err, stderr.String())
	}
	return raw, nil
}

// decodeEnvelope decodes the CLI envelope and enforces ok:true.
func decodeEnvelope(raw []byte) (*envelope, error) {
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("orcabridge: decode orca CLI envelope: %w", err)
	}
	if !env.OK {
		code, message := "unknown", "orca CLI reported ok:false without an error envelope"
		if env.Error != nil {
			code, message = env.Error.Code, env.Error.Message
			if code == "" {
				code = "unknown"
			}
		}
		return nil, &CLIError{Code: code, Message: message}
	}
	return &env, nil
}

// decodeWorkerReceipt normalizes the flexible worker-start result document.
func decodeWorkerReceipt(raw []byte) (*WorkerReceipt, error) {
	env, err := decodeEnvelope(raw)
	if err != nil {
		return nil, err
	}
	result := env.Result
	receipt := &WorkerReceipt{
		State:      firstStringOr(result, "ready", "workerState", "state", "status"),
		Stage:      firstStringOr(result, "", "stage", "failedStage"),
		SetupState: firstStringOr(result, "", "setup.state", "setup"),
	}
	receipt.DispatchID = firstStringOr(result, "", "dispatch.id", "dispatchId", "worker.dispatchId")
	receipt.TaskID = firstStringOr(result, "", "task.id", "taskId", "worker.taskId")
	receipt.RunID = firstStringOr(result, "", "run.id", "runId", "worker.runId")
	receipt.AgentTerminalHandle = firstStringOr(result, "", "worker.agentTerminalHandle", "agentTerminalHandle", "terminal.handle", "terminal")
	receipt.WorktreeID = firstStringOr(result, "", "resource.worktreeId", "worktree.id", "worktreeId")
	receipt.WorktreePath = firstStringOr(result, "", "resource.worktreePath", "worktree.path")
	if receipt.DispatchID == "" {
		return nil, fmt.Errorf("orcabridge: worker-start result carried no dispatch id: %s", truncateJSON(result))
	}
	if err := ValidateOrcaDispatchID(receipt.DispatchID); err != nil {
		return nil, err
	}
	if receipt.AgentTerminalHandle != "" {
		if err := ValidateOrcaTerminalHandle(receipt.AgentTerminalHandle); err != nil {
			return nil, err
		}
	}
	if receipt.WorktreeID != "" {
		if err := ValidateOrcaWorktreeID(receipt.WorktreeID); err != nil {
			return nil, err
		}
	}
	return receipt, nil
}

// firstString resolves the first existing dotted path to a non-empty string.
func firstString(raw json.RawMessage, paths ...string) (string, bool) {
	for _, path := range paths {
		if value, ok := lookupPath(raw, path); ok {
			if s, ok := value.(string); ok && s != "" {
				return s, true
			}
		}
	}
	return "", false
}

func firstStringOr(raw json.RawMessage, fallback string, paths ...string) string {
	if value, ok := firstString(raw, paths...); ok {
		return value
	}
	return fallback
}

// lookupPath walks a JSON document by dotted path segments.
func lookupPath(raw json.RawMessage, path string) (any, bool) {
	var current any
	if err := json.Unmarshal(raw, &current); err != nil {
		return nil, false
	}
	for _, segment := range splitPath(path) {
		obj, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = obj[segment]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func splitPath(path string) []string {
	var segments []string
	current := ""
	for i := 0; i < len(path); i++ {
		switch path[i] {
		case '\\':
			if i+1 < len(path) {
				i++
				current += string(path[i])
			}
		case '.':
			segments = append(segments, current)
			current = ""
		default:
			current += string(path[i])
		}
	}
	return append(segments, current)
}

func truncateJSON(raw json.RawMessage) string {
	const limit = 400
	if len(raw) <= limit {
		return string(raw)
	}
	return string(raw[:limit]) + "..."
}

package orcabridge

import (
	"context"
	"fmt"

	"github.com/multica-ai/multica/server/internal/daemon"
)

// DaemonLifecyclePort reuses the existing Daemon task lifecycle for
// Orca-bound execution: claim, start, complete, fail, and cancellation
// acknowledgement. The bridge owns no second run state machine; the HiveCrew
// task row advanced by these verbs stays the single execution-lifecycle
// authority.
type DaemonLifecyclePort interface {
	// ClaimTask claims the next queued task for one runtime through the
	// existing daemon claim entry.
	ClaimTask(ctx context.Context, runtimeID string) (*DaemonTask, error)
	// StartTask moves a claimed (dispatched) task to running.
	StartTask(ctx context.Context, taskID string) error
	// CompleteTask settles one task as completed with its output evidence.
	CompleteTask(ctx context.Context, completion TaskCompletion) error
	// FailTask settles one task as failed.
	FailTask(ctx context.Context, failure TaskFailure) error
	// AckTaskCancelled acknowledges an observed cancellation so the server
	// can settle deferred finalization.
	AckTaskCancelled(ctx context.Context, taskID string) error
}

// DaemonTask is the claimed-task projection the bridge consumes. It carries
// only the identity fields the mapping needs.
type DaemonTask struct {
	ID          string
	AgentID     string
	RuntimeID   string
	IssueID     string
	WorkspaceID string
	ProjectID   string
	ThreadName  string
}

// TaskCompletion settles one task as completed.
type TaskCompletion struct {
	TaskID     string
	Output     string
	BranchName string
	SessionID  string
	WorkDir    string
}

// TaskFailure settles one task as failed.
type TaskFailure struct {
	TaskID        string
	Error         string
	SessionID     string
	WorkDir       string
	FailureReason string
}

// DaemonClientAdapter adapts the existing daemon.Client HTTP surface to the
// port. It converts values only; endpoint semantics, retries, and auth stay
// with the existing client.
type DaemonClientAdapter struct {
	Client *daemon.Client
}

// NewDaemonLifecyclePort builds the production lifecycle port over one
// already-authenticated daemon client.
func NewDaemonLifecyclePort(client *daemon.Client) DaemonLifecyclePort {
	return &DaemonClientAdapter{Client: client}
}

// ClaimTask claims the next task for one runtime.
func (a *DaemonClientAdapter) ClaimTask(ctx context.Context, runtimeID string) (*DaemonTask, error) {
	task, err := a.client().ClaimTask(ctx, runtimeID)
	if err != nil {
		return nil, fmt.Errorf("orcabridge: daemon claim: %w", err)
	}
	if task == nil {
		return nil, nil
	}
	return daemonTaskFromClient(task), nil
}

// StartTask moves a claimed task to running.
func (a *DaemonClientAdapter) StartTask(ctx context.Context, taskID string) error {
	if err := ValidateHiveCrewTaskID(taskID); err != nil {
		return err
	}
	if err := a.client().StartTask(ctx, taskID); err != nil {
		return fmt.Errorf("orcabridge: daemon start: %w", err)
	}
	return nil
}

// CompleteTask settles one task as completed.
func (a *DaemonClientAdapter) CompleteTask(ctx context.Context, completion TaskCompletion) error {
	if err := ValidateHiveCrewTaskID(completion.TaskID); err != nil {
		return err
	}
	if err := a.client().CompleteTask(ctx,
		completion.TaskID,
		completion.Output,
		completion.BranchName,
		completion.SessionID,
		completion.WorkDir,
		false, "",
	); err != nil {
		return fmt.Errorf("orcabridge: daemon complete: %w", err)
	}
	return nil
}

// FailTask settles one task as failed.
func (a *DaemonClientAdapter) FailTask(ctx context.Context, failure TaskFailure) error {
	if err := ValidateHiveCrewTaskID(failure.TaskID); err != nil {
		return err
	}
	if err := a.client().FailTask(ctx,
		failure.TaskID,
		failure.Error,
		failure.SessionID,
		failure.WorkDir,
		failure.FailureReason,
		false, "",
	); err != nil {
		return fmt.Errorf("orcabridge: daemon fail: %w", err)
	}
	return nil
}

// AckTaskCancelled acknowledges an observed cancellation.
func (a *DaemonClientAdapter) AckTaskCancelled(ctx context.Context, taskID string) error {
	if err := ValidateHiveCrewTaskID(taskID); err != nil {
		return err
	}
	if err := a.client().AckTaskCancelled(ctx, taskID); err != nil {
		return fmt.Errorf("orcabridge: daemon cancel-ack: %w", err)
	}
	return nil
}

func (a *DaemonClientAdapter) client() *daemon.Client {
	if a == nil || a.Client == nil {
		panic("orcabridge: daemon client adapter has no client")
	}
	return a.Client
}

func daemonTaskFromClient(task *daemon.Task) *DaemonTask {
	return &DaemonTask{
		ID:          task.ID,
		AgentID:     task.AgentID,
		RuntimeID:   task.RuntimeID,
		IssueID:     task.IssueID,
		WorkspaceID: task.WorkspaceID,
		ProjectID:   task.ProjectID,
		ThreadName:  task.ThreadName,
	}
}

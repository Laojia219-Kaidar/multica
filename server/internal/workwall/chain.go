package workwall

import (
	"context"
	"errors"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Execution-chain hydration (HIV-797).
//
// resolveExecutionChain walks the authoritative read rows behind one agent's
// current task so an Owner-visible card can trace:
//
//	Employee/Agent -> Runtime/Profile -> Project -> Issue -> Task -> Run
//	  -> Execution Receipt
//
// It is a read projection only: every identifier it returns comes from an
// existing row (issue / project / runtime_profile / execution_receipt /
// agent_task_queue.autopilot_run_id). Nothing is inferred, copied across
// workspaces, or synthesized when evidence is missing — an absent link simply
// stays empty on the DTO.
//
// Fail-closed rules:
//   - Every issue/project/profile lookup is workspace-scoped; a row outside
//     the snapshot workspace resolves to "no rows" and is reported as absent,
//     never as another workspace's data.
//   - A receipt row is exposed ONLY when its workspace/issue/task lineage
//     matches the task being projected and its terminal status is inside the
//     closed set the schema CHECK allows. Runtime snapshots, result
//     snapshots, digests, errors, prompts and env payloads on the receipt are
//     never read into the projection.
//
// Narrow-read rule: the chain loads only the columns it renders. The store
// surface below resolves workspace.issue_prefix, runtime_profile
// (id, workspace_id, display_name) and execution_receipt (task_id,
// workspace_id, issue_id, terminal_status) — never the workspace
// settings/context, profile commands/fixed_args, or receipt snapshots,
// digests and errors those tables also carry.

// chainStore is the narrow read surface the chain walk needs. It is satisfied
// by *db.Queries in production and faked in unit tests. Issue and project
// rows stay on the shared workspace-scoped lookups; the workspace prefix,
// runtime profile and execution receipt reads are the Work Wall's own narrow
// projections, which select only the columns the card renders.
type chainStore interface {
	GetIssueInWorkspace(ctx context.Context, arg db.GetIssueInWorkspaceParams) (db.Issue, error)
	GetProjectInWorkspace(ctx context.Context, arg db.GetProjectInWorkspaceParams) (db.Project, error)
	GetWorkspaceIssuePrefix(ctx context.Context, id pgtype.UUID) (string, error)
	GetRuntimeProfileForWorkWall(ctx context.Context, arg db.GetRuntimeProfileForWorkWallParams) (db.GetRuntimeProfileForWorkWallRow, error)
	GetExecutionReceiptForWorkWall(ctx context.Context, taskID pgtype.UUID) (db.GetExecutionReceiptForWorkWallRow, error)
}

// Compile-time proof that production binds the walk to the generated narrow
// reads: if the Work Wall chain ever tries to reach a broad row type again,
// this assignment stops compiling.
var _ chainStore = (*db.Queries)(nil)

// chainStoreFor is the production seam binding the walk to the concrete query
// layer (same pattern as runtimeInventoryStoreFor). Tests swap it for a fake.
var chainStoreFor = func(q *db.Queries) chainStore { return q }

// ExecutionChain carries the hydrated chain identifiers for one agent card.
// Every field is optional evidence: empty means "no authoritative row", not
// "unknown string".
type ExecutionChain struct {
	TaskID string

	IssueID         string
	IssueIdentifier string
	IssueTitle      string

	ProjectID    string
	ProjectTitle string

	RuntimeProfileID   string
	RuntimeProfileName string

	// RunID is set ONLY from agent_task_queue.autopilot_run_id — the one
	// authoritative execution identifier this version stores for a task.
	// Direct (comment/assignment-triggered) tasks have no separate Run row:
	// the task itself is the execution unit, so RunID stays empty there and
	// the UI must not display a fabricated run reference.
	RunID string

	ExecutionReceiptRef    string
	ExecutionReceiptStatus string
}

// receiptTerminalStatuses is the closed terminal_status set enforced by the
// execution_receipt schema CHECK. A receipt outside this set (including a
// claimed-but-unfinalized NULL row) is not exposed on the work wall.
func receiptTerminalStatusOK(s string) bool {
	switch s {
	case "completed", "failed", "cancelled":
		return true
	default:
		return false
	}
}

// resolveIssuePrefix returns the stored workspace issue prefix (e.g. "HIV").
// An empty prefix means the exact human identifier cannot be composed; the
// chain then leaves IssueIdentifier empty rather than inventing one. Only the
// prefix column is read — never workspace settings, context or repos.
func resolveIssuePrefix(ctx context.Context, store chainStore, workspaceID pgtype.UUID) (string, error) {
	return store.GetWorkspaceIssuePrefix(ctx, workspaceID)
}

// resolveExecutionChain hydrates the chain for the task currently shown on the
// card (active task, or the most recent terminal task when idle). A nil task
// yields a nil chain. rt may be nil (no runtime row).
func resolveExecutionChain(
	ctx context.Context,
	store chainStore,
	workspaceID pgtype.UUID,
	issuePrefix string,
	rt *db.AgentRuntime,
	task *db.AgentTaskQueue,
) (*ExecutionChain, error) {
	if task == nil {
		return nil, nil
	}
	chain := &ExecutionChain{TaskID: uuidStr(task.ID)}

	if task.AutopilotRunID.Valid {
		chain.RunID = uuidStr(task.AutopilotRunID)
	}

	if rt != nil && rt.ProfileID.Valid {
		profile, err := store.GetRuntimeProfileForWorkWall(ctx, db.GetRuntimeProfileForWorkWallParams{
			ID:          rt.ProfileID,
			WorkspaceID: workspaceID,
		})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		if err == nil {
			chain.RuntimeProfileID = uuidStr(profile.ID)
			chain.RuntimeProfileName = profile.DisplayName
		}
	}

	if task.IssueID.Valid {
		issue, err := store.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{
			ID:          task.IssueID,
			WorkspaceID: workspaceID,
		})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		if err == nil {
			chain.IssueID = uuidStr(issue.ID)
			chain.IssueTitle = issue.Title
			if issuePrefix != "" && issue.Number > 0 {
				chain.IssueIdentifier = issuePrefix + "-" + strconv.Itoa(int(issue.Number))
			}
			if issue.ProjectID.Valid {
				project, perr := store.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{
					ID:          issue.ProjectID,
					WorkspaceID: workspaceID,
				})
				if perr != nil && !errors.Is(perr, pgx.ErrNoRows) {
					return nil, perr
				}
				if perr == nil {
					chain.ProjectID = uuidStr(project.ID)
					chain.ProjectTitle = project.Title
				}
			}
		}
	}

	receipt, err := store.GetExecutionReceiptForWorkWall(ctx, task.ID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if err == nil && receiptLineageMatches(receipt, workspaceID, task) {
		if status := textStr(receipt.TerminalStatus); receiptTerminalStatusOK(status) {
			chain.ExecutionReceiptRef = "receipt://" + chain.TaskID
			chain.ExecutionReceiptStatus = status
		}
	}

	return chain, nil
}

// receiptLineageMatches is the fail-closed receipt gate: the row must belong
// to the same workspace, the same issue and (by construction of the lookup)
// the exact task being projected. Any mismatch hides the receipt entirely.
// The narrow row carries lineage and terminal status only, so there is no
// payload field the gate could accidentally expose.
func receiptLineageMatches(receipt db.GetExecutionReceiptForWorkWallRow, workspaceID pgtype.UUID, task *db.AgentTaskQueue) bool {
	if !receipt.WorkspaceID.Valid || receipt.WorkspaceID != workspaceID {
		return false
	}
	if !receipt.IssueID.Valid || !task.IssueID.Valid || receipt.IssueID != task.IssueID {
		return false
	}
	return receipt.TaskID == task.ID
}

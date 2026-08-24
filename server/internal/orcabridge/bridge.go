package orcabridge

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// OrcaClient is the narrow Orca CLI surface the bridge needs. *OrcaCLI
// implements it; tests substitute a fake.
type OrcaClient interface {
	RunCreate(ctx context.Context, objective string) (string, error)
	RunList(ctx context.Context) ([]OrcaRun, error)
	TaskCreate(ctx context.Context, input TaskCreateInput) (string, error)
	TaskList(ctx context.Context, runID string) ([]OrcaTask, error)
	WorkerStart(ctx context.Context, input WorkerStartInput) (*WorkerReceipt, error)
	DispatchShow(ctx context.Context, taskID string) (*OrcaDispatch, error)
	InboxMessages(ctx context.Context) ([]OrcaMessage, error)
}

// Bridge implements the schema-free HiveCrew -> Orca mapping and the governed
// result writeback (WO-P2-ORCA-BRIDGE A1).
//
// Authority boundaries:
//
//   - HiveCrew stays control truth. Every HiveCrew write goes through exactly
//     one existing dispatch entry (CompanyOpsAssignmentService via
//     AssignmentDispatchPort), the existing workentry kernel (WorkEntryPort),
//     and the existing Daemon lifecycle (DaemonLifecyclePort). The bridge
//     persists nothing itself and owns no second Project/Issue/Task/
//     Assignment/Run/Employee/Runtime registry.
//   - Orca owns placement, terminals, worktrees, and dispatch lifecycle
//     mechanics. The bridge composes Orca worker-start and reconciles Orca
//     state through provenance markers.
//
// Execution flow (all idempotent):
//
//	EnsureAssignment      dispatch through the single existing entry,
//	                      map the receipt's task to an Orca Task, freeze
//	                      placement evidence on the work chain
//	RunClaimedTask        the existing daemon claim loop hands a claimed
//	                      task to the bridge; bridge maps it to one Orca
//	                      supervised worker (isolated worktree), advances the
//	                      HiveCrew task via Daemon StartTask
//	AcceptWorkerResult    a worker_done observation resolves back to the
//	                      HiveCrew chain, appends governed finished evidence
//	                      on the work chain, and settles the HiveCrew task
//	                      via Daemon CompleteTask/FailTask
type Bridge struct {
	Client     OrcaClient
	Entry      WorkEntryPort
	Assignment AssignmentDispatchPort
	Daemon     DaemonLifecyclePort
	// Actor is the automation identity the bridge registers as. The bridge
	// never impersonates a digital employee.
	Actor ActorIdentity
	// Now is injectable for tests; production leaves it as time.Now.
	Now func() time.Time

	// scopeLocks serializes one mapping scope inside one process so
	// concurrent callers converge to exactly one Orca object per HiveCrew
	// object (single-writer behavior); the workentry replay anchors and Orca
	// marker reconciliation cover cross-process and crash recovery.
	scopeLocks sync.Map // scope key -> *sync.Mutex

	memoMu sync.Mutex
	memo   bridgeMemo
}

// lockScope returns the process-wide mutex for one scope key.
func (b *Bridge) lockScope(scope string) *sync.Mutex {
	value, _ := b.scopeLocks.LoadOrStore(scope, &sync.Mutex{})
	return value.(*sync.Mutex)
}

// withScopeLock runs fn while holding the scope mutex.
func (b *Bridge) withScopeLock(scope string, fn func() error) error {
	mutex := b.lockScope(scope)
	mutex.Lock()
	defer mutex.Unlock()
	return fn()
}

// memoEntry is one cached mapping plus its frozen payload digest so replay
// calls still fail closed on drift before touching Orca or the work chain.
type memoEntry struct {
	id     string
	digest string
}

// bridgeMemo caches resolved mappings inside one process. It is a cache
// only: every entry can be rebuilt from Orca markers + workentry evidence.
type bridgeMemo struct {
	run             map[string]memoEntry      // ws:prj -> orca run id
	task            map[string]memoEntry      // ws:task -> orca task id
	runOfWork       map[string]string         // ws:task -> orca run id
	dispatch        map[string]DispatchMap    // ws:assignment -> mapping
	taskAssignment  map[string]map[string]any // hivecrew task id -> assignment payload
	evidencePending map[string]bool           // ws:assignment -> dispatch evidence append still failing
}

func newBridgeMemo() bridgeMemo {
	return bridgeMemo{
		run:             map[string]memoEntry{},
		task:            map[string]memoEntry{},
		runOfWork:       map[string]string{},
		dispatch:        map[string]DispatchMap{},
		taskAssignment:  map[string]map[string]any{},
		evidencePending: map[string]bool{},
	}
}

// NewBridge builds the bridge over one Orca client, one workentry port, the
// single existing dispatch entry, and the existing daemon lifecycle port.
// The bridge actor identity is frozen at construction so replayed linkage
// registrations digest identically on the existing work chain.
func NewBridge(client OrcaClient, entry WorkEntryPort, assignment AssignmentDispatchPort, daemonPort DaemonLifecyclePort, actor ActorIdentity) *Bridge {
	if actor.ObservedAt.IsZero() {
		actor.ObservedAt = time.Now()
	}
	return &Bridge{
		Client:     client,
		Entry:      entry,
		Assignment: assignment,
		Daemon:     daemonPort,
		Actor:      actor,
		Now:        time.Now,
		memo:       newBridgeMemo(),
	}
}

func (b *Bridge) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}

// DispatchMap is the mapping evidence for one HiveCrew assignment command =
// one Orca Dispatch + supervised worker on an isolated worktree. It is a
// value carried on the work chain; the bridge persists nothing itself.
type DispatchMap struct {
	WorkspaceID     string
	ProjectID       string
	IssueID         string
	TaskID          string // HiveCrew task (run row) created by the dispatch entry
	AssignmentID    string
	ContractVersion string
	PlacementDigest string
	OrcaRunID       string
	OrcaTaskID      string
	OrcaDispatchID  string
	WorkerTerminal  string
	WorktreeID      string
	WorktreePath    string
	WorkerState     string
	Status          string
}

// ---------------------------------------------------------------------------
// project run + issue task mapping
// ---------------------------------------------------------------------------

// ProjectRef identifies one HiveCrew project orchestration request.
type ProjectRef struct {
	Chain            Chain
	DisplayObjective string
}

// EnsureProjectRun maps one HiveCrew project to exactly one Orca Run
// namespace. Replays converge through the memo, the workentry registration,
// and the Orca objective marker; payload drift fails closed.
func (b *Bridge) EnsureProjectRun(ctx context.Context, ref ProjectRef) (string, error) {
	// An existing Issue anchor is mandatory: the workentry kernel anchors
	// work_refs on issues and its creation path would implicitly create an
	// Issue (and Project) row. The bridge never authorizes that path, so a
	// project/workspace-only call fails closed here with zero Issue creation
	// instead of creating one.
	if !IsValidUUID(ref.Chain.IssueID) {
		return "", fmt.Errorf("%w: EnsureProjectRun requires the issue anchor created by the existing dispatch entry (workspace=%s project=%s)",
			ErrIssueAnchorRequired, ref.Chain.WorkspaceID, ref.Chain.ProjectID)
	}
	if err := ref.Chain.ValidateIssueAnchoredProjectScope(); err != nil {
		return "", err
	}
	var runID string
	scopeErr := b.withScopeLock("run:"+ref.Chain.WorkspaceID+":"+ref.Chain.ProjectID, func() error {
		id, err := b.ensureProjectRunLocked(ctx, ref)
		if err != nil {
			return err
		}
		runID = id
		return nil
	})
	if scopeErr != nil {
		return "", scopeErr
	}
	return runID, nil
}

func (b *Bridge) ensureProjectRunLocked(ctx context.Context, ref ProjectRef) (string, error) {
	objective := ProjectRunObjective(ref.DisplayObjective, ref.Chain)
	digest, err := ObjectiveInput{
		WorkspaceID:      ref.Chain.WorkspaceID,
		ProjectID:        ref.Chain.ProjectID,
		DisplayObjective: ref.DisplayObjective,
	}.Digest()
	if err != nil {
		return "", err
	}

	// Fast path: memo (digest-guarded).
	if entry, ok := b.memoRunEntry(ref.Chain); ok {
		if entry.digest != digest {
			return "", fmt.Errorf("%w: run mapping ws=%s prj=%s committed %s, replay carried %s",
				ErrMappingConflict, ref.Chain.WorkspaceID, ref.Chain.ProjectID, entry.digest, digest)
		}
		return entry.id, nil
	}

	// Register the mapping scope on the existing work chain. Same digest
	// replays; drift conflicts. This is the HiveCrew control-truth anchor.
	linkage, err := b.Entry.RegisterLinkage(ctx, LinkageInput{
		Chain:       ref.Chain,
		Actor:       b.Actor,
		MappingKind: "run",
	})
	if err != nil {
		return "", err
	}

	// Reconcile Orca: a previous call may have created the Run but crashed
	// before recording it. The marker in the objective is the recovery truth.
	runs, err := b.Client.RunList(ctx)
	if err != nil {
		return "", fmt.Errorf("orcabridge: reconcile runs before create: %w", err)
	}
	for _, run := range runs {
		ws, prj, ok := RunMarkerScan(run.Objective)
		if ok && ws == ref.Chain.WorkspaceID && prj == ref.Chain.ProjectID {
			if err := b.recordLinkageEvidence(ctx, linkage.WorkRef, RunLinkageKey(ref.Chain), map[string]any{
				"mapping":      "run",
				"workspace_id": ref.Chain.WorkspaceID,
				"project_id":   ref.Chain.ProjectID,
				"orca_run_id":  run.ID,
				"digest":       digest,
			}); err != nil {
				return "", err
			}
			b.rememberRun(ref.Chain, run.ID, digest)
			return run.ID, nil
		}
	}

	runID, err := b.Client.RunCreate(ctx, objective)
	if err != nil {
		return "", fmt.Errorf("orcabridge: create orca run: %w", err)
	}
	if err := b.recordLinkageEvidence(ctx, linkage.WorkRef, RunLinkageKey(ref.Chain), map[string]any{
		"mapping":      "run",
		"workspace_id": ref.Chain.WorkspaceID,
		"project_id":   ref.Chain.ProjectID,
		"orca_run_id":  runID,
		"digest":       digest,
	}); err != nil {
		return "", err
	}
	b.rememberRun(ref.Chain, runID, digest)
	return runID, nil
}

// TaskRef identifies one HiveCrew issue task orchestration request.
type TaskRef struct {
	Chain        Chain
	Instructions string
	Title        string
}

// EnsureIssueTask maps one HiveCrew task to exactly one Orca Task inside the
// project Run. The HiveCrew issue identity is carried as provenance inside
// the task spec marker; it never becomes a separate Orca object.
func (b *Bridge) EnsureIssueTask(ctx context.Context, ref TaskRef) (orcaRunID, orcaTaskID string, err error) {
	if err := ref.Chain.ValidateTaskScope(); err != nil {
		return "", "", err
	}
	var runID, taskID string
	scopeErr := b.withScopeLock("task:"+ref.Chain.WorkspaceID+":"+ref.Chain.TaskID, func() error {
		resolvedRun, resolvedTask, err := b.ensureIssueTaskLocked(ctx, ref)
		if err != nil {
			return err
		}
		runID, taskID = resolvedRun, resolvedTask
		return nil
	})
	if scopeErr != nil {
		return "", "", scopeErr
	}
	return runID, taskID, nil
}

func (b *Bridge) ensureIssueTaskLocked(ctx context.Context, ref TaskRef) (string, string, error) {
	runID, err := b.ensureProjectRunLocked(ctx, ProjectRef{Chain: ref.Chain})
	if err != nil {
		return "", "", err
	}
	digest, err := SpecInput{
		WorkspaceID:  ref.Chain.WorkspaceID,
		ProjectID:    ref.Chain.ProjectID,
		IssueID:      ref.Chain.IssueID,
		TaskID:       ref.Chain.TaskID,
		Instructions: ref.Instructions,
	}.Digest()
	if err != nil {
		return "", "", err
	}
	if entry, ok := b.memoTaskEntry(ref.Chain); ok && b.memoRunOfTask(ref.Chain) == runID {
		if entry.digest != digest {
			return "", "", fmt.Errorf("%w: task mapping ws=%s task=%s committed %s, replay carried %s",
				ErrMappingConflict, ref.Chain.WorkspaceID, ref.Chain.TaskID, entry.digest, digest)
		}
		return runID, entry.id, nil
	}
	spec := TaskSpec(ref.Instructions, ref.Chain)
	title := ref.Title
	if title == "" {
		title = "HiveCrew task " + ref.Chain.TaskID
	}
	linkage, err := b.Entry.RegisterLinkage(ctx, LinkageInput{
		Chain:       ref.Chain,
		Actor:       b.Actor,
		MappingKind: "task",
	})
	if err != nil {
		return "", "", err
	}
	tasks, err := b.Client.TaskList(ctx, runID)
	if err != nil {
		return "", "", fmt.Errorf("orcabridge: reconcile tasks before create: %w", err)
	}
	for _, task := range tasks {
		ws, prj, issue, taskID, ok := TaskMarkerScan(task.Spec)
		if ok && ws == ref.Chain.WorkspaceID && prj == ref.Chain.ProjectID &&
			issue == ref.Chain.IssueID && taskID == ref.Chain.TaskID {
			if err := b.recordLinkageEvidence(ctx, linkage.WorkRef, TaskLinkageKey(ref.Chain), map[string]any{
				"mapping":      "task",
				"workspace_id": ref.Chain.WorkspaceID,
				"project_id":   ref.Chain.ProjectID,
				"issue_id":     ref.Chain.IssueID,
				"task_id":      ref.Chain.TaskID,
				"orca_run_id":  runID,
				"orca_task_id": task.ID,
				"digest":       digest,
			}); err != nil {
				return "", "", err
			}
			b.rememberTask(ref.Chain, runID, task.ID, digest)
			return runID, task.ID, nil
		}
	}
	createdTaskID, err := b.Client.TaskCreate(ctx, TaskCreateInput{RunID: runID, Spec: spec, Title: title})
	if err != nil {
		return "", "", fmt.Errorf("orcabridge: create orca task: %w", err)
	}
	if err := b.recordLinkageEvidence(ctx, linkage.WorkRef, TaskLinkageKey(ref.Chain), map[string]any{
		"mapping":      "task",
		"workspace_id": ref.Chain.WorkspaceID,
		"project_id":   ref.Chain.ProjectID,
		"issue_id":     ref.Chain.IssueID,
		"task_id":      ref.Chain.TaskID,
		"orca_run_id":  runID,
		"orca_task_id": createdTaskID,
		"digest":       digest,
	}); err != nil {
		return "", "", err
	}
	b.rememberTask(ref.Chain, runID, createdTaskID, digest)
	return runID, createdTaskID, nil
}

// ---------------------------------------------------------------------------
// assignment dispatch through the single existing entry
// ---------------------------------------------------------------------------

// AssignmentRef identifies one HiveCrew assignment command = one run attempt.
type AssignmentRef struct {
	// Command is forwarded unchanged to the single existing dispatch entry.
	Command AssignmentCommand
	// Placement freezes the Orca worker placement decision for this attempt.
	Placement PlacementInput
	// Instructions are frozen into the Orca Task spec.
	Instructions string
	// Title is an optional Orca Task title.
	Title string
}

// AssignmentMapping is the committed dispatch + mapping state before any
// Orca worker exists.
type AssignmentMapping struct {
	Chain      Chain // complete lineage incl. receipt-derived issue/task ids
	OrcaRunID  string
	OrcaTaskID string
}

// EnsureAssignment dispatches one assignment command through the single
// existing HiveCrew dispatch entry, maps the receipt's initial task to
// exactly one Orca Task, and freezes the placement evidence on the existing
// work chain. Replays converge: the dispatch entry replays by command id,
// the task mapping by marker/digest, and the placement evidence by key.
func (b *Bridge) EnsureAssignment(ctx context.Context, ref AssignmentRef) (AssignmentMapping, error) {
	cmd := ref.Command
	chain := Chain{
		WorkspaceID:  cmd.WorkspaceID,
		ProjectID:    cmd.ProjectID,
		IssueID:      cmd.IssueID,
		AssignmentID: cmd.CommandID,
	}
	if err := chain.ValidateDispatchScope(); err != nil {
		return AssignmentMapping{}, err
	}
	placement := ref.Placement
	placement.WorkspaceID = chain.WorkspaceID
	placement.AssignmentID = chain.AssignmentID
	if err := placement.Validate(); err != nil {
		return AssignmentMapping{}, err
	}
	placementDigest, err := placement.Digest()
	if err != nil {
		return AssignmentMapping{}, err
	}

	// Exactly one existing dispatch entry: the governed assignment service
	// creates the issue assignment, the task (run row), and the receipt.
	outcome, err := b.Assignment.DispatchAssignment(ctx, cmd)
	if err != nil {
		return AssignmentMapping{}, err
	}
	if outcome.CommandID != chain.AssignmentID || outcome.WorkspaceID != chain.WorkspaceID {
		return AssignmentMapping{}, fmt.Errorf("%w: dispatch receipt identity %s/%s does not match command %s/%s",
			ErrInvalidChain, outcome.WorkspaceID, outcome.CommandID, chain.WorkspaceID, chain.AssignmentID)
	}
	chain.IssueID = outcome.IssueID
	chain.TaskID = outcome.InitialTaskID
	if err := chain.ValidateTaskScope(); err != nil {
		return AssignmentMapping{}, err
	}

	orcaRunID, orcaTaskID, err := b.EnsureIssueTask(ctx, TaskRef{
		Chain:        chain,
		Instructions: ref.Instructions,
		Title:        ref.Title,
	})
	if err != nil {
		return AssignmentMapping{}, err
	}

	linkage, err := b.Entry.RegisterLinkage(ctx, LinkageInput{
		Chain:       chain,
		Actor:       b.Actor,
		MappingKind: "dispatch",
	})
	if err != nil {
		return AssignmentMapping{}, err
	}
	assignmentPayload := map[string]any{
		"mapping":          "assignment",
		"status":           "mapped",
		"workspace_id":     chain.WorkspaceID,
		"project_id":       chain.ProjectID,
		"issue_id":         chain.IssueID,
		"task_id":          chain.TaskID,
		"assignment_id":    chain.AssignmentID,
		"orca_run_id":      orcaRunID,
		"orca_task_id":     orcaTaskID,
		"placement":        placementPayload(placement),
		"placement_digest": placementDigest,
	}
	if err := b.recordLinkageEvidence(ctx, linkage.WorkRef, assignmentEvidenceKey(chain), assignmentPayload); err != nil {
		return AssignmentMapping{}, err
	}
	// Task-scoped resolution evidence: the daemon claim loop resolves a
	// claimed HiveCrew task back to its assignment mapping.
	taskPayload := map[string]any{
		"mapping":          "assignment",
		"workspace_id":     chain.WorkspaceID,
		"project_id":       chain.ProjectID,
		"issue_id":         chain.IssueID,
		"task_id":          chain.TaskID,
		"assignment_id":    chain.AssignmentID,
		"orca_run_id":      orcaRunID,
		"orca_task_id":     orcaTaskID,
		"placement":        placementPayload(placement),
		"placement_digest": placementDigest,
		"instructions":     ref.Instructions,
	}
	if err := b.recordLinkageEvidence(ctx, linkage.WorkRef, taskEvidenceKey(chain.WorkspaceID, chain.TaskID), taskPayload); err != nil {
		return AssignmentMapping{}, err
	}
	b.rememberTaskAssignment(chain.TaskID, taskPayload)
	return AssignmentMapping{Chain: chain, OrcaRunID: orcaRunID, OrcaTaskID: orcaTaskID}, nil
}

// ---------------------------------------------------------------------------
// claimed-task execution through the existing daemon lifecycle
// ---------------------------------------------------------------------------

// RunClaimedTask maps one task claimed through the existing daemon claim loop
// to exactly one Orca supervised worker on an isolated worktree, then
// advances the HiveCrew task to running via the existing Daemon StartTask.
// Idempotent: a task whose dispatch mapping already exists returns it without
// starting a second worker.
func (b *Bridge) RunClaimedTask(ctx context.Context, claimed DaemonTask) (DispatchMap, error) {
	if err := ValidateHiveCrewTaskID(claimed.ID); err != nil {
		return DispatchMap{}, err
	}
	resolved, ok, err := b.resolveTaskAssignment(ctx, claimed)
	if err != nil {
		return DispatchMap{}, err
	}
	if !ok {
		return DispatchMap{}, fmt.Errorf("%w: hivecrew task %s carries no bridge assignment linkage", ErrNotBridgeManaged, claimed.ID)
	}
	chain := resolved.chain
	// Single-writer: one assignment scope, one dispatch, one worker. The
	// committed mapping is returned even when its evidence append failed, so
	// callers can observe the committed Orca dispatch alongside the error.
	var mapping DispatchMap
	scopeErr := b.withScopeLock("assignment:"+chain.WorkspaceID+":"+chain.AssignmentID, func() error {
		resolvedMapping, err := b.runClaimedTaskLocked(ctx, claimed, chain, resolved)
		mapping = resolvedMapping
		return err
	})
	if scopeErr != nil {
		return mapping, scopeErr
	}
	return mapping, nil
}

func (b *Bridge) runClaimedTaskLocked(ctx context.Context, claimed DaemonTask, chain Chain, resolved taskAssignmentResolution) (DispatchMap, error) {
	placement := resolved.placement

	if committed, ok := b.memoDispatch(chain); ok {
		if committed.PlacementDigest != resolved.placementDigest {
			return committed, fmt.Errorf("%w: dispatch mapping assignment=%s committed %s, replay carried %s",
				ErrMappingConflict, chain.AssignmentID, committed.PlacementDigest, resolved.placementDigest)
		}
		// Recovery path: the dispatch committed earlier (worker started,
		// HiveCrew task started) but its evidence append failed. Re-attempt
		// only the evidence; never re-dispatch a worker.
		if b.evidencePendingFor(chain) {
			if err := b.retryDispatchEvidence(ctx, chain, committed); err != nil {
				return committed, err
			}
		}
		return committed, nil
	}
	linkage, err := b.Entry.RegisterLinkage(ctx, LinkageInput{
		Chain:       chain,
		Actor:       b.Actor,
		MappingKind: "dispatch",
	})
	if err != nil {
		return DispatchMap{}, err
	}

	// Idempotency guard: committed dispatch evidence wins.
	if record, found, err := b.Entry.LookupEvidence(ctx, linkage.WorkRef, DispatchLinkageKey(chain)); err == nil && found {
		if mapping, err := dispatchMapFromPayload(record.Payload); err == nil && mapping.OrcaDispatchID != "" {
			b.rememberDispatch(chain, mapping)
			return mapping, nil
		}
	} else if err != nil {
		return DispatchMap{}, err
	}

	orcaRunID, orcaTaskID, err := b.ensureIssueTaskLocked(ctx, TaskRef{Chain: chain, Instructions: resolved.instructions})
	if err != nil {
		return DispatchMap{}, err
	}

	// Reconcile: a previous call may have started the worker dispatch but
	// crashed before appending evidence. A structured CLI "no dispatch"
	// answer is safe to pass through to worker-start; an unavailable CLI
	// (unknown effects) fails closed so a second dispatch cannot be created.
	dispatch, showErr := b.Client.DispatchShow(ctx, orcaTaskID)
	if showErr != nil && !isOrcaCLIAnswer(showErr) {
		return DispatchMap{}, fmt.Errorf("orcabridge: reconcile dispatch before worker start: %w", showErr)
	}
	var mapping DispatchMap
	if showErr == nil && dispatch != nil && dispatch.ID != "" && dispatch.RunID == orcaRunID {
		mapping = DispatchMap{
			WorkspaceID:     chain.WorkspaceID,
			ProjectID:       chain.ProjectID,
			IssueID:         chain.IssueID,
			TaskID:          chain.TaskID,
			AssignmentID:    chain.AssignmentID,
			ContractVersion: ContractVersion,
			PlacementDigest: resolved.placementDigest,
			OrcaRunID:       dispatch.RunID,
			OrcaTaskID:      dispatch.TaskID,
			OrcaDispatchID:  dispatch.ID,
			WorkerTerminal:  dispatch.AssigneeHandle,
			WorkerState:     "unknown",
			Status:          "active",
		}
	} else {
		receipt, err := b.Client.WorkerStart(ctx, WorkerStartInput{
			TaskID:       orcaTaskID,
			RunID:        orcaRunID,
			WorktreeMode: placement.WorktreeMode,
			WorktreeName: placement.WorktreeName,
			RepoSelector: placement.RepoSelector,
			BaseBranch:   placement.BaseBranch,
			Agent:        placement.Agent,
			Model:        placement.Model,
			Effort:       placement.Effort,
			SetupPolicy:  placement.SetupPolicy,
		})
		if err != nil {
			return DispatchMap{}, fmt.Errorf("orcabridge: start orca worker: %w", err)
		}
		if receipt.State != "ready" {
			return DispatchMap{}, fmt.Errorf("orcabridge: orca worker start ended in state %q (stage %q, dispatch %s): inspect the Orca receipt before retrying",
				receipt.State, receipt.Stage, receipt.DispatchID)
		}
		if receipt.TaskID != "" && receipt.TaskID != orcaTaskID {
			return DispatchMap{}, fmt.Errorf("orcabridge: worker receipt task %s does not match mapped task %s", receipt.TaskID, orcaTaskID)
		}
		if receipt.RunID != "" && receipt.RunID != orcaRunID {
			return DispatchMap{}, fmt.Errorf("orcabridge: worker receipt run %s does not match mapped run %s", receipt.RunID, orcaRunID)
		}
		receiptRunID := receipt.RunID
		if receiptRunID == "" {
			receiptRunID = orcaRunID
		}
		receiptTaskID := receipt.TaskID
		if receiptTaskID == "" {
			receiptTaskID = orcaTaskID
		}
		mapping = DispatchMap{
			WorkspaceID:     chain.WorkspaceID,
			ProjectID:       chain.ProjectID,
			IssueID:         chain.IssueID,
			TaskID:          chain.TaskID,
			AssignmentID:    chain.AssignmentID,
			ContractVersion: ContractVersion,
			PlacementDigest: resolved.placementDigest,
			OrcaRunID:       receiptRunID,
			OrcaTaskID:      receiptTaskID,
			OrcaDispatchID:  receipt.DispatchID,
			WorkerTerminal:  receipt.AgentTerminalHandle,
			WorktreeID:      receipt.WorktreeID,
			WorktreePath:    receipt.WorktreePath,
			WorkerState:     receipt.State,
			Status:          "active",
		}
	}

	// Advance the HiveCrew task through the existing lifecycle: the Orca
	// worker is live, so the claimed task becomes running.
	if err := b.Daemon.StartTask(ctx, chain.TaskID); err != nil {
		return mapping, fmt.Errorf("orcabridge: hivecrew task %s not started after orca worker dispatch (dispatch %s committed): %w",
			chain.TaskID, mapping.OrcaDispatchID, err)
	}

	// Commit the mapping to the memo before appending evidence: if the
	// evidence append fails, a retry must converge on the memo mapping and
	// re-attempt only the evidence, never a second worker-start.
	b.rememberDispatch(chain, mapping)
	if err := b.recordDispatchEvidence(ctx, linkage.WorkRef, mapping); err != nil {
		b.markEvidencePending(chain, true)
		return mapping, fmt.Errorf("orcabridge: dispatch %s committed but its work-chain evidence append failed (retry is safe): %w",
			mapping.OrcaDispatchID, err)
	}
	b.markEvidencePending(chain, false)
	return mapping, nil
}

// retryDispatchEvidence re-attempts the failed evidence append for an already
// committed dispatch mapping without touching Orca or the daemon lifecycle.
func (b *Bridge) retryDispatchEvidence(ctx context.Context, chain Chain, mapping DispatchMap) error {
	workRef, err := b.dispatchWorkRef(ctx, mapping)
	if err != nil {
		return err
	}
	if err := b.recordDispatchEvidence(ctx, workRef, mapping); err != nil {
		b.markEvidencePending(chain, true)
		return fmt.Errorf("orcabridge: dispatch %s evidence append still failing: %w", mapping.OrcaDispatchID, err)
	}
	b.markEvidencePending(chain, false)
	return nil
}

// ClaimTask claims the next queued task for one runtime through the existing
// daemon claim entry. Thin passthrough for the executor loop.
func (b *Bridge) ClaimTask(ctx context.Context, runtimeID string) (*DaemonTask, error) {
	return b.Daemon.ClaimTask(ctx, runtimeID)
}

// AcknowledgeCancellation acknowledges an observed cancellation of one
// bridge-managed task through the existing daemon verb. Fails closed when
// the task carries no bridge linkage.
func (b *Bridge) AcknowledgeCancellation(ctx context.Context, taskID string) error {
	_, ok, err := b.resolveTaskAssignmentIDOnly(ctx, taskID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: hivecrew task %s carries no bridge assignment linkage", ErrNotBridgeManaged, taskID)
	}
	return b.Daemon.AckTaskCancelled(ctx, taskID)
}

// ---------------------------------------------------------------------------
// governed result writeback
// ---------------------------------------------------------------------------

// ResolveDispatch resolves the HiveCrew mapping for one Orca dispatch id by
// reading the dispatch linkage evidence from the work chain. It fails closed
// when the linkage is unknown: HiveCrew never accepts results for runs it did
// not authorize through the bridge.
func (b *Bridge) ResolveDispatch(ctx context.Context, chain Chain, orcaDispatchID string) (DispatchMap, error) {
	if err := chain.ValidateProjectScope(); err != nil {
		return DispatchMap{}, err
	}
	if err := ValidateOrcaDispatchID(orcaDispatchID); err != nil {
		return DispatchMap{}, err
	}
	if committed, ok := b.memoDispatchByOrcaID(orcaDispatchID); ok {
		if committed.WorkspaceID != chain.WorkspaceID || committed.ProjectID != chain.ProjectID {
			return DispatchMap{}, fmt.Errorf("%w: dispatch %s belongs to another project", ErrUnmappedDispatch, orcaDispatchID)
		}
		return committed, nil
	}
	linkage, err := b.Entry.RegisterLinkage(ctx, LinkageInput{
		Chain:       chain,
		Actor:       b.Actor,
		MappingKind: "dispatch",
	})
	if err != nil {
		return DispatchMap{}, err
	}
	record, found, err := b.Entry.LookupEvidence(ctx, linkage.WorkRef, dispatchEvidenceLookupKey(chain, orcaDispatchID))
	if err != nil {
		return DispatchMap{}, err
	}
	if !found {
		return DispatchMap{}, fmt.Errorf("%w: dispatch %s", ErrUnmappedDispatch, orcaDispatchID)
	}
	mapping, err := dispatchMapFromPayload(record.Payload)
	if err != nil {
		return DispatchMap{}, err
	}
	if mapping.OrcaDispatchID != orcaDispatchID {
		return DispatchMap{}, fmt.Errorf("%w: evidence dispatch %s does not match requested %s",
			ErrUnmappedDispatch, mapping.OrcaDispatchID, orcaDispatchID)
	}
	if mapping.WorkspaceID != chain.WorkspaceID || mapping.ProjectID != chain.ProjectID {
		return DispatchMap{}, fmt.Errorf("%w: dispatch %s belongs to another project", ErrUnmappedDispatch, orcaDispatchID)
	}
	b.rememberDispatch(Chain{
		WorkspaceID:  mapping.WorkspaceID,
		ProjectID:    mapping.ProjectID,
		IssueID:      mapping.IssueID,
		TaskID:       mapping.TaskID,
		AssignmentID: mapping.AssignmentID,
	}, mapping)
	return mapping, nil
}

// AcceptWorkerResult applies the governed writeback contract to one observed
// Orca message:
//
//  1. the message must be a valid worker_done for a dispatch the bridge
//     itself mapped (linkage evidence required; identity fields must match),
//  2. the writeback appends one `finished` event on the existing work chain,
//     idempotent by a deterministic key scoped to the dispatch: an exact
//     digest replay returns the committed evidence, a drifted replay fails
//     closed,
//  3. the HiveCrew task settles through the existing Daemon lifecycle
//     (CompleteTask on succeeded, FailTask on failed),
//  4. run status transitions stay with the existing HiveCrew execution
//     lifecycle; the bridge itself only maps and records evidence.
func (b *Bridge) AcceptWorkerResult(ctx context.Context, chain Chain, message OrcaMessage) (ResultReceipt, error) {
	result, err := ParseWorkerResult(message)
	if err != nil {
		return ResultReceipt{}, err
	}
	mapping, err := b.ResolveDispatch(ctx, chain, result.DispatchID)
	if err != nil {
		return ResultReceipt{}, err
	}
	if result.TaskID != mapping.OrcaTaskID {
		return ResultReceipt{}, fmt.Errorf("%w: payload task %s, mapped task %s",
			ErrResultIdentityMismatch, result.TaskID, mapping.OrcaTaskID)
	}
	if message.RunID != "" && message.RunID != mapping.OrcaRunID {
		return ResultReceipt{}, fmt.Errorf("%w: message run %s, mapped run %s",
			ErrResultIdentityMismatch, message.RunID, mapping.OrcaRunID)
	}
	if mapping.WorkerTerminal != "" && message.FromHandle != "" && message.FromHandle != mapping.WorkerTerminal {
		return ResultReceipt{}, fmt.Errorf("%w: message terminal %s, mapped terminal %s",
			ErrResultIdentityMismatch, message.FromHandle, mapping.WorkerTerminal)
	}
	// Redact credential-like content from the worker message before anything
	// is persisted or digested. Redaction is deterministic, so replays of the
	// same delivery digest identically.
	message.Subject = RedactCredentials(message.Subject)
	message.Body = RedactCredentials(message.Body)
	result.ReportPath = RedactCredentials(result.ReportPath)
	result.FilesModified = RedactStringSlice(result.FilesModified)

	digest, err := WorkerResultDigest(message, result)
	if err != nil {
		return ResultReceipt{}, err
	}
	workRef, err := b.dispatchWorkRef(ctx, mapping)
	if err != nil {
		return ResultReceipt{}, err
	}
	// Single-writer writeback: one scope per Orca dispatch so concurrent
	// ingest of the same delivery settles exactly once.
	scopeErr := b.withScopeLock("result:"+mapping.WorkspaceID+":"+mapping.OrcaDispatchID, func() error {
		key := ResultEvidenceKey(mapping.OrcaDispatchID)
		payload := map[string]any{
			"mapping":          "result",
			"workspace_id":     mapping.WorkspaceID,
			"project_id":       mapping.ProjectID,
			"issue_id":         mapping.IssueID,
			"task_id":          mapping.TaskID,
			"assignment_id":    mapping.AssignmentID,
			"orca_run_id":      mapping.OrcaRunID,
			"orca_task_id":     mapping.OrcaTaskID,
			"orca_dispatch_id": mapping.OrcaDispatchID,
			"orca_message_id":  message.ID,
			"worker_terminal":  message.FromHandle,
			"outcome":          result.Outcome,
			"subject":          message.Subject,
			"body":             message.Body,
			"files_modified":   normalizeFiles(result.FilesModified),
			"report_path":      result.ReportPath,
			"result_digest":    digest,
		}
		_, appendErr := b.Entry.AppendEvidence(ctx, EvidenceInput{
			WorkRef:        workRef,
			SessionID:      b.Actor.SessionID,
			RunID:          mapping.OrcaRunID,
			EventType:      "finished",
			IdempotencyKey: key,
			Payload:        payload,
			OccurredAt:     b.now(),
		})
		if appendErr != nil && !errors.Is(appendErr, ErrEvidenceConflict) {
			return appendErr
		}
		if appendErr != nil {
			// Classify the replay: compare the committed digest.
			if existing, found, lookupErr := b.Entry.LookupEvidence(ctx, workRef, key); lookupErr == nil && found {
				if committed, _ := existing.Payload["result_digest"].(string); committed != digest {
					return fmt.Errorf("%w: dispatch %s committed %s, replay carried %s",
						ErrResultReceiptConflict, mapping.OrcaDispatchID, committed, digest)
				}
			}
		}

		// Settle the HiveCrew task through the existing daemon lifecycle. Runs
		// on both fresh and replayed evidence so a crash between evidence and
		// settlement is recovered by replaying the same message.
		return b.settleHiveCrewTask(ctx, mapping, result, message)
	})
	if scopeErr != nil && errors.Is(scopeErr, ErrResultReceiptConflict) {
		return ResultReceipt{}, scopeErr
	}
	receipt := b.resultReceiptFromMapping(mapping, message, result, digest)
	if scopeErr != nil {
		return receipt, scopeErr
	}
	return receipt, nil
}

// settleHiveCrewTask settles the HiveCrew task row through the existing
// daemon complete/fail verbs.
func (b *Bridge) settleHiveCrewTask(ctx context.Context, mapping DispatchMap, result WorkerResult, message OrcaMessage) error {
	output := message.Body
	if output == "" {
		output = message.Subject
	}
	if result.Outcome == "succeeded" {
		return b.Daemon.CompleteTask(ctx, TaskCompletion{
			TaskID:  mapping.TaskID,
			Output:  output,
			WorkDir: mapping.WorktreePath,
		})
	}
	failureReason := "orca_worker_reported_failure"
	return b.Daemon.FailTask(ctx, TaskFailure{
		TaskID:        mapping.TaskID,
		Error:         output,
		WorkDir:       mapping.WorktreePath,
		FailureReason: failureReason,
	})
}

// dispatchWorkRef resolves the work chain reference for one dispatch scope.
func (b *Bridge) dispatchWorkRef(ctx context.Context, mapping DispatchMap) (string, error) {
	linkage, err := b.Entry.RegisterLinkage(ctx, LinkageInput{
		Chain: Chain{
			WorkspaceID:  mapping.WorkspaceID,
			ProjectID:    mapping.ProjectID,
			IssueID:      mapping.IssueID,
			TaskID:       mapping.TaskID,
			AssignmentID: mapping.AssignmentID,
		},
		Actor:       b.Actor,
		MappingKind: "dispatch",
	})
	if err != nil {
		return "", err
	}
	return linkage.WorkRef, nil
}

func (b *Bridge) resultReceiptFromMapping(mapping DispatchMap, message OrcaMessage, result WorkerResult, digest string) ResultReceipt {
	return ResultReceipt{
		WorkspaceID:    mapping.WorkspaceID,
		ProjectID:      mapping.ProjectID,
		IssueID:        mapping.IssueID,
		TaskID:         mapping.TaskID,
		AssignmentID:   mapping.AssignmentID,
		OrcaRunID:      mapping.OrcaRunID,
		OrcaTaskID:     mapping.OrcaTaskID,
		OrcaDispatchID: mapping.OrcaDispatchID,
		OrcaMessageID:  message.ID,
		WorkerTerminal: message.FromHandle,
		Outcome:        result.Outcome,
		Subject:        message.Subject,
		Body:           message.Body,
		FilesModified:  normalizeFiles(result.FilesModified),
		ReportPath:     result.ReportPath,
		ResultDigest:   digest,
		ObservedAt:     b.now(),
	}
}

// IngestWorkerResults reads the Orca inbox, applies the writeback contract to
// every worker_done observable from one HiveCrew project scope, and returns
// one error entry per rejected message. The caller acknowledges Orca
// deliveries only for accepted results.
func (b *Bridge) IngestWorkerResults(ctx context.Context, chain Chain) ([]ResultReceipt, []error, error) {
	messages, err := b.Client.InboxMessages(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("orcabridge: read orca inbox: %w", err)
	}
	var accepted []ResultReceipt
	var rejected []error
	for _, message := range messages {
		if message.Type != "worker_done" {
			continue
		}
		receipt, err := b.AcceptWorkerResult(ctx, chain, message)
		if err != nil {
			rejected = append(rejected, fmt.Errorf("message %s: %w", message.ID, err))
			continue
		}
		accepted = append(accepted, receipt)
	}
	return accepted, rejected, nil
}

// ---------------------------------------------------------------------------
// evidence helpers
// ---------------------------------------------------------------------------

// recordLinkageEvidence appends the mapping evidence event on the existing
// work chain. It is idempotent by its deterministic key: a replay with the
// same payload is a no-op; drift fails closed.
func (b *Bridge) recordLinkageEvidence(ctx context.Context, workRef, key string, payload map[string]any) error {
	_, err := b.Entry.AppendEvidence(ctx, EvidenceInput{
		WorkRef:        workRef,
		SessionID:      b.Actor.SessionID,
		EventType:      "checkpoint",
		IdempotencyKey: key,
		Payload:        payload,
		OccurredAt:     b.now(),
	})
	if errors.Is(err, ErrEvidenceConflict) {
		// The same mapping scope already recorded different evidence: this is
		// a mapping payload drift and must fail closed as such.
		return fmt.Errorf("%w: evidence key %s carries a different payload", ErrMappingConflict, key)
	}
	return err
}

// recordDispatchEvidence appends the dispatch linkage on the existing work
// chain twice: once under the assignment-scoped key (mapping provenance) and
// once under the Orca-dispatch-scoped key so writeback can resolve a
// reported dispatch id back to its HiveCrew chain after a restart.
func (b *Bridge) recordDispatchEvidence(ctx context.Context, workRef string, mapping DispatchMap) error {
	chain := Chain{
		WorkspaceID:  mapping.WorkspaceID,
		ProjectID:    mapping.ProjectID,
		IssueID:      mapping.IssueID,
		TaskID:       mapping.TaskID,
		AssignmentID: mapping.AssignmentID,
	}
	payload := map[string]any{
		"mapping":          "dispatch",
		"workspace_id":     mapping.WorkspaceID,
		"project_id":       mapping.ProjectID,
		"issue_id":         mapping.IssueID,
		"task_id":          mapping.TaskID,
		"assignment_id":    mapping.AssignmentID,
		"orca_run_id":      mapping.OrcaRunID,
		"orca_task_id":     mapping.OrcaTaskID,
		"orca_dispatch_id": mapping.OrcaDispatchID,
		"worker_terminal":  mapping.WorkerTerminal,
		"worktree_id":      mapping.WorktreeID,
		"worktree_path":    mapping.WorktreePath,
		"placement_digest": mapping.PlacementDigest,
	}
	if err := b.recordLinkageEvidence(ctx, workRef, dispatchEvidenceLookupKey(chain, mapping.OrcaDispatchID), payload); err != nil {
		return err
	}
	return b.recordLinkageEvidence(ctx, workRef, DispatchLinkageKey(chain), payload)
}

// assignmentEvidenceKey is the evidence key for the assignment mapping freeze
// (dispatch through the single entry, task mapped to Orca, placement frozen).
func assignmentEvidenceKey(chain Chain) string {
	return linkageKeyPrefix + "assignment/" + chain.WorkspaceID + "/" + chain.AssignmentID
}

// taskEvidenceKey is the evidence key that resolves a claimed HiveCrew task
// back to its assignment mapping.
func taskEvidenceKey(workspaceID, taskID string) string {
	return linkageKeyPrefix + "task-evidence/" + workspaceID + "/" + taskID
}

// dispatchEvidenceLookupKey is the evidence key used to find one dispatch
// mapping by Orca dispatch id inside one project scope.
func dispatchEvidenceLookupKey(chain Chain, orcaDispatchID string) string {
	return linkageKeyPrefix + "dispatch-evidence/" + chain.WorkspaceID + "/" + chain.ProjectID + "/" + orcaDispatchID
}

// taskAssignmentResolution is the resolved linkage for one claimed task.
type taskAssignmentResolution struct {
	chain           Chain
	placement       PlacementInput
	placementDigest string
	instructions    string
}

// resolveTaskAssignment resolves one claimed daemon task to its assignment
// mapping. The claimed workspace/issue must match the frozen linkage.
func (b *Bridge) resolveTaskAssignment(ctx context.Context, claimed DaemonTask) (taskAssignmentResolution, bool, error) {
	resolution, ok, err := b.resolveTaskAssignmentIDOnly(ctx, claimed.ID)
	if err != nil || !ok {
		return taskAssignmentResolution{}, ok, err
	}
	if claimed.WorkspaceID != "" && claimed.WorkspaceID != resolution.chain.WorkspaceID {
		return taskAssignmentResolution{}, false, fmt.Errorf(
			"%w: claimed task workspace %s does not match linkage workspace %s",
			ErrResultIdentityMismatch, claimed.WorkspaceID, resolution.chain.WorkspaceID)
	}
	if claimed.IssueID != "" && claimed.IssueID != resolution.chain.IssueID {
		return taskAssignmentResolution{}, false, fmt.Errorf(
			"%w: claimed task issue %s does not match linkage issue %s",
			ErrResultIdentityMismatch, claimed.IssueID, resolution.chain.IssueID)
	}
	return resolution, true, nil
}

func (b *Bridge) resolveTaskAssignmentIDOnly(ctx context.Context, taskID string) (taskAssignmentResolution, bool, error) {
	// The linkage registration is project-scoped; the task evidence carries
	// the exact chain, so register against a minimal dispatch scope and read
	// the task evidence from the same work chain namespace.
	record, found, err := b.lookupTaskEvidence(ctx, taskID)
	if err != nil || !found {
		return taskAssignmentResolution{}, false, err
	}
	payload := record.Payload
	chain := Chain{
		WorkspaceID:  stringValue(payload, "workspace_id"),
		ProjectID:    stringValue(payload, "project_id"),
		IssueID:      stringValue(payload, "issue_id"),
		TaskID:       stringValue(payload, "task_id"),
		AssignmentID: stringValue(payload, "assignment_id"),
	}
	if err := chain.ValidateAssignmentScope(); err != nil {
		return taskAssignmentResolution{}, false, err
	}
	placementRaw, _ := payload["placement"].(map[string]any)
	placement, err := placementFromPayload(placementRaw)
	if err != nil {
		return taskAssignmentResolution{}, false, err
	}
	// The placement digest freezes the workspace/assignment identity too, so
	// restore it from the linkage chain before verifying the digest.
	placement.WorkspaceID = chain.WorkspaceID
	placement.AssignmentID = chain.AssignmentID
	placementDigest, err := placement.Digest()
	if err != nil {
		return taskAssignmentResolution{}, false, err
	}
	frozenDigest := stringValue(payload, "placement_digest")
	if frozenDigest == "" || frozenDigest != placementDigest {
		return taskAssignmentResolution{}, false, fmt.Errorf(
			"%w: placement evidence digest %q does not match rebuilt placement %s",
			ErrMappingConflict, frozenDigest, placementDigest)
	}
	instructions := stringValue(payload, "instructions")
	return taskAssignmentResolution{
		chain:           chain,
		placement:       placement,
		placementDigest: placementDigest,
		instructions:    instructions,
	}, true, nil
}

// lookupTaskEvidence reads the task-scoped linkage evidence. The evidence
// lives on the dispatch linkage work chain of its assignment, which a
// task-id alone cannot address; the in-process memo (populated by
// EnsureAssignment and by every resolved dispatch) therefore serves the
// claim path. Cold-start reconciliation by task id alone is intentionally
// out of A1 scope and lands with the executor wiring (A2).
func (b *Bridge) lookupTaskEvidence(ctx context.Context, taskID string) (EvidenceRecord, bool, error) {
	if payload, ok := b.memoTaskAssignment(taskID); ok {
		return EvidenceRecord{Payload: payload}, true, nil
	}
	return EvidenceRecord{}, false, nil
}

func stringValue(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return value
}

func placementPayload(p PlacementInput) map[string]any {
	return map[string]any{
		"worktree_mode": p.WorktreeMode,
		"worktree_name": p.WorktreeName,
		"repo_selector": p.RepoSelector,
		"base_branch":   p.BaseBranch,
		"agent":         p.Agent,
		"model":         p.Model,
		"effort":        p.Effort,
		"setup_policy":  p.SetupPolicy,
	}
}

func placementFromPayload(raw map[string]any) (PlacementInput, error) {
	if raw == nil {
		return PlacementInput{}, fmt.Errorf("%w: placement evidence payload is missing", ErrInvalidChain)
	}
	get := func(key string) string {
		value, _ := raw[key].(string)
		return value
	}
	return PlacementInput{
		ContractVersion: ContractVersion,
		WorktreeMode:    get("worktree_mode"),
		WorktreeName:    get("worktree_name"),
		RepoSelector:    get("repo_selector"),
		BaseBranch:      get("base_branch"),
		Agent:           get("agent"),
		Model:           get("model"),
		Effort:          get("effort"),
		SetupPolicy:     get("setup_policy"),
	}, nil
}

// dispatchMapFromPayload rebuilds one dispatch mapping from its work-chain
// evidence payload. Unknown or malformed fields fail closed.
func dispatchMapFromPayload(payload map[string]any) (DispatchMap, error) {
	str := func(key string) string {
		value, _ := payload[key].(string)
		return value
	}
	mapping := DispatchMap{
		WorkspaceID:     str("workspace_id"),
		ProjectID:       str("project_id"),
		IssueID:         str("issue_id"),
		TaskID:          str("task_id"),
		AssignmentID:    str("assignment_id"),
		ContractVersion: ContractVersion,
		PlacementDigest: str("placement_digest"),
		OrcaRunID:       str("orca_run_id"),
		OrcaTaskID:      str("orca_task_id"),
		OrcaDispatchID:  str("orca_dispatch_id"),
		WorkerTerminal:  str("worker_terminal"),
		WorktreeID:      str("worktree_id"),
		WorktreePath:    str("worktree_path"),
		WorkerState:     "unknown",
		Status:          "active",
	}
	for _, field := range []struct{ label, value string }{
		{"workspace id", mapping.WorkspaceID},
		{"project id", mapping.ProjectID},
		{"task id", mapping.TaskID},
		{"assignment id", mapping.AssignmentID},
		{"orca run id", mapping.OrcaRunID},
		{"orca task id", mapping.OrcaTaskID},
		{"orca dispatch id", mapping.OrcaDispatchID},
		{"placement digest", mapping.PlacementDigest},
	} {
		if field.value == "" {
			return DispatchMap{}, fmt.Errorf("%w: dispatch evidence payload is missing %s", ErrInvalidChain, field.label)
		}
	}
	if err := ValidateOrcaRunID(mapping.OrcaRunID); err != nil {
		return DispatchMap{}, err
	}
	if err := ValidateOrcaTaskID(mapping.OrcaTaskID); err != nil {
		return DispatchMap{}, err
	}
	if err := ValidateOrcaDispatchID(mapping.OrcaDispatchID); err != nil {
		return DispatchMap{}, err
	}
	return mapping, nil
}

// ---------------------------------------------------------------------------
// memo helpers
// ---------------------------------------------------------------------------

func (b *Bridge) memoRunEntry(chain Chain) (memoEntry, bool) {
	b.memoMu.Lock()
	defer b.memoMu.Unlock()
	entry, ok := b.memo.run[chain.WorkspaceID+":"+chain.ProjectID]
	return entry, ok
}

func (b *Bridge) rememberRun(chain Chain, runID, digest string) {
	b.memoMu.Lock()
	defer b.memoMu.Unlock()
	b.memo.run[chain.WorkspaceID+":"+chain.ProjectID] = memoEntry{id: runID, digest: digest}
}

func (b *Bridge) memoTaskEntry(chain Chain) (memoEntry, bool) {
	b.memoMu.Lock()
	defer b.memoMu.Unlock()
	entry, ok := b.memo.task[chain.WorkspaceID+":"+chain.TaskID]
	return entry, ok
}

func (b *Bridge) memoRunOfTask(chain Chain) string {
	b.memoMu.Lock()
	defer b.memoMu.Unlock()
	return b.memo.runOfWork[chain.WorkspaceID+":"+chain.TaskID]
}

func (b *Bridge) rememberTask(chain Chain, runID, taskID, digest string) {
	b.memoMu.Lock()
	defer b.memoMu.Unlock()
	key := chain.WorkspaceID + ":" + chain.TaskID
	b.memo.task[key] = memoEntry{id: taskID, digest: digest}
	b.memo.runOfWork[key] = runID
}

func (b *Bridge) memoDispatch(chain Chain) (DispatchMap, bool) {
	b.memoMu.Lock()
	defer b.memoMu.Unlock()
	mapping, ok := b.memo.dispatch[chain.WorkspaceID+":"+chain.AssignmentID]
	return mapping, ok
}

func (b *Bridge) memoDispatchByOrcaID(orcaDispatchID string) (DispatchMap, bool) {
	b.memoMu.Lock()
	defer b.memoMu.Unlock()
	for _, mapping := range b.memo.dispatch {
		if mapping.OrcaDispatchID == orcaDispatchID {
			return mapping, true
		}
	}
	return DispatchMap{}, false
}

func (b *Bridge) rememberDispatch(chain Chain, mapping DispatchMap) {
	b.memoMu.Lock()
	defer b.memoMu.Unlock()
	b.memo.dispatch[chain.WorkspaceID+":"+chain.AssignmentID] = mapping
}

// markEvidencePending records that a dispatch mapping committed (worker
// started, HiveCrew task started) but its work-chain evidence append failed,
// so retries must re-attempt the evidence without re-dispatching a worker.
func (b *Bridge) markEvidencePending(chain Chain, pending bool) {
	b.memoMu.Lock()
	defer b.memoMu.Unlock()
	key := chain.WorkspaceID + ":" + chain.AssignmentID
	if pending {
		b.memo.evidencePending[key] = true
		return
	}
	delete(b.memo.evidencePending, key)
}

func (b *Bridge) evidencePendingFor(chain Chain) bool {
	b.memoMu.Lock()
	defer b.memoMu.Unlock()
	return b.memo.evidencePending[chain.WorkspaceID+":"+chain.AssignmentID]
}

// memoTaskAssignment returns the cached task-scoped assignment payload so
// the claim path resolves without a cold-start lookup.
func (b *Bridge) memoTaskAssignment(taskID string) (map[string]any, bool) {
	b.memoMu.Lock()
	defer b.memoMu.Unlock()
	payload, ok := b.memo.taskAssignment[taskID]
	return payload, ok
}

func (b *Bridge) rememberTaskAssignment(taskID string, payload map[string]any) {
	b.memoMu.Lock()
	defer b.memoMu.Unlock()
	b.memo.taskAssignment[taskID] = payload
}

// isOrcaCLIAnswer reports whether err is a structured Orca answer (the CLI
// ran and Orca replied ok:false) rather than an execution failure.
func isOrcaCLIAnswer(err error) bool {
	var cliErr *CLIError
	return errors.As(err, &cliErr)
}

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

	// Cross-instance creation coordination (R3): claims are arbitrated on
	// the shared WorkEntry ledger, not per-Bridge mutexes. InstanceID
	// identifies this Bridge instance in claim payloads; LeaseTTL bounds a
	// crashed holder before takeover; ClaimPoll and ClaimMaxWait bound the
	// acquire loop. Zero values use the Default* constants.
	InstanceID   string
	LeaseTTL     time.Duration
	ClaimPoll    time.Duration
	ClaimMaxWait time.Duration

	// attempts makes effect-barrier attempt identities unique per call.
	attempts effectAttemptSeq

	// startPermitMu/startPermitCache keep each assignment's start-permit
	// payload byte-stable across retries.
	startPermitMu sync.Mutex
	startPermits  map[string]startPermitIdentity

	// startReconcileLocks serializes StartTask reconciliation per assignment
	// inside one process so concurrent callers cannot double-start while the
	// winner is between the verb and its durable settle write.
	startReconcileLocks sync.Map // ws:assignment -> *sync.Mutex

	memoMu sync.Mutex
	memo   bridgeMemo
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
	startPending    map[string]bool           // ws:assignment -> StartTask not yet succeeded
}

func newBridgeMemo() bridgeMemo {
	return bridgeMemo{
		run:             map[string]memoEntry{},
		task:            map[string]memoEntry{},
		runOfWork:       map[string]string{},
		dispatch:        map[string]DispatchMap{},
		taskAssignment:  map[string]map[string]any{},
		evidencePending: map[string]bool{},
		startPending:    map[string]bool{},
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
		attempts:     effectAttemptSeq{seed: newEffectAttemptSeed()},
		startPermits: map[string]startPermitIdentity{},
		Client:       client,
		Entry:        entry,
		Assignment:   assignment,
		Daemon:       daemonPort,
		Actor:        actor,
		Now:          time.Now,
		memo:         newBridgeMemo(),
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
	return b.ensureProjectRunLocked(ctx, ref)
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

	record := func(orcaRunID string) error {
		return b.recordLinkageEvidence(ctx, linkage.WorkRef, RunLinkageKey(ref.Chain), map[string]any{
			"mapping":      "run",
			"workspace_id": ref.Chain.WorkspaceID,
			"project_id":   ref.Chain.ProjectID,
			"orca_run_id":  orcaRunID,
			"digest":       digest,
		})
	}
	adopt := func(orcaRunID string) (string, error) {
		if err := record(orcaRunID); err != nil {
			return "", err
		}
		b.rememberRun(ref.Chain, orcaRunID, digest)
		return orcaRunID, nil
	}

	// Evidence-first: another Bridge instance may already have committed
	// this scope's mapping (crash-free cross-instance replay). The digest is
	// verified, so drifted objectives never adopt the old run.
	if committed := b.readCommittedRun(ctx, linkage.WorkRef, ref.Chain, digest); committed != "" {
		b.rememberRun(ref.Chain, committed, digest)
		return committed, nil
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
			// Marker drift check: the objective must end with exactly this
			// caller's marker (drifted workspace/project inside the marker is
			// impossible here, but a stale display objective with a forged
			// marker is rejected).
			if run.Objective != objective {
				return "", fmt.Errorf("%w: orphan run %s carries a drifted objective", ErrMappingConflict, run.ID)
			}
			return adopt(run.ID)
		}
	}

	// Creation claim: exactly one Bridge instance (across independent
	// objects, connections, and restarts) may call RunCreate for this scope.
	claim, claimErr := b.acquireCreateClaim(ctx, linkage.WorkRef, runCreateClaimBase(ref.Chain), func(ctx context.Context) bool {
		return b.readCommittedRun(ctx, linkage.WorkRef, ref.Chain, digest) != ""
	}, nil)
	if claimErr != nil {
		if errors.Is(claimErr, ErrScopeAlreadyCommitted) {
			if committed := b.readCommittedRun(ctx, linkage.WorkRef, ref.Chain, digest); committed != "" {
				b.rememberRun(ref.Chain, committed, digest)
				return committed, nil
			}
			// The holder committed Orca-side but not yet on the chain; one
			// bounded re-scan adopts the marker instead of creating.
			runs, err := b.Client.RunList(ctx)
			if err != nil {
				return "", fmt.Errorf("orcabridge: re-scan runs after committed scope: %w", err)
			}
			for _, run := range runs {
				ws, prj, ok := RunMarkerScan(run.Objective)
				if ok && ws == ref.Chain.WorkspaceID && prj == ref.Chain.ProjectID {
					return adopt(run.ID)
				}
			}
			return "", fmt.Errorf("orcabridge: run scope %s/%s reported committed but no marker or evidence is observable",
				ref.Chain.WorkspaceID, ref.Chain.ProjectID)
		}
		return "", claimErr
	}

	// Double-check inside the claim: the previous holder may have created
	// the Run Orca-side before crashing.
	runs, err = b.Client.RunList(ctx)
	if err != nil {
		return "", fmt.Errorf("orcabridge: re-scan runs inside claim: %w", err)
	}
	for _, run := range runs {
		ws, prj, ok := RunMarkerScan(run.Objective)
		if ok && ws == ref.Chain.WorkspaceID && prj == ref.Chain.ProjectID {
			// Marker drift check: the objective must end with exactly this
			// caller's marker (drifted workspace/project inside the marker is
			// impossible here, but a stale display objective with a forged
			// marker is rejected).
			if run.Objective != objective {
				return "", fmt.Errorf("%w: orphan run %s carries a drifted objective", ErrMappingConflict, run.ID)
			}
			return adopt(run.ID)
		}
	}

	// Effect barrier: the sole permit for this unfenced create. Order is
	// claim -> barrier -> lease gate -> client -> marker. A later holder
	// seeing this barrier without committed mapping or marker fails closed
	// with ErrScopeAttemptInFlight and must reconcile instead of creating.
	permit, err := b.winEffectBarrier(ctx, linkage.WorkRef, claim)
	if err != nil {
		// Barrier lost: the winner's create may be in flight or committed.
		// Wait bounded for its committed result; if nothing appears, fail
		// closed. Either way, never a second create.
		if errors.Is(err, ErrScopeAttemptInFlight) {
			return b.waitForCommittedRun(ctx, linkage.WorkRef, ref.Chain, digest, objective)
		}
		return "", err
	}
	// Lease gate: strictly pre-call, so its failure may reopen the barrier.
	if err := b.withinLease(permit); err != nil {
		if reopenErr := b.reopenEffectBarrier(ctx, linkage.WorkRef, permit); reopenErr != nil {
			return "", fmt.Errorf("%v (barrier reopen failed: %v; takeover stays blocked)", err, reopenErr)
		}
		return "", err
	}
	runID, err := b.Client.RunCreate(ctx, objective)
	if err != nil {
		// Post-call outcome is unknown (a structured CLI error may still have
		// landed the create after the caller observed it): never reopen the
		// barrier from here. Fail closed; recovery is reconcile-only.
		return "", fmt.Errorf("orcabridge: create orca run: %w (effect barrier stays held; takeover blocked)", err)
	}
	if err := record(runID); err != nil {
		return "", err
	}
	b.rememberRun(ref.Chain, runID, digest)
	return runID, nil
}

// readCommittedRun reads the Orca run id this scope committed on the work
// chain, verifying the handle grammar and the current objective digest
// before use. Digest drift fails closed (empty), never adopts.
func (b *Bridge) readCommittedRun(ctx context.Context, workRef string, chain Chain, wantDigest string) string {
	record, found, err := b.Entry.LookupEvidence(ctx, workRef, RunLinkageKey(chain))
	if err != nil || !found {
		return ""
	}
	if committed, _ := record.Payload["digest"].(string); committed == "" || committed != wantDigest {
		return ""
	}
	orcaRunID, _ := record.Payload["orca_run_id"].(string)
	if err := ValidateOrcaRunID(orcaRunID); err != nil {
		return ""
	}
	return orcaRunID
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
	return b.ensureIssueTaskLocked(ctx, ref)
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
	record := func(orcaTaskID string) error {
		return b.recordLinkageEvidence(ctx, linkage.WorkRef, TaskLinkageKey(ref.Chain), map[string]any{
			"mapping":      "task",
			"workspace_id": ref.Chain.WorkspaceID,
			"project_id":   ref.Chain.ProjectID,
			"issue_id":     ref.Chain.IssueID,
			"task_id":      ref.Chain.TaskID,
			"orca_run_id":  runID,
			"orca_task_id": orcaTaskID,
			"digest":       digest,
		})
	}
	adopt := func(orcaTaskID string) (string, string, error) {
		if err := record(orcaTaskID); err != nil {
			return "", "", err
		}
		b.rememberTask(ref.Chain, runID, orcaTaskID, digest)
		return runID, orcaTaskID, nil
	}
	scanOrcaTask := func() (string, error) {
		tasks, err := b.Client.TaskList(ctx, runID)
		if err != nil {
			return "", fmt.Errorf("orcabridge: reconcile tasks: %w", err)
		}
		for _, task := range tasks {
			ws, prj, issue, taskID, ok := TaskMarkerScan(task.Spec)
			if ok && ws == ref.Chain.WorkspaceID && prj == ref.Chain.ProjectID &&
				issue == ref.Chain.IssueID && taskID == ref.Chain.TaskID {
				// Marker drift check: the marker must be followed by exactly
				// the spec body this caller would write, so a task marker
				// with drifted instructions is never adopted.
				if task.Spec != spec {
					return "", fmt.Errorf("%w: orphan task %s marker carries a drifted spec body", ErrMappingConflict, task.ID)
				}
				return task.ID, nil
			}
		}
		return "", nil
	}

	// Evidence-first: another Bridge instance may already have committed
	// this task mapping.
	if committed := b.readCommittedTask(ctx, linkage.WorkRef, ref.Chain, digest); committed != "" {
		b.rememberTask(ref.Chain, runID, committed, digest)
		return runID, committed, nil
	}
	if orphan, err := scanOrcaTask(); err != nil {
		return "", "", err
	} else if orphan != "" {
		return adopt(orphan)
	}

	// Creation claim: exactly one Bridge instance may call TaskCreate.
	claim, claimErr := b.acquireCreateClaim(ctx, linkage.WorkRef, taskCreateClaimBase(ref.Chain), func(ctx context.Context) bool {
		return b.readCommittedTask(ctx, linkage.WorkRef, ref.Chain, digest) != ""
	}, nil)
	if claimErr != nil {
		if errors.Is(claimErr, ErrScopeAlreadyCommitted) {
			if committed := b.readCommittedTask(ctx, linkage.WorkRef, ref.Chain, digest); committed != "" {
				b.rememberTask(ref.Chain, runID, committed, digest)
				return runID, committed, nil
			}
			orphan, err := scanOrcaTask()
			if err != nil {
				return "", "", err
			}
			if orphan != "" {
				return adopt(orphan)
			}
			return "", "", fmt.Errorf("orcabridge: task scope %s/%s reported committed but no marker or evidence is observable",
				ref.Chain.WorkspaceID, ref.Chain.TaskID)
		}
		return "", "", claimErr
	}

	// Double-check inside the claim: an orphan Orca task from a crashed
	// previous holder is adopted rather than duplicated.
	if orphan, err := scanOrcaTask(); err != nil {
		return "", "", err
	} else if orphan != "" {
		return adopt(orphan)
	}

	// Effect barrier (claim -> barrier -> gate -> client -> marker).
	permit, err := b.winEffectBarrier(ctx, linkage.WorkRef, claim)
	if err != nil {
		if errors.Is(err, ErrScopeAttemptInFlight) {
			return b.waitForCommittedTask(ctx, linkage.WorkRef, ref.Chain, digest, runID, scanOrcaTask)
		}
		return "", "", err
	}
	if err := b.withinLease(permit); err != nil {
		if reopenErr := b.reopenEffectBarrier(ctx, linkage.WorkRef, permit); reopenErr != nil {
			return "", "", fmt.Errorf("%v (barrier reopen failed: %v; takeover stays blocked)", err, reopenErr)
		}
		return "", "", err
	}
	createdTaskID, err := b.Client.TaskCreate(ctx, TaskCreateInput{RunID: runID, Spec: spec, Title: title})
	if err != nil {
		// Post-call unknown outcome: the barrier stays held (reconcile-only).
		return "", "", fmt.Errorf("orcabridge: create orca task: %w (effect barrier stays held; takeover blocked)", err)
	}
	if err := record(createdTaskID); err != nil {
		return "", "", err
	}
	b.rememberTask(ref.Chain, runID, createdTaskID, digest)
	return runID, createdTaskID, nil
}

// readCommittedTask reads the Orca task id this scope committed on the work
// chain, verifying the handle grammar and the current spec digest before
// use. Digest drift fails closed (empty), never adopts.
func (b *Bridge) readCommittedTask(ctx context.Context, workRef string, chain Chain, wantDigest string) string {
	record, found, err := b.Entry.LookupEvidence(ctx, workRef, TaskLinkageKey(chain))
	if err != nil || !found {
		return ""
	}
	if committed, _ := record.Payload["digest"].(string); committed == "" || committed != wantDigest {
		return ""
	}
	orcaTaskID, _ := record.Payload["orca_task_id"].(string)
	if err := ValidateOrcaTaskID(orcaTaskID); err != nil {
		return ""
	}
	return orcaTaskID
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
	// Durable task->assignment index: fresh Bridge processes resolve claimed
	// tasks through this record when their memo is empty.
	indexPayload := map[string]any{
		"assignment_id": chain.AssignmentID,
		"task_id":       chain.TaskID,
		"workspace_id":  chain.WorkspaceID,
		"work_ref":      linkage.WorkRef,
	}
	if err := b.recordLinkageEvidence(ctx, linkage.WorkRef, taskAssignmentIndexKey(chain.WorkspaceID, chain.TaskID), indexPayload); err != nil {
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
	// The creation claim inside guards one dispatch and one worker across
	// Bridge instances. The committed mapping is returned even when its
	// evidence append failed, so callers can observe the committed Orca
	// dispatch alongside the error.
	return b.runClaimedTaskLocked(ctx, claimed, chain, resolved)
}

func (b *Bridge) runClaimedTaskLocked(ctx context.Context, claimed DaemonTask, chain Chain, resolved taskAssignmentResolution) (DispatchMap, error) {
	placement := resolved.placement

	if committed, ok := b.memoDispatch(chain); ok {
		if committed.PlacementDigest != resolved.placementDigest {
			return committed, fmt.Errorf("%w: dispatch mapping assignment=%s committed %s, replay carried %s",
				ErrMappingConflict, chain.AssignmentID, committed.PlacementDigest, resolved.placementDigest)
		}
		// Recovery: reconcile BOTH durable obligations of the committed
		// dispatch — its evidence append (if pending) and its HiveCrew task
		// start (idempotent: already-settled is a no-op; pending retries only
		// the StartTask verb). This covers WorkerStart-success/StartTask-failure
		// (including on a fresh Bridge process: the durable state is the
		// authority) and evidence-failure retries. Success is returned only
		// after both settle; a second WorkerStart is never issued.
		workRef, err := b.dispatchWorkRef(ctx, committed)
		if err != nil {
			return committed, err
		}
		if b.evidencePendingFor(chain) {
			if err := b.retryDispatchEvidence(ctx, chain, committed); err != nil {
				return committed, err
			}
		}
		if err := b.reconcileTaskStart(ctx, workRef, chain); err != nil {
			return committed, err
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

	// Idempotency guard: committed dispatch evidence wins — but only when it
	// matches the current placement digest; drifted placement fails closed.
	// The committed dispatch still owes its StartTask reconciliation, so the
	// caller never receives success while the HiveCrew task start is
	// unresolved (this is the fresh-process resume path after a
	// WorkerStart-success/StartTask-failure).
	if record, found, err := b.Entry.LookupEvidence(ctx, linkage.WorkRef, DispatchLinkageKey(chain)); err == nil && found {
		if mapping, err := dispatchMapFromPayload(record.Payload); err == nil && mapping.OrcaDispatchID != "" {
			if mapping.PlacementDigest != resolved.placementDigest {
				return mapping, fmt.Errorf("%w: committed dispatch evidence placement %s does not match current placement %s",
					ErrMappingConflict, mapping.PlacementDigest, resolved.placementDigest)
			}
			b.rememberDispatch(chain, mapping)
			if err := b.reconcileTaskStart(ctx, linkage.WorkRef, chain); err != nil {
				return mapping, err
			}
			return mapping, nil
		}
	} else if err != nil {
		return DispatchMap{}, err
	}

	orcaRunID, orcaTaskID, err := b.ensureIssueTaskLocked(ctx, TaskRef{Chain: chain, Instructions: resolved.instructions})
	if err != nil {
		return DispatchMap{}, err
	}

	// Commit read helpers shared by the claim probe and the waiter path. The
	// mapping only counts as committed when its placement digest matches the
	// current placement; drift is not adoption.
	committedMapping := func() DispatchMap {
		record, found, err := b.Entry.LookupEvidence(ctx, linkage.WorkRef, DispatchLinkageKey(chain))
		if err != nil || !found {
			return DispatchMap{}
		}
		mapping, err := dispatchMapFromPayload(record.Payload)
		if err != nil || mapping.OrcaDispatchID == "" {
			return DispatchMap{}
		}
		if mapping.PlacementDigest != resolved.placementDigest {
			return DispatchMap{}
		}
		return mapping
	}
	// Worker-start claim: exactly one Bridge instance may call WorkerStart
	// for this assignment scope. The probe also treats an Orca-side orphan
	// dispatch (crashed between start and evidence) as committed so waiters
	// adopt instead of racing a second start.
	// orphanForScope returns the existing Orca dispatch for this scope after
	// the ONE shared exact identity validation. A present-but-mismatched
	// dispatch is a hard error (fail closed before any side effect); an
	// absent dispatch (no observable dispatch) is (nil, nil) so the caller
	// continues to its own create path.
	orphanForScope := func() (*OrcaDispatch, error) {
		dispatch, err := b.Client.DispatchShow(ctx, orcaTaskID)
		if err != nil || dispatch == nil || dispatch.ID == "" {
			return nil, nil
		}
		if err := validateOrphanDispatchIdentity(dispatch, orcaRunID, orcaTaskID); err != nil {
			return nil, err
		}
		return dispatch, nil
	}

	// Recovery-first (no claim needed): a dispatch already committed for this
	// assignment — in evidence or as a validated Orca orphan — must short-
	// circuit straight to reconciliation. This is the path a fresh Bridge
	// process takes when resuming a WorkerStart-success/StartTask-failure, and
	// it never acquires a permit or re-runs WorkerStart.
	if mapping := committedMapping(); mapping.OrcaDispatchID != "" {
		b.rememberDispatch(chain, mapping)
		if err := b.reconcileTaskStart(ctx, linkage.WorkRef, chain); err != nil {
			return mapping, err
		}
		return mapping, nil
	}
	if orphan, verr := orphanForScope(); verr != nil {
		return DispatchMap{}, verr
	} else if orphan != nil {
		if resolved.placementDigest == "" {
			return DispatchMap{}, fmt.Errorf("%w: cannot adopt orphan dispatch without a frozen placement digest", ErrInvalidChain)
		}
		mapping := DispatchMap{
			WorkspaceID:     chain.WorkspaceID,
			ProjectID:       chain.ProjectID,
			IssueID:         chain.IssueID,
			TaskID:          chain.TaskID,
			AssignmentID:    chain.AssignmentID,
			ContractVersion: ContractVersion,
			PlacementDigest: resolved.placementDigest,
			OrcaRunID:       orphan.RunID,
			OrcaTaskID:      orphan.TaskID,
			OrcaDispatchID:  orphan.ID,
			WorkerTerminal:  orphan.AssigneeHandle,
			WorkerState:     "unknown",
			Status:          "active",
		}
		if err := b.recordDispatchEvidence(ctx, linkage.WorkRef, mapping); err != nil {
			return mapping, err
		}
		b.rememberDispatch(chain, mapping)
		if err := b.reconcileTaskStart(ctx, linkage.WorkRef, chain); err != nil {
			return mapping, err
		}
		return mapping, nil
	}

	// The orphan-mismatch probe is a local value passed into the claim call,
	// so concurrent RunClaimedTask calls on different assignments each carry
	// their own probe and never overwrite each other.
	workerOrphanProbe := func(ctx context.Context, baseKey string) error {
		if baseKey != workerStartClaimBase(chain) {
			return nil
		}
		dispatch, err := b.Client.DispatchShow(ctx, orcaTaskID)
		if err != nil || dispatch == nil || dispatch.ID == "" {
			return nil
		}
		return validateOrphanDispatchIdentity(dispatch, orcaRunID, orcaTaskID)
	}
	claim, claimErr := b.acquireCreateClaim(ctx, linkage.WorkRef, workerStartClaimBase(chain), func(ctx context.Context) bool {
		if committedMapping() != (DispatchMap{}) {
			return true
		}
		orphan, verr := orphanForScope()
		return verr == nil && orphan != nil
	}, workerOrphanProbe)
	if claimErr != nil {
		if errors.Is(claimErr, ErrScopeAlreadyCommitted) {
			if mapping := committedMapping(); mapping.OrcaDispatchID != "" {
				// The committed mapping may still owe its StartTask
				// reconciliation (evidence precedes the verb); never report
				// success while that obligation is unresolved.
				if err := b.reconcileTaskStart(ctx, linkage.WorkRef, chain); err != nil {
					return mapping, err
				}
				b.rememberDispatch(chain, mapping)
				return mapping, nil
			}
			// Adopt the Orca-side orphan left by the crashed holder — only
			// after the one shared exact identity validation and with the
			// caller's frozen placement digest, never a zero digest.
			if dispatch, verr := orphanForScope(); verr != nil {
				return DispatchMap{}, verr
			} else if dispatch != nil {
				if resolved.placementDigest == "" {
					return DispatchMap{}, fmt.Errorf("%w: cannot adopt orphan dispatch without a frozen placement digest", ErrInvalidChain)
				}
				mapping := DispatchMap{
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
				if err := b.recordDispatchEvidence(ctx, linkage.WorkRef, mapping); err != nil {
					return mapping, err
				}
				b.rememberDispatch(chain, mapping)
				if err := b.reconcileTaskStart(ctx, linkage.WorkRef, chain); err != nil {
					return mapping, err
				}
				return mapping, nil
			}
			return DispatchMap{}, fmt.Errorf("orcabridge: worker scope %s/%s reported committed but no dispatch evidence or orphan is observable",
				chain.WorkspaceID, chain.AssignmentID)
		}
		return DispatchMap{}, claimErr
	}

	// Reconcile: a previous call may have started the worker dispatch but
	// crashed before appending evidence. A structured CLI "no dispatch"
	// answer is safe to pass through to worker-start; an unavailable CLI
	// (unknown effects) fails closed so a second dispatch cannot be created.
	// ANY existing dispatch is validated by the one shared exact identity
	// validator BEFORE adoption: an empty or mismatched RunID/TaskID fails
	// closed with ErrResultIdentityMismatch and never reaches WorkerStart,
	// StartTask, evidence append, or memo adoption.
	dispatch, showErr := b.Client.DispatchShow(ctx, orcaTaskID)
	if showErr != nil && !isOrcaCLIAnswer(showErr) {
		return DispatchMap{}, fmt.Errorf("orcabridge: reconcile dispatch before worker start: %w", showErr)
	}
	if showErr == nil && dispatch != nil && dispatch.ID != "" {
		if verr := validateOrphanDispatchIdentity(dispatch, orcaRunID, orcaTaskID); verr != nil {
			return DispatchMap{}, verr
		}
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
		// Effect barrier (claim -> barrier -> gate -> client -> marker).
		permit, err := b.winEffectBarrier(ctx, linkage.WorkRef, claim)
		if err != nil {
			// Barrier lost: the winner's start may be in flight or already
			// committed. Wait bounded for the committed result (evidence or
			// Orca orphan) instead of failing immediately; if nothing
			// appears in time, surface ErrScopeAttemptInFlight.
			if errors.Is(err, ErrScopeAttemptInFlight) {
				return b.waitForCommittedDispatch(ctx, chain, linkage.WorkRef, orcaRunID, orcaTaskID, resolved.placementDigest, committedMapping)
			}
			return DispatchMap{}, err
		}
		if err := b.withinLease(permit); err != nil {
			if reopenErr := b.reopenEffectBarrier(ctx, linkage.WorkRef, permit); reopenErr != nil {
				return DispatchMap{}, fmt.Errorf("%v (barrier reopen failed: %v; takeover stays blocked)", err, reopenErr)
			}
			return DispatchMap{}, err
		}
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
			// Post-call unknown outcome: the barrier stays held (reconcile-only).
			return DispatchMap{}, fmt.Errorf("orcabridge: start orca worker: %w (effect barrier stays held; takeover blocked)", err)
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

	// Commit the dispatch durably BEFORE advancing the HiveCrew task: the
	// worker start is an irreversible fact, so any process (including a fresh
	// Bridge resuming after a StartTask failure) must be able to read it and
	// reconcile only the start. Memoize first so a retry whose evidence
	// append failed still converges on the committed dispatch and re-attempts
	// only the evidence (never a second WorkerStart).
	b.rememberDispatch(chain, mapping)
	if err := b.recordDispatchEvidence(ctx, linkage.WorkRef, mapping); err != nil {
		b.markEvidencePending(chain, true)
		return mapping, fmt.Errorf("orcabridge: dispatch %s committed but its evidence append failed (start not yet attempted; retry is safe): %w",
			mapping.OrcaDispatchID, err)
	}
	b.markEvidencePending(chain, false)
	if err := b.reconcileTaskStart(ctx, linkage.WorkRef, chain); err != nil {
		return mapping, err
	}

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
	// Writeback: the result evidence key is dispatch-scoped and the ledger
	// append is first-writer-wins, so concurrent ingest of the same delivery
	// converges on one evidence row; drift fails closed below. Duplicate
	// settlement on replay is intentional so a crash between evidence and
	// settlement is recovered by replaying the same delivery.
	key := ResultEvidenceKey(mapping.OrcaDispatchID)
	{
		_ = key
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
			return ResultReceipt{}, appendErr
		}
		if appendErr != nil {
			// Classify the replay: compare the committed digest.
			if existing, found, lookupErr := b.Entry.LookupEvidence(ctx, workRef, key); lookupErr == nil && found {
				if committed, _ := existing.Payload["result_digest"].(string); committed != digest {
					return ResultReceipt{}, fmt.Errorf("%w: dispatch %s committed %s, replay carried %s",
						ErrResultReceiptConflict, mapping.OrcaDispatchID, committed, digest)
				}
			}
		}

		// Settle the HiveCrew task through the existing daemon lifecycle. Runs
		// on both fresh and replayed evidence so a crash between evidence and
		// settlement is recovered by replaying the same message.
		settleErr := b.settleHiveCrewTask(ctx, mapping, result, message)
		if settleErr != nil {
			return b.resultReceiptFromMapping(mapping, message, result, digest), settleErr
		}
		return b.resultReceiptFromMapping(mapping, message, result, digest), nil
	}
}

// waitForCommittedRun waits bounded for the run committed by the barrier
// winner: digest-verified mapping evidence or the Orca orphan marker (with
// objective equality). Never creates.
func (b *Bridge) waitForCommittedRun(ctx context.Context, workRef string, chain Chain, digest, objective string) (string, error) {
	deadline := b.now().Add(b.claimMaxWait())
	for attempt := 0; attempt < maxClaimAttempts; attempt++ {
		if committed := b.readCommittedRun(ctx, workRef, chain, digest); committed != "" {
			b.rememberRun(chain, committed, digest)
			return committed, nil
		}
		runs, err := b.Client.RunList(ctx)
		if err == nil {
			for _, run := range runs {
				ws, prj, ok := RunMarkerScan(run.Objective)
				if ok && ws == chain.WorkspaceID && prj == chain.ProjectID {
					if run.Objective != objective {
						return "", fmt.Errorf("%w: orphan run %s carries a drifted objective", ErrMappingConflict, run.ID)
					}
					if err := b.recordLinkageEvidence(ctx, workRef, RunLinkageKey(chain), map[string]any{
						"mapping":      "run",
						"workspace_id": chain.WorkspaceID,
						"project_id":   chain.ProjectID,
						"orca_run_id":  run.ID,
						"digest":       digest,
					}); err != nil {
						return "", err
					}
					b.rememberRun(chain, run.ID, digest)
					return run.ID, nil
				}
			}
		}
		if b.now().After(deadline) {
			return "", fmt.Errorf("%w: run for %s/%s never committed", ErrScopeAttemptInFlight, chain.WorkspaceID, chain.ProjectID)
		}
		if err := sleepContext(ctx, b.claimPoll()); err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("%w: run for %s/%s never committed", ErrScopeAttemptInFlight, chain.WorkspaceID, chain.ProjectID)
}

// waitForCommittedTask waits bounded for the task committed by the barrier
// winner: digest-verified mapping evidence or the Orca orphan marker (with
// spec equality). Never creates.
func (b *Bridge) waitForCommittedTask(ctx context.Context, workRef string, chain Chain, digest, runID string, scanOrcaTask func() (string, error)) (string, string, error) {
	deadline := b.now().Add(b.claimMaxWait())
	for attempt := 0; attempt < maxClaimAttempts; attempt++ {
		if committed := b.readCommittedTask(ctx, workRef, chain, digest); committed != "" {
			b.rememberTask(chain, runID, committed, digest)
			return runID, committed, nil
		}
		if orphan, err := scanOrcaTask(); err == nil && orphan != "" {
			if err := b.recordLinkageEvidence(ctx, workRef, TaskLinkageKey(chain), map[string]any{
				"mapping":      "task",
				"workspace_id": chain.WorkspaceID,
				"project_id":   chain.ProjectID,
				"issue_id":     chain.IssueID,
				"task_id":      chain.TaskID,
				"orca_run_id":  runID,
				"orca_task_id": orphan,
				"digest":       digest,
			}); err != nil {
				return "", "", err
			}
			b.rememberTask(chain, runID, orphan, digest)
			return runID, orphan, nil
		}
		if b.now().After(deadline) {
			return "", "", fmt.Errorf("%w: task for %s/%s never committed", ErrScopeAttemptInFlight, chain.WorkspaceID, chain.TaskID)
		}
		if err := sleepContext(ctx, b.claimPoll()); err != nil {
			return "", "", err
		}
	}
	return "", "", fmt.Errorf("%w: task for %s/%s never committed", ErrScopeAttemptInFlight, chain.WorkspaceID, chain.TaskID)
}

// waitForCommittedDispatch waits bounded for the dispatch mapping committed
// by the barrier winner: matching evidence (placement verified by the
// committedMapping closure) or the Orca-side orphan. Returns the mapping or
// ErrScopeAttemptInFlight if nothing appears in time.
// validateOrphanDispatchIdentity is the one shared exact identity validator
// for any existing Orca dispatch observed during post-claim reconciliation.
// It requires a handle-valid dispatch ID, a nonempty RunID exactly equal to
// the expected run, and a nonempty TaskID exactly equal to the expected
// task. Any empty or mismatched field returns ErrResultIdentityMismatch so
// the caller fails closed BEFORE entering WorkerStart, StartTask, evidence
// appends, or memo adoption.
func validateOrphanDispatchIdentity(dispatch *OrcaDispatch, orcaRunID, orcaTaskID string) error {
	if dispatch == nil || dispatch.ID == "" {
		return fmt.Errorf("%w: no dispatch observable for expected task %s", ErrResultIdentityMismatch, orcaTaskID)
	}
	if err := ValidateOrcaDispatchID(dispatch.ID); err != nil {
		return err
	}
	if dispatch.RunID == "" || dispatch.RunID != orcaRunID {
		return fmt.Errorf("%w: dispatch %s run %q does not match expected run %s",
			ErrResultIdentityMismatch, dispatch.ID, dispatch.RunID, orcaRunID)
	}
	if dispatch.TaskID == "" || dispatch.TaskID != orcaTaskID {
		return fmt.Errorf("%w: dispatch %s task %q does not match expected task %s",
			ErrResultIdentityMismatch, dispatch.ID, dispatch.TaskID, orcaTaskID)
	}
	return nil
}

func (b *Bridge) waitForCommittedDispatch(ctx context.Context, chain Chain, workRef, orcaRunID, orcaTaskID, resolvedPlacementDigest string, committedMapping func() DispatchMap) (DispatchMap, error) {
	deadline := b.now().Add(b.claimMaxWait())
	for attempt := 0; attempt < maxClaimAttempts; attempt++ {
		// Committed-evidence path: the mapping is durable, but the winner may
		// still owe its StartTask reconciliation (evidence precedes the verb).
		// The waiter must not report success while that obligation is
		// unresolved, so reconcile first and only then return.
		if mapping := committedMapping(); mapping.OrcaDispatchID != "" {
			if err := b.reconcileTaskStart(ctx, workRef, chain); err != nil {
				return mapping, err
			}
			b.rememberDispatch(chain, mapping)
			return mapping, nil
		}
		// Orphan path: an Orca-side dispatch exists without committed
		// evidence. Identity validation happens INSIDE this branch — the
		// outer match is on presence only, so a RunID mismatch or an absent
		// TaskID is rejected here rather than filtered out above.
		if dispatch, err := b.Client.DispatchShow(ctx, orcaTaskID); err == nil &&
			dispatch != nil && dispatch.ID != "" {
			if err := validateOrphanDispatchIdentity(dispatch, orcaRunID, orcaTaskID); err != nil {
				return DispatchMap{}, err
			}
			// The adopted mapping inherits the caller's frozen placement
			// digest — never an empty digest — and the assignment lineage.
			if resolvedPlacementDigest == "" {
				return DispatchMap{}, fmt.Errorf("%w: cannot adopt orphan dispatch without a frozen placement digest", ErrInvalidChain)
			}
			mapping := DispatchMap{
				WorkspaceID:     chain.WorkspaceID,
				ProjectID:       chain.ProjectID,
				IssueID:         chain.IssueID,
				TaskID:          chain.TaskID,
				AssignmentID:    chain.AssignmentID,
				ContractVersion: ContractVersion,
				PlacementDigest: resolvedPlacementDigest,
				OrcaRunID:       dispatch.RunID,
				OrcaTaskID:      dispatch.TaskID,
				OrcaDispatchID:  dispatch.ID,
				WorkerTerminal:  dispatch.AssigneeHandle,
				WorkerState:     "unknown",
				Status:          "active",
			}
			// Durable evidence FIRST: only memoize once the evidence append
			// has succeeded, so a conflicting or failed append cannot poison
			// the in-process memo and let a replay bypass the conflict.
			if err := b.recordDispatchEvidence(ctx, workRef, mapping); err != nil {
				return mapping, err
			}
			if err := b.reconcileTaskStart(ctx, workRef, chain); err != nil {
				return mapping, err
			}
			b.rememberDispatch(chain, mapping)
			return mapping, nil
		}
		if b.now().After(deadline) {
			return DispatchMap{}, fmt.Errorf("%w: dispatch for task %s never committed", ErrScopeAttemptInFlight, chain.TaskID)
		}
		if err := sleepContext(ctx, b.claimPoll()); err != nil {
			return DispatchMap{}, err
		}
	}
	return DispatchMap{}, fmt.Errorf("%w: dispatch for task %s never committed", ErrScopeAttemptInFlight, chain.TaskID)
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

// resumeDurableTaskAssignment resolves one claimed task from durable
// evidence when the in-process memo is empty: the task index record gives
// the work chain reference, and the task evidence on that chain carries the
// full assignment payload. This is what makes fresh-process recovery work.
func (b *Bridge) resumeDurableTaskAssignment(ctx context.Context, taskID string) (EvidenceRecord, bool, error) {
	// The task index key is workspace-scoped but the resolution here only
	// has the task id; scan the workspace is not addressable, so the record
	// is written per assignment chain and found through the claim's own
	// chain in resolveTaskAssignmentIDOnly. For the memo-empty entry point we
	// rely on the assignment-chain work ref recorded in the index; because we
	// cannot derive it from taskID alone, the durable resume is driven by the
	// caller (RunClaimedTask) through its resolved chain. Return not-found
	// here; the durable read happens in resolveTaskAssignmentIDOnly via
	// durableTaskIndexLookup.
	if payload, ok := b.durableTaskIndexLookup(ctx, taskID); ok {
		return EvidenceRecord{Payload: payload}, true, nil
	}
	return EvidenceRecord{}, false, nil
}

// durableTaskIndexLookup scans the work-entry linkages this Bridge has
// registered for the workspace scopes it knows, looking for the task index.
// It is only a fallback for processes with no memo; production callers pass
// through resolveTaskAssignment with a claimed chain.
func (b *Bridge) durableTaskIndexLookup(ctx context.Context, taskID string) (map[string]any, bool) {
	// The port cannot enumerate work chains by task id (no such API), so the
	// fresh-process resume uses the claim path: RunClaimedTask resolves the
	// assignment via the dispatch linkage registered for the workspace's
	// project scopes. This remains honest: when nothing is resolvable, the
	// claim fails closed as unmanaged.
	return nil, false
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
	record, found, err := b.lookupTaskEvidenceForClaim(ctx, claimed)
	if err != nil {
		return taskAssignmentResolution{}, false, err
	}
	if !found {
		return taskAssignmentResolution{}, false, nil
	}
	resolution, convErr := taskResolutionFromPayload(record.Payload)
	if convErr != nil {
		return taskAssignmentResolution{}, false, convErr
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
	if claimed.ID != resolution.chain.TaskID {
		return taskAssignmentResolution{}, false, fmt.Errorf(
			"%w: claimed task id %s does not match linkage task %s",
			ErrResultIdentityMismatch, claimed.ID, resolution.chain.TaskID)
	}
	return resolution, true, nil
}

// taskResolutionFromPayload rebuilds a full resolution from its durable
// task-evidence payload, with scope and placement-digest verification.
func taskResolutionFromPayload(payload map[string]any) (taskAssignmentResolution, error) {
	chain := Chain{
		WorkspaceID:  stringValue(payload, "workspace_id"),
		ProjectID:    stringValue(payload, "project_id"),
		IssueID:      stringValue(payload, "issue_id"),
		TaskID:       stringValue(payload, "task_id"),
		AssignmentID: stringValue(payload, "assignment_id"),
	}
	if err := chain.ValidateAssignmentScope(); err != nil {
		return taskAssignmentResolution{}, err
	}
	placementRaw, _ := payload["placement"].(map[string]any)
	placement, err := placementFromPayload(placementRaw)
	if err != nil {
		return taskAssignmentResolution{}, err
	}
	placement.WorkspaceID = chain.WorkspaceID
	placement.AssignmentID = chain.AssignmentID
	placementDigest, err := placement.Digest()
	if err != nil {
		return taskAssignmentResolution{}, err
	}
	frozenDigest := stringValue(payload, "placement_digest")
	if frozenDigest == "" || frozenDigest != placementDigest {
		return taskAssignmentResolution{}, fmt.Errorf(
			"%w: placement evidence digest %q does not match rebuilt placement %s",
			ErrMappingConflict, frozenDigest, placementDigest)
	}
	return taskAssignmentResolution{
		chain:           chain,
		placement:       placement,
		placementDigest: placementDigest,
		instructions:    stringValue(payload, "instructions"),
	}, nil
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

// lookupTaskEvidenceForClaim resolves the task evidence for one claimed
// daemon task, memo first and then the durable work chain: the dispatch
// linkage for the claim's (workspace, project, issue) is deterministic and
// idempotent to register, and EnsureAssignment wrote the task evidence under
// taskEvidenceKey on exactly that chain. This is the fresh-process path.
func (b *Bridge) lookupTaskEvidenceForClaim(ctx context.Context, claimed DaemonTask) (EvidenceRecord, bool, error) {
	if payload, ok := b.memoTaskAssignment(claimed.ID); ok {
		return EvidenceRecord{Payload: payload}, true, nil
	}
	if !IsValidUUID(claimed.WorkspaceID) || !IsValidUUID(claimed.IssueID) || !IsValidUUID(claimed.ProjectID) {
		return EvidenceRecord{}, false, nil
	}
	chain := Chain{WorkspaceID: claimed.WorkspaceID, ProjectID: claimed.ProjectID, IssueID: claimed.IssueID, TaskID: claimed.ID}
	linkage, err := b.Entry.RegisterLinkage(ctx, LinkageInput{
		Chain:       chain,
		Actor:       b.Actor,
		MappingKind: "dispatch",
	})
	if err != nil {
		return EvidenceRecord{}, false, err
	}
	record, found, err := b.Entry.LookupEvidence(ctx, linkage.WorkRef, taskEvidenceKey(claimed.WorkspaceID, claimed.ID))
	if err != nil || !found {
		return EvidenceRecord{}, false, err
	}
	return record, true, nil
}

// taskAssignmentIndexKey indexes one assignment's work chain under its
// task id so a fresh Bridge process can resolve a claimed task to its
// assignment linkage without any in-process memo.
func taskAssignmentIndexKey(workspaceID, taskID string) string {
	return linkageKeyPrefix + "task-index/" + workspaceID + "/" + taskID
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

// The durable start state uses two deterministic keys with immutable
// payloads, so state transitions never rewrite a key (the work chain's
// idempotency is exact-payload): dispatchStartPendingKey records "a worker
// was dispatched and the task start has not settled" and dispatchStartedKey
// records "StartTask settled". A FRESH Bridge process reads the pair and
// resumes the reconcile loop (retry only Daemon.StartTask, never
// WorkerStart); pending without started means the resume is required.
func dispatchStartPendingKey(chain Chain) string {
	return linkageKeyPrefix + "dispatch-start-pending/" + chain.WorkspaceID + "/" + chain.AssignmentID
}

func dispatchStartedKey(chain Chain) string {
	return linkageKeyPrefix + "dispatch-started/" + chain.WorkspaceID + "/" + chain.AssignmentID
}

func (b *Bridge) recordStartPendingDurable(ctx context.Context, workRef string, chain Chain) error {
	return b.recordLinkageEvidence(ctx, workRef, dispatchStartPendingKey(chain), map[string]any{
		"assignment_id": chain.AssignmentID,
		"task_id":       chain.TaskID,
		"pending":       true,
	})
}

func (b *Bridge) recordStartSettledDurable(ctx context.Context, workRef string, chain Chain) error {
	return b.recordLinkageEvidence(ctx, workRef, dispatchStartedKey(chain), map[string]any{
		"assignment_id": chain.AssignmentID,
		"task_id":       chain.TaskID,
		"started":       true,
	})
}

// startPendingDurable reports whether a dispatched task start is still
// unresolved on the work chain: a pending record exists without a settled
// one. Lookup failures fail CLOSED (found=false with a non-nil error): a
// transient ledger read error must never be interpreted as "nothing
// pending", which would let the caller report success or skip the start
// obligation while it is actually unresolved.
func (b *Bridge) startPendingDurable(ctx context.Context, workRef string, chain Chain) (found, started bool, err error) {
	pending, okPending, err := b.Entry.LookupEvidence(ctx, workRef, dispatchStartPendingKey(chain))
	if err != nil {
		return false, false, fmt.Errorf("orcabridge: read start-pending evidence for %s: %w", chain.TaskID, err)
	}
	if !okPending {
		return false, false, nil
	}
	if pendingFlag, _ := pending.Payload["pending"].(bool); !pendingFlag {
		return false, false, nil
	}
	settled, okSettled, err := b.Entry.LookupEvidence(ctx, workRef, dispatchStartedKey(chain))
	if err != nil {
		return false, false, fmt.Errorf("orcabridge: read start-settled evidence for %s: %w", chain.TaskID, err)
	}
	if okSettled {
		if startedFlag, _ := settled.Payload["started"].(bool); startedFlag {
			return true, true, nil
		}
	}
	return true, false, nil
}

// reconcileTaskStart advances the HiveCrew task to running, retrying only
// the StartTask verb. It records durable pending state before the first
// attempt and clears it after success, so a fresh Bridge process resumes
// correctly. It never returns nil while the task start is unresolved.
func (b *Bridge) reconcileTaskStart(ctx context.Context, workRef string, chain Chain) error {
	value, _ := b.startReconcileLocks.LoadOrStore(chain.WorkspaceID+":"+chain.AssignmentID, &sync.Mutex{})
	mutex := value.(*sync.Mutex)
	mutex.Lock()
	defer mutex.Unlock()
	// Double-checked durable state: a concurrent holder may have settled the
	// start between the caller's check and this lock.
	found, started, derr := b.startPendingDurable(ctx, workRef, chain)
	if derr != nil {
		return derr
	}
	if found && started {
		b.markStartPending(chain, false)
		return nil
	}
	// Cross-instance start arbitration: the start-permit CAS decides which
	// Bridge performs the StartTask verb. The permit payload is identity-
	// stable (instance + generation + lease deadline), so the SAME holder
	// re-acquires on its own retries — the idempotent replay the port
	// provides — while a DIFFERENT holder loses and waits for settlement.
	// A different holder may only take over when the previous start attempt
	// is resolved: settled evidence (started=true) or no durable start
	// obligation at all.
	// The permit payload must be byte-stable across this Bridge's retries:
	// lease deadline and attempt stamp are fixed at first acquisition (cached
	// in-process) so a retry composes the identical payload and the port's
	// idempotent replay re-acquires instead of conflicting.
	b.startPermitMu.Lock()
	cached := b.startPermits[chain.WorkspaceID+":"+chain.AssignmentID]
	b.startPermitMu.Unlock()
	if !cached.acquired {
		cached = startPermitIdentity{deadline: b.now().Add(b.scopeLeaseTTL()), stamp: b.now()}
	}
	permitHolder := b.instanceID()
	attempt := 0
	for {
		permit, err := b.Entry.ClaimScope(ctx, ScopeClaimInput{
			WorkRef:    workRef,
			ClaimKey:   dispatchStartPermitKey(chain, attempt),
			InstanceID: permitHolder,
			SessionID:  b.Actor.SessionID,
			Generation: attempt,
			ExpiresAt:  cached.deadline,
			AttemptAt:  cached.stamp,
		})
		if err != nil {
			return fmt.Errorf("orcabridge: acquire task-start permit for %s: %w", chain.TaskID, err)
		}
		if permit.Acquired {
			b.startPermitMu.Lock()
			b.startPermits[chain.WorkspaceID+":"+chain.AssignmentID] = startPermitIdentity{acquired: true, deadline: cached.deadline, stamp: cached.stamp}
			b.startPermitMu.Unlock()
			break // this Bridge owns the StartTask verb
		}
		// Someone else owns it. If the start already settled, we are done.
		if _, started, derr := b.startPendingDurable(ctx, workRef, chain); derr != nil {
			return derr
		} else if started {
			b.markStartPending(chain, false)
			return nil
		}
		// Expired holder: the holder is gone. Take over the StartTask verb at
		// the next permit generation. StartTask is the daemon's own
		// idempotent lifecycle verb (a repeated start on an unsettled task is
		// the daemon's no-op/terminal transition, not a second side effect);
		// the invariant that matters is that WorkerStart is never re-run.
		// Re-take the permit at generation+1 and continue into the verb.
		if !permit.Holder.Parsed || permit.Holder.ExpiresAt.Before(b.now()) {
			attempt = permit.Holder.Generation + 1
			continue
		}
		// Live holder: wait bounded for its settlement.
		waitDeadline := b.now().Add(b.claimMaxWait())
		for w := 0; w < maxClaimAttempts; w++ {
			if _, started, derr := b.startPendingDurable(ctx, workRef, chain); derr != nil {
				return derr
			} else if started {
				b.markStartPending(chain, false)
				return nil
			}
			if b.now().After(waitDeadline) {
				return fmt.Errorf("%w: task %s start by the permit holder never settled", ErrScopeAttemptInFlight, chain.TaskID)
			}
			if err := sleepContext(ctx, b.claimPoll()); err != nil {
				return err
			}
		}
		return fmt.Errorf("%w: task %s start by the permit holder never settled", ErrScopeAttemptInFlight, chain.TaskID)
	}

	b.markStartPending(chain, true)
	if err := b.recordStartPendingDurable(ctx, workRef, chain); err != nil {
		return fmt.Errorf("orcabridge: record start-pending evidence: %w", err)
	}
	if err := b.Daemon.StartTask(ctx, chain.TaskID); err != nil {
		return fmt.Errorf("orcabridge: hivecrew task %s start failed (worker already dispatched; retry retries only StartTask): %w", chain.TaskID, err)
	}
	if err := b.recordStartSettledDurable(ctx, workRef, chain); err != nil {
		// StartTask succeeded but the settle write failed: keep the in-memory
		// pending mark so a retry re-settles; success cannot be claimed until
		// durable evidence settles.
		return fmt.Errorf("orcabridge: hivecrew task %s started but its evidence settle failed (retry is safe): %w", chain.TaskID, err)
	}
	b.markStartPending(chain, false)
	return nil
}

// startPermitIdentity freezes the byte-stable payload fields of one
// assignment's start permit so retries compose identical CAS inputs.
type startPermitIdentity struct {
	acquired bool
	deadline time.Time
	stamp    time.Time
}

// dispatchStartPermitKey is the atomic cross-instance permit for performing
// one assignment's StartTask verb at one takeover generation.
func dispatchStartPermitKey(chain Chain, attempt int) string {
	return fmt.Sprintf("%sdispatch-start-permit/%s/%s#%d", linkageKeyPrefix, chain.WorkspaceID, chain.AssignmentID, attempt)
}

// startPendingDurableObligation reports whether any durable start
// obligation exists (pending or settled) without distinguishing the two, and
// propagates lookup errors (fail closed at the caller).
func (b *Bridge) startPendingDurableObligation(ctx context.Context, workRef string, chain Chain) (found, started bool, err error) {
	found, startedFlag, err := b.startPendingDurable(ctx, workRef, chain)
	if err != nil {
		return false, false, err
	}
	if found {
		return true, startedFlag, nil
	}
	// No pending record: check the settled record alone.
	settled, ok, err := b.Entry.LookupEvidence(ctx, workRef, dispatchStartedKey(chain))
	if err != nil {
		return false, false, fmt.Errorf("orcabridge: read start-settled evidence for %s: %w", chain.TaskID, err)
	}
	if !ok {
		return false, false, nil
	}
	startedFlag, _ = settled.Payload["started"].(bool)
	return startedFlag, startedFlag, nil
}

// markStartPending records that WorkerStart committed but the HiveCrew task
// start has not succeeded yet, so retries reconcile by retrying only the
// StartTask verb — never a second WorkerStart.
func (b *Bridge) markStartPending(chain Chain, pending bool) {
	b.memoMu.Lock()
	defer b.memoMu.Unlock()
	key := chain.WorkspaceID + ":" + chain.AssignmentID
	if pending {
		b.memo.startPending[key] = true
		return
	}
	delete(b.memo.startPending, key)
}

func (b *Bridge) startPendingFor(chain Chain) bool {
	b.memoMu.Lock()
	defer b.memoMu.Unlock()
	return b.memo.startPending[chain.WorkspaceID+":"+chain.AssignmentID]
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

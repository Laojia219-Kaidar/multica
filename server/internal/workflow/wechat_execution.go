package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ---------------------------------------------------------------------------
// WeChat content production execution bridge (HIVECREW-WECHAT-REAL-OPERATIONS-V1
// / WO-30).
//
// The bridge turns one validated WechatContentProductionRequest into the four
// frozen nodes' EXISTING Issue/Assignment/Task/Run chains, one node at a time,
// over the existing CompanyOps authorities. It creates no new queue, task
// table, run table, or migration: durable state lives in the existing workflow
// instance/event ledger (migration 342) and the existing CompanyOps tables.
//
// Fail-closed rules (P0-GATE-01/02/03):
//   - a node completes only with a server-side completed execution receipt AND
//     a materialized non-empty ArtifactCandidate of its own;
//   - a failed/cancelled Run, a missing receipt, a blank output, a dispatch
//     authority rejection, or a cross-Project pin halts the production as
//     failed;
//   - the editorial-review-report node is an approval gate: the production
//     pauses until an explicit Owner decision; changes_requested blocks the
//     downstream publication-package node;
//   - the publication package candidate is AWAITING PUBLICATION forever in
//     this slice; without a WeChat platform receipt nothing is ever "published".
// ---------------------------------------------------------------------------

const (
	// WechatEventNodeDispatched records one node's CompanyOps assignment
	// dispatch receipt (command/issue/task) into the workflow event ledger.
	WechatEventNodeDispatched = "wechat.node.dispatched"
	// WechatEventNodeCompleted records server-side completion proof plus the
	// node's own materialized candidate.
	WechatEventNodeCompleted = "wechat.node.completed"
	// WechatEventNodeFailed records a fail-closed node halt.
	WechatEventNodeFailed = "wechat.node.failed"
	// WechatEventAwaitingApproval records the approval gate halt.
	WechatEventAwaitingApproval = "wechat.production.awaiting_approval"
	// WechatEventApproved / WechatEventChangesRequested record the Owner
	// decision receipts produced by ReviewProduction.
	WechatEventApproved         = "wechat.production.approved"
	WechatEventChangesRequested = "wechat.production.changes_requested"
	// WechatEventAwaitingPublication marks the terminal candidate state of
	// this slice: the publication package is ready and forever "待发布"
	// unless a future, separately authorized publisher produces a platform
	// receipt. It is never "published".
	WechatEventAwaitingPublication = "wechat.production.awaiting_publication"
)

// wechatEventStartKind reuses the engine's start kind so the existing
// LoadStartedInstanceByIdempotencyInWorkspace receipt lookup keeps working.
const wechatEventStartKind = "workflow.started"

var (
	// ErrWechatProductionNotFound means no production instance exists in the
	// caller's workspace for the requested id.
	ErrWechatProductionNotFound = errors.New("wechat production instance not found")
	// ErrWechatProductionConflict means an idempotency replay carries a
	// different immutable payload than the recorded command.
	ErrWechatProductionConflict = errors.New("wechat production idempotency conflict")
	// ErrWechatDefinitionPin means the request does not pin the exact
	// published definition version (missing, digest mismatch, cross-Project).
	ErrWechatDefinitionPin = errors.New("wechat definition version pin mismatch")
	// ErrWechatNodeAuthorityRejected wraps permanent authority/dispatch
	// rejections (missing binding, stale authority, cross-Project). The
	// orchestrator fails the production only on this class; transient
	// infrastructure errors propagate without failing the production.
	ErrWechatNodeAuthorityRejected = errors.New("wechat node authority or dispatch rejected")
	// ErrWechatReviewUnavailable means no Owner decision is currently
	// reviewable (wrong state or missing candidate).
	ErrWechatReviewUnavailable = errors.New("wechat production has no reviewable candidate in its current state")
)

// WechatProductionReviewDecision is the Owner decision over a node candidate.
type WechatProductionReviewDecision string

const (
	WechatReviewApproved         WechatProductionReviewDecision = "approved"
	WechatReviewChangesRequested WechatProductionReviewDecision = "changes_requested"
)

// WechatNodeExecutionPlan is the deterministic per-node dispatch plan derived
// from one validated request. HandoffNote and InputDigest are finalized at
// dispatch time (they embed the completed upstream lineage); CommandID and
// WorkOrderSourceRef are stable from derivation.
type WechatNodeExecutionPlan struct {
	Node               WechatContentNodeContract
	WorkOrderSourceRef string
	CommandID          string
	HandoffNote        string
	InputDigest        string
}

// WechatNodeDispatch is the server-side assignment receipt for one node.
type WechatNodeDispatch struct {
	CommandID string
	IssueID   string
	TaskID    string
}

// WechatNodeExecutionObservation is the fresh server-side read of one node's
// execution. ReceiptCompleted must come from GetExecutionReceipt
// (Terminal.Status == "completed"), never from a client claim (P0-GATE-01).
type WechatNodeExecutionObservation struct {
	State            string
	ReceiptCompleted bool
	CandidateID      string
}

// WechatNodeExecutor is the narrow seam the WO-50 integrator wires to the
// existing CompanyOps services. Implementations must reuse the existing
// Issue projection, assignment Dispatch, execution receipt, and artifact
// materialization paths; they must not create new queues or tables.
type WechatNodeExecutor interface {
	// EnsureNodeIssue projects the node's derived WorkOrder into its own
	// existing Issue (idempotent per WorkOrder source ref).
	EnsureNodeIssue(ctx context.Context, workspaceID string, plan WechatNodeExecutionPlan) (string, error)
	// DispatchNode runs the existing CompanyOps assignment Dispatch for the
	// node Issue and enqueues its canonical Task. Permanent authority
	// rejections must wrap ErrWechatNodeAuthorityRejected.
	DispatchNode(ctx context.Context, workspaceID string, plan WechatNodeExecutionPlan, issueID string) (WechatNodeDispatch, error)
	// ReadNodeExecution reads the fresh server-side execution state, the
	// completed-receipt flag, and the node's own candidate if materialized.
	ReadNodeExecution(ctx context.Context, workspaceID string, issueID string, taskID string) (WechatNodeExecutionObservation, error)
	// MaterializeNodeCandidate materializes the completed task output into
	// the node's own ArtifactCandidate through the existing
	// MaterializeCompletedTask path (idempotent per task; fails on blank
	// output or missing completed receipt).
	MaterializeNodeCandidate(ctx context.Context, workspaceID string, taskID string) (string, error)
	// ReviewNodeCandidate records the Owner decision on the node's active
	// candidate through the existing ReviewArtifact path.
	ReviewNodeCandidate(ctx context.Context, workspaceID string, issueID string, candidateID string, decision WechatProductionReviewDecision, reviewID string) error
}

// WechatProductionStore is the narrow durable seam over the existing workflow
// instance/event tables. *Repository satisfies it via the adapter below.
type WechatProductionStore interface {
	LoadInstance(ctx context.Context, workspaceID string, id string) (WorkflowInstance, error)
	SaveInstance(ctx context.Context, workspaceID string, inst WorkflowInstance) error
	UpdateInstance(ctx context.Context, workspaceID string, inst WorkflowInstance) error
	AppendEvent(ctx context.Context, ev Event) error
	ListEvents(ctx context.Context, workspaceID string, instanceID string) ([]Event, error)
	LoadEventByIdempotency(ctx context.Context, workspaceID string, instanceID string, key string) (Event, error)
	LoadPublishedVersion(ctx context.Context, workspaceID string, definitionID string, version int) (WorkflowDefinitionVersion, error)
}

// wechatRepositoryStore adapts the canonical workflow Repository.
type wechatRepositoryStore struct{ repo *Repository }

func (s wechatRepositoryStore) LoadInstance(ctx context.Context, workspaceID string, id string) (WorkflowInstance, error) {
	inst, err := s.repo.LoadInstanceInWorkspace(ctx, workspaceID, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return WorkflowInstance{}, ErrWechatProductionNotFound
	}
	return inst, err
}

func (s wechatRepositoryStore) SaveInstance(ctx context.Context, workspaceID string, inst WorkflowInstance) error {
	return s.repo.SaveInstanceInWorkspace(ctx, workspaceID, inst)
}

func (s wechatRepositoryStore) UpdateInstance(ctx context.Context, workspaceID string, inst WorkflowInstance) error {
	return s.repo.UpdateInstanceInWorkspace(ctx, workspaceID, inst)
}

func (s wechatRepositoryStore) AppendEvent(ctx context.Context, ev Event) error {
	return s.repo.AppendEvent(ctx, ev)
}

func (s wechatRepositoryStore) ListEvents(ctx context.Context, workspaceID string, instanceID string) ([]Event, error) {
	return s.repo.ListEventsInWorkspace(ctx, workspaceID, instanceID)
}

func (s wechatRepositoryStore) LoadEventByIdempotency(ctx context.Context, workspaceID string, instanceID string, key string) (Event, error) {
	ev, err := s.repo.LoadEventByIdempotencyInWorkspace(ctx, workspaceID, instanceID, key)
	if errors.Is(err, pgx.ErrNoRows) {
		return Event{}, ErrWechatProductionNotFound
	}
	return ev, err
}

func (s wechatRepositoryStore) LoadPublishedVersion(ctx context.Context, workspaceID string, definitionID string, version int) (WorkflowDefinitionVersion, error) {
	return s.repo.LoadPublishedDefinitionVersion(ctx, workspaceID, definitionID, version)
}

// WechatNodeLineageRecord is one node's durable six-member lineage as
// reconstructed from the workflow event ledger plus a fresh live observation.
type WechatNodeLineageRecord struct {
	Node           WechatContentNodeKey `json:"node"`
	Order          int                  `json:"order"`
	WorkOrderRef   string               `json:"work_order_ref"`
	CommandID      string               `json:"command_id,omitempty"`
	IssueID        string               `json:"issue_id,omitempty"`
	TaskID         string               `json:"task_id,omitempty"`
	CandidateID    string               `json:"candidate_id,omitempty"`
	State          string               `json:"state"` // pending | dispatched | completed | failed
	LiveState      string               `json:"live_state,omitempty"`
	ReviewDecision string               `json:"review_decision,omitempty"`
	Failure        string               `json:"failure,omitempty"`
}

// WechatProductionView is the read model the operations UI renders. It is
// always derived from the durable instance row plus the append-only event
// ledger, never from process memory.
type WechatProductionView struct {
	InstanceID        string                    `json:"instance_id"`
	DefinitionID      string                    `json:"definition_id"`
	DefinitionVersion int                       `json:"definition_version"`
	ProjectID         string                    `json:"project_id"`
	Status            InstanceStatus            `json:"status"`
	CurrentNode       WechatContentNodeKey      `json:"current_node,omitempty"`
	Nodes             []WechatNodeLineageRecord `json:"nodes"`
	ApprovalState     string                    `json:"approval_state"`    // none | awaiting | approved | changes_requested
	PublicationState  string                    `json:"publication_state"` // none | awaiting_publication — never "published"
}

// deriveWechatProductionInstanceID deterministically binds one idempotency
// key to one production instance, so a replayed start finds its instance.
func deriveWechatProductionInstanceID(idempotencyKey string) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(
		"hivecrew:wechat-content-production:instance:"+idempotencyKey,
	)).String()
}

// deriveWechatNodeCommandID deterministically binds one (idempotency key,
// node) pair to one CompanyOps assignment command, making Dispatch replays
// collapse onto the committed receipt.
func deriveWechatNodeCommandID(idempotencyKey string, node WechatContentNodeKey) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(
		"hivecrew:wechat-content-production:command:"+idempotencyKey+":"+string(node),
	)).String()
}

// deriveWechatNodeWorkOrderSourceRef derives the node's own WorkOrder source
// ref from the request's base WorkOrder ref. Each node gets its own ref so the
// existing 1:1 WorkOrder->Issue projection yields one Issue per node
// (P0-GATE-03). The derived ref must still match the frozen pattern; an
// over-length or illegal derivation fails closed.
func deriveWechatNodeWorkOrderSourceRef(base string, node WechatContentNodeKey) (string, error) {
	derived := base + "--" + string(node)
	if !wechatWorkOrderSourceRefPattern.MatchString(derived) {
		return "", fmt.Errorf("derived node work_order_source_ref for %q violates the frozen pattern", node)
	}
	return derived, nil
}

// DeriveWechatNodeExecutionPlans re-validates the request and derives the four
// node plans in frozen order. HandoffNote/InputDigest are finalized by the
// orchestrator at dispatch time via ComposeWechatNodeHandoffNote.
func DeriveWechatNodeExecutionPlans(req WechatContentProductionRequest) ([]WechatNodeExecutionPlan, error) {
	if err := ValidateWechatContentProductionRequest(req); err != nil {
		return nil, err
	}
	nodes := WechatContentNodeContracts()
	plans := make([]WechatNodeExecutionPlan, 0, len(nodes))
	for _, node := range nodes {
		ref, err := deriveWechatNodeWorkOrderSourceRef(req.Authority.WorkOrderSourceRef, node.Key)
		if err != nil {
			return nil, err
		}
		plans = append(plans, WechatNodeExecutionPlan{
			Node:               node,
			WorkOrderSourceRef: ref,
			CommandID:          deriveWechatNodeCommandID(req.IdempotencyKey, node.Key),
		})
	}
	return plans, nil
}

// wechatNodeDirectives is the frozen per-node work directive. It tells the
// executing Agent exactly which artifact kind to produce and, for the
// publication package, that no external publishing is performed.
var wechatNodeDirectives = map[WechatContentNodeKey]string{
	WechatContentNodeResearchMaterialPackage: "Produce the research material package (wechat.research-material-package.v1): gather, verify, and organize the source references below into a structured research pack with per-source citations.",
	WechatContentNodeArticleDraft:            "Produce the article draft (wechat.article-draft.v1): write the complete WeChat article draft from the approved research material package referenced below.",
	WechatContentNodeEditorialReviewReport:   "Produce the editorial review report (wechat.editorial-review-report.v1): review the article draft referenced below against the brief, section by section, recording pass/issues and required changes.",
	WechatContentNodeWechatPublicationPackage: "Produce the WeChat publication package (wechat.wechat-publication-package.v1): assemble the final publish-ready package (title, digest, cover copy, body) from the approved draft and editorial review report referenced below. " +
		"The package remains AWAITING PUBLICATION; no external publishing is performed.",
}

// ComposeWechatNodeHandoffNote deterministically composes the exact handoff
// text dispatched to a node's Agent. The upstream section is embedded only
// when the upstream node has completed, so a replay of the same dispatch
// recomposes the identical note (the CompanyOps assignment receipt would
// conflict on any drift).
func ComposeWechatNodeHandoffNote(brief WechatContentBrief, node WechatContentNodeContract, upstream *WechatNodeLineageRecord) (string, error) {
	directive, ok := wechatNodeDirectives[node.Key]
	if !ok {
		return "", fmt.Errorf("no frozen directive for node %q", node.Key)
	}
	if node.RequiredUpstream != nil && upstream == nil {
		return "", fmt.Errorf("node %q requires its completed upstream lineage", node.Key)
	}
	if upstream != nil && (node.RequiredUpstream == nil || *node.RequiredUpstream != upstream.Node) {
		return "", fmt.Errorf("upstream lineage %q does not match node %q required upstream", upstream.Node, node.Key)
	}
	if upstream != nil && (upstream.State != "completed" || upstream.CandidateID == "") {
		return "", fmt.Errorf("upstream node %q has no completed candidate lineage", upstream.Node)
	}

	var b strings.Builder
	b.WriteString(directive)
	b.WriteString("\n\n## Brief\n")
	fmt.Fprintf(&b, "- Subject: %s\n", brief.Subject)
	fmt.Fprintf(&b, "- Objective: %s\n", brief.Objective)
	fmt.Fprintf(&b, "- Audience: %s\n", brief.Audience)
	fmt.Fprintf(&b, "- Tone: %s\n", brief.Tone)
	fmt.Fprintf(&b, "- Deadline: %s\n", brief.Deadline)
	fmt.Fprintf(&b, "- Approval policy: %s\n", brief.ApprovalPolicy)
	b.WriteString("\n## Source references\n")
	for _, ref := range brief.SourceRefs {
		fmt.Fprintf(&b, "- %s\n", ref)
	}
	if upstream != nil {
		b.WriteString("\n## Upstream node artifact (existing CompanyOps lineage)\n")
		fmt.Fprintf(&b, "- Node: %s\n", upstream.Node)
		fmt.Fprintf(&b, "- Issue: %s\n", upstream.IssueID)
		fmt.Fprintf(&b, "- Task: %s\n", upstream.TaskID)
		fmt.Fprintf(&b, "- Candidate: %s\n", upstream.CandidateID)
	}
	b.WriteString("\n## Additional instructions\n")
	b.WriteString(brief.HandoffNote)
	note := b.String()
	if len(note) > WechatContentHandoffNoteMaxBytes {
		return "", fmt.Errorf("composed handoff note for node %q exceeds %d UTF-8 bytes", node.Key, WechatContentHandoffNoteMaxBytes)
	}
	return note, nil
}

// wechatHandoffInputDigest mirrors service.CompanyOpsHandoffInputDigest: the
// server computes the digest from the exact handoff note; callers never
// supply it.
func wechatHandoffInputDigest(handoffNote string) string {
	sum := sha256.Sum256([]byte(handoffNote))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// WechatProductionOrchestrator drives one WeChat content production through
// the four frozen nodes over the existing CompanyOps execution authorities.
type WechatProductionOrchestrator struct {
	store WechatProductionStore
	exec  WechatNodeExecutor
}

// NewWechatProductionOrchestrator wires the orchestrator to the canonical
// workflow Repository and a WO-50-provided executor.
func NewWechatProductionOrchestrator(repo *Repository, exec WechatNodeExecutor) (*WechatProductionOrchestrator, error) {
	if repo == nil || repo.Q == nil {
		return nil, fmt.Errorf("wechat production orchestrator requires the workflow repository")
	}
	return NewWechatProductionOrchestratorWithStore(wechatRepositoryStore{repo: repo}, exec)
}

// NewWechatProductionOrchestratorWithStore is the test-friendly constructor.
func NewWechatProductionOrchestratorWithStore(store WechatProductionStore, exec WechatNodeExecutor) (*WechatProductionOrchestrator, error) {
	if store == nil || exec == nil {
		return nil, fmt.Errorf("wechat production orchestrator requires a store and an executor")
	}
	return &WechatProductionOrchestrator{store: store, exec: exec}, nil
}

// verifyWechatDefinitionPin fail-closed checks that the request pins the exact
// published definition version: the version must exist in the caller's
// workspace, its digest must equal the request digest, and its Project must
// equal the request Project (no cross-Project pinning).
func (o *WechatProductionOrchestrator) verifyWechatDefinitionPin(ctx context.Context, workspaceID string, req WechatContentProductionRequest) error {
	version, err := o.store.LoadPublishedVersion(ctx, workspaceID, req.Definition.DefinitionID, req.Definition.Version)
	if err != nil {
		return fmt.Errorf("%w: published definition version is not readable in this workspace", ErrWechatDefinitionPin)
	}
	if version.Digest != req.Definition.Digest {
		return fmt.Errorf("%w: digest does not match the published version", ErrWechatDefinitionPin)
	}
	if version.ProjectID == "" || version.ProjectID != req.ProjectID {
		return fmt.Errorf("%w: published version belongs to a different Project", ErrWechatDefinitionPin)
	}
	return nil
}

// StartProduction validates the request, verifies the definition pin, creates
// the production instance (or replays an identical in-flight/completed start),
// and reconciles forward: node 1 is dispatched immediately.
func (o *WechatProductionOrchestrator) StartProduction(ctx context.Context, workspaceID string, req WechatContentProductionRequest, actor string) (WechatProductionView, error) {
	if o == nil {
		return WechatProductionView{}, fmt.Errorf("wechat production orchestrator is not configured")
	}
	if strings.TrimSpace(workspaceID) == "" {
		return WechatProductionView{}, fmt.Errorf("workspace_id is required")
	}
	plans, err := DeriveWechatNodeExecutionPlans(req)
	if err != nil {
		return WechatProductionView{}, err
	}
	if err := o.verifyWechatDefinitionPin(ctx, workspaceID, req); err != nil {
		return WechatProductionView{}, err
	}
	instanceID := deriveWechatProductionInstanceID(req.IdempotencyKey)
	if existing, err := o.store.LoadInstance(ctx, workspaceID, instanceID); err == nil {
		if existing.DefinitionID != req.Definition.DefinitionID ||
			existing.DefinitionVersion != req.Definition.Version ||
			existing.Context.ProjectID != req.ProjectID {
			return WechatProductionView{}, fmt.Errorf("%w: idempotency key was already used for another production", ErrWechatProductionConflict)
		}
		return o.reconcile(ctx, workspaceID, existing, req, plans, actor)
	} else if !errors.Is(err, ErrWechatProductionNotFound) {
		return WechatProductionView{}, fmt.Errorf("read wechat production start receipt: %w", err)
	}

	inst := WorkflowInstance{
		ID:                instanceID,
		WorkspaceID:       workspaceID,
		DefinitionID:      req.Definition.DefinitionID,
		DefinitionVersion: req.Definition.Version,
		Context:           ContextRef{ProjectID: req.ProjectID},
		StageIndex:        1,
		Status:            StatusRunning,
	}
	if err := o.store.SaveInstance(ctx, workspaceID, inst); err != nil {
		return WechatProductionView{}, fmt.Errorf("persist wechat production instance: %w", err)
	}
	if err := o.appendEvent(ctx, Event{
		InstanceID:     instanceID,
		Kind:           wechatEventStartKind,
		SourceRef:      fmt.Sprintf("hivecrew://wechat-content/%s/started", instanceID),
		Actor:          actor,
		IdempotencyKey: req.IdempotencyKey,
	}); err != nil {
		return WechatProductionView{}, fmt.Errorf("persist wechat production start receipt: %w", err)
	}
	return o.reconcile(ctx, workspaceID, inst, req, plans, actor)
}

// ReconcileProduction re-drives an existing production. The caller resubmits
// the same validated request; the bridge verifies that the request derives the
// exact path instance before touching anything, so a poll can never steer
// another production. Advancement is poll-driven in this slice (the daemon
// auto-advance hook is WO-50 wiring, not part of WO-30).
func (o *WechatProductionOrchestrator) ReconcileProduction(ctx context.Context, workspaceID string, instanceID string, req WechatContentProductionRequest, actor string) (WechatProductionView, error) {
	if o == nil {
		return WechatProductionView{}, fmt.Errorf("wechat production orchestrator is not configured")
	}
	plans, err := DeriveWechatNodeExecutionPlans(req)
	if err != nil {
		return WechatProductionView{}, err
	}
	if deriveWechatProductionInstanceID(req.IdempotencyKey) != instanceID {
		return WechatProductionView{}, fmt.Errorf("%w: request idempotency key derives a different production instance", ErrWechatProductionConflict)
	}
	inst, err := o.store.LoadInstance(ctx, workspaceID, instanceID)
	if err != nil {
		return WechatProductionView{}, err
	}
	if inst.DefinitionID != req.Definition.DefinitionID || inst.DefinitionVersion != req.Definition.Version || inst.Context.ProjectID != req.ProjectID {
		return WechatProductionView{}, fmt.Errorf("%w: request does not match the recorded production", ErrWechatProductionConflict)
	}
	return o.reconcile(ctx, workspaceID, inst, req, plans, actor)
}

// GetProduction rebuilds the read model from the durable ledger and enriches
// the current node with one fresh server-side observation. It performs no
// writes.
func (o *WechatProductionOrchestrator) GetProduction(ctx context.Context, workspaceID string, instanceID string) (WechatProductionView, error) {
	if o == nil {
		return WechatProductionView{}, fmt.Errorf("wechat production orchestrator is not configured")
	}
	inst, err := o.store.LoadInstance(ctx, workspaceID, instanceID)
	if err != nil {
		return WechatProductionView{}, err
	}
	events, err := o.store.ListEvents(ctx, workspaceID, instanceID)
	if err != nil {
		return WechatProductionView{}, fmt.Errorf("read wechat production events: %w", err)
	}
	view := BuildWechatProductionView(inst, events)
	return o.enrichCurrentNode(ctx, workspaceID, view)
}

// ReviewProduction records the Owner decision over the currently reviewable
// candidate: the editorial-review-report candidate while the gate is halted,
// or the publication-package candidate once the production reached
// awaiting_publication. Approval of the gate re-arms the production
// (StatusRunning); the next reconcile dispatches the publication-package
// node. changes_requested keeps the gate halted and blocks downstream.
func (o *WechatProductionOrchestrator) ReviewProduction(ctx context.Context, workspaceID string, instanceID string, decision WechatProductionReviewDecision, reviewID string, actor string) (WechatProductionView, error) {
	if o == nil {
		return WechatProductionView{}, fmt.Errorf("wechat production orchestrator is not configured")
	}
	if decision != WechatReviewApproved && decision != WechatReviewChangesRequested {
		return WechatProductionView{}, fmt.Errorf("decision must be approved or changes_requested")
	}
	if strings.TrimSpace(reviewID) == "" || !wechatUUIDPattern.MatchString(reviewID) {
		return WechatProductionView{}, fmt.Errorf("review_id must be a canonical UUID")
	}
	inst, err := o.store.LoadInstance(ctx, workspaceID, instanceID)
	if err != nil {
		return WechatProductionView{}, err
	}
	if _, err := o.store.LoadEventByIdempotency(ctx, workspaceID, instanceID, reviewID); err == nil {
		events, listErr := o.store.ListEvents(ctx, workspaceID, instanceID)
		if listErr != nil {
			return WechatProductionView{}, fmt.Errorf("read wechat production events: %w", listErr)
		}
		return o.enrichCurrentNode(ctx, workspaceID, BuildWechatProductionView(inst, events))
	} else if !errors.Is(err, ErrWechatProductionNotFound) {
		return WechatProductionView{}, fmt.Errorf("read wechat review receipt: %w", err)
	}

	events, err := o.store.ListEvents(ctx, workspaceID, instanceID)
	if err != nil {
		return WechatProductionView{}, fmt.Errorf("read wechat production events: %w", err)
	}
	view := BuildWechatProductionView(inst, events)

	var target *WechatNodeLineageRecord
	switch inst.Status {
	case StatusPaused:
		target = view.nodePtr(WechatContentNodeEditorialReviewReport)
	case StatusCompleted:
		target = view.nodePtr(WechatContentNodeWechatPublicationPackage)
	default:
		return WechatProductionView{}, ErrWechatReviewUnavailable
	}
	if target == nil || target.CandidateID == "" || target.IssueID == "" {
		return WechatProductionView{}, ErrWechatReviewUnavailable
	}

	if err := o.exec.ReviewNodeCandidate(ctx, workspaceID, target.IssueID, target.CandidateID, decision, reviewID); err != nil {
		return WechatProductionView{}, fmt.Errorf("record owner review: %w", err)
	}
	kind := WechatEventApproved
	if decision == WechatReviewChangesRequested {
		kind = WechatEventChangesRequested
	}
	if err := o.appendEvent(ctx, Event{
		InstanceID:     instanceID,
		Kind:           kind,
		SourceRef:      fmt.Sprintf("hivecrew://wechat-content/node/%s/review/%s", target.Node, reviewID),
		Actor:          actor,
		IdempotencyKey: reviewID,
	}); err != nil {
		return WechatProductionView{}, fmt.Errorf("persist wechat review receipt: %w", err)
	}
	if inst.Status == StatusPaused && decision == WechatReviewApproved {
		inst.Status = StatusRunning
		if err := o.store.UpdateInstance(ctx, workspaceID, inst); err != nil {
			return WechatProductionView{}, fmt.Errorf("re-arm wechat production after approval: %w", err)
		}
	}
	events, err = o.store.ListEvents(ctx, workspaceID, instanceID)
	if err != nil {
		return WechatProductionView{}, fmt.Errorf("read wechat production events: %w", err)
	}
	return o.enrichCurrentNode(ctx, workspaceID, BuildWechatProductionView(inst, events))
}

// reconcile is the poll-driven state machine. Every step is idempotent:
// dispatch replays collapse onto the committed CompanyOps receipt (same
// command id), and event appends dedup on (instance, idempotency key).
func (o *WechatProductionOrchestrator) reconcile(ctx context.Context, workspaceID string, inst WorkflowInstance, req WechatContentProductionRequest, plans []WechatNodeExecutionPlan, actor string) (WechatProductionView, error) {
	events, err := o.store.ListEvents(ctx, workspaceID, inst.ID)
	if err != nil {
		return WechatProductionView{}, fmt.Errorf("read wechat production events: %w", err)
	}
	if inst.Status != StatusRunning {
		return o.enrichCurrentNode(ctx, workspaceID, BuildWechatProductionView(inst, events))
	}

	view := BuildWechatProductionView(inst, events)
	changed := false
	for i := range plans {
		plan := plans[i]
		rec := view.nodePtr(plan.Node.Key)
		if rec == nil {
			return WechatProductionView{}, fmt.Errorf("frozen node %q missing from the production view", plan.Node.Key)
		}
		if rec.State == "completed" {
			continue
		}

		if rec.State == "pending" {
			var upstream *WechatNodeLineageRecord
			if plan.Node.RequiredUpstream != nil {
				upstream = view.nodePtr(*plan.Node.RequiredUpstream)
				if upstream == nil || upstream.State != "completed" {
					return WechatProductionView{}, fmt.Errorf("node %q upstream %q is not completed", plan.Node.Key, *plan.Node.RequiredUpstream)
				}
			}
			note, err := ComposeWechatNodeHandoffNote(req.Brief, plan.Node, upstream)
			if err != nil {
				return WechatProductionView{}, err
			}
			plan.HandoffNote = note
			plan.InputDigest = wechatHandoffInputDigest(note)

			issueID, err := o.exec.EnsureNodeIssue(ctx, workspaceID, plan)
			if err != nil {
				if errors.Is(err, ErrWechatNodeAuthorityRejected) {
					return o.failNode(ctx, workspaceID, inst, view, plan.Node.Key, "authority_rejected", actor)
				}
				return WechatProductionView{}, fmt.Errorf("ensure node %q issue: %w", plan.Node.Key, err)
			}
			dispatch, err := o.exec.DispatchNode(ctx, workspaceID, plan, issueID)
			if err != nil {
				if errors.Is(err, ErrWechatNodeAuthorityRejected) {
					return o.failNode(ctx, workspaceID, inst, view, plan.Node.Key, "authority_rejected", actor)
				}
				return WechatProductionView{}, fmt.Errorf("dispatch node %q: %w", plan.Node.Key, err)
			}
			if dispatch.CommandID != plan.CommandID || dispatch.IssueID == "" || dispatch.TaskID == "" {
				return WechatProductionView{}, fmt.Errorf("dispatch node %q returned a receipt inconsistent with the derived plan", plan.Node.Key)
			}
			if err := o.appendEvent(ctx, Event{
				InstanceID: inst.ID,
				Kind:       WechatEventNodeDispatched,
				SourceRef: fmt.Sprintf(
					"hivecrew://wechat-content/node/%s/dispatched/%s/%s/%s",
					plan.Node.Key, dispatch.CommandID, dispatch.IssueID, dispatch.TaskID,
				),
				Actor:          actor,
				IdempotencyKey: dispatch.CommandID,
			}); err != nil {
				return WechatProductionView{}, fmt.Errorf("persist node %q dispatch receipt: %w", plan.Node.Key, err)
			}
			rec.CommandID = dispatch.CommandID
			rec.IssueID = dispatch.IssueID
			rec.TaskID = dispatch.TaskID
			rec.State = "dispatched"
			rec.LiveState = "awaiting_claim"
			changed = true
		}

		obs, err := o.exec.ReadNodeExecution(ctx, workspaceID, rec.IssueID, rec.TaskID)
		if err != nil {
			return WechatProductionView{}, fmt.Errorf("read node %q execution: %w", plan.Node.Key, err)
		}
		rec.LiveState = obs.State
		switch {
		case obs.State == "failed" || obs.State == "cancelled":
			return o.failNode(ctx, workspaceID, inst, view, plan.Node.Key, "run_"+obs.State, actor)
		case obs.State == "completed" && !obs.ReceiptCompleted:
			// The task row claims completion but the server-side execution
			// receipt is missing: fail closed (P0-GATE-01), never advance.
			return o.failNode(ctx, workspaceID, inst, view, plan.Node.Key, "receipt_missing", actor)
		case obs.ReceiptCompleted:
			candidateID := obs.CandidateID
			if candidateID == "" {
				candidateID, err = o.exec.MaterializeNodeCandidate(ctx, workspaceID, rec.TaskID)
				if err != nil {
					// Blank output or a missing completed receipt fails the
					// node closed; the candidate is never synthesized.
					return o.failNode(ctx, workspaceID, inst, view, plan.Node.Key, "materialize_failed", actor)
				}
			}
			if candidateID == "" {
				return o.failNode(ctx, workspaceID, inst, view, plan.Node.Key, "candidate_missing", actor)
			}
			if err := o.appendEvent(ctx, Event{
				InstanceID:     inst.ID,
				Kind:           WechatEventNodeCompleted,
				SourceRef:      fmt.Sprintf("hivecrew://wechat-content/node/%s/candidate/%s", plan.Node.Key, candidateID),
				Actor:          actor,
				IdempotencyKey: rec.CommandID + ":completed",
			}); err != nil {
				return WechatProductionView{}, fmt.Errorf("persist node %q completion: %w", plan.Node.Key, err)
			}
			rec.CandidateID = candidateID
			rec.State = "completed"
			changed = true

			switch plan.Node.Key {
			case WechatContentNodeEditorialReviewReport:
				// Approval gate: the production halts until the Owner decides.
				if err := o.appendEvent(ctx, Event{
					InstanceID:     inst.ID,
					Kind:           WechatEventAwaitingApproval,
					SourceRef:      fmt.Sprintf("hivecrew://wechat-content/node/%s/awaiting-approval", plan.Node.Key),
					Actor:          actor,
					IdempotencyKey: inst.ID + ":awaiting-approval",
				}); err != nil {
					return WechatProductionView{}, fmt.Errorf("persist approval gate halt: %w", err)
				}
				inst.Status = StatusPaused
				inst.StageIndex = plan.Node.Order
				if err := o.store.UpdateInstance(ctx, workspaceID, inst); err != nil {
					return WechatProductionView{}, fmt.Errorf("persist approval gate halt: %w", err)
				}
				view = BuildWechatProductionView(inst, mustListEvents(ctx, o.store, workspaceID, inst.ID))
				return o.enrichCurrentNode(ctx, workspaceID, view)
			case WechatContentNodeWechatPublicationPackage:
				// Terminal for this slice: 待发布, never published.
				if err := o.appendEvent(ctx, Event{
					InstanceID:     inst.ID,
					Kind:           WechatEventAwaitingPublication,
					SourceRef:      fmt.Sprintf("hivecrew://wechat-content/node/%s/awaiting-publication", plan.Node.Key),
					Actor:          actor,
					IdempotencyKey: inst.ID + ":awaiting-publication",
				}); err != nil {
					return WechatProductionView{}, fmt.Errorf("persist awaiting-publication receipt: %w", err)
				}
				inst.Status = StatusCompleted
				inst.StageIndex = plan.Node.Order
				if err := o.store.UpdateInstance(ctx, workspaceID, inst); err != nil {
					return WechatProductionView{}, fmt.Errorf("persist awaiting-publication state: %w", err)
				}
				view = BuildWechatProductionView(inst, mustListEvents(ctx, o.store, workspaceID, inst.ID))
				return o.enrichCurrentNode(ctx, workspaceID, view)
			default:
				inst.StageIndex = plan.Node.Order + 1
				changed = true
				continue
			}
		default:
			// awaiting_claim / running: nothing more to do this poll.
			if changed {
				if err := o.store.UpdateInstance(ctx, workspaceID, inst); err != nil {
					return WechatProductionView{}, fmt.Errorf("persist wechat production progress: %w", err)
				}
			}
			return o.enrichCurrentNode(ctx, workspaceID, view)
		}
	}
	if changed {
		if err := o.store.UpdateInstance(ctx, workspaceID, inst); err != nil {
			return WechatProductionView{}, fmt.Errorf("persist wechat production progress: %w", err)
		}
	}
	return o.enrichCurrentNode(ctx, workspaceID, view)
}

// failNode records a fail-closed node halt and fails the production. It never
// synthesizes a candidate, a receipt, or an advance.
func (o *WechatProductionOrchestrator) failNode(ctx context.Context, workspaceID string, inst WorkflowInstance, view WechatProductionView, node WechatContentNodeKey, reason string, actor string) (WechatProductionView, error) {
	commandID := ""
	if rec := view.nodePtr(node); rec != nil {
		commandID = rec.CommandID
	}
	idempotency := commandID + ":failed"
	if commandID == "" {
		idempotency = inst.ID + ":" + string(node) + ":failed"
	}
	if err := o.appendEvent(ctx, Event{
		InstanceID:     inst.ID,
		Kind:           WechatEventNodeFailed,
		SourceRef:      fmt.Sprintf("hivecrew://wechat-content/node/%s/failed?reason=%s", node, reason),
		Actor:          actor,
		IdempotencyKey: idempotency,
	}); err != nil {
		return WechatProductionView{}, fmt.Errorf("persist node %q failure: %w", node, err)
	}
	inst.Status = StatusFailed
	if err := o.store.UpdateInstance(ctx, workspaceID, inst); err != nil {
		return WechatProductionView{}, fmt.Errorf("persist wechat production failure: %w", err)
	}
	return BuildWechatProductionView(inst, mustListEvents(ctx, o.store, workspaceID, inst.ID)), nil
}

// enrichCurrentNode fills the current node's fresh live observation. Read
// errors propagate: a readback that cannot observe the server-side state must
// not present a stale state as fresh.
func (o *WechatProductionOrchestrator) enrichCurrentNode(ctx context.Context, workspaceID string, view WechatProductionView) (WechatProductionView, error) {
	if view.Status != StatusRunning || view.CurrentNode == "" {
		return view, nil
	}
	rec := view.nodePtr(view.CurrentNode)
	if rec == nil || rec.State != "dispatched" || rec.IssueID == "" || rec.TaskID == "" {
		return view, nil
	}
	obs, err := o.exec.ReadNodeExecution(ctx, workspaceID, rec.IssueID, rec.TaskID)
	if err != nil {
		return WechatProductionView{}, fmt.Errorf("read node %q execution: %w", rec.Node, err)
	}
	rec.LiveState = obs.State
	if obs.CandidateID != "" && rec.CandidateID == "" {
		rec.CandidateID = obs.CandidateID
	}
	return view, nil
}

func (o *WechatProductionOrchestrator) appendEvent(ctx context.Context, ev Event) error {
	now := time.Now().UTC()
	ev.OccurredAt = now
	ev.ObservedAt = now
	return o.store.AppendEvent(ctx, ev)
}

func mustListEvents(ctx context.Context, store WechatProductionStore, workspaceID string, instanceID string) []Event {
	events, err := store.ListEvents(ctx, workspaceID, instanceID)
	if err != nil {
		return events
	}
	return events
}

// nodePtr returns the mutable record for one frozen node key.
func (v *WechatProductionView) nodePtr(node WechatContentNodeKey) *WechatNodeLineageRecord {
	for i := range v.Nodes {
		if v.Nodes[i].Node == node {
			return &v.Nodes[i]
		}
	}
	return nil
}

// BuildWechatProductionView reconstructs the read model purely from the
// durable instance row and the append-only event ledger.
func BuildWechatProductionView(inst WorkflowInstance, events []Event) WechatProductionView {
	view := WechatProductionView{
		InstanceID:        inst.ID,
		DefinitionID:      inst.DefinitionID,
		DefinitionVersion: inst.DefinitionVersion,
		ProjectID:         inst.Context.ProjectID,
		Status:            inst.Status,
		ApprovalState:     "none",
		PublicationState:  "none",
	}
	nodes := WechatContentNodeContracts()
	view.Nodes = make([]WechatNodeLineageRecord, 0, len(nodes))
	for _, node := range nodes {
		view.Nodes = append(view.Nodes, WechatNodeLineageRecord{
			Node:  node.Key,
			Order: node.Order,
			State: "pending",
		})
	}

	for _, ev := range events {
		ref := parseWechatEventRef(ev.SourceRef)
		switch ev.Kind {
		case WechatEventNodeDispatched:
			if rec := view.nodePtr(ref.node); rec != nil && rec.State == "pending" {
				rec.CommandID = ref.segment("dispatched", 0)
				rec.IssueID = ref.segment("dispatched", 1)
				rec.TaskID = ref.segment("dispatched", 2)
				rec.State = "dispatched"
				rec.LiveState = "awaiting_claim"
			}
		case WechatEventNodeCompleted:
			if rec := view.nodePtr(ref.node); rec != nil {
				rec.CandidateID = ref.segment("candidate", 0)
				rec.State = "completed"
			}
		case WechatEventNodeFailed:
			if rec := view.nodePtr(ref.node); rec != nil {
				rec.State = "failed"
				rec.Failure = ref.query("reason")
			}
		case WechatEventAwaitingApproval:
			view.ApprovalState = "awaiting"
		case WechatEventApproved:
			if rec := view.nodePtr(ref.node); rec != nil {
				rec.ReviewDecision = string(WechatReviewApproved)
			}
			if ref.node == WechatContentNodeEditorialReviewReport {
				view.ApprovalState = "approved"
			}
		case WechatEventChangesRequested:
			if rec := view.nodePtr(ref.node); rec != nil {
				rec.ReviewDecision = string(WechatReviewChangesRequested)
			}
			if ref.node == WechatContentNodeEditorialReviewReport {
				view.ApprovalState = "changes_requested"
			}
		case WechatEventAwaitingPublication:
			view.PublicationState = "awaiting_publication"
		}
	}

	for _, rec := range view.Nodes {
		if rec.State != "completed" {
			view.CurrentNode = rec.Node
			break
		}
	}
	return view
}

// wechatEventRef is the parsed form of a hivecrew://wechat-content/... event
// source ref. IDs travel in path segments so the ledger stays self-describing.
type wechatEventRef struct {
	node     WechatContentNodeKey
	segments []string
	queries  map[string]string
}

func parseWechatEventRef(sourceRef string) wechatEventRef {
	ref := wechatEventRef{queries: map[string]string{}}
	rest := strings.TrimPrefix(sourceRef, "hivecrew://wechat-content/")
	path, query, _ := strings.Cut(rest, "?")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	// parts: ["node", "<key>", ...rest] or ["<instance>", "started"]
	if len(parts) >= 2 && parts[0] == "node" {
		ref.node = WechatContentNodeKey(parts[1])
		ref.segments = parts[2:]
	}
	for _, pair := range strings.Split(query, "&") {
		k, v, ok := strings.Cut(pair, "=")
		if ok {
			ref.queries[k] = v
		}
	}
	return ref
}

// segment returns the n-th segment after the named marker, e.g. for
// "dispatched/<command>/<issue>/<task>" segment("dispatched", 1) is <issue>.
func (r wechatEventRef) segment(marker string, offset int) string {
	for i, s := range r.segments {
		if s == marker && i+1+offset < len(r.segments) {
			return r.segments[i+1+offset]
		}
	}
	return ""
}

func (r wechatEventRef) query(key string) string { return r.queries[key] }

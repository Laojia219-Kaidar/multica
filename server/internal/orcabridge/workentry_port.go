package orcabridge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/workentry"
)

// WorkEntryPort is the thin, schema-free surface the bridge needs from the
// existing Universal Work Registration Kernel. The bridge never touches the
// kernel's store or database: HiveCrew control truth and idempotency anchors
// stay entirely inside the existing workentry service/API.
type WorkEntryPort interface {
	// RegisterLinkage idempotently registers one bridge mapping scope on the
	// existing work chain and returns its work_ref. Same intent digest
	// replays the original receipt; a drifted digest conflicts.
	RegisterLinkage(ctx context.Context, in LinkageInput) (LinkageReceipt, error)

	// AppendEvidence appends one structured work event. It is idempotent by
	// (work_ref, idempotency_key): same payload replays, drifted payload
	// conflicts.
	AppendEvidence(ctx context.Context, in EvidenceInput) (EvidenceReceipt, error)

	// LookupEvidence reads one stored event by idempotency key without
	// writing, so writeback can classify replays before appending.
	LookupEvidence(ctx context.Context, workRef, idempotencyKey string) (EvidenceRecord, bool, error)
}

// LinkageInput is one bridge mapping registration on the existing chain.
type LinkageInput struct {
	// Chain identifies the HiveCrew scope being mapped.
	Chain Chain
	// Actor is the bridge automation identity; the bridge never impersonates
	// a digital employee.
	Actor ActorIdentity
	// MappingKind is run | task | dispatch.
	MappingKind string
	// Payload carries the frozen Orca identifiers once known; the linkage
	// event records the mapping evidence.
	Payload map[string]any
}

// ActorIdentity is the minimal automation identity the bridge registers as.
// ObservedAt freezes the actor snapshot time so replayed registrations digest
// identically inside the existing kernel; zero means "now" at bridge build.
type ActorIdentity struct {
	ActorID    string
	CarrierID  string
	SessionID  string
	ObservedAt time.Time
}

// LinkageReceipt is the idempotent registration result.
type LinkageReceipt struct {
	WorkRef  string
	Replayed bool
}

// EvidenceInput is one structured work event append.
type EvidenceInput struct {
	WorkRef        string
	SessionID      string
	RunID          string
	EventType      string // started | progress | checkpoint | finished ...
	IdempotencyKey string
	Payload        map[string]any
	OccurredAt     time.Time
}

// EvidenceReceipt is the append result.
type EvidenceReceipt struct {
	EventID  string
	Sequence int64
	Replayed bool
}

// EvidenceRecord is a stored event read back.
type EvidenceRecord struct {
	EventID        string
	EventType      string
	IdempotencyKey string
	Payload        map[string]any
}

// WorkEntryServiceAdapter adapts the existing workentry.Service to the port.
// It performs no schema access of its own: every call forwards to the
// existing kernel, which owns its receipts, events, and idempotency.
type WorkEntryServiceAdapter struct {
	Service *workentry.Service
	// WorkspaceID scopes every call.
	WorkspaceID string
}

// NewWorkEntryPort builds the production port over the existing kernel.
func NewWorkEntryPort(service *workentry.Service, workspaceID string) WorkEntryPort {
	return &WorkEntryServiceAdapter{Service: service, WorkspaceID: workspaceID}
}

// Mapping idempotency keys. They are derived deterministically from the
// HiveCrew chain so crashed calls replay instead of duplicating evidence.
const (
	linkageKeyPrefix = "orca-bridge/"
)

// RunLinkageKey is the idempotency key for the project -> Orca Run mapping.
func RunLinkageKey(chain Chain) string {
	return linkageKeyPrefix + "run/" + chain.WorkspaceID + "/" + chain.ProjectID
}

// TaskLinkageKey is the idempotency key for the task -> Orca Task mapping.
func TaskLinkageKey(chain Chain) string {
	return linkageKeyPrefix + "task/" + chain.WorkspaceID + "/" + chain.TaskID
}

// DispatchLinkageKey is the idempotency key for the assignment -> Orca
// Dispatch mapping of one run attempt.
func DispatchLinkageKey(chain Chain) string {
	return linkageKeyPrefix + "dispatch/" + chain.WorkspaceID + "/" + chain.AssignmentID
}

// ResultEvidenceKey is the idempotency key for one governed worker_done
// writeback, scoped to the exact Orca dispatch that produced it.
func ResultEvidenceKey(orcaDispatchID string) string {
	return linkageKeyPrefix + "result/" + orcaDispatchID
}

func (a *WorkEntryServiceAdapter) RegisterLinkage(ctx context.Context, in LinkageInput) (LinkageReceipt, error) {
	// The workentry kernel anchors work_refs on issues: its "continued" path
	// requires an existing issue and its "created" path (ConfirmCreate) would
	// implicitly create Project and Issue rows. The bridge must never create
	// company objects, so an existing Issue anchor is mandatory and creation
	// is never authorized.
	if !IsValidUUID(in.Chain.IssueID) {
		return LinkageReceipt{}, fmt.Errorf("%w: mapping kind %q has no existing issue anchor (workspace=%s project=%s)",
			ErrIssueAnchorRequired, in.MappingKind, in.Chain.WorkspaceID, in.Chain.ProjectID)
	}
	if err := in.Chain.ValidateIssueAnchoredProjectScope(); err != nil {
		return LinkageReceipt{}, err
	}
	if strings.TrimSpace(in.Actor.ActorID) == "" ||
		strings.TrimSpace(in.Actor.CarrierID) == "" ||
		strings.TrimSpace(in.Actor.SessionID) == "" {
		return LinkageReceipt{}, fmt.Errorf("%w: bridge actor identity is incomplete", ErrInvalidChain)
	}
	intent := a.linkageIntent(in)
	receipt, err := a.Service.Register(ctx, workentry.RegisterRequest{
		ResolveRequest: workentry.ResolveRequest{
			Actor:  a.actorIdentity(in.Actor),
			Intent: intent,
			// Only the issue selector is passed: the kernel's step-4 project
			// branch produces a project-level match without an issue id, which
			// its continued path rejects. Anchoring on the existing issue lets
			// the kernel derive the project lineage itself and never create.
			IssueID: in.Chain.IssueID,
		},
		// Never authorize the kernel's creation path: a linkage whose issue
		// anchor does not resolve to an existing issue fails closed instead
		// of implicitly creating Project/Issue rows.
		ConfirmCreate: false,
	})
	if err != nil {
		if errors.Is(err, workentry.ErrConflict) {
			return LinkageReceipt{}, fmt.Errorf("%w: workentry register conflict: %v", ErrMappingConflict, err)
		}
		if errors.Is(err, workentry.ErrClassificationRequired) {
			return LinkageReceipt{}, fmt.Errorf("%w: issue %s does not resolve to an existing HiveCrew issue",
				ErrIssueAnchorRequired, in.Chain.IssueID)
		}
		return LinkageReceipt{}, fmt.Errorf("orcabridge: register linkage on work chain: %w", err)
	}
	if receipt.WorkRef == "" {
		return LinkageReceipt{}, fmt.Errorf("orcabridge: workentry register returned an empty work_ref")
	}
	if receipt.Created {
		// Defensive: the kernel created rows despite ConfirmCreate=false.
		// Fail closed loudly if that ever changes.
		return LinkageReceipt{}, fmt.Errorf("orcabridge: workentry register created work rows for linkage %q; the bridge never authorizes creation", in.MappingKind)
	}
	return LinkageReceipt{WorkRef: receipt.WorkRef, Replayed: receipt.Replay.Replayed}, nil
}

// actorIdentity freezes one stable actor snapshot for the bridge. The
// kernel digests the actor into its receipt idempotency anchor, so the
// observed time must be identical across replays of the same scope.
func (a *WorkEntryServiceAdapter) actorIdentity(in ActorIdentity) workentry.WorkActorIdentityV1 {
	observedAt := in.ObservedAt
	if observedAt.IsZero() {
		observedAt = time.Now()
	}
	return workentry.WorkActorIdentityV1{
		ActorType:   workentry.ActorAutomationService,
		ActorID:     in.ActorID,
		CarrierID:   in.CarrierID,
		SessionID:   in.SessionID,
		WorkspaceID: a.WorkspaceID,
		ObservedAt:  observedAt.UTC().Format(time.RFC3339Nano),
	}
}

// linkageIntent builds the deterministic kernel intent for one mapping
// scope. The repo/revision/branch anchor the dedupe key to the HiveCrew
// project, while GoalRef pins the mapping scope itself.
func (a *WorkEntryServiceAdapter) linkageIntent(in LinkageInput) workentry.WorkIntentV1 {
	chain := in.Chain
	goalRef := "hivecrew://orca-bridge/" + in.MappingKind + "/" + chain.ProjectID
	if in.MappingKind == "task" || in.MappingKind == "dispatch" {
		goalRef += "/" + chain.TaskID
	}
	if in.MappingKind == "dispatch" {
		goalRef += "/" + chain.AssignmentID
	}
	return workentry.WorkIntentV1{
		OwnerIntent:             "HiveCrew Orca bridge mapping " + in.MappingKind,
		GoalRef:                 goalRef,
		Objective:               "Map HiveCrew control truth onto the Orca execution plane",
		ExpectedHumanResult:     "One visible Orca object per HiveCrew object with governed result writeback",
		Repo:                    "hivecrew",
		BaselineRevision:        ContractVersion,
		BranchOrWorktree:        "orca-bridge/" + in.MappingKind + "/" + chain.ProjectID,
		ReadScope:               []string{"orca:run", "orca:task", "orca:dispatch"},
		WriteScope:              []string{"orca:run-create", "orca:task-create", "orca:worker-start"},
		ExpectedOutcomes:        []string{"mapped", "dispatched", "result-written-back"},
		CandidateFormalBoundary: workentry.BoundaryCandidate,
	}
}

func (a *WorkEntryServiceAdapter) AppendEvidence(ctx context.Context, in EvidenceInput) (EvidenceReceipt, error) {
	observedAt := in.OccurredAt
	if observedAt.IsZero() {
		observedAt = time.Now()
	}
	// Defense-in-depth: no credential-like content may reach the existing
	// evidence ledger, whatever the caller supplied. Redaction is
	// deterministic so idempotent replays digest identically.
	redactedPayload := RedactStringMap(in.Payload)
	event := workentry.WorkEventV1{
		WorkRef:        in.WorkRef,
		SessionID:      in.SessionID,
		RunID:          in.RunID,
		EventType:      workentry.WorkEventType(in.EventType),
		EventPayload:   redactedPayload,
		IdempotencyKey: in.IdempotencyKey,
		OccurredAt:     observedAt.UTC().Format(time.RFC3339Nano),
		ObservedAt:     observedAt.UTC().Format(time.RFC3339Nano),
	}
	if err := workentry.ValidateWorkEvent(event); err != nil {
		return EvidenceReceipt{}, fmt.Errorf("%w: %v", ErrInvalidChain, err)
	}
	result, err := a.Service.Event(ctx, event)
	if err != nil {
		if errors.Is(err, workentry.ErrConflict) {
			return EvidenceReceipt{}, fmt.Errorf("%w: workentry event conflict: %v", ErrEvidenceConflict, err)
		}
		return EvidenceReceipt{}, fmt.Errorf("orcabridge: append evidence on work chain: %w", err)
	}
	return EvidenceReceipt{EventID: result.EventID, Sequence: result.Sequence, Replayed: result.Replayed}, nil
}

func (a *WorkEntryServiceAdapter) LookupEvidence(ctx context.Context, workRef, idempotencyKey string) (EvidenceRecord, bool, error) {
	result, err := a.Service.Replay(ctx, workentry.ReplayRequest{
		WorkspaceID:    a.WorkspaceID,
		IdempotencyKey: idempotencyKey,
		Kind:           "event",
		WorkRef:        workRef,
	})
	if err != nil {
		if errors.Is(err, workentry.ErrNotFound) {
			return EvidenceRecord{}, false, nil
		}
		return EvidenceRecord{}, false, fmt.Errorf("orcabridge: lookup evidence on work chain: %w", err)
	}
	if result.Event == nil {
		return EvidenceRecord{}, false, nil
	}
	return EvidenceRecord{
		EventID:        result.Event.ID,
		EventType:      string(result.Event.EventType),
		IdempotencyKey: result.Event.IdempotencyKey,
		Payload:        result.Event.EventPayload,
	}, true, nil
}

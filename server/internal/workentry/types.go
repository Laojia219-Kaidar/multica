// Package workentry implements the Universal Work Registration Kernel: the
// versioned actor/intent/receipt/event/handoff/completion contracts and the
// resolve → register/start → event/heartbeat → handoff/finish → reconcile/
// attach/ignore/replay service semantics for the HIVECREW-UNIVERSAL-
// DEVELOPMENT-ENTRY-PROJECT-OS-V1 Goal.
//
// Field shapes below are frozen by WORK-ACTOR-CONTRACT.md §4 and must not be
// renamed or re-typed without a contract version bump.
package workentry

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// ActorType is the closed five-value actor enumeration (WORK-ACTOR-CONTRACT §2).
// Unknown values fail closed in ValidateActorIdentity.
type ActorType string

const (
	ActorRegisteredEmployee    ActorType = "registered_employee"
	ActorExternalAgent         ActorType = "external_agent"
	ActorHumanOperator         ActorType = "human_operator"
	ActorAutomationService     ActorType = "automation_service"
	ActorObservedUnclaimedActor ActorType = "observed_unclaimed_actor"
)

var actorTypes = []ActorType{
	ActorRegisteredEmployee,
	ActorExternalAgent,
	ActorHumanOperator,
	ActorAutomationService,
	ActorObservedUnclaimedActor,
}

// employeeIDPattern mirrors org_directory_types.go's DE-* pattern.
var employeeIDPattern = regexp.MustCompile(`^DE-[A-Z0-9][A-Z0-9_-]{0,126}$`)

// CandidateFormalBoundary marks whether work is a candidate (default) or formal
// (requires additional authority).
type CandidateFormalBoundary string

const (
	BoundaryCandidate CandidateFormalBoundary = "candidate"
	BoundaryFormal    CandidateFormalBoundary = "formal"
)

// WorkActorIdentityV1 is the immutable identity snapshot of one execution
// session (WORK-ACTOR-CONTRACT §4.1).
type WorkActorIdentityV1 struct {
	ActorType    ActorType `json:"actor_type"`
	ActorID      string    `json:"actor_id"`
	EmployeeID   string    `json:"employee_id,omitempty"`
	HumanSponsor string    `json:"human_sponsor,omitempty"`
	CarrierID    string    `json:"carrier_id"`
	RuntimeID    string    `json:"runtime_id,omitempty"`
	ModelRef     string    `json:"model_ref,omitempty"`
	BaseID       string    `json:"base_id,omitempty"`
	HostID       string    `json:"host_id,omitempty"`
	SessionID    string    `json:"session_id"`
	WorkspaceID  string    `json:"workspace_id"`
	ObservedAt   string    `json:"observed_at"`
}

// ValidateActorIdentity applies the closed-enumeration and required-field rules
// from WORK-ACTOR-CONTRACT §2/§4.1. external_agent is NOT forced to carry an
// employee_id (VC-02).
func ValidateActorIdentity(a WorkActorIdentityV1) error {
	switch a.ActorType {
	case ActorRegisteredEmployee, ActorExternalAgent, ActorHumanOperator,
		ActorAutomationService, ActorObservedUnclaimedActor:
	default:
		return fmt.Errorf("actor_type %q is not a closed five-value actor type", a.ActorType)
	}
	if strings.TrimSpace(a.ActorID) == "" && a.ActorType != ActorExternalAgent && a.ActorType != ActorObservedUnclaimedActor {
		return fmt.Errorf("actor_id is required for actor_type %q", a.ActorType)
	}
	if a.ActorType == ActorRegisteredEmployee {
		if !employeeIDPattern.MatchString(a.EmployeeID) {
			return fmt.Errorf("registered_employee requires an employee_id matching DE-*")
		}
	}
	if strings.TrimSpace(a.CarrierID) == "" {
		return fmt.Errorf("carrier_id is required")
	}
	if strings.TrimSpace(a.SessionID) == "" {
		return fmt.Errorf("session_id is required")
	}
	if strings.TrimSpace(a.WorkspaceID) == "" {
		return fmt.Errorf("workspace_id is required")
	}
	if _, err := ParseObservedAt(a.ObservedAt); err != nil {
		return err
	}
	return nil
}

// ParseObservedAt accepts RFC3339 with optional fractional seconds.
func ParseObservedAt(s string) (time.Time, error) {
	if strings.TrimSpace(s) == "" {
		return time.Time{}, fmt.Errorf("observed_at is required (RFC3339)")
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("observed_at must be RFC3339: %w", err)
	}
	return t, nil
}

// WorkIntentV1 is the pre-registration intent declaration used for dedupe and
// ownership classification (WORK-ACTOR-CONTRACT §4.2).
type WorkIntentV1 struct {
	OwnerIntent              string                  `json:"owner_intent"`
	GoalRef                  string                  `json:"goal_ref"`
	ExternalCampaignRef      string                  `json:"external_campaign_ref,omitempty"`
	Objective                string                  `json:"objective"`
	ExpectedHumanResult      string                  `json:"expected_human_result"`
	Repo                     string                  `json:"repo"`
	BaselineRevision         string                  `json:"baseline_revision"`
	BranchOrWorktree         string                  `json:"branch_or_worktree"`
	ReadScope                []string                `json:"read_scope"`
	WriteScope               []string                `json:"write_scope"`
	MutexKeys                []string                `json:"mutex_keys,omitempty"`
	ExpectedOutcomes         []string                `json:"expected_outcomes"`
	CandidateFormalBoundary  CandidateFormalBoundary `json:"candidate_formal_boundary"`
}

// ValidateIntent enforces the required fields of WorkIntentV1.
func ValidateIntent(i WorkIntentV1) error {
	required := map[string]string{
		"owner_intent":          i.OwnerIntent,
		"goal_ref":              i.GoalRef,
		"objective":             i.Objective,
		"expected_human_result": i.ExpectedHumanResult,
		"repo":                  i.Repo,
		"baseline_revision":     i.BaselineRevision,
		"branch_or_worktree":    i.BranchOrWorktree,
	}
	for name, v := range required {
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if len(i.ReadScope) == 0 {
		return fmt.Errorf("read_scope is required")
	}
	if len(i.WriteScope) == 0 {
		return fmt.Errorf("write_scope is required")
	}
	if len(i.ExpectedOutcomes) == 0 {
		return fmt.Errorf("expected_outcomes is required")
	}
	switch i.CandidateFormalBoundary {
	case "", BoundaryCandidate:
	case BoundaryFormal:
	default:
		return fmt.Errorf("candidate_formal_boundary must be candidate or formal")
	}
	return nil
}

// ResolutionDecision is the frozen register/resolve disposition.
type ResolutionDecision string

const (
	DecisionCreated                ResolutionDecision = "created"
	DecisionContinued              ResolutionDecision = "continued"
	DecisionClassificationRequired ResolutionDecision = "classification_required"
)

// ReplayInfo is the exact-replay provenance carried by every receipt.
type ReplayInfo struct {
	Replayed           bool   `json:"replayed"`
	OriginalReceiptRef string `json:"original_receipt_ref,omitempty"`
	ObservedAt         string `json:"observed_at"`
}

// WorkRegistrationReceiptV1 is the immutable, idempotently-replayable
// registration receipt (WORK-ACTOR-CONTRACT §4.3).
type WorkRegistrationReceiptV1 struct {
	WorkRef                 string                `json:"work_ref"`
	ProjectID               string                `json:"project_id,omitempty"`
	IssueID                 string                `json:"issue_id,omitempty"`
	TaskID                  string                `json:"task_id,omitempty"`
	ActorIdentity           WorkActorIdentityV1   `json:"actor_identity"`
	ResolutionDecision      ResolutionDecision    `json:"resolution_decision"`
	DedupeKey               string                `json:"dedupe_key"`
	DedupeDigest            string                `json:"dedupe_digest"`
	Created                 bool                  `json:"created"`
	Continued               bool                  `json:"continued"`
	ClassificationRequired  bool                  `json:"classification_required"`
	Replay                  ReplayInfo            `json:"replay"`
}

// WorkEventType is the closed event-type enumeration (WORK-ACTOR-CONTRACT §4.4).
type WorkEventType string

const (
	EventStarted          WorkEventType = "started"
	EventProgress         WorkEventType = "progress"
	EventToolFileScope    WorkEventType = "tool_file_scope"
	EventCheckpoint       WorkEventType = "checkpoint"
	EventBlocked          WorkEventType = "blocked"
	EventResumed          WorkEventType = "resumed"
	EventCandidateReady   WorkEventType = "candidate_ready"
	EventReviewRequested  WorkEventType = "review_requested"
	EventRepairRequested  WorkEventType = "repair_requested"
	EventHandoff          WorkEventType = "handoff"
	EventFinished         WorkEventType = "finished"
	EventAbandonedRecovered WorkEventType = "abandoned_recovered"
)

var workEventTypes = map[WorkEventType]bool{
	EventStarted: true, EventProgress: true, EventToolFileScope: true,
	EventCheckpoint: true, EventBlocked: true, EventResumed: true,
	EventCandidateReady: true, EventReviewRequested: true,
	EventRepairRequested: true, EventHandoff: true, EventFinished: true,
	EventAbandonedRecovered: true,
}

// WorkEventV1 is one append-only structured work event (WORK-ACTOR-CONTRACT
// §4.4). event_payload must never carry secrets or chain-of-thought.
type WorkEventV1 struct {
	EventID        string         `json:"event_id"`
	WorkRef        string         `json:"work_ref"`
	SessionID      string         `json:"session_id"`
	RunID          string         `json:"run_id,omitempty"`
	EventType      WorkEventType  `json:"event_type"`
	EventPayload   map[string]any `json:"event_payload"`
	BlockerReason  string         `json:"blocker_reason,omitempty"`
	Receiver       string         `json:"receiver,omitempty"`
	IdempotencyKey string         `json:"idempotency_key"`
	OccurredAt     string         `json:"occurred_at"`
	ObservedAt     string         `json:"observed_at"`
}

// ValidateWorkEvent enforces the closed event-type enumeration and required
// fields, including blocker_reason for blocked events.
func ValidateWorkEvent(e WorkEventV1) error {
	if !workEventTypes[e.EventType] {
		return fmt.Errorf("event_type %q is not a closed work event type", e.EventType)
	}
	if strings.TrimSpace(e.WorkRef) == "" {
		return fmt.Errorf("work_ref is required")
	}
	if strings.TrimSpace(e.SessionID) == "" {
		return fmt.Errorf("session_id is required")
	}
	if strings.TrimSpace(e.IdempotencyKey) == "" {
		return fmt.Errorf("idempotency_key is required")
	}
	if e.EventType == EventBlocked && strings.TrimSpace(e.BlockerReason) == "" {
		return fmt.Errorf("blocker_reason is required for blocked events")
	}
	if _, err := ParseObservedAt(e.ObservedAt); err != nil {
		return err
	}
	if strings.TrimSpace(e.OccurredAt) == "" {
		return fmt.Errorf("occurred_at is required")
	}
	return nil
}

// WorkHandoffV1 is one completed candidate handoff package
// (WORK-ACTOR-CONTRACT §4.5).
type WorkHandoffV1 struct {
	WorkRef           string                `json:"work_ref"`
	Revision          string                `json:"revision"`
	BranchOrWorktree  string                `json:"branch_or_worktree"`
	DiffFiles         []string              `json:"diff_files"`
	Tests             []EvidenceItem        `json:"tests"`
	BrowserEvidence   []EvidenceItem        `json:"browser_evidence,omitempty"`
	APIEvidence       []EvidenceItem        `json:"api_evidence,omitempty"`
	DBEvidence        []EvidenceItem        `json:"db_evidence,omitempty"`
	ArtifactRefs      []string              `json:"artifact_refs"`
	RemainingBlockers []string              `json:"remaining_blockers,omitempty"`
	Receiver          string                `json:"receiver,omitempty"`
	NextAction        string                `json:"next_action"`
}

// EvidenceItem records one executed check with its result and evidence ref.
type EvidenceItem struct {
	Command     string `json:"command"`
	Result      string `json:"result"`
	EvidenceRef string `json:"evidence_ref,omitempty"`
}

// ReviewDecision is the independent reviewer disposition (PASS|REVISE).
type ReviewDecision string

const (
	ReviewPass   ReviewDecision = "PASS"
	ReviewRevise ReviewDecision = "REVISE"
)

// ProjectLifecycleConsequence is the post-completion lifecycle action.
type ProjectLifecycleConsequence string

const (
	LifecycleContinue     ProjectLifecycleConsequence = "continue"
	LifecyclePauseDispatch ProjectLifecycleConsequence = "pause_dispatch"
	LifecycleResume       ProjectLifecycleConsequence = "resume"
	LifecycleClose        ProjectLifecycleConsequence = "close"
	LifecycleSupersede    ProjectLifecycleConsequence = "supersede"
)

// WorkCompletionV1 is the candidate completion + independent review contract
// (WORK-ACTOR-CONTRACT §4.6). finish() always routes into review; it never
// auto-passes.
type WorkCompletionV1 struct {
	WorkRef                    string                      `json:"work_ref"`
	CompletionCandidate        CompletionCandidate         `json:"completion_candidate"`
	Review                     CompletionReview            `json:"review"`
	RepairLineage              []RepairLineageEntry        `json:"repair_lineage,omitempty"`
	AcceptedArtifact           *AcceptedArtifact           `json:"accepted_artifact,omitempty"`
	ProjectLifecycleConsequence ProjectLifecycleConsequence `json:"project_lifecycle_consequence"`
}

// CompletionCandidate identifies the candidate artifact under review.
type CompletionCandidate struct {
	ArtifactRef string `json:"artifact_ref"`
	Digest      string `json:"digest"`
	Revision    string `json:"revision"`
}

// CompletionReview is the independent review decision block.
type CompletionReview struct {
	ReviewerActorID string         `json:"reviewer_actor_id"`
	Decision        ReviewDecision `json:"decision"`
	EvidenceRefs    []string       `json:"evidence_refs"`
	ReviewedAt      string         `json:"reviewed_at"`
}

// RepairLineageEntry records one review → rework edge.
type RepairLineageEntry struct {
	ReviewID      string `json:"review_id"`
	ReworkTaskID  string `json:"rework_task_id"`
	FromCandidate string `json:"from_candidate"`
	ToCandidate   string `json:"to_candidate"`
}

// AcceptedArtifact records the promoted formal artifact (filled after PASS).
type AcceptedArtifact struct {
	FormalArtifactRef string `json:"formal_artifact_ref"`
	FormalVisible     bool   `json:"formal_visible"`
	PromotionID       string `json:"promotion_id"`
	Sequence          int64  `json:"sequence"`
}

// ValidateCompletion enforces the minimal completion contract.
func ValidateCompletion(c WorkCompletionV1) error {
	if strings.TrimSpace(c.WorkRef) == "" {
		return fmt.Errorf("work_ref is required")
	}
	if strings.TrimSpace(c.CompletionCandidate.ArtifactRef) == "" {
		return fmt.Errorf("completion_candidate.artifact_ref is required")
	}
	if strings.TrimSpace(c.CompletionCandidate.Digest) == "" {
		return fmt.Errorf("completion_candidate.digest is required")
	}
	switch c.Review.Decision {
	case "", ReviewPass, ReviewRevise:
	default:
		return fmt.Errorf("review.decision must be PASS or REVISE")
	}
	switch c.ProjectLifecycleConsequence {
	case "", LifecycleContinue, LifecyclePauseDispatch, LifecycleResume, LifecycleClose, LifecycleSupersede:
	default:
		return fmt.Errorf("project_lifecycle_consequence is invalid")
	}
	return nil
}

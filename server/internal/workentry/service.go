package workentry

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Service implements the Universal Work Registration Kernel verbs against a
// Store. It never creates a second task/run table set and never auto-passes a
// completion candidate.
type Service struct {
	store Store
	now   func() time.Time
}

// NewService constructs the kernel service. now defaults to time.Now when nil
// (tests may inject a fixed clock).
func NewService(store Store) *Service {
	return &Service{store: store, now: time.Now}
}

func (s *Service) nowString() string {
	return s.now().UTC().Format(time.RFC3339Nano)
}

// invalid wraps a validation failure as ErrInvalidRequest so HTTP/CLI layers
// can classify it without sniffing error strings.
func invalid(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %v", ErrInvalidRequest, err)
}

// ---------------------------------------------------------------------------
// register / replay
// ---------------------------------------------------------------------------

// RegisterRequest is register/start input. ConfirmCreate authorizes step-7
// creation only when resolution could not confirm ownership.
type RegisterRequest struct {
	ResolveRequest
	ConfirmCreate bool `json:"confirm_create"`
}

// Register implements the idempotent register/start verb:
//
//	same dedupe_key + same digest → replay the original receipt (replayed=true)
//	same dedupe_key + diff digest  → ErrConflict (409)
//	classification_required        → ErrClassificationRequired (no creation)
func (s *Service) Register(ctx context.Context, req RegisterRequest) (WorkRegistrationReceiptV1, error) {
	if s == nil || s.store == nil {
		return WorkRegistrationReceiptV1{}, ErrUnavailable
	}
	if err := ValidateActorIdentity(req.Actor); err != nil {
		return WorkRegistrationReceiptV1{}, invalid(err)
	}
	if err := ValidateIntent(req.Intent); err != nil {
		return WorkRegistrationReceiptV1{}, invalid(err)
	}
	ws := req.Actor.WorkspaceID
	key := DedupeKey(ws, req.Intent.GoalRef, req.Intent.Repo, req.Intent.BaselineRevision, req.Intent.BranchOrWorktree)
	digest, err := ReceiptDigest(req.Actor, req.Intent)
	if err != nil {
		return WorkRegistrationReceiptV1{}, err
	}

	// Idempotency anchor: same key + same digest → replay; diff digest → 409.
	existing, err := s.store.GetReceipt(ctx, ws, key)
	if err != nil {
		return WorkRegistrationReceiptV1{}, fmt.Errorf("read work registration receipt: %w", err)
	}
	if existing != nil {
		if existing.Digest != digest {
			return WorkRegistrationReceiptV1{}, ErrConflict
		}
		return receiptFromRecord(*existing, true, s.nowString()), nil
	}

	resolved, err := s.resolveInternal(ctx, req.ResolveRequest)
	if err != nil {
		return WorkRegistrationReceiptV1{}, err
	}

	if resolved.ResolutionDecision == DecisionClassificationRequired {
		receipt := WorkRegistrationReceiptV1{
			WorkRef:                FormatWorkRef(ws, "", "", ""),
			ActorIdentity:          req.Actor,
			ResolutionDecision:     DecisionClassificationRequired,
			DedupeKey:              key,
			DedupeDigest:           digest,
			ClassificationRequired: true,
			Replay:                 ReplayInfo{Replayed: false, ObservedAt: s.nowString()},
		}
		if !req.ConfirmCreate {
			// Never create on an unconfirmed classification (VC-07).
			return receipt, ErrClassificationRequired
		}
	}

	return s.create(ctx, req, key, digest)
}

// create performs the created/continued projection and persists the immutable
// receipt anchor. It is only reached when ownership is confirmed (continued)
// or creation is explicitly authorized (ConfirmCreate).
func (s *Service) create(ctx context.Context, req RegisterRequest, key, digest string) (WorkRegistrationReceiptV1, error) {
	ws := req.Actor.WorkspaceID
	resolved, err := s.resolveInternal(ctx, req.ResolveRequest)
	if err != nil {
		return WorkRegistrationReceiptV1{}, err
	}

	receipt := WorkRegistrationReceiptV1{
		ActorIdentity: req.Actor,
		DedupeKey:     key,
		DedupeDigest:  digest,
		Replay:        ReplayInfo{Replayed: false, ObservedAt: s.nowString()},
	}

	switch resolved.ResolutionDecision {
	case DecisionContinued:
		receipt.ResolutionDecision = DecisionContinued
		receipt.Continued = true
		receipt.ProjectID, receipt.IssueID, receipt.TaskID = lineageFromMatches(resolved.Matches)
		if receipt.IssueID == "" {
			return WorkRegistrationReceiptV1{}, ErrInvalidRequest
		}
		receipt.WorkRef = FormatWorkRef(ws, receipt.ProjectID, receipt.IssueID, receipt.TaskID)
	case DecisionClassificationRequired:
		// Only reachable when ConfirmCreate is true.
		result, err := s.store.CreateWork(ctx, CreateWorkRequest{
			WorkspaceID: ws,
			Title:       strings.TrimSpace(req.Intent.Objective),
			Description: strings.TrimSpace(req.Intent.ExpectedHumanResult),
			ProjectID:   req.ProjectID,
			IssueID:     req.IssueID,
		})
		if err != nil {
			return WorkRegistrationReceiptV1{}, fmt.Errorf("create work projection: %w", err)
		}
		receipt.ResolutionDecision = DecisionCreated
		receipt.Created = true
		receipt.ProjectID = result.ProjectID
		receipt.IssueID = result.IssueID
		receipt.WorkRef = FormatWorkRef(ws, result.ProjectID, result.IssueID, "")

		// Reuse external_work_order_link when the intent carries a Goal/WorkOrder
		// selector, so a later exact replay continues into this Issue.
		if strings.TrimSpace(req.Intent.GoalRef) != "" {
			if err := s.store.PutWorkOrderLink(ctx, ExternalWorkOrderLink{
				WorkspaceID:    ws,
				WorkOrderRef:   req.Intent.GoalRef,
				LinkedRevision: req.Intent.BaselineRevision,
				LinkedDigest:   digest,
				IssueID:        receipt.IssueID,
			}); err != nil && err != ErrConflict {
				return WorkRegistrationReceiptV1{}, fmt.Errorf("persist external work order link: %w", err)
			}
		}
	default:
		return WorkRegistrationReceiptV1{}, ErrInvalidRequest
	}

	if err := s.store.PutReceipt(ctx, ReceiptRecord{
		WorkspaceID: ws,
		DedupeKey:   key,
		Digest:      digest,
		WorkRef:     receipt.WorkRef,
		ProjectID:   receipt.ProjectID,
		IssueID:     receipt.IssueID,
		TaskID:      receipt.TaskID,
		Decision:    receipt.ResolutionDecision,
		Actor:       req.Actor,
		Intent:      req.Intent,
		CreatedAt:   s.nowString(),
	}); err != nil {
		if err == ErrConflict {
			return WorkRegistrationReceiptV1{}, ErrConflict
		}
		return WorkRegistrationReceiptV1{}, fmt.Errorf("persist work registration receipt: %w", err)
	}
	return receipt, nil
}

// lineageFromMatches extracts the richest lineage from resolve matches.
func lineageFromMatches(matches []Match) (projectID, issueID, taskID string) {
	for _, m := range matches {
		if m.ProjectID != "" {
			projectID = m.ProjectID
		}
		if m.IssueID != "" {
			issueID = m.IssueID
		}
		if m.TaskID != "" {
			taskID = m.TaskID
		}
	}
	return projectID, issueID, taskID
}

func receiptFromRecord(r ReceiptRecord, replayed bool, observedAt string) WorkRegistrationReceiptV1 {
	receipt := WorkRegistrationReceiptV1{
		WorkRef:            r.WorkRef,
		ProjectID:          r.ProjectID,
		IssueID:            r.IssueID,
		TaskID:             r.TaskID,
		ActorIdentity:      r.Actor,
		ResolutionDecision: r.Decision,
		DedupeKey:          r.DedupeKey,
		DedupeDigest:       r.Digest,
		Replay: ReplayInfo{
			Replayed:           replayed,
			OriginalReceiptRef: r.WorkRef,
			ObservedAt:         observedAt,
		},
	}
	switch r.Decision {
	case DecisionCreated:
		receipt.Created = true
	case DecisionContinued:
		receipt.Continued = true
	case DecisionClassificationRequired:
		receipt.ClassificationRequired = true
	}
	return receipt
}

// resolveInternal reuses the dedupe chain after validation.
func (s *Service) resolveInternal(ctx context.Context, req ResolveRequest) (ResolveResult, error) {
	digest, err := ReceiptDigest(req.Actor, req.Intent)
	if err != nil {
		return ResolveResult{}, err
	}
	key := DedupeKey(req.Actor.WorkspaceID, req.Intent.GoalRef, req.Intent.Repo, req.Intent.BaselineRevision, req.Intent.BranchOrWorktree)
	return s.resolve(ctx, req, key, digest)
}

// ---------------------------------------------------------------------------
// start / status / heartbeat / event
// ---------------------------------------------------------------------------

// StartRequest marks execution start for a work_ref.
type StartRequest struct {
	WorkRef     string `json:"work_ref"`
	SessionID   string `json:"session_id"`
	RunID       string `json:"run_id"`
	ActorID     string `json:"actor_id"`
	WorkspaceID string `json:"workspace_id"`
}

// EventResult is the append response carrying the stored sequence.
type EventResult struct {
	EventID  string `json:"event_id"`
	Sequence int64  `json:"sequence"`
	Replayed bool   `json:"replayed"`
}

// Start appends a started event for the given work.
func (s *Service) Start(ctx context.Context, req StartRequest) (EventResult, error) {
	if strings.TrimSpace(req.WorkRef) == "" {
		return EventResult{}, ErrInvalidRequest
	}
	now := s.nowString()
	event := WorkEventV1{
		EventID:        newID(),
		WorkRef:        req.WorkRef,
		SessionID:      req.SessionID,
		RunID:          req.RunID,
		EventType:      EventStarted,
		EventPayload:   map[string]any{"actor_id": req.ActorID},
		IdempotencyKey: "start:" + req.WorkRef + ":" + req.SessionID + ":" + req.RunID,
		OccurredAt:     now,
		ObservedAt:     now,
	}
	return s.Event(ctx, event)
}

// StatusRequest selects the work to describe.
type StatusRequest struct {
	WorkRef     string `json:"work_ref"`
	WorkspaceID string `json:"workspace_id"`
}

// StatusResult is the read-only status projection.
type StatusResult struct {
	WorkRef   string             `json:"work_ref"`
	Found     bool               `json:"found"`
	ProjectID string             `json:"project_id,omitempty"`
	IssueID   string             `json:"issue_id,omitempty"`
	TaskID    string             `json:"task_id,omitempty"`
	Decision  ResolutionDecision `json:"resolution_decision,omitempty"`
}

// Status reads the stored receipt for a work_ref (read-only).
func (s *Service) Status(ctx context.Context, req StatusRequest) (StatusResult, error) {
	if s == nil || s.store == nil {
		return StatusResult{}, ErrUnavailable
	}
	r, err := s.store.FindReceiptByWorkRef(ctx, req.WorkspaceID, req.WorkRef)
	if err != nil {
		return StatusResult{}, err
	}
	if r == nil {
		return StatusResult{WorkRef: req.WorkRef, Found: false}, nil
	}
	return StatusResult{
		WorkRef: r.WorkRef, Found: true, ProjectID: r.ProjectID, IssueID: r.IssueID,
		TaskID: r.TaskID, Decision: r.Decision,
	}, nil
}

// HeartbeatResult acknowledges a heartbeat.
type HeartbeatResult struct {
	Accepted        bool   `json:"accepted"`
	LastHeartbeatAt string `json:"last_heartbeat_at"`
}

// Heartbeat upserts terminal/现场 presence (reused terminal_presence table).
func (s *Service) Heartbeat(ctx context.Context, hb HeartbeatRecord) (HeartbeatResult, error) {
	if s == nil || s.store == nil {
		return HeartbeatResult{}, ErrUnavailable
	}
	if strings.TrimSpace(hb.WorkspaceID) == "" {
		return HeartbeatResult{}, ErrInvalidRequest
	}
	if strings.TrimSpace(hb.HeartbeatAt) == "" {
		hb.HeartbeatAt = s.nowString()
	}
	if err := s.store.UpsertHeartbeat(ctx, hb); err != nil {
		return HeartbeatResult{}, err
	}
	return HeartbeatResult{Accepted: true, LastHeartbeatAt: hb.HeartbeatAt}, nil
}

// Event appends one structured work event (idempotent by
// (work_ref, idempotency_key)). No chain-of-thought or secret payloads.
func (s *Service) Event(ctx context.Context, event WorkEventV1) (EventResult, error) {
	if s == nil || s.store == nil {
		return EventResult{}, ErrUnavailable
	}
	if err := ValidateWorkEvent(event); err != nil {
		return EventResult{}, invalid(err)
	}
	if event.EventID == "" {
		event.EventID = newID()
	}
	if event.ObservedAt == "" {
		event.ObservedAt = s.nowString()
	}
	rec := EventRecord{
		ID:             event.EventID,
		WorkspaceID:    workspaceFromWorkRef(event.WorkRef),
		WorkRef:        event.WorkRef,
		SessionID:      event.SessionID,
		RunID:          event.RunID,
		EventType:      event.EventType,
		EventPayload:   event.EventPayload,
		BlockerReason:  event.BlockerReason,
		Receiver:       event.Receiver,
		IdempotencyKey: event.IdempotencyKey,
		OccurredAt:     event.OccurredAt,
		ObservedAt:     event.ObservedAt,
	}
	stored, err := s.store.AppendEvent(ctx, rec)
	if err != nil {
		return EventResult{}, err
	}
	return EventResult{EventID: stored.ID, Sequence: stored.Sequence, Replayed: stored.ID != event.EventID}, nil
}

// workspaceFromWorkRef extracts the workspace id from hivecrew://<ws>/...
func workspaceFromWorkRef(workRef string) string {
	rest := strings.TrimPrefix(workRef, "hivecrew://")
	if idx := strings.Index(rest, "/"); idx > 0 {
		return rest[:idx]
	}
	return ""
}

// ---------------------------------------------------------------------------
// handoff / finish
// ---------------------------------------------------------------------------

// HandoffResult is the handoff acknowledgment. handoff never auto-passes.
type HandoffResult struct {
	HandoffID    string `json:"handoff_id"`
	WorkRef      string `json:"work_ref"`
	EventID      string `json:"event_id"`
	ReviewRouted bool   `json:"review_routed"`
	AutoPassed   bool   `json:"auto_passed"`
}

// Handoff accepts a candidate handoff package and appends a structured handoff
// event. It is candidate-only evidence; it never promotes to formal.
func (s *Service) Handoff(ctx context.Context, pkg WorkHandoffV1) (HandoffResult, error) {
	if s == nil || s.store == nil {
		return HandoffResult{}, ErrUnavailable
	}
	if strings.TrimSpace(pkg.WorkRef) == "" {
		return HandoffResult{}, ErrInvalidRequest
	}
	if strings.TrimSpace(pkg.Revision) == "" {
		return HandoffResult{}, ErrInvalidRequest
	}
	now := s.nowString()
	event := WorkEventV1{
		EventID:        newID(),
		WorkRef:        pkg.WorkRef,
		SessionID:      "handoff",
		EventType:      EventHandoff,
		EventPayload:   map[string]any{"revision": pkg.Revision, "branch_or_worktree": pkg.BranchOrWorktree, "next_action": pkg.NextAction},
		Receiver:       pkg.Receiver,
		IdempotencyKey: "handoff:" + pkg.WorkRef + ":" + pkg.Revision,
		OccurredAt:     now,
		ObservedAt:     now,
	}
	ev, err := s.Event(ctx, event)
	if err != nil {
		return HandoffResult{}, err
	}
	if err := s.store.SaveHandoff(ctx, HandoffRecord{
		WorkspaceID: workspaceFromWorkRef(pkg.WorkRef),
		WorkRef:     pkg.WorkRef,
		Package:     pkg,
	}); err != nil {
		return HandoffResult{}, err
	}
	return HandoffResult{HandoffID: newID(), WorkRef: pkg.WorkRef, EventID: ev.EventID, ReviewRouted: true, AutoPassed: false}, nil
}

// CompletionResult is the finish acknowledgment. finish always routes to
// independent review; PASS is only recorded after a reviewer decision.
type CompletionResult struct {
	WorkRef      string `json:"work_ref"`
	EventID      string `json:"event_id"`
	ReviewRouted bool   `json:"review_routed"`
	AutoPassed   bool   `json:"auto_passed"`
}

// Finish submits a completion candidate and routes it to independent review.
// The submitted candidate is never auto-accepted.
func (s *Service) Finish(ctx context.Context, c WorkCompletionV1) (CompletionResult, error) {
	if s == nil || s.store == nil {
		return CompletionResult{}, ErrUnavailable
	}
	if err := ValidateCompletion(c); err != nil {
		return CompletionResult{}, invalid(err)
	}
	now := s.nowString()
	event := WorkEventV1{
		EventID:        newID(),
		WorkRef:        c.WorkRef,
		SessionID:      "finish",
		EventType:      EventCandidateReady,
		EventPayload:   map[string]any{"artifact_ref": c.CompletionCandidate.ArtifactRef, "digest": c.CompletionCandidate.Digest, "revision": c.CompletionCandidate.Revision},
		IdempotencyKey: "finish:" + c.WorkRef + ":" + c.CompletionCandidate.Digest,
		OccurredAt:     now,
		ObservedAt:     now,
	}
	ev, err := s.Event(ctx, event)
	if err != nil {
		return CompletionResult{}, err
	}
	if err := s.store.SaveCompletion(ctx, CompletionRecord{
		WorkspaceID:    workspaceFromWorkRef(c.WorkRef),
		WorkRef:        c.WorkRef,
		Package:        c,
		RoutedToReview: true,
	}); err != nil {
		return CompletionResult{}, err
	}
	return CompletionResult{WorkRef: c.WorkRef, EventID: ev.EventID, ReviewRouted: true, AutoPassed: false}, nil
}

// ---------------------------------------------------------------------------
// reconcile / attach / ignore / replay / sync
// ---------------------------------------------------------------------------

// Reconcile returns the read-only inbox diagnostic (unclaimed work).
func (s *Service) Reconcile(ctx context.Context, workspaceID string) ([]InboxItem, error) {
	if s == nil || s.store == nil {
		return nil, ErrUnavailable
	}
	return s.store.ListInbox(ctx, workspaceID)
}

// AttachRequest links an unclaimed inbox entry to a project/issue.
type AttachRequest struct {
	WorkspaceID string `json:"workspace_id"`
	InboxID     string `json:"inbox_id"`
	ProjectID   string `json:"project_id,omitempty"`
	IssueID     string `json:"issue_id,omitempty"`
}

// AttachResult acknowledges the attach.
type AttachResult struct {
	Linked  bool   `json:"linked"`
	WorkRef string `json:"work_ref,omitempty"`
}

// Attach links an unclaimed action into the project ledger (VC-05).
func (s *Service) Attach(ctx context.Context, req AttachRequest) (AttachResult, error) {
	if s == nil || s.store == nil {
		return AttachResult{}, ErrUnavailable
	}
	if strings.TrimSpace(req.WorkspaceID) == "" || strings.TrimSpace(req.InboxID) == "" {
		return AttachResult{}, ErrInvalidRequest
	}
	if strings.TrimSpace(req.ProjectID) == "" && strings.TrimSpace(req.IssueID) == "" {
		return AttachResult{}, ErrInvalidRequest
	}
	if err := s.store.AttachInbox(ctx, req.WorkspaceID, req.InboxID, req.ProjectID, req.IssueID); err != nil {
		return AttachResult{}, err
	}
	return AttachResult{Linked: true, WorkRef: FormatWorkRef(req.WorkspaceID, req.ProjectID, req.IssueID, "")}, nil
}

// IgnoreRequest ignores an unclaimed inbox entry.
type IgnoreRequest struct {
	WorkspaceID string `json:"workspace_id"`
	InboxID     string `json:"inbox_id"`
	Reason      string `json:"reason"`
}

// IgnoreResult acknowledges the ignore.
type IgnoreResult struct {
	Ignored bool `json:"ignored"`
}

// Ignore marks an unclaimed action as ignored.
func (s *Service) Ignore(ctx context.Context, req IgnoreRequest) (IgnoreResult, error) {
	if s == nil || s.store == nil {
		return IgnoreResult{}, ErrUnavailable
	}
	if strings.TrimSpace(req.WorkspaceID) == "" || strings.TrimSpace(req.InboxID) == "" {
		return IgnoreResult{}, ErrInvalidRequest
	}
	if err := s.store.IgnoreInbox(ctx, req.WorkspaceID, req.InboxID, req.Reason); err != nil {
		return IgnoreResult{}, err
	}
	return IgnoreResult{Ignored: true}, nil
}

// ReplayRequest replays a receipt or event by its idempotency key.
type ReplayRequest struct {
	WorkspaceID    string `json:"workspace_id"`
	IdempotencyKey string `json:"idempotency_key"`
	// Kind selects receipt vs event replay; default receipt.
	Kind    string `json:"kind,omitempty"`
	WorkRef string `json:"work_ref,omitempty"`
}

// ReplayResult is the replayed receipt or event.
type ReplayResult struct {
	Receipt *WorkRegistrationReceiptV1 `json:"receipt,omitempty"`
	Event   *EventRecord               `json:"event,omitempty"`
}

// Replay returns the original receipt/event for an idempotency key (no write).
func (s *Service) Replay(ctx context.Context, req ReplayRequest) (ReplayResult, error) {
	if s == nil || s.store == nil {
		return ReplayResult{}, ErrUnavailable
	}
	if strings.TrimSpace(req.IdempotencyKey) == "" {
		return ReplayResult{}, ErrInvalidRequest
	}
	if req.Kind == "event" {
		ev, err := s.store.GetEvent(ctx, req.WorkspaceID, req.WorkRef, req.IdempotencyKey)
		if err != nil {
			return ReplayResult{}, err
		}
		if ev == nil {
			return ReplayResult{}, ErrNotFound
		}
		return ReplayResult{Event: ev}, nil
	}
	r, err := s.store.GetReceipt(ctx, req.WorkspaceID, req.IdempotencyKey)
	if err != nil {
		return ReplayResult{}, err
	}
	if r == nil {
		return ReplayResult{}, ErrNotFound
	}
	receipt := receiptFromRecord(*r, true, s.nowString())
	return ReplayResult{Receipt: &receipt}, nil
}

// SyncEntry is one offline spool entry replayed in order.
type SyncEntry struct {
	Verb             string         `json:"verb"`
	IdempotencyKey   string         `json:"idempotency_key"`
	PayloadDigest    string         `json:"payload_digest"`
	CanonicalPayload map[string]any `json:"canonical_payload"`
}

// SyncResult reports per-entry sync outcomes without failing the whole batch.
type SyncResult struct {
	Synced    int         `json:"synced"`
	Replayed  int         `json:"replayed"`
	Conflicts []SyncIssue `json:"conflicts"`
}

// SyncIssue is one conflicted/skipped entry.
type SyncIssue struct {
	Index  int    `json:"index"`
	Verb   string `json:"verb"`
	Reason string `json:"reason"`
}

type syncRegisterPayload struct {
	ActorIdentity WorkActorIdentityV1 `json:"actor_identity"`
	Intent        WorkIntentV1        `json:"intent"`
	ConfirmCreate bool                `json:"confirm_create"`
}

type syncEventPayload struct {
	Event WorkEventV1 `json:"event"`
}

// Sync replays an ordered offline spool. Conflicted entries are reported
// individually and do not interrupt the rest (API-AND-ADAPTER-CONTRACT §5.3).
func (s *Service) Sync(ctx context.Context, entries []SyncEntry) (SyncResult, error) {
	if s == nil || s.store == nil {
		return SyncResult{}, ErrUnavailable
	}
	var out SyncResult
	for idx, e := range entries {
		switch e.Verb {
		case "register":
			b, err := json.Marshal(e.CanonicalPayload)
			if err != nil {
				out.Conflicts = append(out.Conflicts, SyncIssue{Index: idx, Verb: e.Verb, Reason: "invalid payload"})
				continue
			}
			var p syncRegisterPayload
			if err := json.Unmarshal(b, &p); err != nil {
				out.Conflicts = append(out.Conflicts, SyncIssue{Index: idx, Verb: e.Verb, Reason: "invalid register payload"})
				continue
			}
			receipt, err := s.Register(ctx, RegisterRequest{
				ResolveRequest: ResolveRequest{Actor: p.ActorIdentity, Intent: p.Intent},
				ConfirmCreate:  p.ConfirmCreate,
			})
			if err != nil {
				if err == ErrConflict || err == ErrClassificationRequired {
					out.Conflicts = append(out.Conflicts, SyncIssue{Index: idx, Verb: e.Verb, Reason: err.Error()})
					continue
				}
				return SyncResult{}, err
			}
			if receipt.Replay.Replayed {
				out.Replayed++
			} else {
				out.Synced++
			}
		case "event":
			b, err := json.Marshal(e.CanonicalPayload)
			if err != nil {
				out.Conflicts = append(out.Conflicts, SyncIssue{Index: idx, Verb: e.Verb, Reason: "invalid payload"})
				continue
			}
			var p syncEventPayload
			if err := json.Unmarshal(b, &p); err != nil {
				out.Conflicts = append(out.Conflicts, SyncIssue{Index: idx, Verb: e.Verb, Reason: "invalid event payload"})
				continue
			}
			ev, err := s.Event(ctx, p.Event)
			if err != nil {
				if err == ErrConflict {
					out.Conflicts = append(out.Conflicts, SyncIssue{Index: idx, Verb: e.Verb, Reason: "conflict"})
					continue
				}
				return SyncResult{}, err
			}
			if ev.Replayed {
				out.Replayed++
			} else {
				out.Synced++
			}
		default:
			out.Conflicts = append(out.Conflicts, SyncIssue{Index: idx, Verb: e.Verb, Reason: "unsupported verb"})
		}
	}
	return out, nil
}

package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/continuousdispatch"
)

// Per-Issue outcome buckets for one bounded drain pass. Every Issue the
// validated projection raised is reported in exactly one bucket; no bucket
// ever prevents the remaining suggestions from being attempted.
const (
	WorkConservingDrainDispatched      = "dispatched"
	WorkConservingDrainAlreadyTerminal = "already_terminal"
	WorkConservingDrainBlocked         = "blocked"
	WorkConservingDrainConflict        = "conflict"
	WorkConservingDrainSourceGap       = "source_gap"
)

// Whole-drain states. source_gap means no suggestion could be trusted: the
// projection itself was missing, stale, or invalid, so nothing was dispatched.
const (
	WorkConservingDrainStateReady     = "ready"
	WorkConservingDrainStateSourceGap = "source_gap"
)

const (
	// workConservingDrainMaxBatch is the hard server-side fan-out bound for
	// one drain call. The caller chooses only a value in [1, MaxBatch];
	// Employee, Agent, Runtime, Provider, model, account and generation stay
	// server-selected by the projection and the exact trigger.
	workConservingDrainMaxBatch = 20
	// workConservingDrainProjectionLimit is the pagination metadata sent with
	// the projection request. It never truncates the provider's global plan;
	// the batch bound alone limits how many suggestions are dispatched.
	workConservingDrainProjectionLimit = workConservingPageSize
)

var ErrWorkConservingDrainNotConfigured = errors.New("work-conserving drain is not configured")

// WorkConservingDrainProjector is the read-only seam for the already validated
// Goal-scoped ready projection. Implementations must not write Task, lease,
// receipt, or DB rows.
type WorkConservingDrainProjector interface {
	ProjectWorkConserving(context.Context, WorkConservingProjectionRequest) (WorkConservingProjection, error)
}

// WorkConservingDrainDispatcher is the exact-trigger seam. The signature is
// deliberately selector-free: the drain passes only workspace, project, issue,
// actor and a server-built handoff note; the trigger recomputes employee,
// Agent, Runtime, model, account and generation immediately before writing.
type WorkConservingDrainDispatcher interface {
	DispatchIssue(context.Context, pgtype.UUID, pgtype.UUID, pgtype.UUID, pgtype.UUID, string) (ContinuousDispatchTriggerResult, error)
}

var (
	_ WorkConservingDrainProjector  = (*FileWorkConservingProjectionProvider)(nil)
	_ WorkConservingDrainDispatcher = (*ContinuousDispatchTriggerService)(nil)
)

// WorkConservingDrainRequest is the complete caller-controlled input. BatchSize
// is the only scheduling freedom: it bounds dispatch attempts per call. There
// is intentionally no Employee, Agent, Runtime, Provider, model, account, or
// generation field.
type WorkConservingDrainRequest struct {
	WorkspaceID pgtype.UUID
	ProjectID   pgtype.UUID
	ActorUserID pgtype.UUID
	BatchSize   int
}

// WorkConservingDrainIssueResult is the deterministic per-Issue outcome. One
// row exists for every suggestion attempted in this batch and for every
// blocked-backlog entry the projection reported (blocked rows carry no receipt
// and were never dispatched).
type WorkConservingDrainIssueResult struct {
	IssueID       string                     `json:"issue_id"`
	GoalID        string                     `json:"goal_id,omitempty"`
	EmployeeID    string                     `json:"employee_id,omitempty"`
	Outcome       string                     `json:"outcome"`
	Reason        string                     `json:"reason,omitempty"`
	Receiver      string                     `json:"receiver,omitempty"`
	WakeCondition string                     `json:"wake_condition,omitempty"`
	Receipt       *ContinuousDispatchReceipt `json:"receipt,omitempty"`
	NotAttempted  bool                       `json:"not_attempted,omitempty"`
}

// WorkConservingDrainResult is the truthful partial result of one drain pass:
// it reports what was dispatched and why each remaining Issue could not run.
type WorkConservingDrainResult struct {
	State               string                           `json:"state"`
	ReasonCode          string                           `json:"reason_code,omitempty"`
	ProjectionState     WorkConservingProjectionState    `json:"projection_state,omitempty"`
	GoalID              string                           `json:"goal_id,omitempty"`
	Authority           WorkConservingAuthoritySnapshot  `json:"authority"`
	BatchSize           int                              `json:"batch_size"`
	Results             []WorkConservingDrainIssueResult `json:"results"`
	DeferredSuggestions int                              `json:"deferred_suggestions"`
	Dispatched          int                              `json:"dispatched"`
	AlreadyTerminal     int                              `json:"already_terminal"`
	Blocked             int                              `json:"blocked"`
	Conflicts           int                              `json:"conflicts"`
	SourceGaps          int                              `json:"source_gaps"`
}

// WorkConservingDrainService turns one validated work-conserving projection
// into multiple exact continuous-dispatch calls. It owns no scheduler, no
// queue, and no second validation layer: it reuses the projection validation
// and the exact trigger, and it continues after single-Issue failures.
type WorkConservingDrainService struct {
	projector  WorkConservingDrainProjector
	dispatcher WorkConservingDrainDispatcher
	now        func() time.Time
}

func NewWorkConservingDrainService(projector WorkConservingDrainProjector, dispatcher WorkConservingDrainDispatcher) *WorkConservingDrainService {
	return &WorkConservingDrainService{projector: projector, dispatcher: dispatcher, now: time.Now}
}

// WithClock returns a copy with an explicit validation clock. It keeps
// projection freshness checks deterministic in tests; the clock never enters
// the dispatch handoff note, so exact replays stay byte-identical.
func (s *WorkConservingDrainService) WithClock(now func() time.Time) *WorkConservingDrainService {
	if s == nil {
		return nil
	}
	cp := *s
	if now != nil {
		cp.now = now
	}
	return &cp
}

// Drain projects the current global plan, validates it, then dispatches at
// most req.BatchSize ready suggestions through the exact trigger. A blocked or
// conflicting Issue is reported independently and never prevents the other
// safe suggestions from dispatching. The error return is non-nil only for
// whole-drain failures (configuration, invalid request, or a projection that
// failed closed); per-Issue failures are classified inside the result.
func (s *WorkConservingDrainService) Drain(ctx context.Context, req WorkConservingDrainRequest) (WorkConservingDrainResult, error) {
	if s == nil || s.projector == nil || s.dispatcher == nil {
		return WorkConservingDrainResult{}, ErrWorkConservingDrainNotConfigured
	}
	if err := validateWorkConservingDrainRequest(req); err != nil {
		return WorkConservingDrainResult{}, err
	}
	now := s.now().UTC()
	if now.IsZero() {
		return WorkConservingDrainResult{}, fmt.Errorf("%w: drain clock is invalid", ErrWorkConservingProjectionSourceGap)
	}
	projectionReq := WorkConservingProjectionRequest{
		WorkspaceID: req.WorkspaceID, ProjectID: req.ProjectID,
		Limit: workConservingDrainProjectionLimit, Offset: 0,
	}
	projection, err := s.projector.ProjectWorkConserving(ctx, projectionReq)
	if err != nil {
		return newWorkConservingDrainSourceGapResult("projection_source_gap"), fmt.Errorf("work-conserving drain projection: %w", err)
	}
	if err := ValidateWorkConservingProjectionAt(projection, projectionReq, now); err != nil {
		return newWorkConservingDrainSourceGapResult("projection_validation_failed"), fmt.Errorf("work-conserving drain validation: %w", err)
	}
	result := WorkConservingDrainResult{
		State: WorkConservingDrainStateReady, ProjectionState: projection.State,
		GoalID: projection.GoalID, Authority: projection.Authority, BatchSize: req.BatchSize,
		Results: []WorkConservingDrainIssueResult{},
	}

	batch := projection.Suggestions
	if len(batch) > req.BatchSize {
		result.DeferredSuggestions = len(batch) - req.BatchSize
		batch = batch[:req.BatchSize]
	}
	for _, suggestion := range batch {
		result.append(workConservingDrainAttempt(ctx, s.dispatcher, req, projection.GoalID, suggestion))
	}
	for _, blocked := range projection.BlockedBacklog {
		result.append(WorkConservingDrainIssueResult{
			IssueID: blocked.IssueID, GoalID: blocked.GoalID, Outcome: WorkConservingDrainBlocked,
			Reason: workConservingDrainBlockedReason(blocked), Receiver: blocked.Receiver,
			WakeCondition: blocked.WakeCondition, NotAttempted: true,
		})
	}
	return result, nil
}

// workConservingDrainAttempt performs one exact dispatch attempt and turns the
// trigger outcome into a deterministic per-Issue row. It never returns an
// error: a single failed suggestion must not stop the drain.
func workConservingDrainAttempt(
	ctx context.Context,
	dispatcher WorkConservingDrainDispatcher,
	req WorkConservingDrainRequest,
	goalID string,
	suggestion continuousdispatch.WorkConservingSuggestion,
) WorkConservingDrainIssueResult {
	row := WorkConservingDrainIssueResult{
		IssueID: suggestion.IssueID, GoalID: suggestion.GoalID, EmployeeID: suggestion.EmployeeID,
		Receiver: suggestion.Receiver, WakeCondition: suggestion.WakeCondition,
	}
	if suggestion.GoalID == "" {
		row.GoalID = goalID
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		row.Outcome, row.Reason, row.NotAttempted = WorkConservingDrainSourceGap, ctxErr.Error(), true
		return row
	}
	issueID := parseDispatchUUID(suggestion.IssueID)
	if !issueID.Valid || issueID.Bytes == ([16]byte{}) || shadowUUIDString(issueID) != suggestion.IssueID {
		row.Outcome, row.Reason, row.NotAttempted = WorkConservingDrainSourceGap, "suggestion issue id is not a canonical uuid", true
		return row
	}
	trigger, err := dispatcher.DispatchIssue(ctx, req.WorkspaceID, req.ProjectID, issueID, req.ActorUserID, workConservingDrainHandoffNote(goalID, suggestion.IssueID))
	row.Outcome, row.Reason = classifyWorkConservingDrainDispatch(err)
	if err == nil {
		receipt := trigger.Receipt
		row.Receipt = &receipt
	}
	return row
}

// classifyWorkConservingDrainDispatch maps one exact-trigger outcome onto the
// five drain buckets, reusing the trigger's own sentinel errors instead of a
// second validation layer.
func classifyWorkConservingDrainDispatch(err error) (outcome, reason string) {
	if err == nil {
		return WorkConservingDrainDispatched, ""
	}
	switch {
	case errors.Is(err, ErrContinuousDispatchIssueNotReady):
		// The Issue reached done/cancelled/backlog between the projection and
		// the exact write: nothing left to dispatch.
		return WorkConservingDrainAlreadyTerminal, ErrContinuousDispatchIssueNotReady.Error()
	case errors.Is(err, ErrContinuousDispatchNotReady):
		// The recomputed next action is no longer ready/fallback: report and
		// let the next projection decide; never force a write.
		return WorkConservingDrainBlocked, ErrContinuousDispatchNotReady.Error()
	case errors.Is(err, ErrContinuousDispatchConflict), errors.Is(err, ErrContinuousDispatchReceiptConflict),
		errors.Is(err, ErrContinuousDispatchIssueDrift), errors.Is(err, ErrContinuousDispatchRouteDrift),
		errors.Is(err, ErrContinuousDispatchReviewLineageDrift):
		return WorkConservingDrainConflict, err.Error()
	case errors.Is(err, ErrContinuousDispatchSourceGap), errors.Is(err, ErrContinuousDispatchIssueAbsent),
		errors.Is(err, ErrContinuousDispatchProjectAbsent), errors.Is(err, ErrWorkConservingProjectionSourceGap),
		errors.Is(err, ErrAuthorityReviewDispatchSourceGap), errors.Is(err, context.Canceled),
		errors.Is(err, context.DeadlineExceeded):
		return WorkConservingDrainSourceGap, err.Error()
	default:
		// Unknown failures fail closed as evidence gaps, with the original
		// error preserved verbatim for the operator.
		return WorkConservingDrainSourceGap, err.Error()
	}
}

// workConservingDrainHandoffNote is byte-stable for one goal/issue pair. It
// intentionally excludes time and batch size: the note is part of the exact
// dispatch digest, and a varying note would turn a safe replay into a
// generation conflict.
func workConservingDrainHandoffNote(goalID, issueID string) string {
	return fmt.Sprintf(
		"work-conserving drain (goal=%s issue=%s): execute the current server-validated ready frontier action; employee, agent, runtime, model, account and generation are server-selected",
		goalID, issueID,
	)
}

func workConservingDrainBlockedReason(blocked continuousdispatch.WorkConservingBlockedIssue) string {
	reasons := make([]string, 0, len(blocked.Reasons))
	for _, reason := range blocked.Reasons {
		if reason != "" {
			reasons = append(reasons, string(reason))
		}
	}
	return strings.Join(reasons, ",")
}

func (r *WorkConservingDrainResult) append(row WorkConservingDrainIssueResult) {
	r.Results = append(r.Results, row)
	switch row.Outcome {
	case WorkConservingDrainDispatched:
		r.Dispatched++
	case WorkConservingDrainAlreadyTerminal:
		r.AlreadyTerminal++
	case WorkConservingDrainBlocked:
		r.Blocked++
	case WorkConservingDrainConflict:
		r.Conflicts++
	case WorkConservingDrainSourceGap:
		r.SourceGaps++
	}
}

func validateWorkConservingDrainRequest(req WorkConservingDrainRequest) error {
	for name, value := range map[string]pgtype.UUID{
		"workspace_id": req.WorkspaceID, "project_id": req.ProjectID, "actor_user_id": req.ActorUserID,
	} {
		if !value.Valid || value.Bytes == ([16]byte{}) {
			return fmt.Errorf("%s is required", name)
		}
	}
	if req.BatchSize < 1 || req.BatchSize > workConservingDrainMaxBatch {
		return fmt.Errorf("batch_size must be between 1 and %d", workConservingDrainMaxBatch)
	}
	return nil
}

func newWorkConservingDrainSourceGapResult(reasonCode string) WorkConservingDrainResult {
	return WorkConservingDrainResult{
		State: WorkConservingDrainStateSourceGap, ReasonCode: reasonCode,
		BatchSize: 0, Results: []WorkConservingDrainIssueResult{},
	}
}

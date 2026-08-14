package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/continuousdispatch"
)

// WorkConservingProjectionSchemaV1 is the version of the optional Goal-scoped
// read projection. It is deliberately separate from the ordinary shadow
// response: a work-conserving plan is a global recommendation, not a page of
// Project Issues.
const WorkConservingProjectionSchemaV1 = "hivecrew.work-conserving-projection/v1"

type WorkConservingProjectionState string

const (
	WorkConservingProjectionReady     WorkConservingProjectionState = "ready"
	WorkConservingProjectionBlocked   WorkConservingProjectionState = "blocked"
	WorkConservingProjectionSourceGap WorkConservingProjectionState = "source_gap"
)

var ErrWorkConservingProjectionSourceGap = errors.New("work-conserving projection source gap")

// WorkConservingProjectionRequest is intentionally Goal-scoped and contains
// no route selector. A future Authority-backed provider resolves the exact
// Goal, Issue, Employee, Agent, Runtime, quota and write-path records from
// this workspace/project pair. Pagination is metadata for the surrounding
// endpoint only; it must never be used to truncate the provider's global plan.
type WorkConservingProjectionRequest struct {
	WorkspaceID pgtype.UUID
	ProjectID   pgtype.UUID
	Limit       int
	Offset      int
}

// WorkConservingProjectionProvider is a read-only seam for the complete
// Authority-backed Goal mapping. Implementations must not create Tasks,
// leases, receipts, DB rows, or HTTP writes. Until a complete provider is
// composed, the handler emits a truthful source_gap projection.
type WorkConservingProjectionProvider interface {
	ProjectWorkConserving(context.Context, WorkConservingProjectionRequest) (WorkConservingProjection, error)
}

// WorkConservingProjection is the embedded API representation of one global
// plan. Total is supplied by the provider's full Goal snapshot and is not
// derived from the page returned by ContinuousDispatchShadow.
type WorkConservingProjection struct {
	SchemaVersion  string                                          `json:"schema_version"`
	State          WorkConservingProjectionState                   `json:"state"`
	ReasonCode     string                                          `json:"reason_code,omitempty"`
	Blocked        bool                                            `json:"blocked"`
	GoalID         string                                          `json:"goal_id,omitempty"`
	Suggestions    []continuousdispatch.WorkConservingSuggestion   `json:"suggestions"`
	BlockedBacklog []continuousdispatch.WorkConservingBlockedIssue `json:"blocked_backlog"`
	Mismatch       continuousdispatch.WorkConservingMismatch       `json:"mismatch"`
	Total          int                                             `json:"total"`
	Limit          int                                             `json:"limit"`
	Offset         int                                             `json:"offset"`
	NoWrite        bool                                            `json:"no_write"`
}

// NewWorkConservingSourceGapProjection is the stable fail-closed payload
// used when the optional provider is absent, errors, or returns an invalid
// or partial snapshot. blocked is represented by the state plus empty plan;
// no suggestion is executable by itself and no write is performed.
func NewWorkConservingSourceGapProjection(limit, offset int) WorkConservingProjection {
	return WorkConservingProjection{
		SchemaVersion:  WorkConservingProjectionSchemaV1,
		State:          WorkConservingProjectionSourceGap,
		ReasonCode:     "source_gap",
		Blocked:        true,
		Suggestions:    []continuousdispatch.WorkConservingSuggestion{},
		BlockedBacklog: []continuousdispatch.WorkConservingBlockedIssue{},
		Limit:          limit,
		Offset:         offset,
		NoWrite:        true,
	}
}

func validWorkConservingProjectionRequest(req WorkConservingProjectionRequest) bool {
	return req.WorkspaceID.Valid && req.WorkspaceID.Bytes != ([16]byte{}) &&
		req.ProjectID.Valid && req.ProjectID.Bytes != ([16]byte{}) &&
		req.Limit > 0 && req.Limit <= 200 && req.Offset >= 0
}

// ValidateWorkConservingProjection rejects incomplete provider output before
// it reaches the response. It requires a full Goal identity and a provider
// supplied total. Empty plans are valid only when the provider explicitly
// reports blocked; source_gap is reserved for missing/invalid evidence.
func ValidateWorkConservingProjection(p WorkConservingProjection, req WorkConservingProjectionRequest) error {
	if !validWorkConservingProjectionRequest(req) {
		return fmt.Errorf("%w: invalid request scope", ErrWorkConservingProjectionSourceGap)
	}
	if p.SchemaVersion != WorkConservingProjectionSchemaV1 {
		return fmt.Errorf("%w: schema version is missing or unsupported", ErrWorkConservingProjectionSourceGap)
	}
	if strings.TrimSpace(p.GoalID) == "" {
		return fmt.Errorf("%w: goal identity is missing", ErrWorkConservingProjectionSourceGap)
	}
	if p.State != WorkConservingProjectionReady && p.State != WorkConservingProjectionBlocked {
		return fmt.Errorf("%w: provider state is incomplete", ErrWorkConservingProjectionSourceGap)
	}
	if p.Total < 0 || p.Limit != req.Limit || p.Offset != req.Offset {
		return fmt.Errorf("%w: global plan pagination metadata is invalid", ErrWorkConservingProjectionSourceGap)
	}
	if p.State == WorkConservingProjectionReady && p.Total > 0 && len(p.Suggestions) == 0 && len(p.BlockedBacklog) == 0 {
		return fmt.Errorf("%w: ready plan has no plan entries", ErrWorkConservingProjectionSourceGap)
	}
	if p.State == WorkConservingProjectionBlocked && p.Total > 0 && len(p.BlockedBacklog) == 0 {
		return fmt.Errorf("%w: blocked plan has no blocked backlog", ErrWorkConservingProjectionSourceGap)
	}
	if p.Total < len(p.Suggestions)+len(p.BlockedBacklog) {
		return fmt.Errorf("%w: plan entries exceed global total", ErrWorkConservingProjectionSourceGap)
	}
	seen := make(map[string]struct{}, len(p.Suggestions)+len(p.BlockedBacklog))
	for _, suggestion := range p.Suggestions {
		if strings.TrimSpace(suggestion.IssueID) == "" || strings.TrimSpace(suggestion.GoalID) == "" || suggestion.GoalID != p.GoalID ||
			strings.TrimSpace(suggestion.EmployeeID) == "" || strings.TrimSpace(suggestion.AgentID) == "" || strings.TrimSpace(suggestion.RuntimeID) == "" ||
			strings.TrimSpace(suggestion.Receiver) == "" || strings.TrimSpace(suggestion.WakeCondition) == "" {
			return fmt.Errorf("%w: suggestion is partial", ErrWorkConservingProjectionSourceGap)
		}
		if _, duplicate := seen[suggestion.IssueID]; duplicate {
			return fmt.Errorf("%w: plan contains duplicate issue identities", ErrWorkConservingProjectionSourceGap)
		}
		seen[suggestion.IssueID] = struct{}{}
	}
	for _, blocked := range p.BlockedBacklog {
		if strings.TrimSpace(blocked.IssueID) == "" || strings.TrimSpace(blocked.GoalID) == "" || blocked.GoalID != p.GoalID ||
			strings.TrimSpace(blocked.Receiver) == "" || strings.TrimSpace(blocked.WakeCondition) == "" || len(blocked.Reasons) == 0 {
			return fmt.Errorf("%w: blocked issue is partial", ErrWorkConservingProjectionSourceGap)
		}
		if _, duplicate := seen[blocked.IssueID]; duplicate {
			return fmt.Errorf("%w: plan contains duplicate issue identities", ErrWorkConservingProjectionSourceGap)
		}
		seen[blocked.IssueID] = struct{}{}
	}
	return nil
}

package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/continuousdispatch"
)

var (
	ErrContinuousDispatchIssueAbsent = errors.New("continuous dispatch issue not found in project")
	ErrContinuousDispatchNotReady    = errors.New("continuous dispatch issue has no executable next action")
)

const continuousDispatchTriggerPageSize = 200

type ContinuousDispatchProjectInspector interface {
	InspectProject(context.Context, pgtype.UUID, pgtype.UUID, int, int) (*ContinuousDispatchShadowResult, error)
}

type ContinuousDispatchExactDispatcher interface {
	Dispatch(context.Context, ContinuousDispatchRequest) (ContinuousDispatchReceipt, error)
}

// ContinuousDispatchTriggerResult carries the exact server-recomputed action
// and committed receipt. It is a response projection, not a new Task state.
type ContinuousDispatchTriggerResult struct {
	Action  continuousdispatch.NextAction
	Receipt ContinuousDispatchReceipt
}

// ContinuousDispatchTriggerService prevents callers from choosing an
// employee, Agent, Runtime, model, account, or generation. It recomputes the
// current shadow decision and only dispatches a ready/fallback server result.
type ContinuousDispatchTriggerService struct {
	inspector  ContinuousDispatchProjectInspector
	dispatcher ContinuousDispatchExactDispatcher
}

func NewContinuousDispatchTriggerService(
	inspector ContinuousDispatchProjectInspector,
	dispatcher ContinuousDispatchExactDispatcher,
) *ContinuousDispatchTriggerService {
	return &ContinuousDispatchTriggerService{inspector: inspector, dispatcher: dispatcher}
}

func (s *ContinuousDispatchTriggerService) DispatchIssue(
	ctx context.Context,
	workspaceID, projectID, issueID, actorUserID pgtype.UUID,
	handoffNote string,
) (ContinuousDispatchTriggerResult, error) {
	if s == nil || s.inspector == nil || s.dispatcher == nil {
		return ContinuousDispatchTriggerResult{}, fmt.Errorf("continuous dispatch trigger dependencies are required")
	}
	for name, value := range map[string]pgtype.UUID{
		"workspace_id": workspaceID, "project_id": projectID, "issue_id": issueID, "actor_user_id": actorUserID,
	} {
		if !value.Valid || value.Bytes == ([16]byte{}) {
			return ContinuousDispatchTriggerResult{}, fmt.Errorf("%s is required", name)
		}
	}

	wantedIssueID := shadowUUIDString(issueID)
	for offset := 0; ; offset += continuousDispatchTriggerPageSize {
		page, err := s.inspector.InspectProject(ctx, workspaceID, projectID, continuousDispatchTriggerPageSize, offset)
		if err != nil {
			return ContinuousDispatchTriggerResult{}, err
		}
		if page == nil || page.SchemaVersion != ContinuousDispatchShadowSchemaV1 ||
			page.WorkspaceID != shadowUUIDString(workspaceID) || page.ProjectID != shadowUUIDString(projectID) {
			return ContinuousDispatchTriggerResult{}, ErrContinuousDispatchSourceGap
		}
		for _, item := range page.Items {
			if item.IssueID != wantedIssueID {
				continue
			}
			return s.dispatchShadowItem(ctx, item, actorUserID, handoffNote)
		}
		if len(page.Items) == 0 || offset+len(page.Items) >= page.Total {
			return ContinuousDispatchTriggerResult{}, ErrContinuousDispatchIssueAbsent
		}
	}
}

func (s *ContinuousDispatchTriggerService) dispatchShadowItem(
	ctx context.Context,
	item ContinuousDispatchShadowItem,
	actorUserID pgtype.UUID,
	handoffNote string,
) (ContinuousDispatchTriggerResult, error) {
	action := item.NextAction
	if (action.State != continuousdispatch.StateReady && action.State != continuousdispatch.StateFallback) || action.Selected == nil {
		return ContinuousDispatchTriggerResult{}, ErrContinuousDispatchNotReady
	}
	selected := action.Selected
	if !item.DispatchIdentity.Complete() || item.DispatchIdentity.IssueID != item.IssueID ||
		strings.TrimSpace(selected.EmployeeID) == "" || strings.TrimSpace(selected.Model) == "" ||
		strings.TrimSpace(selected.AccountRef) == "" {
		return ContinuousDispatchTriggerResult{}, ErrContinuousDispatchSourceGap
	}
	agentID := parseDispatchUUID(selected.AgentID)
	runtimeID := parseDispatchUUID(selected.RuntimeID)
	if !agentID.Valid || !runtimeID.Valid {
		return ContinuousDispatchTriggerResult{}, ErrContinuousDispatchSourceGap
	}

	receipt, err := s.dispatcher.Dispatch(ctx, ContinuousDispatchRequest{
		Identity: item.DispatchIdentity,
		Route: ContinuousDispatchRoute{
			EmployeeRef:  continuousDispatchEmployeeRefPrefix + selected.EmployeeID,
			LocalAgentID: agentID,
			RuntimeID:    runtimeID,
			Model:        selected.Model,
			AccountRef:   selected.AccountRef,
		},
		ActorUserID: actorUserID,
		HandoffNote: handoffNote,
	})
	if err != nil {
		return ContinuousDispatchTriggerResult{}, err
	}
	return ContinuousDispatchTriggerResult{Action: action, Receipt: receipt}, nil
}

var _ ContinuousDispatchExactDispatcher = (*ContinuousDispatchService)(nil)

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/featureflag"
)

func (s *TaskService) writerLeaseCompletionMode(ctx context.Context) (WriterLeaseMode, error) {
	decision := s.FeatureFlags.Decision(ctx, featureflags.WriterLeaseMode, false)
	if decision.Reason == featureflag.ReasonError {
		return "", fmt.Errorf("%w: feature flag provider error", ErrWriterLeaseInvalidMode)
	}
	mode, err := NormalizeWriterLeaseMode(decision.Variant)
	if err != nil {
		return "", err
	}
	return mode, nil
}

func requireWriterLeaseCompletionTransaction(mode WriterLeaseMode, tx TxStarter) error {
	if mode == WriterLeaseModeEnforce && tx == nil {
		return fmt.Errorf("%w: transaction starter unavailable", ErrWriterLeaseFenceRejected)
	}
	return nil
}

func validateWriterLeaseTaskKind(kind string) error {
	switch WriterLeaseTaskKind(strings.TrimSpace(kind)) {
	case WriterLeaseTaskKindWork, WriterLeaseTaskKindRepair:
		return nil
	default:
		return fmt.Errorf("%w: task kind %q cannot complete under writer lease enforcement", ErrWriterLeaseFenceRejected, kind)
	}
}

// validateWriterLeaseTerminalProof is the server-side half of the daemon
// terminal fence. It must run after the task row is locked and before any
// terminal mutation in the same transaction.
func (s *TaskService) validateWriterLeaseTerminalProof(ctx context.Context, qtx *db.Queries, task db.AgentTaskQueue, proof []WriterLeaseTerminalProof) error {
	if err := validateWriterLeaseTaskKind(task.TaskKind); err != nil {
		return err
	}
	targets, runtime, err := s.authoritativeWriterLeaseTargets(ctx, qtx, task)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		if len(proof) != 0 {
			return fmt.Errorf("%w: proof supplied for task without github targets", ErrWriterLeaseFenceRejected)
		}
		return nil
	}
	keys := make([]string, 0, len(targets))
	for _, target := range targets {
		keys = append(keys, target.MutexKey)
	}
	sort.Strings(keys)

	rows, err := qtx.LockWriterLeasesForCompletion(ctx, keys)
	if err != nil {
		return fmt.Errorf("lock writer leases for completion: %w", err)
	}
	if len(rows) != len(targets) {
		return fmt.Errorf("%w: authoritative lease row is missing", ErrWriterLeaseFenceRejected)
	}
	return validateWriterLeaseProofRows(targets, runtime, task, proof, rows)
}

func validateWriterLeaseProofRows(targets []WriterLeaseTarget, runtime db.AgentRuntime, task db.AgentTaskQueue, proof []WriterLeaseTerminalProof, rows []db.LockWriterLeasesForCompletionRow) error {
	if len(proof) != len(targets) {
		return fmt.Errorf("%w: proof target count does not match authoritative target count", ErrWriterLeaseFenceRejected)
	}
	targetByResource := make(map[string]WriterLeaseTarget, len(targets))
	for _, target := range targets {
		if _, duplicate := targetByResource[target.ResourceID]; duplicate {
			return fmt.Errorf("%w: authoritative target set contains duplicate resource", ErrWriterLeaseFenceRejected)
		}
		targetByResource[target.ResourceID] = target
	}
	proofByResource := make(map[string]WriterLeaseTerminalProof, len(proof))
	for _, item := range proof {
		resourceID := item.ResourceID.String()
		if item.ResourceID == uuid.Nil || item.LeaseToken == uuid.Nil || item.FenceGeneration <= 0 {
			return fmt.Errorf("%w: malformed terminal proof", ErrWriterLeaseFenceRejected)
		}
		if _, duplicate := proofByResource[resourceID]; duplicate {
			return fmt.Errorf("%w: duplicate resource proof", ErrWriterLeaseFenceRejected)
		}
		if _, known := targetByResource[resourceID]; !known {
			return fmt.Errorf("%w: proof contains non-authoritative resource", ErrWriterLeaseFenceRejected)
		}
		proofByResource[resourceID] = item
	}
	if len(proofByResource) != len(targetByResource) {
		return fmt.Errorf("%w: proof is missing an authoritative resource", ErrWriterLeaseFenceRejected)
	}
	if len(rows) != len(targets) {
		return fmt.Errorf("%w: authoritative lease row is missing", ErrWriterLeaseFenceRejected)
	}
	holder := WriterLeaseHolderID(runtime.DaemonID.String, runtime.ID.String(), task.ID.String())
	rowsByKey := make(map[string]db.LockWriterLeasesForCompletionRow, len(rows))
	for _, row := range rows {
		rowsByKey[row.MutexKey] = row
	}
	for resourceID, target := range targetByResource {
		item := proofByResource[resourceID]
		row, ok := rowsByKey[target.MutexKey]
		if !ok || !row.HolderID.Valid || row.HolderID.String != holder || row.Status != string(WriteLeaseHeld) || !row.LeaseToken.Valid || row.LeaseToken.Bytes != item.LeaseToken || row.FenceGeneration != item.FenceGeneration || !row.NotExpired {
			return fmt.Errorf("%w: resource %s lease token or generation is stale", ErrWriterLeaseFenceRejected, resourceID)
		}
	}
	return nil
}

// authoritativeWriterLeaseTargets reconstructs targets from current server
// state. URL/ref/mutex key/holder values in the daemon request are never used.
func (s *TaskService) authoritativeWriterLeaseTargets(ctx context.Context, qtx *db.Queries, task db.AgentTaskQueue) ([]WriterLeaseTarget, db.AgentRuntime, error) {
	agent, err := qtx.GetAgent(ctx, task.AgentID)
	if err != nil {
		return nil, db.AgentRuntime{}, fmt.Errorf("load task agent for writer lease fence: %w", err)
	}
	runtime, err := qtx.GetAgentRuntime(ctx, task.RuntimeID)
	if err != nil {
		return nil, db.AgentRuntime{}, fmt.Errorf("load task runtime for writer lease fence: %w", err)
	}
	if runtime.WorkspaceID != agent.WorkspaceID || !runtime.DaemonID.Valid || strings.TrimSpace(runtime.DaemonID.String) == "" {
		return nil, db.AgentRuntime{}, fmt.Errorf("%w: runtime ownership unavailable", ErrWriterLeaseFenceRejected)
	}
	if persisted, legacy, err := DecodePersistedWriterLeaseClaim(task, runtime.WorkspaceID.String()); err != nil {
		return nil, db.AgentRuntime{}, err
	} else if !legacy {
		if persisted.Mode != WriterLeaseModeEnforce {
			return nil, runtime, nil
		}
		return persisted.Targets, runtime, nil
	}
	projectID, ok, err := terminalTaskProjectID(ctx, qtx, task)
	if err != nil {
		return nil, db.AgentRuntime{}, err
	}
	if !ok {
		return nil, runtime, nil
	}
	project, err := qtx.GetProject(ctx, projectID)
	if err != nil {
		return nil, db.AgentRuntime{}, fmt.Errorf("load task project for writer lease fence: %w", err)
	}
	if project.WorkspaceID != agent.WorkspaceID {
		return nil, db.AgentRuntime{}, fmt.Errorf("%w: project workspace mismatch", ErrWriterLeaseFenceRejected)
	}
	resources, err := qtx.ListProjectResources(ctx, project.ID)
	if err != nil {
		return nil, db.AgentRuntime{}, fmt.Errorf("load project resources for writer lease fence: %w", err)
	}
	leaseResources := make([]WriterLeaseResource, 0, len(resources))
	for _, resource := range resources {
		if resource.WorkspaceID != agent.WorkspaceID {
			return nil, db.AgentRuntime{}, fmt.Errorf("%w: project resource workspace mismatch", ErrWriterLeaseFenceRejected)
		}
		if resource.ResourceType != "github_repo" {
			continue
		}
		var ref struct {
			URL               string `json:"url"`
			Ref               string `json:"ref"`
			DefaultBranchHint string `json:"default_branch_hint"`
		}
		if err := json.Unmarshal(resource.ResourceRef, &ref); err != nil || strings.TrimSpace(ref.URL) == "" {
			return nil, db.AgentRuntime{}, fmt.Errorf("%w: invalid github resource", ErrWriterLeaseFenceRejected)
		}
		leaseResources = append(leaseResources, WriterLeaseResource{ID: uuid.UUID(resource.ID.Bytes), ResourceType: resource.ResourceType, URL: ref.URL, Ref: ref.Ref, DefaultBranchHint: ref.DefaultBranchHint})
	}
	if len(leaseResources) == 0 {
		return nil, runtime, nil
	}
	targets, err := ResolveWriterLeaseTargets(WriterLeaseModeEnforce, agent.WorkspaceID.String(), project.ID.String(), runtime.DaemonID.String, runtime.ID.String(), task.ID.String(), leaseResources)
	if err != nil {
		return nil, db.AgentRuntime{}, fmt.Errorf("resolve writer lease targets for completion: %w", err)
	}
	return targets, runtime, nil
}

func terminalTaskProjectID(ctx context.Context, qtx *db.Queries, task db.AgentTaskQueue) (pgtype.UUID, bool, error) {
	if task.IssueID.Valid {
		issue, err := qtx.GetIssue(ctx, task.IssueID)
		return issue.ProjectID, issue.ProjectID.Valid, err
	}
	if task.ChatSessionID.Valid {
		chat, err := qtx.GetChatSession(ctx, task.ChatSessionID)
		return chat.ProjectID, chat.ProjectID.Valid, err
	}
	if qc, ok := (&TaskService{}).parseQuickCreateContext(task); ok && strings.TrimSpace(qc.ProjectID) != "" {
		projectID, err := util.ParseUUID(strings.TrimSpace(qc.ProjectID))
		return projectID, err == nil && projectID.Valid, err
	}
	return pgtype.UUID{}, false, nil
}

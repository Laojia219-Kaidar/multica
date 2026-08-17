package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
type writerLeaseCompletionEvidence struct {
	workspaceID   pgtype.UUID
	targetDigest  string
	proofSnapshot []byte
	proofDigest   string
	receiptDigest string
}

type writerLeaseProofSnapshotItem struct {
	ResourceID       string `json:"resource_id"`
	MutexKey         string `json:"mutex_key"`
	FenceGeneration  int64  `json:"fence_generation"`
	LeaseTokenSHA256 string `json:"lease_token_sha256"`
}

// validateWriterLeaseTerminalProof returns the canonical evidence that is
// committed beside the task terminal mutation. The token is hashed in memory;
// it is never serialized into the receipt.
func (s *TaskService) validateWriterLeaseTerminalProof(ctx context.Context, qtx *db.Queries, task db.AgentTaskQueue, proof []WriterLeaseTerminalProof) (writerLeaseCompletionEvidence, error) {
	var evidence writerLeaseCompletionEvidence
	if err := validateWriterLeaseTaskKind(task.TaskKind); err != nil {
		return evidence, err
	}
	targets, runtime, err := s.authoritativeWriterLeaseTargets(ctx, qtx, task)
	if err != nil {
		return evidence, err
	}
	evidence.workspaceID = runtime.WorkspaceID
	if persisted, legacy, decodeErr := DecodePersistedWriterLeaseClaim(task, runtime.WorkspaceID.String()); decodeErr != nil {
		return evidence, decodeErr
	} else if !legacy {
		evidence.targetDigest = persisted.Digest
	} else if _, digest, digestErr := CanonicalWriterLeaseClaim(WriterLeaseModeEnforce, runtime.WorkspaceID.String(), targets); digestErr != nil {
		return evidence, digestErr
	} else {
		evidence.targetDigest = digest
	}
	if len(targets) == 0 {
		if len(proof) != 0 {
			return evidence, fmt.Errorf("%w: proof supplied for task without github targets", ErrWriterLeaseFenceRejected)
		}
		return finishWriterLeaseCompletionEvidence(task, evidence, nil), nil
	}
	keys := make([]string, 0, len(targets))
	for _, target := range targets {
		keys = append(keys, target.MutexKey)
	}
	sort.Strings(keys)

	rows, err := qtx.LockWriterLeasesForCompletion(ctx, keys)
	if err != nil {
		return evidence, fmt.Errorf("lock writer leases for completion: %w", err)
	}
	if len(rows) != len(targets) {
		return evidence, fmt.Errorf("%w: authoritative lease row is missing", ErrWriterLeaseFenceRejected)
	}
	if err := validateWriterLeaseProofRows(targets, runtime, task, proof, rows); err != nil {
		return evidence, err
	}
	return finishWriterLeaseCompletionEvidence(task, evidence, writerLeaseProofSnapshot(targets, proof)), nil
}

func writerLeaseProofSnapshot(targets []WriterLeaseTarget, proof []WriterLeaseTerminalProof) []writerLeaseProofSnapshotItem {
	targetByResource := make(map[string]WriterLeaseTarget, len(targets))
	for _, target := range targets {
		targetByResource[target.ResourceID] = target
	}
	items := make([]writerLeaseProofSnapshotItem, 0, len(proof))
	for _, item := range proof {
		digest := item.LeaseTokenSHA256
		if digest == "" {
			hash := sha256.Sum256(item.LeaseToken[:])
			digest = "sha256:" + hex.EncodeToString(hash[:])
		}
		target := targetByResource[item.ResourceID.String()]
		items = append(items, writerLeaseProofSnapshotItem{
			ResourceID:       item.ResourceID.String(),
			MutexKey:         target.MutexKey,
			FenceGeneration:  item.FenceGeneration,
			LeaseTokenSHA256: digest,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ResourceID < items[j].ResourceID })
	return items
}

func finishWriterLeaseCompletionEvidence(task db.AgentTaskQueue, evidence writerLeaseCompletionEvidence, items []writerLeaseProofSnapshotItem) writerLeaseCompletionEvidence {
	if items == nil {
		items = []writerLeaseProofSnapshotItem{}
	}
	snapshot, err := json.Marshal(items)
	if err != nil {
		panic(fmt.Sprintf("marshal writer lease proof snapshot: %v", err))
	}
	evidence.proofSnapshot = snapshot
	proofSum := sha256.Sum256(snapshot)
	evidence.proofDigest = "sha256:" + hex.EncodeToString(proofSum[:])
	receiptEnvelope, err := json.Marshal(struct {
		TaskID       string          `json:"task_id"`
		TargetDigest string          `json:"target_digest"`
		ProofDigest  string          `json:"proof_digest"`
		Snapshot     json.RawMessage `json:"proof_snapshot"`
	}{task.ID.String(), evidence.targetDigest, evidence.proofDigest, json.RawMessage(snapshot)})
	if err != nil {
		panic(fmt.Sprintf("marshal writer lease completion receipt: %v", err))
	}
	receiptSum := sha256.Sum256(receiptEnvelope)
	evidence.receiptDigest = "sha256:" + hex.EncodeToString(receiptSum[:])
	return evidence
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
		if item.ResourceID == uuid.Nil || (item.LeaseToken == uuid.Nil && strings.TrimSpace(item.LeaseTokenSHA256) == "") || item.FenceGeneration <= 0 {
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
		rowDigest := ""
		if row.LeaseToken.Valid {
			hash := sha256.Sum256(row.LeaseToken.Bytes[:])
			rowDigest = "sha256:" + hex.EncodeToString(hash[:])
		}
		itemDigest := item.LeaseTokenSHA256
		if itemDigest == "" {
			hash := sha256.Sum256(item.LeaseToken[:])
			itemDigest = "sha256:" + hex.EncodeToString(hash[:])
		}
		if !ok || !row.HolderID.Valid || row.HolderID.String != holder || row.Status != string(WriteLeaseHeld) || !row.LeaseToken.Valid || rowDigest != itemDigest || row.FenceGeneration != item.FenceGeneration || !row.NotExpired {
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

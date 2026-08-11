package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/companyops"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	companyOpsRuntimeSnapshotSchema  = "hivecrew.execution-runtime-snapshot.secret-free-local.v2"
	companyOpsTerminalSnapshotSchema = "hivecrew.execution-terminal-snapshot.v1"
	companyOpsRetryLineageLimit      = 64
	artifactRevisionEvidenceKind     = "artifact_revision"
)

type companyOpsAssignmentLineage struct {
	workspaceID pgtype.UUID
	commandID   pgtype.UUID
	rootTaskID  pgtype.UUID
	receipt     AssignmentDispatchReceipt
}

// CompanyOpsExecutionPayloadObservation is built from the exact daemon response
// before any response bytes are written. Secret values are accepted only so the
// builder can preserve their key names; they are never copied or hashed.
type CompanyOpsExecutionPayloadObservation struct {
	TaskID                string
	AgentID               string
	RuntimeID             string
	AgentName             string
	Instructions          string
	CustomEnv             map[string]string
	CustomArgs            []string
	MCPConfig             json.RawMessage
	AgentModel            string
	ThinkingLevel         string
	ServiceTier           string
	RuntimeConfig         json.RawMessage
	Skills                any
	SkillRefs             any
	DisabledRuntimeSkills any
	ConnectedApps         any
	RuntimeName           string
	RuntimeMode           string
	RuntimeProvider       string
}

// CompanyOpsExecutionPayloadEvidence is a secret-free projection of the exact
// daemon payload. Digests bind non-secret execution inputs without persisting
// raw instructions, MCP configuration, runtime configuration, or custom args.
type CompanyOpsExecutionPayloadEvidence struct {
	TaskID                      string   `json:"task_id"`
	AgentID                     string   `json:"agent_id"`
	RuntimeID                   string   `json:"runtime_id"`
	AgentName                   string   `json:"agent_name"`
	AgentModel                  string   `json:"agent_model"`
	ThinkingLevel               string   `json:"thinking_level"`
	ServiceTier                 string   `json:"service_tier"`
	RuntimeName                 string   `json:"runtime_name"`
	RuntimeMode                 string   `json:"runtime_mode"`
	RuntimeProvider             string   `json:"runtime_provider"`
	InstructionsDigest          string   `json:"instructions_digest"`
	CustomEnvKeys               []string `json:"custom_env_keys"`
	CustomArgsDigest            string   `json:"custom_args_digest"`
	MCPConfigDigest             string   `json:"mcp_config_digest"`
	RuntimeConfigDigest         string   `json:"runtime_config_digest"`
	SkillsDigest                string   `json:"skills_digest"`
	SkillRefsDigest             string   `json:"skill_refs_digest"`
	DisabledRuntimeSkillsDigest string   `json:"disabled_runtime_skills_digest"`
	ConnectedAppsDigest         string   `json:"connected_apps_digest"`
}

// companyOpsRuntimeSnapshot contains the exact local Assignment lineage plus
// the secret-free projection of the payload sent to the daemon. Authority refs
// not yet observable at this boundary are explicitly named instead of invented.
type companyOpsRuntimeSnapshot struct {
	SchemaVersion           string                             `json:"schema_version"`
	Coverage                string                             `json:"coverage"`
	TaskID                  string                             `json:"task_id"`
	AssignmentRootTaskID    string                             `json:"assignment_root_task_id"`
	AssignmentCommandID     string                             `json:"assignment_command_id"`
	Attempt                 int32                              `json:"attempt"`
	Payload                 CompanyOpsExecutionPayloadEvidence `json:"payload"`
	OmittedSecretFields     []string                           `json:"omitted_secret_fields"`
	UnobservedAuthorityRefs []string                           `json:"unobserved_authority_refs"`
}

type companyOpsCompletedSnapshot struct {
	SchemaVersion string          `json:"schema_version"`
	Status        string          `json:"status"`
	Result        json.RawMessage `json:"result"`
}

type companyOpsFailedSnapshot struct {
	SchemaVersion string `json:"schema_version"`
	Status        string `json:"status"`
	Error         string `json:"error"`
	FailureReason string `json:"failure_reason"`
}

// ensureCompanyOpsExecutionClaim appends the immutable claim receipt for an
// assignment task. Non-CompanyOps tasks are deliberately a no-op. The caller
// must pass transaction-bound queries so a receipt failure rolls back the task
// token and any delivery receipt persisted by the common claim finalizer.
func ensureCompanyOpsExecutionClaim(
	ctx context.Context,
	queries *db.Queries,
	task db.AgentTaskQueue,
	payloadEvidence *CompanyOpsExecutionPayloadEvidence,
) error {
	lineage, err := resolveCompanyOpsAssignmentLineage(ctx, queries, task)
	if err != nil || lineage == nil {
		return err
	}

	if payloadEvidence == nil {
		return fmt.Errorf("%w: CompanyOps claim is missing exact daemon payload evidence", ErrExecutionReceiptConflict)
	}
	if payloadEvidence.TaskID != util.UUIDToString(task.ID) ||
		payloadEvidence.AgentID != util.UUIDToString(task.AgentID) ||
		payloadEvidence.RuntimeID != util.UUIDToString(task.RuntimeID) {
		return fmt.Errorf("%w: daemon payload identity does not match claimed task", ErrExecutionReceiptConflict)
	}

	runtimeSnapshot, runtimeDigest, err := canonicalSnapshot(companyOpsRuntimeSnapshot{
		SchemaVersion:        companyOpsRuntimeSnapshotSchema,
		Coverage:             "exact-daemon-payload-secret-free-local-projection",
		TaskID:               util.UUIDToString(task.ID),
		AssignmentRootTaskID: util.UUIDToString(lineage.rootTaskID),
		AssignmentCommandID:  util.UUIDToString(lineage.commandID),
		Attempt:              task.Attempt,
		Payload:              *payloadEvidence,
		OmittedSecretFields: []string{
			"agent.custom_env.values",
			"task.auth_token",
		},
		UnobservedAuthorityRefs: []string{
			"capacity_ref",
			"credential_ref",
			"endpoint_ref",
			"harness_ref",
		},
	})
	if err != nil {
		return fmt.Errorf("build CompanyOps runtime snapshot: %w", err)
	}
	if !task.DispatchedAt.Valid {
		return fmt.Errorf("CompanyOps execution claim requires dispatched_at")
	}

	repository := NewCompanyOpsPersistenceRepositoryWithQueries(queries)
	claim := ExecutionReceiptClaimSnapshot{
		TaskID:              task.ID,
		WorkspaceID:         lineage.workspaceID,
		IssueID:             task.IssueID,
		AssignmentCommandID: lineage.commandID,
		Target:              lineage.receipt.Target,
		RuntimeSnapshot:     runtimeSnapshot,
		RuntimeDigest:       runtimeDigest,
		ClaimedAt:           task.DispatchedAt.Time.UTC(),
	}

	// A stale claim redelivery refreshes agent_task_queue.dispatched_at. Preserve
	// the first claim time while still comparing every immutable target/runtime
	// field so the same delivery replays and any drift conflicts.
	existing, err := repository.GetExecutionReceipt(ctx, task.ID)
	if err == nil {
		claim.ClaimedAt = existing.Claim.ClaimedAt
	} else if !errors.Is(err, ErrExecutionReceiptNotFound) {
		return err
	}
	_, err = repository.CreateExecutionReceiptClaim(ctx, claim)
	if err != nil {
		return fmt.Errorf("append CompanyOps execution claim: %w", err)
	}
	return nil
}

// requireCompanyOpsExecutionClaim prevents an assignment task from entering
// running without the immutable receipt created by the claim finalizer.
func requireCompanyOpsExecutionClaim(
	ctx context.Context,
	queries *db.Queries,
	task db.AgentTaskQueue,
) error {
	lineage, err := resolveCompanyOpsAssignmentLineage(ctx, queries, task)
	if err != nil || lineage == nil {
		return err
	}
	receipt, err := NewCompanyOpsPersistenceRepositoryWithQueries(queries).GetExecutionReceipt(ctx, task.ID)
	if err != nil {
		return fmt.Errorf("require CompanyOps execution claim: %w", err)
	}
	if err := validateCompanyOpsExecutionClaim(receipt.Claim, task, lineage); err != nil {
		return err
	}
	return nil
}

func finalizeCompanyOpsExecutionCompleted(
	ctx context.Context,
	queries *db.Queries,
	task db.AgentTaskQueue,
	result []byte,
) error {
	terminal, assignment, err := companyOpsCompletedTerminal(ctx, queries, task, result)
	if err != nil || !assignment {
		return err
	}
	_, err = NewCompanyOpsPersistenceRepositoryWithQueries(queries).FinalizeExecutionReceipt(ctx, terminal)
	if err != nil {
		return fmt.Errorf("finalize completed CompanyOps execution receipt: %w", err)
	}
	return nil
}

func finalizeCompanyOpsExecutionFailed(
	ctx context.Context,
	queries *db.Queries,
	task db.AgentTaskQueue,
	errMsg, failureReason string,
) error {
	terminal, assignment, err := companyOpsFailedTerminal(ctx, queries, task, errMsg, failureReason)
	if err != nil || !assignment {
		return err
	}
	_, err = NewCompanyOpsPersistenceRepositoryWithQueries(queries).FinalizeExecutionReceipt(ctx, terminal)
	if err != nil {
		return fmt.Errorf("finalize failed CompanyOps execution receipt: %w", err)
	}
	return nil
}

// replayCompanyOpsExecutionCompleted verifies an already-completed assignment
// callback against the immutable stored terminal. It never repairs a missing
// receipt; recovery belongs to a separately governed recovery path.
func replayCompanyOpsExecutionCompleted(
	ctx context.Context,
	queries *db.Queries,
	task db.AgentTaskQueue,
	result []byte,
) (bool, error) {
	terminal, assignment, err := companyOpsCompletedTerminal(ctx, queries, task, result)
	if err != nil || !assignment {
		return assignment, err
	}
	return true, requireExactCompanyOpsTerminal(ctx, queries, terminal)
}

func replayCompanyOpsExecutionFailed(
	ctx context.Context,
	queries *db.Queries,
	task db.AgentTaskQueue,
	errMsg, failureReason string,
) (bool, error) {
	terminal, assignment, err := companyOpsFailedTerminal(ctx, queries, task, errMsg, failureReason)
	if err != nil || !assignment {
		return assignment, err
	}
	return true, requireExactCompanyOpsTerminal(ctx, queries, terminal)
}

func companyOpsCompletedTerminal(
	ctx context.Context,
	queries *db.Queries,
	task db.AgentTaskQueue,
	result []byte,
) (ExecutionReceiptTerminal, bool, error) {
	lineage, err := resolveCompanyOpsAssignmentLineage(ctx, queries, task)
	if err != nil || lineage == nil {
		return ExecutionReceiptTerminal{}, lineage != nil, err
	}
	if task.Status != "completed" || !task.CompletedAt.Valid {
		return ExecutionReceiptTerminal{}, true, fmt.Errorf("%w: completed callback does not match task terminal state", ErrExecutionReceiptConflict)
	}
	if err := requireCompanyOpsExecutionClaim(ctx, queries, task); err != nil {
		return ExecutionReceiptTerminal{}, true, err
	}
	canonicalResult, err := canonicalJSON(result)
	if err != nil {
		return ExecutionReceiptTerminal{}, true, fmt.Errorf("canonicalize CompanyOps completion result: %w", err)
	}
	resultSnapshot, outputDigest, err := canonicalSnapshot(companyOpsCompletedSnapshot{
		SchemaVersion: companyOpsTerminalSnapshotSchema,
		Status:        "completed",
		Result:        canonicalResult,
	})
	if err != nil {
		return ExecutionReceiptTerminal{}, true, fmt.Errorf("build completed CompanyOps terminal snapshot: %w", err)
	}
	return ExecutionReceiptTerminal{
		TaskID:         task.ID,
		Status:         "completed",
		CompletedAt:    task.CompletedAt.Time.UTC(),
		OutputDigest:   outputDigest,
		ResultSnapshot: resultSnapshot,
	}, true, nil
}

func companyOpsFailedTerminal(
	ctx context.Context,
	queries *db.Queries,
	task db.AgentTaskQueue,
	errMsg, failureReason string,
) (ExecutionReceiptTerminal, bool, error) {
	lineage, err := resolveCompanyOpsAssignmentLineage(ctx, queries, task)
	if err != nil || lineage == nil {
		return ExecutionReceiptTerminal{}, lineage != nil, err
	}
	if task.Status != "failed" || !task.CompletedAt.Valid {
		return ExecutionReceiptTerminal{}, true, fmt.Errorf("%w: failed callback does not match task terminal state", ErrExecutionReceiptConflict)
	}
	if err := requireCompanyOpsExecutionClaim(ctx, queries, task); err != nil {
		return ExecutionReceiptTerminal{}, true, err
	}
	resultSnapshot, outputDigest, err := canonicalSnapshot(companyOpsFailedSnapshot{
		SchemaVersion: companyOpsTerminalSnapshotSchema,
		Status:        "failed",
		Error:         errMsg,
		FailureReason: failureReason,
	})
	if err != nil {
		return ExecutionReceiptTerminal{}, true, fmt.Errorf("build failed CompanyOps terminal snapshot: %w", err)
	}
	return ExecutionReceiptTerminal{
		TaskID:         task.ID,
		Status:         "failed",
		CompletedAt:    task.CompletedAt.Time.UTC(),
		OutputDigest:   outputDigest,
		ResultSnapshot: resultSnapshot,
		Error:          errMsg,
	}, true, nil
}

func requireExactCompanyOpsTerminal(
	ctx context.Context,
	queries *db.Queries,
	expected ExecutionReceiptTerminal,
) error {
	receipt, err := NewCompanyOpsPersistenceRepositoryWithQueries(queries).GetExecutionReceipt(ctx, expected.TaskID)
	if err != nil {
		return fmt.Errorf("read CompanyOps terminal replay: %w", err)
	}
	if receipt.Terminal == nil || !executionTerminalsEqual(*receipt.Terminal, expected) {
		return ErrExecutionReceiptConflict
	}
	return nil
}

func validateCompanyOpsExecutionClaim(
	claim ExecutionReceiptClaimSnapshot,
	task db.AgentTaskQueue,
	lineage *companyOpsAssignmentLineage,
) error {
	if claim.TaskID != task.ID || claim.WorkspaceID != lineage.workspaceID ||
		claim.IssueID != task.IssueID || claim.AssignmentCommandID != lineage.commandID ||
		claim.Target != lineage.receipt.Target || claim.RuntimeDigest != companyOpsDigest(claim.RuntimeSnapshot) {
		return ErrExecutionReceiptConflict
	}
	var snapshot companyOpsRuntimeSnapshot
	decoder := json.NewDecoder(bytes.NewReader(claim.RuntimeSnapshot))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return fmt.Errorf("%w: decode execution runtime snapshot: %v", ErrExecutionReceiptConflict, err)
	}
	if err := ensureJSONDecoderEOF(decoder); err != nil {
		return fmt.Errorf("%w: execution runtime snapshot tail: %v", ErrExecutionReceiptConflict, err)
	}
	canonical, _, err := canonicalSnapshot(snapshot)
	if err != nil || !bytes.Equal(canonical, claim.RuntimeSnapshot) ||
		snapshot.SchemaVersion != companyOpsRuntimeSnapshotSchema ||
		snapshot.Coverage != "exact-daemon-payload-secret-free-local-projection" ||
		snapshot.TaskID != util.UUIDToString(task.ID) ||
		snapshot.AssignmentRootTaskID != util.UUIDToString(lineage.rootTaskID) ||
		snapshot.AssignmentCommandID != util.UUIDToString(lineage.commandID) ||
		snapshot.Attempt != task.Attempt ||
		snapshot.Payload.TaskID != snapshot.TaskID ||
		snapshot.Payload.AgentID != util.UUIDToString(task.AgentID) ||
		snapshot.Payload.RuntimeID != util.UUIDToString(task.RuntimeID) {
		return ErrExecutionReceiptConflict
	}
	return nil
}

func ensureJSONDecoderEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

// BuildCompanyOpsExecutionPayloadEvidence converts the exact daemon response
// into a deterministic secret-free projection. It deliberately records only
// custom environment key names; values never enter the receipt or its digest.
func BuildCompanyOpsExecutionPayloadEvidence(observed CompanyOpsExecutionPayloadObservation) (CompanyOpsExecutionPayloadEvidence, error) {
	for name, value := range map[string]string{
		"task_id":    observed.TaskID,
		"agent_id":   observed.AgentID,
		"runtime_id": observed.RuntimeID,
	} {
		parsed, err := util.ParseUUID(value)
		if err != nil || util.UUIDToString(parsed) != value {
			return CompanyOpsExecutionPayloadEvidence{}, fmt.Errorf("%s must be a canonical UUID", name)
		}
	}
	customEnvKeys := make([]string, 0, len(observed.CustomEnv))
	for key := range observed.CustomEnv {
		customEnvKeys = append(customEnvKeys, key)
	}
	sort.Strings(customEnvKeys)

	digests := make([]string, 8)
	values := []any{
		observed.Instructions,
		observed.CustomArgs,
		observed.MCPConfig,
		observed.RuntimeConfig,
		observed.Skills,
		observed.SkillRefs,
		observed.DisabledRuntimeSkills,
		observed.ConnectedApps,
	}
	for index, value := range values {
		_, digest, err := canonicalSnapshot(value)
		if err != nil {
			return CompanyOpsExecutionPayloadEvidence{}, fmt.Errorf("digest daemon payload field %d: %w", index, err)
		}
		digests[index] = digest
	}
	return CompanyOpsExecutionPayloadEvidence{
		TaskID:                      observed.TaskID,
		AgentID:                     observed.AgentID,
		RuntimeID:                   observed.RuntimeID,
		AgentName:                   observed.AgentName,
		AgentModel:                  observed.AgentModel,
		ThinkingLevel:               observed.ThinkingLevel,
		ServiceTier:                 observed.ServiceTier,
		RuntimeName:                 observed.RuntimeName,
		RuntimeMode:                 observed.RuntimeMode,
		RuntimeProvider:             observed.RuntimeProvider,
		InstructionsDigest:          digests[0],
		CustomEnvKeys:               customEnvKeys,
		CustomArgsDigest:            digests[1],
		MCPConfigDigest:             digests[2],
		RuntimeConfigDigest:         digests[3],
		SkillsDigest:                digests[4],
		SkillRefsDigest:             digests[5],
		DisabledRuntimeSkillsDigest: digests[6],
		ConnectedAppsDigest:         digests[7],
	}, nil
}

// resolveCompanyOpsAssignmentLineage classifies only the canonical assignment
// trigger and follows retry_of_task_id back to the immutable initial task.
// Other task families are untouched.
func resolveCompanyOpsAssignmentLineage(
	ctx context.Context,
	queries *db.Queries,
	task db.AgentTaskQueue,
) (*companyOpsAssignmentLineage, error) {
	if queries == nil {
		return nil, fmt.Errorf("CompanyOps execution queries are required")
	}
	if !task.IssueID.Valid {
		return nil, nil
	}

	original := task
	current := task
	visited := make(map[[16]byte]struct{}, 4)
	var commandID pgtype.UUID
	canonicalDetected := false
	lineageDrift := false
	for depth := 0; depth < companyOpsRetryLineageLimit; depth++ {
		if !current.ID.Valid {
			if canonicalDetected {
				return nil, fmt.Errorf("%w: retry lineage contains an invalid task id", ErrExecutionReceiptConflict)
			}
			return nil, nil
		}
		if _, duplicate := visited[current.ID.Bytes]; duplicate {
			if canonicalDetected {
				return nil, fmt.Errorf("%w: retry lineage cycle", ErrExecutionReceiptConflict)
			}
			return nil, nil
		}
		visited[current.ID.Bytes] = struct{}{}

		isAssignmentEvidence := current.TriggerEvidenceKind.Valid &&
			current.TriggerEvidenceKind.String == assignmentDispatchEvidenceKind
		isArtifactRevisionEvidence := current.TriggerEvidenceKind.Valid &&
			current.TriggerEvidenceKind.String == artifactRevisionEvidenceKind
		if isAssignmentEvidence || isArtifactRevisionEvidence {
			canonicalDetected = true
			if lineageDrift {
				return nil, fmt.Errorf("%w: retry lineage changed trigger evidence", ErrExecutionReceiptConflict)
			}
			if !current.TriggerEvidenceRefID.Valid {
				return nil, fmt.Errorf("%w: CompanyOps task is missing trigger evidence", ErrExecutionReceiptConflict)
			}
			observedCommandID := current.TriggerEvidenceRefID
			if isArtifactRevisionEvidence {
				issue, err := queries.GetIssue(ctx, original.IssueID)
				if err != nil {
					return nil, fmt.Errorf("load CompanyOps revision Issue: %w", err)
				}
				event, err := queries.GetArtifactEvent(ctx, db.GetArtifactEventParams{
					WorkspaceID: issue.WorkspaceID,
					ID:          current.TriggerEvidenceRefID,
				})
				if err != nil || event.EventType != string(companyops.ArtifactEventChangesRequested) {
					return nil, fmt.Errorf("%w: artifact revision evidence is not an exact changes_requested event", ErrExecutionReceiptConflict)
				}
				observedCommandID = event.LineageID
			}
			if commandID.Valid && commandID != observedCommandID {
				return nil, fmt.Errorf("%w: retry lineage changed assignment command", ErrExecutionReceiptConflict)
			}
			commandID = observedCommandID
		} else if current.RetryOfTaskID.Valid {
			// We do not yet know whether the root is CompanyOps. Remember the
			// descendant drift and fail only if a canonical assignment appears.
			lineageDrift = true
		}

		if current.AgentID != original.AgentID || current.IssueID != original.IssueID {
			if canonicalDetected {
				return nil, fmt.Errorf("%w: retry lineage changed Agent or Issue", ErrExecutionReceiptConflict)
			}
			lineageDrift = true
		}
		if !current.RetryOfTaskID.Valid {
			if !canonicalDetected {
				return nil, nil
			}
			if (!isAssignmentEvidence && !isArtifactRevisionEvidence) || lineageDrift || !commandID.Valid {
				return nil, fmt.Errorf("%w: retry root is not the canonical assignment task", ErrExecutionReceiptConflict)
			}
			issue, err := queries.GetIssue(ctx, original.IssueID)
			if err != nil {
				return nil, fmt.Errorf("load CompanyOps execution Issue: %w", err)
			}
			repository := NewCompanyOpsPersistenceRepositoryWithQueries(queries)
			receipt, found, err := repository.GetAssignmentDispatchReceipt(ctx, issue.WorkspaceID, commandID)
			if err != nil {
				return nil, err
			}
			if !found {
				return nil, fmt.Errorf("%w: assignment dispatch receipt not found", ErrExecutionReceiptConflict)
			}
			expectedAgentRef := "/api/agents/" + util.UUIDToString(original.AgentID)
			if receipt.CommandID != commandID || receipt.WorkspaceID != issue.WorkspaceID ||
				receipt.IssueID != original.IssueID || receipt.LocalAgentID != original.AgentID ||
				(isAssignmentEvidence && receipt.InitialTaskID != current.ID) || receipt.Target.AgentRef != expectedAgentRef {
				return nil, fmt.Errorf("%w: assignment receipt does not match retry lineage", ErrExecutionReceiptConflict)
			}
			return &companyOpsAssignmentLineage{
				workspaceID: issue.WorkspaceID,
				commandID:   commandID,
				rootTaskID:  receipt.InitialTaskID,
				receipt:     receipt,
			}, nil
		}
		if !current.ParentTaskID.Valid || current.ParentTaskID != current.RetryOfTaskID {
			if canonicalDetected {
				return nil, fmt.Errorf("%w: retry parent lineage mismatch", ErrExecutionReceiptConflict)
			}
			lineageDrift = true
		}
		parent, err := queries.GetAgentTask(ctx, current.RetryOfTaskID)
		if err != nil {
			if canonicalDetected {
				return nil, fmt.Errorf("load CompanyOps retry parent: %w", err)
			}
			return nil, nil
		}
		current = parent
	}
	if canonicalDetected {
		return nil, fmt.Errorf("%w: retry lineage exceeds %d tasks", ErrExecutionReceiptConflict, companyOpsRetryLineageLimit)
	}
	return nil, nil
}

func canonicalSnapshot(value any) (json.RawMessage, string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, "", err
	}
	return payload, companyOpsDigest(payload), nil
}

func canonicalJSON(value []byte) (json.RawMessage, error) {
	if len(bytes.TrimSpace(value)) == 0 {
		return json.RawMessage("null"), nil
	}
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("multiple JSON values")
		}
		return nil, err
	}
	payload, err := json.Marshal(decoded)
	if err != nil {
		return nil, err
	}
	return payload, nil
}

func companyOpsDigest(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

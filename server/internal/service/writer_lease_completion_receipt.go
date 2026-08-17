package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func verifyWriterLeaseCompletionReceipt(taskID, targetDigest string, receipt db.WriterLeaseCompletionReceipt) error {
	if receipt.TargetDigest != targetDigest || receipt.TaskID.String() != taskID {
		return fmt.Errorf("writer lease completion receipt identity mismatch")
	}
	var snapshot any
	if err := json.Unmarshal(receipt.ProofSnapshot, &snapshot); err != nil {
		return fmt.Errorf("writer lease completion proof snapshot is invalid: %w", err)
	}
	canonicalSnapshot, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	proofSum := sha256.Sum256(canonicalSnapshot)
	proofDigest := "sha256:" + hex.EncodeToString(proofSum[:])
	if receipt.ProofDigest != proofDigest {
		return fmt.Errorf("writer lease completion proof digest mismatch")
	}
	envelope, err := json.Marshal(struct {
		TaskID       string          `json:"task_id"`
		TargetDigest string          `json:"target_digest"`
		ProofDigest  string          `json:"proof_digest"`
		Snapshot     json.RawMessage `json:"proof_snapshot"`
	}{taskID, targetDigest, proofDigest, json.RawMessage(canonicalSnapshot)})
	if err != nil {
		return err
	}
	receiptSum := sha256.Sum256(envelope)
	if receipt.ReceiptDigest != "sha256:"+hex.EncodeToString(receiptSum[:]) {
		return fmt.Errorf("writer lease completion receipt digest mismatch")
	}
	return nil
}

func (s *TaskService) persistWriterLeaseCompletionReceipt(ctx context.Context, qtx *db.Queries, task db.AgentTaskQueue, evidence writerLeaseCompletionEvidence) error {
	_, err := qtx.InsertWriterLeaseCompletionReceipt(ctx, db.InsertWriterLeaseCompletionReceiptParams{
		WorkspaceID:   evidence.workspaceID,
		TaskID:        task.ID,
		TargetDigest:  evidence.targetDigest,
		ProofSnapshot: evidence.proofSnapshot,
		ProofDigest:   evidence.proofDigest,
		ReceiptDigest: evidence.receiptDigest,
	})
	if err == nil {
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("insert writer lease completion receipt: %w", err)
	}
	existing, getErr := qtx.GetWriterLeaseCompletionReceipt(ctx, db.GetWriterLeaseCompletionReceiptParams{
		WorkspaceID: evidence.workspaceID,
		TaskID:      task.ID,
	})
	if getErr != nil {
		return fmt.Errorf("read writer lease completion receipt replay: %w", getErr)
	}
	if existing.TargetDigest != evidence.targetDigest ||
		existing.ProofDigest != evidence.proofDigest ||
		existing.ReceiptDigest != evidence.receiptDigest ||
		!canonicalJSONEqual(existing.ProofSnapshot, evidence.proofSnapshot) {
		return fmt.Errorf("%w: writer lease completion receipt drift", ErrWriterLeaseFenceRejected)
	}
	return nil
}

func canonicalJSONEqual(left, right []byte) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	leftCanonical, leftErr := json.Marshal(leftValue)
	rightCanonical, rightErr := json.Marshal(rightValue)
	return leftErr == nil && rightErr == nil && string(leftCanonical) == string(rightCanonical)
}

func (s *TaskService) requireWriterLeaseCompletionReceipt(ctx context.Context, qtx *db.Queries, task db.AgentTaskQueue) error {
	runtime, err := qtx.GetAgentRuntime(ctx, task.RuntimeID)
	if err != nil {
		return fmt.Errorf("load runtime for completed writer lease receipt: %w", err)
	}
	if _, err := qtx.GetWriterLeaseCompletionReceipt(ctx, db.GetWriterLeaseCompletionReceiptParams{
		WorkspaceID: runtime.WorkspaceID,
		TaskID:      task.ID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: completed task has no writer lease receipt", ErrWriterLeaseFenceRejected)
		}
		return fmt.Errorf("read completed writer lease receipt: %w", err)
	}
	claim, legacy, err := DecodePersistedWriterLeaseClaim(task, runtime.WorkspaceID.String())
	if err != nil {
		return err
	}
	if legacy || claim.Mode != WriterLeaseModeEnforce {
		return fmt.Errorf("%w: completed task lacks enforced migration-406 claim", ErrWriterLeaseFenceRejected)
	}
	receipt, err := qtx.GetWriterLeaseCompletionReceipt(ctx, db.GetWriterLeaseCompletionReceiptParams{WorkspaceID: runtime.WorkspaceID, TaskID: task.ID})
	if err != nil {
		return err
	}
	if err := verifyWriterLeaseCompletionReceipt(task.ID.String(), claim.Digest, receipt); err != nil {
		return fmt.Errorf("%w: %v", ErrWriterLeaseFenceRejected, err)
	}
	return nil
}

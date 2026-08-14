package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/continuousdispatch"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var ErrContinuousDispatchReceiptConflict = errors.New("continuous dispatch receipt conflicts with committed generation")

// ContinuousDispatchReceipt is immutable evidence that one exact dispatch
// generation created one Task on one employee/Agent/runtime/model/account
// route. It does not replace Task, Issue, employee, or Runtime truth.
type ContinuousDispatchReceipt struct {
	Identity      continuousdispatch.DispatchIdentity
	TaskID        pgtype.UUID
	EmployeeRef   string
	LocalAgentID  pgtype.UUID
	RuntimeID     pgtype.UUID
	Model         string
	AccountRef    string
	RequestDigest string
}

type ContinuousDispatchReceiptRepository struct {
	queries *db.Queries
}

func NewContinuousDispatchReceiptRepository(queries *db.Queries) *ContinuousDispatchReceiptRepository {
	return &ContinuousDispatchReceiptRepository{queries: queries}
}

// Append returns the existing immutable receipt for an exact replay. A second
// payload for the same five-field identity is a conflict, never a replacement.
func (r *ContinuousDispatchReceiptRepository) Append(ctx context.Context, receipt ContinuousDispatchReceipt) (ContinuousDispatchReceipt, error) {
	if r == nil || r.queries == nil {
		return ContinuousDispatchReceipt{}, fmt.Errorf("continuous dispatch receipt queries are required")
	}
	if err := validateContinuousDispatchReceipt(receipt); err != nil {
		return ContinuousDispatchReceipt{}, err
	}
	row, err := r.queries.InsertContinuousDispatchReceipt(ctx, continuousDispatchReceiptParams(receipt))
	if err == nil {
		return continuousDispatchReceiptFromDB(row), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ContinuousDispatchReceipt{}, fmt.Errorf("insert continuous dispatch receipt: %w", err)
	}
	existing, getErr := r.Get(ctx, receipt.Identity)
	if getErr != nil {
		return ContinuousDispatchReceipt{}, fmt.Errorf("reload continuous dispatch receipt: %w", getErr)
	}
	if !continuousDispatchReceiptsEqual(existing, receipt) {
		return ContinuousDispatchReceipt{}, ErrContinuousDispatchReceiptConflict
	}
	return existing, nil
}

func (r *ContinuousDispatchReceiptRepository) Get(ctx context.Context, identity continuousdispatch.DispatchIdentity) (ContinuousDispatchReceipt, error) {
	if r == nil || r.queries == nil {
		return ContinuousDispatchReceipt{}, fmt.Errorf("continuous dispatch receipt queries are required")
	}
	if !identity.Complete() {
		return ContinuousDispatchReceipt{}, fmt.Errorf("continuous dispatch identity is incomplete")
	}
	row, err := r.queries.GetContinuousDispatchReceipt(ctx, db.GetContinuousDispatchReceiptParams{
		WorkspaceID:       parseDispatchUUID(identity.WorkspaceID),
		IssueID:           parseDispatchUUID(identity.IssueID),
		Stage:             identity.Stage,
		CandidateRevision: identity.CandidateRevision,
		Generation:        identity.Generation,
	})
	if err != nil {
		return ContinuousDispatchReceipt{}, err
	}
	return continuousDispatchReceiptFromDB(row), nil
}

func validateContinuousDispatchReceipt(receipt ContinuousDispatchReceipt) error {
	if !receipt.Identity.Complete() {
		return fmt.Errorf("continuous dispatch identity is incomplete")
	}
	if !parseDispatchUUID(receipt.Identity.WorkspaceID).Valid || !parseDispatchUUID(receipt.Identity.IssueID).Valid {
		return fmt.Errorf("continuous dispatch identity UUIDs are invalid")
	}
	for name, value := range map[string]pgtype.UUID{
		"task_id": receipt.TaskID, "local_agent_id": receipt.LocalAgentID, "runtime_id": receipt.RuntimeID,
	} {
		if !value.Valid || value.Bytes == ([16]byte{}) {
			return fmt.Errorf("%s is required", name)
		}
	}
	for name, value := range map[string]string{
		"employee_ref": receipt.EmployeeRef, "model": receipt.Model, "account_ref": receipt.AccountRef,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if len(receipt.RequestDigest) != 71 || !strings.HasPrefix(receipt.RequestDigest, "sha256:") {
		return fmt.Errorf("request_digest must be a canonical sha256 digest")
	}
	for _, char := range receipt.RequestDigest[len("sha256:"):] {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return fmt.Errorf("request_digest must be a canonical sha256 digest")
		}
	}
	return nil
}

func parseDispatchUUID(value string) pgtype.UUID {
	var parsed pgtype.UUID
	_ = parsed.Scan(value)
	return parsed
}

func continuousDispatchReceiptParams(receipt ContinuousDispatchReceipt) db.InsertContinuousDispatchReceiptParams {
	return db.InsertContinuousDispatchReceiptParams{
		WorkspaceID: parseDispatchUUID(receipt.Identity.WorkspaceID), IssueID: parseDispatchUUID(receipt.Identity.IssueID),
		Stage: receipt.Identity.Stage, CandidateRevision: receipt.Identity.CandidateRevision, Generation: receipt.Identity.Generation,
		TaskID: receipt.TaskID, EmployeeRef: receipt.EmployeeRef, LocalAgentID: receipt.LocalAgentID,
		RuntimeID: receipt.RuntimeID, Model: receipt.Model, AccountRef: receipt.AccountRef, RequestDigest: receipt.RequestDigest,
	}
}

func continuousDispatchReceiptFromDB(row db.ContinuousDispatchReceipt) ContinuousDispatchReceipt {
	return ContinuousDispatchReceipt{
		Identity: continuousdispatch.DispatchIdentity{
			WorkspaceID: shadowUUIDString(row.WorkspaceID), IssueID: shadowUUIDString(row.IssueID), Stage: row.Stage,
			CandidateRevision: row.CandidateRevision, Generation: row.Generation,
		},
		TaskID: row.TaskID, EmployeeRef: row.EmployeeRef, LocalAgentID: row.LocalAgentID,
		RuntimeID: row.RuntimeID, Model: row.Model, AccountRef: row.AccountRef, RequestDigest: row.RequestDigest,
	}
}

func continuousDispatchReceiptsEqual(left, right ContinuousDispatchReceipt) bool {
	return left.Identity == right.Identity && left.TaskID == right.TaskID && left.EmployeeRef == right.EmployeeRef &&
		left.LocalAgentID == right.LocalAgentID && left.RuntimeID == right.RuntimeID && left.Model == right.Model &&
		left.AccountRef == right.AccountRef && left.RequestDigest == right.RequestDigest
}

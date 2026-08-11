package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/companyops"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var companyOpsWorkOrderDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

var (
	// ErrCompanyOpsWorkOrderProjectionConflict means a WorkOrder source ref is
	// already linked to a different immutable authority observation.
	ErrCompanyOpsWorkOrderProjectionConflict = errors.New("companyops WorkOrder projection payload conflict")
	// ErrCompanyOpsWorkOrderProjectionOrphan means the provenance link exists,
	// but its local Issue projection is missing. The service fails closed rather
	// than silently creating a second Issue for the same authoritative object.
	ErrCompanyOpsWorkOrderProjectionOrphan = errors.New("companyops WorkOrder projection Issue is missing")
)

// CompanyOpsWorkOrderProjectionRequest is the minimum authority observation
// required to create a local HiveCrew execution projection. The canonical
// Issue title comes only from WorkOrder.DisplayName; it is presentation data,
// not an identity selector, and is never copied into the provenance link.
type CompanyOpsWorkOrderProjectionRequest struct {
	WorkspaceID      pgtype.UUID
	ActorUserID      pgtype.UUID
	WorkOrder        companyops.AuthoritySnapshot
	SourceObservedAt time.Time
}

// CompanyOpsWorkOrderProjection returns the local Issue and its immutable
// provenance-only link. Created is false for an exact idempotent replay.
type CompanyOpsWorkOrderProjection struct {
	Issue   db.Issue
	Link    ExternalWorkOrderLink
	Created bool
}

// CompanyOpsWorkOrderProjectionService composes the canonical Issue writer and
// the external WorkOrder link in one database transaction.
type CompanyOpsWorkOrderProjectionService struct {
	issueService *IssueService
}

func NewCompanyOpsWorkOrderProjectionService(issueService *IssueService) (*CompanyOpsWorkOrderProjectionService, error) {
	if issueService == nil {
		return nil, fmt.Errorf("companyops WorkOrder projection Issue service is required")
	}
	if issueService.Queries == nil {
		return nil, fmt.Errorf("companyops WorkOrder projection queries are required")
	}
	if issueService.TxStarter == nil {
		return nil, fmt.Errorf("companyops WorkOrder projection transaction starter is required")
	}
	return &CompanyOpsWorkOrderProjectionService{issueService: issueService}, nil
}

// Project creates or finds the one local Issue projection for an exact
// authoritative WorkOrder observation. It does not copy or mutate WorkOrder
// lifecycle state; HiveCosm remains the WorkOrder authority.
func (s *CompanyOpsWorkOrderProjectionService) Project(
	ctx context.Context,
	req CompanyOpsWorkOrderProjectionRequest,
) (CompanyOpsWorkOrderProjection, error) {
	if s == nil || s.issueService == nil {
		return CompanyOpsWorkOrderProjection{}, fmt.Errorf("companyops WorkOrder projection service is required")
	}
	if err := validateCompanyOpsWorkOrderProjectionRequest(req); err != nil {
		return CompanyOpsWorkOrderProjection{}, err
	}
	// PostgreSQL TIMESTAMPTZ stores microseconds. Canonicalize the initial
	// evidence timestamp before writing it. A replay deliberately does not
	// compare observed_at because each HTTP authority read is a new observation
	// even when source_ref/revision/digest are unchanged.
	req.SourceObservedAt = req.SourceObservedAt.UTC().Truncate(time.Microsecond)

	tx, err := s.issueService.TxStarter.Begin(ctx)
	if err != nil {
		return CompanyOpsWorkOrderProjection{}, fmt.Errorf("begin companyops WorkOrder projection transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.Background())
		}
	}()
	qtx := s.issueService.Queries.WithTx(tx)

	if err := qtx.LockCompanyOpsAssignmentCommand(ctx, db.LockCompanyOpsAssignmentCommandParams{
		WorkspaceID: req.WorkspaceID,
		CommandID:   companyOpsWorkOrderProjectionLockID(req.WorkOrder.SourceRef),
	}); err != nil {
		return CompanyOpsWorkOrderProjection{}, fmt.Errorf("lock companyops WorkOrder projection: %w", err)
	}

	if _, err := qtx.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		UserID:      req.ActorUserID,
		WorkspaceID: req.WorkspaceID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return CompanyOpsWorkOrderProjection{}, fmt.Errorf("actor_user_id is not a member of the workspace")
		}
		return CompanyOpsWorkOrderProjection{}, fmt.Errorf("resolve companyops WorkOrder projection actor: %w", err)
	}

	existing, err := qtx.GetExternalWorkOrderLink(ctx, db.GetExternalWorkOrderLinkParams{
		WorkspaceID:  req.WorkspaceID,
		WorkOrderRef: req.WorkOrder.SourceRef,
	})
	if err == nil {
		link := externalWorkOrderLinkFromDB(existing)
		if !companyOpsWorkOrderProjectionLinkMatches(link, req) {
			return CompanyOpsWorkOrderProjection{}, ErrCompanyOpsWorkOrderProjectionConflict
		}
		issue, err := qtx.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{
			ID:          link.IssueID,
			WorkspaceID: req.WorkspaceID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return CompanyOpsWorkOrderProjection{}, ErrCompanyOpsWorkOrderProjectionOrphan
		}
		if err != nil {
			return CompanyOpsWorkOrderProjection{}, fmt.Errorf("read companyops WorkOrder Issue projection: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return CompanyOpsWorkOrderProjection{}, fmt.Errorf("commit companyops WorkOrder projection replay: %w", err)
		}
		committed = true
		return CompanyOpsWorkOrderProjection{Issue: issue, Link: link, Created: false}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return CompanyOpsWorkOrderProjection{}, fmt.Errorf("read external WorkOrder link: %w", err)
	}

	issueParams := IssueCreateParams{
		WorkspaceID:    req.WorkspaceID,
		Title:          strings.TrimSpace(req.WorkOrder.DisplayName),
		Status:         "todo",
		Priority:       "none",
		CreatorType:    "member",
		CreatorID:      req.ActorUserID,
		AllowDuplicate: true,
	}
	issueResult, err := s.issueService.createInTransaction(ctx, tx, qtx, issueParams)
	if err != nil {
		return CompanyOpsWorkOrderProjection{}, fmt.Errorf("create companyops WorkOrder Issue projection: %w", err)
	}

	requestedLink := ExternalWorkOrderLink{
		WorkspaceID:      req.WorkspaceID,
		WorkOrderRef:     req.WorkOrder.SourceRef,
		LinkedRevision:   req.WorkOrder.Revision,
		LinkedDigest:     req.WorkOrder.ContentDigest,
		SourceObservedAt: req.SourceObservedAt,
		FreshnessAtLink:  req.WorkOrder.Freshness,
		IssueID:          issueResult.Issue.ID,
	}
	repository := NewCompanyOpsPersistenceRepositoryWithQueries(qtx)
	link, err := repository.EnsureExternalWorkOrderLink(ctx, requestedLink)
	if err != nil {
		if errors.Is(err, ErrExternalWorkOrderLinkConflict) {
			return CompanyOpsWorkOrderProjection{}, ErrCompanyOpsWorkOrderProjectionConflict
		}
		return CompanyOpsWorkOrderProjection{}, fmt.Errorf("append external WorkOrder link: %w", err)
	}
	if !externalWorkOrderLinksEqual(link, requestedLink) {
		return CompanyOpsWorkOrderProjection{}, ErrCompanyOpsWorkOrderProjectionConflict
	}

	if err := tx.Commit(ctx); err != nil {
		return CompanyOpsWorkOrderProjection{}, fmt.Errorf("commit companyops WorkOrder projection: %w", err)
	}
	committed = true
	issueResult = s.issueService.finishCreate(ctx, issueParams, IssueCreateOpts{
		ActorID:  util.UUIDToString(req.ActorUserID),
		Platform: "companyops",
	}, issueResult)

	return CompanyOpsWorkOrderProjection{Issue: issueResult.Issue, Link: link, Created: true}, nil
}

func validateCompanyOpsWorkOrderProjectionRequest(req CompanyOpsWorkOrderProjectionRequest) error {
	for name, value := range map[string]pgtype.UUID{
		"workspace_id":  req.WorkspaceID,
		"actor_user_id": req.ActorUserID,
	} {
		if !value.Valid || value.Bytes == ([16]byte{}) {
			return fmt.Errorf("%s is required", name)
		}
	}
	if req.WorkOrder.Kind != "WorkOrder" {
		return fmt.Errorf("WorkOrder authority kind must be WorkOrder")
	}
	if strings.TrimSpace(req.WorkOrder.SourceRef) == "" {
		return fmt.Errorf("work_order_ref is required")
	}
	if req.WorkOrder.SourceRef != strings.TrimSpace(req.WorkOrder.SourceRef) {
		return fmt.Errorf("work_order_ref must not contain surrounding whitespace")
	}
	if strings.TrimSpace(req.WorkOrder.Revision) == "" {
		return fmt.Errorf("work_order_revision is required")
	}
	if !companyOpsWorkOrderDigestPattern.MatchString(req.WorkOrder.ContentDigest) {
		return fmt.Errorf("work_order_digest must be a canonical sha256 digest")
	}
	if req.SourceObservedAt.IsZero() {
		return fmt.Errorf("source_observed_at is required")
	}
	if req.WorkOrder.Freshness != "current" {
		return fmt.Errorf("WorkOrder freshness must be current")
	}
	if strings.TrimSpace(req.WorkOrder.DisplayName) == "" {
		return fmt.Errorf("WorkOrder authority display_name is required")
	}
	return nil
}

func companyOpsWorkOrderProjectionLockID(workOrderRef string) pgtype.UUID {
	return util.MustParseUUID(uuid.NewSHA1(
		uuid.NameSpaceURL,
		[]byte("hivecrew:companyops:workorder-projection:"+workOrderRef),
	).String())
}

func companyOpsWorkOrderProjectionLinkMatches(
	link ExternalWorkOrderLink,
	req CompanyOpsWorkOrderProjectionRequest,
) bool {
	return link.WorkspaceID == req.WorkspaceID &&
		link.WorkOrderRef == req.WorkOrder.SourceRef &&
		link.LinkedRevision == req.WorkOrder.Revision &&
		link.LinkedDigest == req.WorkOrder.ContentDigest &&
		link.FreshnessAtLink == req.WorkOrder.Freshness &&
		link.IssueID.Valid
}

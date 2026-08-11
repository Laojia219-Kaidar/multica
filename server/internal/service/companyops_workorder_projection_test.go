package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/companyops"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type workOrderProjectionFixture struct {
	workspaceID pgtype.UUID
	userID      pgtype.UUID
}

func seedWorkOrderProjectionFixture(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
) workOrderProjectionFixture {
	t.Helper()
	suffix := uuid.NewString()
	var fixture workOrderProjectionFixture
	if err := pool.QueryRow(ctx,
		`INSERT INTO "user" (name, email) VALUES ($1, $2) RETURNING id`,
		"WorkOrder Projection Test",
		fmt.Sprintf("workorder-projection-%s@hivecrew.test", suffix),
	).Scan(&fixture.userID); err != nil {
		t.Fatalf("seed WorkOrder projection user: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO workspace (name, slug) VALUES ($1, $2) RETURNING id`,
		"WorkOrder Projection Test",
		"workorder-projection-"+suffix,
	).Scan(&fixture.workspaceID); err != nil {
		t.Fatalf("seed WorkOrder projection workspace: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'owner')`,
		fixture.workspaceID,
		fixture.userID,
	); err != nil {
		t.Fatalf("seed WorkOrder projection member: %v", err)
	}

	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM external_work_order_link WHERE workspace_id = $1`, fixture.workspaceID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM issue WHERE workspace_id = $1`, fixture.workspaceID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM member WHERE workspace_id = $1`, fixture.workspaceID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM workspace WHERE id = $1`, fixture.workspaceID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM "user" WHERE id = $1`, fixture.userID)
	})
	return fixture
}

func newWorkOrderProjectionService(
	t *testing.T,
	queries *db.Queries,
	txStarter TxStarter,
	bus *events.Bus,
) *CompanyOpsWorkOrderProjectionService {
	t.Helper()
	issueService := NewIssueService(queries, txStarter, bus, nil, nil)
	service, err := NewCompanyOpsWorkOrderProjectionService(issueService)
	if err != nil {
		t.Fatalf("NewCompanyOpsWorkOrderProjectionService: %v", err)
	}
	return service
}

func workOrderProjectionRequest(
	fixture workOrderProjectionFixture,
	workOrderRef string,
) CompanyOpsWorkOrderProjectionRequest {
	return CompanyOpsWorkOrderProjectionRequest{
		WorkspaceID:      fixture.workspaceID,
		ActorUserID:      fixture.userID,
		SourceObservedAt: time.Date(2026, 8, 11, 3, 4, 5, 987654321, time.FixedZone("CST", 8*60*60)),
		WorkOrder: companyops.AuthoritySnapshot{
			Kind:          "WorkOrder",
			SourceRef:     workOrderRef,
			Revision:      "workorder-revision-7",
			ContentDigest: "sha256:" + strings.Repeat("7", 64),
			Freshness:     "current",
			DisplayName:   "  Build the real Owner-to-Outcome loop  ",
		},
	}
}

func TestCompanyOpsWorkOrderProjection_CanonicalCreateReplayAndConflict(t *testing.T) {
	pool := newProductionCompanyOpsPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	fixture := seedWorkOrderProjectionFixture(t, ctx, pool)

	var (
		eventMu     sync.Mutex
		issueEvents int
	)
	bus := events.New()
	bus.Subscribe(protocol.EventIssueCreated, func(event events.Event) {
		if event.WorkspaceID == util.UUIDToString(fixture.workspaceID) {
			eventMu.Lock()
			issueEvents++
			eventMu.Unlock()
		}
	})
	service := newWorkOrderProjectionService(t, db.New(pool), pool, bus)
	req := workOrderProjectionRequest(fixture, "hive://hivecosm/delivery/project/PRJ-HIVECREW-P2/work-order/WO-P2-PROJECTION-001")

	first, err := service.Project(ctx, req)
	if err != nil {
		t.Fatalf("Project first call: %v", err)
	}
	if !first.Created {
		t.Fatal("first Project Created = false, want true")
	}
	if first.Issue.Title != strings.TrimSpace(req.WorkOrder.DisplayName) ||
		first.Issue.Status != "todo" || first.Issue.Priority != "none" ||
		first.Issue.AssigneeType.Valid || first.Issue.AssigneeID.Valid ||
		first.Issue.CreatorType != "member" || first.Issue.CreatorID != fixture.userID {
		t.Fatalf("canonical WorkOrder Issue projection = %+v", first.Issue)
	}
	if first.Link.IssueID != first.Issue.ID || first.Link.WorkOrderRef != req.WorkOrder.SourceRef ||
		first.Link.LinkedRevision != req.WorkOrder.Revision || first.Link.LinkedDigest != req.WorkOrder.ContentDigest ||
		first.Link.FreshnessAtLink != "current" ||
		!first.Link.SourceObservedAt.Equal(req.SourceObservedAt.UTC().Truncate(time.Microsecond)) {
		t.Fatalf("canonical WorkOrder link = %+v", first.Link)
	}

	replayRequest := req
	replayRequest.SourceObservedAt = req.SourceObservedAt.Add(9 * time.Minute)
	replay, err := service.Project(ctx, replayRequest)
	if err != nil {
		t.Fatalf("Project exact replay: %v", err)
	}
	if replay.Created || replay.Issue.ID != first.Issue.ID || replay.Link.IssueID != first.Issue.ID {
		t.Fatalf("exact replay = %+v, want same Issue with Created=false", replay)
	}
	eventMu.Lock()
	gotEvents := issueEvents
	eventMu.Unlock()
	if gotEvents != 1 {
		t.Fatalf("IssueCreated events after replay = %d, want 1", gotEvents)
	}

	conflict := req
	conflict.WorkOrder.Revision = "workorder-revision-8"
	conflict.WorkOrder.ContentDigest = "sha256:" + strings.Repeat("8", 64)
	if _, err := service.Project(ctx, conflict); !errors.Is(err, ErrCompanyOpsWorkOrderProjectionConflict) {
		t.Fatalf("different authority replay error = %v, want %v", err, ErrCompanyOpsWorkOrderProjectionConflict)
	}

	var issueCount, linkCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM issue WHERE workspace_id = $1`, fixture.workspaceID).Scan(&issueCount); err != nil {
		t.Fatalf("count projected Issues: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM external_work_order_link WHERE workspace_id = $1`, fixture.workspaceID).Scan(&linkCount); err != nil {
		t.Fatalf("count WorkOrder links: %v", err)
	}
	if issueCount != 1 || linkCount != 1 {
		t.Fatalf("Issue/link counts = %d/%d, want 1/1", issueCount, linkCount)
	}
}

func TestCompanyOpsWorkOrderProjection_ConcurrentExactReplayCreatesOneIssue(t *testing.T) {
	pool := newProductionCompanyOpsPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	fixture := seedWorkOrderProjectionFixture(t, ctx, pool)
	service := newWorkOrderProjectionService(t, db.New(pool), pool, events.New())
	req := workOrderProjectionRequest(fixture, "hive://hivecosm/delivery/project/PRJ-HIVECREW-P2/work-order/WO-P2-PROJECTION-CONCURRENT")

	const workers = 8
	results := make(chan CompanyOpsWorkOrderProjection, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := service.Project(ctx, req)
			results <- result
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Project: %v", err)
		}
	}
	var (
		issueID      pgtype.UUID
		createdCount int
	)
	for result := range results {
		if !issueID.Valid {
			issueID = result.Issue.ID
		}
		if result.Issue.ID != issueID || result.Link.IssueID != issueID {
			t.Fatalf("concurrent replay returned different Issue: %+v", result)
		}
		if result.Created {
			createdCount++
		}
	}
	if createdCount != 1 {
		t.Fatalf("concurrent Created=true count = %d, want 1", createdCount)
	}

	var issueCount, linkCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM issue WHERE workspace_id = $1`, fixture.workspaceID).Scan(&issueCount); err != nil {
		t.Fatalf("count concurrent projected Issues: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM external_work_order_link WHERE workspace_id = $1`, fixture.workspaceID).Scan(&linkCount); err != nil {
		t.Fatalf("count concurrent WorkOrder links: %v", err)
	}
	if issueCount != 1 || linkCount != 1 {
		t.Fatalf("concurrent Issue/link counts = %d/%d, want 1/1", issueCount, linkCount)
	}
}

func TestCompanyOpsWorkOrderProjection_DifferentSourcesMayShareDisplayTitle(t *testing.T) {
	pool := newProductionCompanyOpsPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	fixture := seedWorkOrderProjectionFixture(t, ctx, pool)
	service := newWorkOrderProjectionService(t, db.New(pool), pool, events.New())

	firstReq := workOrderProjectionRequest(fixture, "hive://hivecosm/delivery/project/PRJ-HIVECREW-P2/work-order/WO-P2-SAME-TITLE-A")
	secondReq := workOrderProjectionRequest(fixture, "hive://hivecosm/delivery/project/PRJ-HIVECREW-P2/work-order/WO-P2-SAME-TITLE-B")
	first, err := service.Project(ctx, firstReq)
	if err != nil {
		t.Fatalf("Project first same-title WorkOrder: %v", err)
	}
	second, err := service.Project(ctx, secondReq)
	if err != nil {
		t.Fatalf("Project second same-title WorkOrder: %v", err)
	}
	if !first.Created || !second.Created || first.Issue.ID == second.Issue.ID {
		t.Fatalf("different source refs collapsed by title: first=%+v second=%+v", first, second)
	}
	if first.Issue.Title != second.Issue.Title {
		t.Fatalf("same authority display title changed: %q/%q", first.Issue.Title, second.Issue.Title)
	}

	var issueCount, linkCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM issue WHERE workspace_id = $1`, fixture.workspaceID).Scan(&issueCount); err != nil {
		t.Fatalf("count same-title projected Issues: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM external_work_order_link WHERE workspace_id = $1`, fixture.workspaceID).Scan(&linkCount); err != nil {
		t.Fatalf("count same-title WorkOrder links: %v", err)
	}
	if issueCount != 2 || linkCount != 2 {
		t.Fatalf("same-title Issue/link counts = %d/%d, want 2/2", issueCount, linkCount)
	}
}

func TestCompanyOpsWorkOrderProjection_RejectsMissingTitleAndNonMemberBeforeWrite(t *testing.T) {
	pool := newProductionCompanyOpsPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	fixture := seedWorkOrderProjectionFixture(t, ctx, pool)
	service := newWorkOrderProjectionService(t, db.New(pool), pool, events.New())

	missingTitle := workOrderProjectionRequest(fixture, "hive://hivecosm/delivery/project/PRJ-HIVECREW-P2/work-order/WO-P2-MISSING-TITLE")
	missingTitle.WorkOrder.DisplayName = "  "
	if _, err := service.Project(ctx, missingTitle); err == nil || !strings.Contains(err.Error(), "display_name") {
		t.Fatalf("missing WorkOrder title error = %v", err)
	}

	nonMember := workOrderProjectionRequest(fixture, "hive://hivecosm/delivery/project/PRJ-HIVECREW-P2/work-order/WO-P2-NON-MEMBER")
	nonMember.ActorUserID = util.MustParseUUID(uuid.NewString())
	if _, err := service.Project(ctx, nonMember); err == nil || !strings.Contains(err.Error(), "not a member") {
		t.Fatalf("non-member actor error = %v", err)
	}

	var issueCount, linkCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM issue WHERE workspace_id = $1`, fixture.workspaceID).Scan(&issueCount); err != nil {
		t.Fatalf("count rejected projected Issues: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM external_work_order_link WHERE workspace_id = $1`, fixture.workspaceID).Scan(&linkCount); err != nil {
		t.Fatalf("count rejected WorkOrder links: %v", err)
	}
	if issueCount != 0 || linkCount != 0 {
		t.Fatalf("rejected Issue/link counts = %d/%d, want 0/0", issueCount, linkCount)
	}
}

func TestCompanyOpsWorkOrderProjection_ExistingOrphanLinkFailsClosed(t *testing.T) {
	pool := newProductionCompanyOpsPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	fixture := seedWorkOrderProjectionFixture(t, ctx, pool)
	service := newWorkOrderProjectionService(t, db.New(pool), pool, events.New())
	req := workOrderProjectionRequest(fixture, "hive://hivecosm/delivery/project/PRJ-HIVECREW-P2/work-order/WO-P2-ORPHAN-LINK")
	missingIssueID := util.MustParseUUID(uuid.NewString())
	if _, err := pool.Exec(ctx, `
		INSERT INTO external_work_order_link (
			workspace_id, work_order_ref, linked_revision, linked_digest,
			source_observed_at, freshness_at_link, issue_id
		) VALUES ($1, $2, $3, $4, $5, 'current', $6)`,
		fixture.workspaceID,
		req.WorkOrder.SourceRef,
		req.WorkOrder.Revision,
		req.WorkOrder.ContentDigest,
		req.SourceObservedAt.UTC().Truncate(time.Microsecond),
		missingIssueID,
	); err != nil {
		t.Fatalf("seed orphan WorkOrder link: %v", err)
	}

	if _, err := service.Project(ctx, req); !errors.Is(err, ErrCompanyOpsWorkOrderProjectionOrphan) {
		t.Fatalf("orphan WorkOrder link error = %v, want %v", err, ErrCompanyOpsWorkOrderProjectionOrphan)
	}
	var issueCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM issue WHERE workspace_id = $1`, fixture.workspaceID).Scan(&issueCount); err != nil {
		t.Fatalf("count orphan-link projected Issues: %v", err)
	}
	if issueCount != 0 {
		t.Fatalf("orphan-link replay created %d Issues, want 0", issueCount)
	}
}

func TestCompanyOpsWorkOrderProjection_LinkFailureRollsBackIssue(t *testing.T) {
	pool := newProductionCompanyOpsPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	fixture := seedWorkOrderProjectionFixture(t, ctx, pool)

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire rollback-test connection: %v", err)
	}
	defer func() {
		_, _ = conn.Exec(context.Background(), `DROP TABLE IF EXISTS pg_temp.external_work_order_link`)
		conn.Release()
	}()
	if _, err := conn.Exec(ctx, `
		CREATE TEMP TABLE external_work_order_link
		(LIKE public.external_work_order_link INCLUDING ALL)
		ON COMMIT PRESERVE ROWS`); err != nil {
		t.Fatalf("create rollback-test temp WorkOrder link table: %v", err)
	}
	if _, err := conn.Exec(ctx, `
		ALTER TABLE pg_temp.external_work_order_link
		ADD CONSTRAINT force_projection_link_failure CHECK (false)`); err != nil {
		t.Fatalf("add rollback-test forced failure: %v", err)
	}

	service := newWorkOrderProjectionService(t, db.New(conn), conn, events.New())
	req := workOrderProjectionRequest(fixture, "hive://hivecosm/delivery/project/PRJ-HIVECREW-P2/work-order/WO-P2-ROLLBACK")
	if _, err := service.Project(ctx, req); err == nil || !strings.Contains(err.Error(), "append external WorkOrder link") {
		t.Fatalf("forced link failure error = %v", err)
	}

	var issueCount, linkCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM issue WHERE workspace_id = $1`, fixture.workspaceID).Scan(&issueCount); err != nil {
		t.Fatalf("count rolled-back projected Issues: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.external_work_order_link WHERE workspace_id = $1`, fixture.workspaceID).Scan(&linkCount); err != nil {
		t.Fatalf("count rolled-back public WorkOrder links: %v", err)
	}
	if issueCount != 0 || linkCount != 0 {
		t.Fatalf("link failure left Issue/link counts = %d/%d, want 0/0", issueCount, linkCount)
	}
}

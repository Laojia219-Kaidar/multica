package service

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// seedPausedProjectFixture seeds workspace/user/runtime/agent/issue and a
// project whose status is togglable for the control-service DB tests.
func seedPausedProjectFixture(t *testing.T, projectStatus string) (*pgxpool.Pool, string, string, string) {
	pool := newResolveOriginatorPool(t)
	ctx := context.Background()
	workspaceID, _, agentID, issueID := seedAttributionFixture(t, pool)

	var projectID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO project (workspace_id, title, status, lead_type, lead_id)
		VALUES ($1, 'control project', $2, 'agent', $3)
		RETURNING id`, workspaceID, projectStatus, agentID).Scan(&projectID); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE issue SET project_id = $1 WHERE id = $2`, projectID, issueID); err != nil {
		t.Fatalf("attach issue to project: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM project WHERE id = $1`, projectID)
	})
	return pool, workspaceID, projectID, issueID
}

func loadIssue(t *testing.T, pool *pgxpool.Pool, issueID string) db.Issue {
	t.Helper()
	var wsID, asgID, projID string
	if err := pool.QueryRow(context.Background(),
		`SELECT workspace_id::text, assignee_id::text, COALESCE(project_id::text,'') FROM issue WHERE id=$1`, issueID).Scan(&wsID, &asgID, &projID); err != nil {
		t.Fatalf("load issue: %v", err)
	}
	is := db.Issue{
		ID:           util.MustParseUUID(issueID),
		WorkspaceID:  util.MustParseUUID(wsID),
		AssigneeID:   util.MustParseUUID(asgID),
		AssigneeType: textValue("agent"),
		CreatorType:  "member",
		Priority:     "medium",
	}
	if projID != "" {
		is.ProjectID = util.MustParseUUID(projID)
	}
	return is
}

// Gauss re-review #2: a paused project must reject task enqueue at the shared
// prepare chokepoint (ErrProjectPausedDispatch) — pause must actually stop new
// dispatch.
func TestPausedProjectGatesEnqueue(t *testing.T) {
	pool, _, _, issueID := seedPausedProjectFixture(t, "paused")
	q := db.New(pool)
	svc := &TaskService{Queries: q, TxStarter: pool, Bus: events.New()}

	_, err := svc.EnqueueTaskForIssue(context.Background(), loadIssue(t, pool, issueID))
	if !errors.Is(err, ErrProjectPausedDispatch) {
		t.Fatalf("enqueue err = %v, want ErrProjectPausedDispatch", err)
	}
}

// Pause twice is idempotent: the second returns Replayed=true (references
// receipt.Replayed, closing the idempotent-replay gap).
func TestPauseDispatchReplay(t *testing.T) {
	pool, workspaceID, projectID, _ := seedPausedProjectFixture(t, "in_progress")
	q := db.New(pool)
	svc := &TaskService{Queries: q, TxStarter: pool, Bus: events.New()}
	ctrl := NewProjectLifecycleControlService(q, svc)
	ctx := context.Background()

	r1, err := ctrl.PauseDispatch(ctx, util.MustParseUUID(workspaceID), util.MustParseUUID(projectID), "k1")
	if err != nil {
		t.Fatalf("first pause: %v", err)
	}
	if !r1.Applied || r1.AfterStatus != "paused" {
		t.Fatalf("first pause receipt = %+v, want applied + paused", r1)
	}
	r2, err := ctrl.PauseDispatch(ctx, util.MustParseUUID(workspaceID), util.MustParseUUID(projectID), "k1")
	if err != nil {
		t.Fatalf("second pause: %v", err)
	}
	if !r2.Replayed || r2.Applied {
		t.Fatalf("second pause receipt = %+v, want replayed (not re-applied)", r2)
	}
}

// Gauss re-review #2 (mention path): paused project gates the mention enqueue.
func TestPausedProjectGatesMentionEnqueue(t *testing.T) {
	pool, _, _, issueID := seedPausedProjectFixture(t, "paused")
	q := db.New(pool)
	svc := &TaskService{Queries: q, TxStarter: pool, Bus: events.New()}

	is := loadIssue(t, pool, issueID)
	_, err := svc.EnqueueTaskForMention(context.Background(), is, is.AssigneeID, pgtype.UUID{})
	if !errors.Is(err, ErrProjectPausedDispatch) {
		t.Fatalf("mention enqueue err = %v, want ErrProjectPausedDispatch", err)
	}
}

// Gauss re-review #2 (quick-create path): paused project gates quick-create.
func TestPausedProjectGatesQuickCreate(t *testing.T) {
	pool, workspaceID, projectID, _ := seedPausedProjectFixture(t, "paused")
	q := db.New(pool)
	svc := &TaskService{Queries: q, TxStarter: pool, Bus: events.New()}

	_, _, agentID, _ := seedAttributionFixture(t, pool)
	_, err := svc.EnqueueQuickCreateTask(context.Background(),
		util.MustParseUUID(workspaceID), pgtype.UUID{}, util.MustParseUUID(agentID), pgtype.UUID{},
		"hi", "medium", "", util.MustParseUUID(projectID), pgtype.UUID{}, nil)
	if !errors.Is(err, ErrProjectPausedDispatch) {
		t.Fatalf("quick-create err = %v, want ErrProjectPausedDispatch", err)
	}
}

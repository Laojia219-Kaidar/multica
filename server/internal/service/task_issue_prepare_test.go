package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

var (
	errPrepareTestUnexpectedQuery = errors.New("unexpected task prepare query")
	errPrepareTestCreate          = errors.New("task prepare create failed")
)

type prepareTestDBTX struct {
	agent             db.Agent
	issue             db.Issue
	createdTaskID     pgtype.UUID
	failCreate        bool
	rejectAll         bool
	queryLog          []string
	unexpectedQueries int
	lastCreateArgs    []any
}

func (d *prepareTestDBTX) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.NewCommandTag(""), nil
}

func (d *prepareTestDBTX) Query(context.Context, string, ...any) (pgx.Rows, error) {
	d.unexpectedQueries++
	return nil, errPrepareTestUnexpectedQuery
}

func (d *prepareTestDBTX) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	if d.rejectAll {
		d.unexpectedQueries++
		return prepareTestErrorRow{err: errPrepareTestUnexpectedQuery}
	}
	switch {
	case strings.Contains(sql, "FROM agent\nWHERE id = $1"):
		d.queryLog = append(d.queryLog, "get_agent")
		return prepareTestAgentRow{agent: d.agent}
	case strings.Contains(sql, "SELECT head_sha FROM"):
		d.queryLog = append(d.queryLog, "get_review_sha")
		return prepareTestErrorRow{err: pgx.ErrNoRows}
	case strings.Contains(sql, "INSERT INTO agent_task_queue"):
		d.queryLog = append(d.queryLog, "create_task")
		d.lastCreateArgs = append([]any(nil), args...)
		if d.failCreate {
			return prepareTestErrorRow{err: errPrepareTestCreate}
		}
		return prepareTestTaskRow{task: taskFromPrepareArgs(d.createdTaskID, args)}
	case strings.Contains(sql, "FROM issue\nWHERE id = $1"):
		d.queryLog = append(d.queryLog, "get_issue")
		return prepareTestIssueRow{issue: d.issue}
	default:
		d.unexpectedQueries++
		return prepareTestErrorRow{err: errPrepareTestUnexpectedQuery}
	}
}

type prepareTestErrorRow struct{ err error }

func (r prepareTestErrorRow) Scan(...any) error { return r.err }

type prepareTestAgentRow struct{ agent db.Agent }

func (r prepareTestAgentRow) Scan(dest ...any) error {
	*(dest[0].(*pgtype.UUID)) = r.agent.ID
	*(dest[1].(*pgtype.UUID)) = r.agent.WorkspaceID
	*(dest[9].(*pgtype.UUID)) = r.agent.OwnerID
	*(dest[13].(*pgtype.UUID)) = r.agent.RuntimeID
	*(dest[15].(*pgtype.Timestamptz)) = r.agent.ArchivedAt
	return nil
}

type prepareTestIssueRow struct{ issue db.Issue }

func (r prepareTestIssueRow) Scan(dest ...any) error {
	*(dest[0].(*pgtype.UUID)) = r.issue.ID
	*(dest[1].(*pgtype.UUID)) = r.issue.WorkspaceID
	return nil
}

type prepareTestTaskRow struct{ task db.AgentTaskQueue }

func (r prepareTestTaskRow) Scan(dest ...any) error {
	*(dest[0].(*pgtype.UUID)) = r.task.ID
	*(dest[1].(*pgtype.UUID)) = r.task.AgentID
	*(dest[2].(*pgtype.UUID)) = r.task.IssueID
	*(dest[3].(*string)) = r.task.Status
	*(dest[4].(*int32)) = r.task.Priority
	*(dest[12].(*pgtype.UUID)) = r.task.RuntimeID
	*(dest[15].(*pgtype.UUID)) = r.task.TriggerCommentID
	*(dest[22].(*pgtype.Text)) = r.task.TriggerSummary
	*(dest[23].(*bool)) = r.task.ForceFreshSession
	*(dest[27].(*pgtype.Text)) = r.task.HandoffNote
	*(dest[33].(*pgtype.UUID)) = r.task.OriginatorUserID
	*(dest[35].(*[]pgtype.UUID)) = r.task.CoalescedCommentIds
	*(dest[39].(*pgtype.Text)) = r.task.OriginatorSource
	*(dest[40].(*pgtype.UUID)) = r.task.DelegatedFromTaskID
	*(dest[42].(*pgtype.UUID)) = r.task.RerunOfTaskID
	*(dest[43].(*pgtype.UUID)) = r.task.RuleVersionID
	*(dest[44].(*pgtype.Text)) = r.task.TriggerEvidenceKind
	*(dest[45].(*pgtype.UUID)) = r.task.TriggerEvidenceRefID
	*(dest[46].(*pgtype.UUID)) = r.task.AccountableUserID
	return nil
}

func taskFromPrepareArgs(id pgtype.UUID, args []any) db.AgentTaskQueue {
	return db.AgentTaskQueue{
		ID:                   id,
		AgentID:              args[0].(pgtype.UUID),
		RuntimeID:            args[1].(pgtype.UUID),
		IssueID:              args[2].(pgtype.UUID),
		Status:               "queued",
		Priority:             args[3].(int32),
		TriggerCommentID:     args[4].(pgtype.UUID),
		CoalescedCommentIds:  args[5].([]pgtype.UUID),
		TriggerSummary:       args[6].(pgtype.Text),
		ForceFreshSession:    args[7].(pgtype.Bool).Bool,
		HandoffNote:          args[9].(pgtype.Text),
		OriginatorUserID:     args[12].(pgtype.UUID),
		AccountableUserID:    args[13].(pgtype.UUID),
		OriginatorSource:     args[16].(pgtype.Text),
		DelegatedFromTaskID:  args[17].(pgtype.UUID),
		RuleVersionID:        args[18].(pgtype.UUID),
		RerunOfTaskID:        args[19].(pgtype.UUID),
		TriggerEvidenceKind:  args[20].(pgtype.Text),
		TriggerEvidenceRefID: args[21].(pgtype.UUID),
	}
}

type orderedPrepareWakeup struct{ effects *[]string }

func (w orderedPrepareWakeup) NotifyTaskAvailable(_, _ string) {
	*w.effects = append(*w.effects, "notify")
}

func prepareTestFixture() (db.Issue, db.Agent) {
	issue := db.Issue{
		ID:           testUUID(71),
		WorkspaceID:  testUUID(72),
		Priority:     "high",
		AssigneeType: pgtype.Text{String: "agent", Valid: true},
		AssigneeID:   testUUID(73),
		CreatorType:  "member",
		CreatorID:    testUUID(74),
	}
	agent := db.Agent{
		ID:          issue.AssigneeID,
		WorkspaceID: issue.WorkspaceID,
		OwnerID:     testUUID(75),
		RuntimeID:   testUUID(76),
	}
	return issue, agent
}

func prepareTestService(queries *db.Queries, effects *[]string) *TaskService {
	bus := events.New()
	bus.SubscribeAll(func(event events.Event) {
		if event.Type == protocol.EventTaskQueued {
			*effects = append(*effects, "broadcast")
		}
	})
	return &TaskService{
		Queries: queries,
		Bus:     bus,
		Wakeup:  orderedPrepareWakeup{effects: effects},
	}
}

func TestTaskIssuePrepare_UsesQTxWithoutEffectsAndOverridesEvidence(t *testing.T) {
	issue, agent := prepareTestFixture()
	base := &prepareTestDBTX{rejectAll: true}
	qtx := &prepareTestDBTX{agent: agent, issue: issue, createdTaskID: testUUID(77)}
	var effects []string
	svc := prepareTestService(db.New(base), &effects)
	commandID := testUUID(78)

	task, err := svc.prepareIssueTaskWithCommentPlan(
		t.Context(),
		db.New(qtx),
		issue,
		pgtype.UUID{},
		nil,
		false,
		"",
		pgtype.UUID{},
		pgtype.UUID{},
		&issueTaskTriggerEvidenceOverride{Kind: assignmentDispatchEvidenceKind, RefID: commandID},
	)
	if err != nil {
		t.Fatalf("prepareIssueTaskWithCommentPlan: %v", err)
	}
	if base.unexpectedQueries != 0 {
		t.Fatalf("base Queries calls = %d, want 0; every prepare read/write must use qtx", base.unexpectedQueries)
	}
	if want := []string{"get_agent", "get_review_sha", "create_task"}; strings.Join(qtx.queryLog, ",") != strings.Join(want, ",") {
		t.Fatalf("qtx query log = %v, want %v", qtx.queryLog, want)
	}
	if len(effects) != 0 {
		t.Fatalf("prepare effects = %v, want none", effects)
	}
	if task.TriggerEvidenceKind != (pgtype.Text{String: assignmentDispatchEvidenceKind, Valid: true}) ||
		task.TriggerEvidenceRefID != commandID {
		t.Fatalf("prepared evidence = %+v/%v, want %q/%v", task.TriggerEvidenceKind, task.TriggerEvidenceRefID, assignmentDispatchEvidenceKind, commandID)
	}
}

func TestTaskIssuePrepare_OuterPreservesAttributionEvidenceAndEffectOrder(t *testing.T) {
	issue, agent := prepareTestFixture()
	dbtx := &prepareTestDBTX{agent: agent, issue: issue, createdTaskID: testUUID(79)}
	var effects []string
	svc := prepareTestService(db.New(dbtx), &effects)

	task, err := svc.enqueueIssueTaskWithCommentPlan(
		t.Context(), issue, pgtype.UUID{}, nil, false, "", pgtype.UUID{}, pgtype.UUID{},
	)
	if err != nil {
		t.Fatalf("enqueueIssueTaskWithCommentPlan: %v", err)
	}
	if task.TriggerEvidenceKind != (pgtype.Text{String: "issue_assignment", Valid: true}) ||
		task.TriggerEvidenceRefID != issue.ID {
		t.Fatalf("ordinary attribution evidence = %+v/%v, want issue_assignment/%v", task.TriggerEvidenceKind, task.TriggerEvidenceRefID, issue.ID)
	}
	if want := []string{"broadcast", "notify"}; strings.Join(effects, ",") != strings.Join(want, ",") {
		t.Fatalf("outer effects = %v, want %v", effects, want)
	}
}

func TestTaskIssuePrepare_ErrorHasNoEffects(t *testing.T) {
	issue, agent := prepareTestFixture()
	dbtx := &prepareTestDBTX{
		agent:         agent,
		issue:         issue,
		createdTaskID: testUUID(80),
		failCreate:    true,
	}
	var effects []string
	svc := prepareTestService(db.New(dbtx), &effects)

	if _, err := svc.enqueueIssueTaskWithCommentPlan(
		t.Context(), issue, pgtype.UUID{}, nil, false, "", pgtype.UUID{}, pgtype.UUID{},
	); !errors.Is(err, errPrepareTestCreate) {
		t.Fatalf("enqueue error = %v, want %v", err, errPrepareTestCreate)
	}
	if len(effects) != 0 {
		t.Fatalf("failed prepare effects = %v, want none", effects)
	}
}

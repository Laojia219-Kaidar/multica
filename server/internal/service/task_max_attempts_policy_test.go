package service

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

// HIV-870: an Issue opts out of automatic retry by setting the existing
// metadata key max_attempts to an exact JSON integer 1; the canonical
// prepareIssueTaskWithCommentPlan funnel persists it into the existing
// agent_task_queue.max_attempts column (no schema change). Issues without the
// key keep the default 2. These tests lock the full chain: metadata parsing,
// prepare-time rejection of invalid values, SQL persistence with the COALESCE(…,
// 2) default, and the retry writers' suppression when the persisted value is 1.

// TestParseIssueMaxAttemptsOverride locks the strict decoder: absent/null means
// "no override" (NULL param → SQL default 2) while a PRESENT key must be an
// exact JSON integer within the int32 attempt bounds; anything else refuses the
// enqueue instead of being coerced.
func TestParseIssueMaxAttemptsOverride(t *testing.T) {
	noOverride := pgtype.Int4{} // NULL param → COALESCE keeps the column default 2
	cases := []struct {
		name     string
		metadata string
		want     pgtype.Int4
		wantErr  bool
	}{
		{"absent key keeps default", `{"other":"x"}`, noOverride, false},
		{"empty object keeps default", `{}`, noOverride, false},
		{"null value keeps default", `{"max_attempts":null}`, noOverride, false},
		{"exact one opts out", `{"max_attempts":1}`, pgtype.Int4{Int32: 1, Valid: true}, false},
		{"two preserves bounded retry", `{"max_attempts":2}`, pgtype.Int4{Int32: 2, Valid: true}, false},
		{"three preserves provider ceiling", `{"max_attempts":3}`, pgtype.Int4{Int32: 3, Valid: true}, false},
		{"int32 upper bound accepted", `{"max_attempts":2147483647}`, pgtype.Int4{Int32: 2147483647, Valid: true}, false},
		{"boolean rejected", `{"max_attempts":true}`, noOverride, true},
		{"string rejected", `{"max_attempts":"1"}`, noOverride, true},
		{"fractional rejected", `{"max_attempts":1.5}`, noOverride, true},
		{"integral float literal rejected", `{"max_attempts":2.0}`, noOverride, true},
		{"exponent literal rejected", `{"max_attempts":1e2}`, noOverride, true},
		{"zero rejected", `{"max_attempts":0}`, noOverride, true},
		{"negative rejected", `{"max_attempts":-1}`, noOverride, true},
		{"over int32 rejected", `{"max_attempts":2147483648}`, noOverride, true},
		{"array value rejected", `{"max_attempts":[1]}`, noOverride, true},
		{"object value rejected", `{"max_attempts":{"n":1}}`, noOverride, true},
		{"unparseable blob degrades to default", `not-json`, noOverride, false},
		{"non-object blob degrades to default", `[1,2]`, noOverride, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseIssueMaxAttemptsOverride([]byte(tc.metadata))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseIssueMaxAttemptsOverride(%s) = %+v, want error", tc.metadata, got)
				}
				if got.Valid {
					t.Fatalf("parseIssueMaxAttemptsOverride(%s) error must not carry an override, got %+v", tc.metadata, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseIssueMaxAttemptsOverride(%s): %v", tc.metadata, err)
			}
			if got != tc.want {
				t.Fatalf("parseIssueMaxAttemptsOverride(%s) = %+v, want %+v", tc.metadata, got, tc.want)
			}
		})
	}
	// Empty metadata means no override.
	got, err := parseIssueMaxAttemptsOverride(nil)
	if err != nil || got.Valid {
		t.Fatalf("parseIssueMaxAttemptsOverride(nil) = %+v, %v; want invalid, nil", got, err)
	}
}

// TestTaskIssuePrepareMaxAttemptsMetadataReachesCreateArgs proves the canonical
// issue funnel passes the parsed override into CreateAgentTask (arg 23) and
// refuses invalid metadata BEFORE any task row is written. The mention/leader
// funnels are untouched by construction — parseIssueMaxAttemptsOverride has no
// caller outside prepareIssueTaskWithCommentPlan.
func TestTaskIssuePrepareMaxAttemptsMetadataReachesCreateArgs(t *testing.T) {
	cases := []struct {
		name        string
		metadata    []byte
		wantArg     pgtype.Int4
		wantErr     bool
		wantCreated bool
	}{
		{"metadata one persists one", []byte(`{"max_attempts":1}`), pgtype.Int4{Int32: 1, Valid: true}, false, true},
		{"metadata three persists three", []byte(`{"max_attempts":3}`), pgtype.Int4{Int32: 3, Valid: true}, false, true},
		{"absent metadata passes null for sql default", nil, pgtype.Int4{}, false, true},
		{"string metadata refused before insert", []byte(`{"max_attempts":"1"}`), pgtype.Int4{}, true, false},
		{"zero metadata refused before insert", []byte(`{"max_attempts":0}`), pgtype.Int4{}, true, false},
		{"fractional metadata refused before insert", []byte(`{"max_attempts":1.5}`), pgtype.Int4{}, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			issue, agent := prepareTestFixture()
			issue.Metadata = tc.metadata
			qtx := &prepareTestDBTX{agent: agent, issue: issue, createdTaskID: testUUID(79)}
			var effects []string
			svc := prepareTestService(db.New(qtx), &effects)

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
				nil,
			)
			created := false
			for _, q := range qtx.queryLog {
				if q == "create_task" {
					created = true
				}
			}
			if tc.wantErr {
				if err == nil {
					t.Fatalf("prepareIssueTaskWithCommentPlan succeeded, want refusal")
				}
				if created {
					t.Fatalf("invalid metadata must be rejected BEFORE CreateAgentTask; queryLog = %v", qtx.queryLog)
				}
				return
			}
			if err != nil {
				t.Fatalf("prepareIssueTaskWithCommentPlan: %v", err)
			}
			if !created {
				t.Fatalf("expected CreateAgentTask; queryLog = %v", qtx.queryLog)
			}
			if len(qtx.lastCreateArgs) < 23 {
				t.Fatalf("CreateAgentTask args = %d, want 23", len(qtx.lastCreateArgs))
			}
			got := qtx.lastCreateArgs[22].(pgtype.Int4)
			if got != tc.wantArg {
				t.Fatalf("CreateAgentTask max_attempts arg = %+v, want %+v", got, tc.wantArg)
			}
			_ = task
		})
	}
}

// TestCreateAgentTaskMaxAttemptsSQLPersistsOverride proves the SQL half against
// a real database: the narg lands in agent_task_queue.max_attempts and a NULL
// param falls back to the column default 2 via COALESCE (HIV-870).
func TestCreateAgentTaskMaxAttemptsSQLPersistsOverride(t *testing.T) {
	pool := newResolveOriginatorPool(t)
	ctx := context.Background()
	q := db.New(pool)
	_, _, agentID, issueID := seedAttributionFixture(t, pool)

	var runtimeID string
	if err := pool.QueryRow(ctx, `SELECT runtime_id::text FROM agent WHERE id = $1`, agentID).Scan(&runtimeID); err != nil {
		t.Fatalf("read agent runtime: %v", err)
	}

	cases := []struct {
		name    string
		arg     pgtype.Int4
		wantRow int32
	}{
		{"null keeps column default 2", pgtype.Int4{}, 2},
		{"one persists one", pgtype.Int4{Int32: 1, Valid: true}, 1},
		{"three persists three", pgtype.Int4{Int32: 3, Valid: true}, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task, err := q.CreateAgentTask(ctx, db.CreateAgentTaskParams{
				AgentID:     util.MustParseUUID(agentID),
				RuntimeID:   util.MustParseUUID(runtimeID),
				IssueID:     util.MustParseUUID(issueID),
				Priority:    2,
				MaxAttempts: tc.arg,
			})
			if err != nil {
				t.Fatalf("CreateAgentTask: %v", err)
			}
			t.Cleanup(func() {
				pool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, task.ID)
			})
			if task.MaxAttempts != tc.wantRow {
				t.Fatalf("returned max_attempts = %d, want %d", task.MaxAttempts, tc.wantRow)
			}
			var row int32
			if err := pool.QueryRow(ctx, `SELECT max_attempts FROM agent_task_queue WHERE id = $1`, task.ID).Scan(&row); err != nil {
				t.Fatalf("read persisted row: %v", err)
			}
			if row != tc.wantRow {
				t.Fatalf("persisted max_attempts = %d, want %d", row, tc.wantRow)
			}
		})
	}
}

type maxAttemptsNoopWakeup struct{}

func (maxAttemptsNoopWakeup) NotifyTaskAvailable(_, _ string) {}

func maxAttemptsRetryService(q *db.Queries) *TaskService {
	return &TaskService{Queries: q, Bus: events.New(), Wakeup: maxAttemptsNoopWakeup{}}
}

// mustLoadTask reloads a task row by hex id for retry-policy assertions.
func mustLoadTask(t *testing.T, q *db.Queries, id string) db.AgentTaskQueue {
	t.Helper()
	task, err := q.GetAgentTask(context.Background(), util.MustParseUUID(id))
	if err != nil {
		t.Fatalf("load task %s: %v", id, err)
	}
	return task
}

// TestMaxAttemptsOneDisablesEveryAutomaticRetryWriter proves the persisted
// value 1 suppresses the automatic retry writers for both a timeout and a
// provider failure — no retry child row is created and the failed first row
// stays durable as the evidence of the single attempt.
func TestMaxAttemptsOneDisablesEveryAutomaticRetryWriter(t *testing.T) {
	pool := newResolveOriginatorPool(t)
	ctx := context.Background()
	q := db.New(pool)
	_, _, agentID, issueID := seedAttributionFixture(t, pool)

	var runtimeID string
	if err := pool.QueryRow(ctx, `SELECT runtime_id::text FROM agent WHERE id = $1`, agentID).Scan(&runtimeID); err != nil {
		t.Fatalf("read agent runtime: %v", err)
	}

	reasons := []string{
		"timeout",
		string(taskfailure.ReasonAgentProviderNetwork),
	}
	for _, reason := range reasons {
		var parentID string
		if err := pool.QueryRow(ctx, `
			INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, attempt, max_attempts, failure_reason)
			VALUES ($1, $2, $3, 'failed', 2, 1, 1, $4)
			RETURNING id::text
		`, agentID, runtimeID, issueID, reason).Scan(&parentID); err != nil {
			t.Fatalf("insert failed parent (%s): %v", reason, err)
		}
		t.Cleanup(func() {
			pool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE parent_task_id = $1 OR id = $1`, parentID)
		})

		if !retryableReasons[reason] {
			t.Fatalf("test bug: reason %q is not retryable", reason)
		}
		// Pure gates first: ceiling must stay 1 (no provider widening) and the
		// failed row (attempt=1) must be ineligible.
		if got := retryAttemptCeiling(reason, 1); got != 1 {
			t.Fatalf("retryAttemptCeiling(%q, 1) = %d, want 1 (disabled budgets are never widened)", reason, got)
		}
		if retryEligible(reason, db.AgentTaskQueue{
			Attempt:     1,
			MaxAttempts: 1,
			IssueID:     util.MustParseUUID(issueID),
		}) {
			t.Fatalf("retryEligible(%q, max_attempts=1) = true, want false", reason)
		}

		svc := maxAttemptsRetryService(q)
		child, err := svc.MaybeRetryFailedTask(ctx, mustLoadTask(t, q, parentID))
		if err != nil {
			t.Fatalf("MaybeRetryFailedTask(%s): %v", reason, err)
		}
		if child != nil {
			t.Fatalf("MaybeRetryFailedTask(%s) created a retry child, want none", reason)
		}
		var children int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE parent_task_id = $1`, parentID).Scan(&children); err != nil {
			t.Fatalf("count retry children: %v", err)
		}
		if children != 0 {
			t.Fatalf("retry children for %s = %d, want 0", reason, children)
		}
		// The first failed row remains durable evidence of the single attempt.
		var status string
		var attempt, budget int32
		if err := pool.QueryRow(ctx, `SELECT status, attempt, max_attempts FROM agent_task_queue WHERE id = $1`, parentID).Scan(&status, &attempt, &budget); err != nil {
			t.Fatalf("reload failed parent: %v", err)
		}
		if status != "failed" || attempt != 1 || budget != 1 {
			t.Fatalf("failed parent evidence = %s/%d/%d, want failed/1/1", status, attempt, budget)
		}
	}
}

// TestMaxAttemptsAboveOnePreservesBoundedRetry proves a supported value > 1
// keeps the existing bounded auto-retry: the first failure earns exactly one
// child at attempt 2, and that child exhausting the budget stops the chain.
func TestMaxAttemptsAboveOnePreservesBoundedRetry(t *testing.T) {
	pool := newResolveOriginatorPool(t)
	ctx := context.Background()
	q := db.New(pool)
	_, _, agentID, issueID := seedAttributionFixture(t, pool)

	var runtimeID string
	if err := pool.QueryRow(ctx, `SELECT runtime_id::text FROM agent WHERE id = $1`, agentID).Scan(&runtimeID); err != nil {
		t.Fatalf("read agent runtime: %v", err)
	}

	var parentID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, attempt, max_attempts, failure_reason)
		VALUES ($1, $2, $3, 'failed', 2, 1, 2, 'timeout')
		RETURNING id::text
	`, agentID, runtimeID, issueID).Scan(&parentID); err != nil {
		t.Fatalf("insert failed parent: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE parent_task_id = $1 OR id = $1`, parentID)
	})

	svc := maxAttemptsRetryService(q)
	child, err := svc.MaybeRetryFailedTask(ctx, mustLoadTask(t, q, parentID))
	if err != nil {
		t.Fatalf("MaybeRetryFailedTask: %v", err)
	}
	if child == nil {
		t.Fatal("MaybeRetryFailedTask created no child, want bounded retry for max_attempts=2")
	}
	if child.Attempt != 2 || child.MaxAttempts != 2 || child.ParentTaskID != mustLoadTask(t, q, parentID).ID {
		t.Fatalf("retry child = attempt %d / max %d, want 2/2 inheriting parent", child.Attempt, child.MaxAttempts)
	}

	// The child failing at attempt 2 exhausts the budget: no grandchild.
	if _, err := pool.Exec(ctx, `
		UPDATE agent_task_queue SET status = 'failed', failure_reason = 'timeout' WHERE id = $1
	`, child.ID); err != nil {
		t.Fatalf("fail child: %v", err)
	}
	grandchild, err := svc.MaybeRetryFailedTask(ctx, mustLoadTask(t, q, child.ID.String()))
	if err != nil {
		t.Fatalf("MaybeRetryFailedTask(child): %v", err)
	}
	if grandchild != nil {
		t.Fatalf("retry chain exceeded budget: grandchild %s created", util.UUIDToString(grandchild.ID))
	}
	var descendants int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM agent_task_queue
		WHERE parent_task_id = $1 OR parent_task_id IN (SELECT id FROM agent_task_queue WHERE parent_task_id = $1)
	`, parentID).Scan(&descendants); err != nil {
		t.Fatalf("count descendants: %v", err)
	}
	if descendants != 1 {
		t.Fatalf("descendants = %d, want exactly the single bounded retry child", descendants)
	}
}

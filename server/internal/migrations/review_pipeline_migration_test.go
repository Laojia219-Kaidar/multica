package migrations

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// TestReviewPipelineV2MigrationsUpDownUp rehearses the ReviewPipelineV2 schema
// change (255-258 + 260, HIV-326 / HIV-350) in an isolated schema: up →
// constraint/index assertions → down → removal assertions → up again. It
// requires the isolated test database at 127.0.0.1:55432 and refuses any other
// target, so it can never touch a default/localhost:5432 instance.
func TestReviewPipelineV2MigrationsUpDownUp(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not set; migration rehearsal is opt-in")
	}
	config, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	if config.Host != "127.0.0.1" || config.Port != 55432 || config.Database != "multica_hivecrew_b2_design_602" {
		t.Skipf("migration rehearsal requires exact isolated database target, got %s:%d/%s",
			config.Host, config.Port, config.Database)
	}

	ctx := context.Background()
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect migration rehearsal database: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })

	schema := "review_pipeline_rehearsal_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	schemaIdent := pgx.Identifier{schema}.Sanitize()
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+schemaIdent); err != nil {
		t.Fatalf("create rehearsal schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+schemaIdent+" CASCADE")
	})
	if _, err := conn.Exec(ctx, "SET search_path TO "+schemaIdent); err != nil {
		t.Fatalf("set rehearsal search_path: %v", err)
	}

	// Minimal base tables matching the columns the 255-258 migrations touch.
	// Real base tables (not TEMP) so CREATE INDEX CONCURRENTLY is legal.
	if _, err := conn.Exec(ctx, `
		CREATE TABLE issue (
			id UUID PRIMARY KEY,
			workspace_id UUID NOT NULL,
			status TEXT NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`); err != nil {
		t.Fatalf("create rehearsal issue table: %v", err)
	}
	if _, err := conn.Exec(ctx, `
		CREATE TABLE agent_task_queue (
			id UUID PRIMARY KEY,
			agent_id UUID NOT NULL,
			issue_id UUID,
			status TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`); err != nil {
		t.Fatalf("create rehearsal agent_task_queue table: %v", err)
	}

	for _, file := range []string{
		"255_issue_review_state.up.sql",
		"256_issue_review_state_open_index.up.sql",
		"257_agent_task_review_kind.up.sql",
		"258_agent_task_review_open_unique.up.sql",
		"260_agent_task_review_open_unique_waiting.up.sql",
	} {
		applyReviewMigration(t, ctx, conn, file)
	}

	assertReviewSchemaUp(t, ctx, conn)

	for _, file := range []string{
		"260_agent_task_review_open_unique_waiting.down.sql",
		"258_agent_task_review_open_unique.down.sql",
		"257_agent_task_review_kind.down.sql",
		"256_issue_review_state_open_index.down.sql",
		"255_issue_review_state.down.sql",
	} {
		applyReviewMigration(t, ctx, conn, file)
	}

	assertReviewSchemaDown(t, ctx, conn, schema)

	for _, file := range []string{
		"255_issue_review_state.up.sql",
		"256_issue_review_state_open_index.up.sql",
		"257_agent_task_review_kind.up.sql",
		"258_agent_task_review_open_unique.up.sql",
		"260_agent_task_review_open_unique_waiting.up.sql",
	} {
		applyReviewMigration(t, ctx, conn, file)
	}
	assertReviewSchemaUp(t, ctx, conn)
}

func applyReviewMigration(t *testing.T, ctx context.Context, conn *pgx.Conn, name string) {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve migration rehearsal source path")
	}
	path := filepath.Join(filepath.Dir(currentFile), "..", "..", "migrations", name)
	sql, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration %s: %v", name, err)
	}
	if _, err := conn.Exec(ctx, string(sql)); err != nil {
		t.Fatalf("apply migration %s: %v", name, err)
	}
}

func assertReviewSchemaUp(t *testing.T, ctx context.Context, conn *pgx.Conn) {
	t.Helper()

	// Closed-enum CHECK on issue.review_state.
	if err := conn.QueryRow(ctx, `
		SELECT count(*) FROM pg_constraint
		WHERE conrelid = 'issue'::regclass AND conname = 'issue_review_state_closed_enum'
	`).Scan(new(int)); err != nil {
		t.Fatalf("inspect review_state CHECK: %v", err)
	}
	issueID := uuid.New()
	if _, err := conn.Exec(ctx, `
		INSERT INTO issue (id, workspace_id, status) VALUES ($1, $2, 'in_review')
	`, issueID, uuid.New()); err != nil {
		t.Fatalf("insert base issue: %v", err)
	}
	if _, err := conn.Exec(ctx, `
		UPDATE issue SET review_state = 'bogus_state' WHERE id = $1
	`, issueID); err == nil {
		t.Fatal("upgraded schema accepted a review_state outside the closed enum")
	}
	if _, err := conn.Exec(ctx, `
		UPDATE issue SET review_state = 'queued' WHERE id = $1
	`, issueID); err != nil {
		t.Fatalf("upgraded schema rejected a valid review_state: %v", err)
	}

	// task_kind closed enum + review rows must carry a candidate target.
	agentID := uuid.New()
	if _, err := conn.Exec(ctx, `
		INSERT INTO agent_task_queue (id, agent_id, status) VALUES ($1, $2, 'queued')
	`, uuid.New(), agentID); err != nil {
		t.Fatalf("insert legacy-shaped work task: %v", err)
	}
	if _, err := conn.Exec(ctx, `
		INSERT INTO agent_task_queue (id, agent_id, issue_id, status, task_kind, review_target_task_id)
		VALUES ($1, $2, $3, 'queued', 'review', NULL)
	`, uuid.New(), agentID, issueID); err == nil {
		t.Fatal("upgraded schema accepted a task_kind='review' row with NULL review_target_task_id")
	}
	if _, err := conn.Exec(ctx, `
		INSERT INTO agent_task_queue (id, agent_id, issue_id, status, task_kind, review_target_task_id)
		VALUES ($1, $2, $3, 'queued', 'bogus_kind', $4)
	`, uuid.New(), agentID, issueID, uuid.New()); err == nil {
		t.Fatal("upgraded schema accepted a task_kind outside the closed enum")
	}
	candidateID := uuid.New()
	if _, err := conn.Exec(ctx, `
		INSERT INTO agent_task_queue (id, agent_id, issue_id, status, task_kind, review_target_task_id)
		VALUES ($1, $2, $3, 'queued', 'review', $4)
	`, uuid.New(), agentID, issueID, candidateID); err != nil {
		t.Fatalf("upgraded schema rejected a valid review task: %v", err)
	}

	// Open-task unique index: a second open review task on the same
	// (issue, candidate) must conflict; a completed one must not.
	secondReview := func(status string, candidate uuid.UUID) error {
		_, err := conn.Exec(ctx, `
			INSERT INTO agent_task_queue (id, agent_id, issue_id, status, task_kind, review_target_task_id)
			VALUES ($1, $2, $3, $4, 'review', $5)
		`, uuid.New(), agentID, issueID, status, candidate)
		return err
	}
	if err := secondReview("queued", candidateID); !isUniqueViolation(err) {
		t.Fatalf("duplicate open review task: got %v, want unique violation", err)
	}
	if err := secondReview("completed", candidateID); err != nil {
		t.Fatalf("second completed review task must not collide: %v", err)
	}

	// 260 (HIV-350): the open-review unique index must also cover
	// waiting_local_directory — the daemon parks a claimed review task there
	// while its workdir is prepared. 258's index alone lets a second open
	// review task for the same (issue, candidate) slip through once the first
	// has left 'queued'; the v2 index must reject it.
	for _, indexName := range []string{"idx_agent_task_review_open_unique", "idx_agent_task_review_open_unique_v2"} {
		var def string
		if err := conn.QueryRow(ctx, `
			SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = $1
		`, indexName).Scan(&def); err != nil {
			t.Fatalf("inspect index %s: %v", indexName, err)
		}
		if indexName == "idx_agent_task_review_open_unique_v2" && !strings.Contains(def, "waiting_local_directory") {
			t.Fatalf("index %s must cover waiting_local_directory, got: %s", indexName, def)
		}
	}
	wldCandidateID := uuid.New()
	if _, err := conn.Exec(ctx, `
		INSERT INTO agent_task_queue (id, agent_id, issue_id, status, task_kind, review_target_task_id)
		VALUES ($1, $2, $3, 'queued', 'review', $4)
	`, uuid.New(), agentID, issueID, wldCandidateID); err != nil {
		t.Fatalf("insert base review task for waiting_local_directory scenario: %v", err)
	}
	if _, err := conn.Exec(ctx, `
		UPDATE agent_task_queue SET status = 'waiting_local_directory'
		WHERE review_target_task_id = $1
	`, wldCandidateID); err != nil {
		t.Fatalf("move review task into waiting_local_directory: %v", err)
	}
	if err := secondReview("queued", wldCandidateID); !isUniqueViolation(err) {
		t.Fatalf("duplicate review task while first is waiting_local_directory: got %v, want unique violation", err)
	}
}

func assertReviewSchemaDown(t *testing.T, ctx context.Context, conn *pgx.Conn, schema string) {
	t.Helper()

	var reviewStateColumns int
	if err := conn.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = 'issue' AND column_name IN ('review_state', 'review_state_reason')
	`, schema).Scan(&reviewStateColumns); err != nil {
		t.Fatalf("inspect review_state columns after down: %v", err)
	}
	if reviewStateColumns != 0 {
		t.Fatalf("review_state columns after down = %d, want 0", reviewStateColumns)
	}

	var taskKindColumns int
	if err := conn.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = 'agent_task_queue' AND column_name IN ('task_kind', 'review_target_task_id')
	`, schema).Scan(&taskKindColumns); err != nil {
		t.Fatalf("inspect task_kind columns after down: %v", err)
	}
	if taskKindColumns != 0 {
		t.Fatalf("task_kind columns after down = %d, want 0", taskKindColumns)
	}

	for _, indexName := range []string{
		"idx_issue_review_state_open",
		"idx_agent_task_review_open_unique",
		"idx_agent_task_review_open_unique_v2",
	} {
		var n int
		if err := conn.QueryRow(ctx, `
			SELECT count(*) FROM pg_indexes WHERE schemaname = $1 AND indexname = $2
		`, schema, indexName).Scan(&n); err != nil {
			t.Fatalf("inspect index %s after down: %v", indexName, err)
		}
		if n != 0 {
			t.Fatalf("index %s after down count = %d, want 0", indexName, n)
		}
	}
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

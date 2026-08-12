package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestOperationalModeMigrationDownUpDownUp is the R3 migration evidence for
// 259_agent_operational_mode: it applies the migration through the OFFICIAL
// migration runner (runMigrations) against the isolated R3 database only —
// never by hand-editing schema_migrations, never against 5432 or the mainline
// DB. The full forward chain is applied first by `cmd/migrate up` (see the
// R3 evidence commands in the task comment); this test then performs the
// target down → target up leg through the same runner and asserts the
// column/check/version state at each step.
//
// The test is opt-in and strictly scoped: it skips unless DATABASE_URL points
// at 127.0.0.1:55432 with the exact R3 database name, so a stray default
// (localhost:5432) can never be touched.
func TestOperationalModeMigrationDownUpDownUp(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL not set; migration roundtrip is opt-in")
	}
	config, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	if config.Host != "127.0.0.1" || config.Port != 55432 || config.Database != "multica_hivecrew_r3_repair_20260812" {
		t.Skipf("R3 migration roundtrip requires exact isolated database target, got %s:%d/%s", config.Host, config.Port, config.Database)
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect R3 rehearsal database: %v", err)
	}
	t.Cleanup(pool.Close)

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve migration rehearsal source path")
	}
	migrationsDir := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", "..", "migrations"))
	upFile := filepath.Join(migrationsDir, "259_agent_operational_mode.up.sql")
	downFile := filepath.Join(migrationsDir, "259_agent_operational_mode.down.sql")

	// Precondition: the full forward chain (including 259) must already be
	// applied by the official runner (cmd/migrate up). Fail loudly if the
	// version is missing instead of silently re-running.
	assertVersionRecorded(t, pool, "259_agent_operational_mode")
	assertOperationalModeColumn(t, pool, true)
	assertOperationalModeCheck(t, pool, true)

	// Target down: roll back only 259 through the official runner. The
	// runner deletes the schema_migrations row itself; no manual editing.
	runTarget(t, pool, "down", []string{downFile})
	assertVersionNotRecorded(t, pool, "259_agent_operational_mode")
	assertOperationalModeColumn(t, pool, false)
	assertOperationalModeCheck(t, pool, false)

	// Target up: re-apply only 259 through the official runner.
	runTarget(t, pool, "up", []string{upFile})
	assertVersionRecorded(t, pool, "259_agent_operational_mode")
	assertOperationalModeColumn(t, pool, true)
	assertOperationalModeCheck(t, pool, true)
}

func runTarget(t *testing.T, pool *pgxpool.Pool, direction string, files []string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := runMigrations(ctx, pool, runOptions{
		Direction:       direction,
		Files:           files,
		AdvisoryLockKey: 7244554146635925501 + 1,
	}); err != nil {
		t.Fatalf("runMigrations %s: %v", direction, err)
	}
}

func assertVersionRecorded(t *testing.T, pool *pgxpool.Pool, version string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = $1)`, version).Scan(&exists); err != nil {
		t.Fatalf("read schema_migrations for %s: %v", version, err)
	}
	if !exists {
		t.Fatalf("schema_migrations does not record %s", version)
	}
}

func assertVersionNotRecorded(t *testing.T, pool *pgxpool.Pool, version string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = $1)`, version).Scan(&exists); err != nil {
		t.Fatalf("read schema_migrations for %s: %v", version, err)
	}
	if exists {
		t.Fatalf("schema_migrations still records %s after down", version)
	}
}

func assertOperationalModeColumn(t *testing.T, pool *pgxpool.Pool, present bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var (
		dataType     string
		columnDef    string
		isNullable   string
		columnExists bool
	)
	if err := pool.QueryRow(ctx, `
		SELECT
			EXISTS (SELECT 1 FROM information_schema.columns
				WHERE table_name = 'agent' AND column_name = 'operational_mode'),
			COALESCE((SELECT data_type FROM information_schema.columns
				WHERE table_name = 'agent' AND column_name = 'operational_mode'), ''),
			COALESCE((SELECT column_default FROM information_schema.columns
				WHERE table_name = 'agent' AND column_name = 'operational_mode'), ''),
			COALESCE((SELECT is_nullable FROM information_schema.columns
				WHERE table_name = 'agent' AND column_name = 'operational_mode'), '')
	`).Scan(&columnExists, &dataType, &columnDef, &isNullable); err != nil {
		t.Fatalf("inspect operational_mode column: %v", err)
	}
	if present && !columnExists {
		t.Fatal("operational_mode column missing after up")
	}
	if !present && columnExists {
		t.Fatal("operational_mode column still present after down")
	}
	if present {
		if dataType != "text" {
			t.Fatalf("operational_mode data_type = %q, want text", dataType)
		}
		if columnDef != "'active'::text" {
			t.Fatalf("operational_mode default = %q, want 'active'::text", columnDef)
		}
		if isNullable != "NO" {
			t.Fatalf("operational_mode is_nullable = %q, want NO", isNullable)
		}
	}
}

func assertOperationalModeCheck(t *testing.T, pool *pgxpool.Pool, present bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var (
		checkDef     string
		checkName    string
		checkPresent bool
	)
	if err := pool.QueryRow(ctx, `
		SELECT
			EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'agent_operational_mode_check' AND conrelid = 'agent'::regclass),
			COALESCE((SELECT conname FROM pg_constraint WHERE conname = 'agent_operational_mode_check' AND conrelid = 'agent'::regclass), ''),
			COALESCE((SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = 'agent_operational_mode_check' AND conrelid = 'agent'::regclass), '')
	`).Scan(&checkPresent, &checkName, &checkDef); err != nil {
		t.Fatalf("inspect operational_mode check: %v", err)
	}
	if present && !checkPresent {
		t.Fatal("agent_operational_mode_check missing after up")
	}
	if !present && checkPresent {
		t.Fatal("agent_operational_mode_check still present after down")
	}
	if present {
		if checkName != "agent_operational_mode_check" {
			t.Fatalf("check name = %q, want agent_operational_mode_check", checkName)
		}
		if checkDef != "CHECK ((operational_mode = ANY (ARRAY['active'::text, 'resting'::text, 'disabled'::text, 'training'::text])))" {
			t.Fatalf("unexpected check definition: %s", checkDef)
		}
	}
}

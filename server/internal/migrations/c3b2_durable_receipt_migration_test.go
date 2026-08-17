package migrations

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestApprovalRevokedDownPreservesImmutableAuditRows(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	downPath := filepath.Join(filepath.Dir(sourceFile), "..", "..", "migrations", "412_artifact_event_approval_revoked.down.sql")
	raw, err := os.ReadFile(downPath)
	if err != nil {
		t.Fatalf("read approval_revoked down migration: %v", err)
	}
	down := string(raw)
	if !strings.Contains(down, "RAISE EXCEPTION") || !strings.Contains(down, "approval_revoked") {
		t.Fatal("approval_revoked down migration lacks the immutable-row rollback guard")
	}
	for _, forbidden := range []string{"DELETE FROM artifact_event", "UPDATE artifact_event"} {
		if strings.Contains(down, forbidden) {
			t.Fatalf("approval_revoked down migration mutates immutable ledger with %q", forbidden)
		}
	}
}

// TestC3b2DurableReceiptMigrationsUpDownUp rehearses the C3b2 migration
// ownership boundary on a random schema. It intentionally uses one ordinary
// PostgreSQL connection rather than a transaction: migrations 408 and 410
// contain CREATE INDEX CONCURRENTLY, which PostgreSQL rejects in a transaction
// block.
func TestC3b2DurableReceiptMigrationsUpDownUp(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not set; C3b2 migration rehearsal is opt-in")
	}
	config, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	if config.Port == 5432 {
		t.Skip("C3b2 migration rehearsal refuses shared PostgreSQL port 5432")
	}
	if !isC3b2LoopbackHost(config.Host) {
		t.Skipf("C3b2 migration rehearsal requires a loopback host, got %q", config.Host)
	}

	ctx := context.Background()
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect isolated PostgreSQL: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })

	schema := "c3b2_migration_rehearsal_" + uuid.NewString()[:8]
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
	createC3b2MigrationBaseTables(t, ctx, conn)

	for _, migration := range []string{
		"407_writer_lease_completion_receipt.up.sql",
		"408_writer_lease_completion_receipt_index.up.sql",
		"409_c3b2_promotion_delivery.up.sql",
		"410_c3b2_promotion_delivery_index.up.sql",
		"411_artifact_event_actor.up.sql",
		"412_artifact_event_approval_revoked.up.sql",
		"413_artifact_event_approval_index.up.sql",
	} {
		applyMigrationFile(t, ctx, conn, migration)
	}

	assertC3b2MigrationObjects(t, ctx, conn, schema)
	assertC3b2ReceiptConstraints(t, ctx, conn)
	assertC3b2DeliveryConstraints(t, ctx, conn)

	for _, migration := range []string{
		"413_artifact_event_approval_index.down.sql",
		"412_artifact_event_approval_revoked.down.sql",
		"411_artifact_event_actor.down.sql",
		"410_c3b2_promotion_delivery_index.down.sql",
		"409_c3b2_promotion_delivery.down.sql",
		"408_writer_lease_completion_receipt_index.down.sql",
		"407_writer_lease_completion_receipt.down.sql",
	} {
		applyMigrationFile(t, ctx, conn, migration)
	}
	assertC3b2MigrationObjectsAbsent(t, ctx, conn, schema)

	for _, migration := range []string{
		"407_writer_lease_completion_receipt.up.sql",
		"408_writer_lease_completion_receipt_index.up.sql",
		"409_c3b2_promotion_delivery.up.sql",
		"410_c3b2_promotion_delivery_index.up.sql",
		"411_artifact_event_actor.up.sql",
		"412_artifact_event_approval_revoked.up.sql",
		"413_artifact_event_approval_index.up.sql",
	} {
		applyMigrationFile(t, ctx, conn, migration)
	}
	assertC3b2MigrationObjects(t, ctx, conn, schema)
}

func isC3b2LoopbackHost(host string) bool {
	ip := net.ParseIP(host)
	return host == "localhost" || host == "::1" || (ip != nil && ip.IsLoopback())
}

func createC3b2MigrationBaseTables(t *testing.T, ctx context.Context, conn *pgx.Conn) {
	t.Helper()
	// These are the pre-C3b2 relations that 409 and 411 extend. The test uses
	// only the columns required by those migrations, keeping the rehearsal
	// independent from the full application schema.
	for _, statement := range []string{
		`CREATE TABLE artifact_promotion_claim (
			workspace_id UUID NOT NULL,
			promotion_id TEXT NOT NULL,
			candidate_id UUID NOT NULL,
			lineage_id UUID NOT NULL,
			payload_digest TEXT
		)`,
		`CREATE TABLE artifact_event (
			id UUID NOT NULL,
			workspace_id UUID NOT NULL,
			lineage_id UUID NOT NULL,
			candidate_id UUID NOT NULL,
			sequence INT NOT NULL,
			event_type TEXT NOT NULL CHECK (event_type IN ('submitted', 'approved'))
		)`,
	} {
		if _, err := conn.Exec(ctx, statement); err != nil {
			t.Fatalf("create C3b2 base table: %v", err)
		}
	}
}

func assertC3b2MigrationObjects(t *testing.T, ctx context.Context, conn *pgx.Conn, schema string) {
	t.Helper()
	for _, table := range []string{"writer_lease_completion_receipt", "artifact_promotion_delivery"} {
		var relation *string
		if err := conn.QueryRow(ctx, `SELECT CASE WHEN c.oid IS NULL THEN NULL ELSE n.nspname || '.' || c.relname END
			FROM pg_class c LEFT JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE c.oid = to_regclass($1)`, schema+"."+table).Scan(&relation); err != nil {
			t.Fatalf("inspect table %s: %v", table, err)
		}
		if relation == nil || *relation != schema+"."+table {
			t.Fatalf("table %s relation = %v, want %s.%s", table, relation, schema, table)
		}
	}
	for _, index := range []string{
		"writer_lease_completion_receipt_task_uidx",
		"artifact_promotion_delivery_promotion_uidx",
		"artifact_event_approval_candidate_idx",
	} {
		assertC3b2IndexExists(t, ctx, conn, schema, index)
	}
	for trigger, spec := range map[string][2]string{
		"writer_lease_completion_receipt_reject_mutation": {"writer_lease_completion_receipt", "reject_writer_lease_completion_receipt_mutation"},
		"artifact_promotion_delivery_reject_delete":       {"artifact_promotion_delivery", "reject_artifact_promotion_delivery_delete"},
		"artifact_promotion_delivery_reject_mutation":     {"artifact_promotion_delivery", "reject_artifact_promotion_delivery_mutation"},
	} {
		var exists bool
		if err := conn.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM pg_trigger t
				JOIN pg_proc p ON p.oid = t.tgfoid
				JOIN pg_namespace pn ON pn.oid = p.pronamespace
				WHERE t.tgrelid = $1::regclass AND t.tgname = $2 AND NOT t.tgisinternal
				  AND p.proname = $3 AND pn.nspname = $4
			)`, schema+"."+spec[0], trigger, spec[1], schema).Scan(&exists); err != nil {
			t.Fatalf("inspect trigger %s: %v", trigger, err)
		}
		if !exists {
			t.Fatalf("trigger %s is missing", trigger)
		}
	}
	for _, function := range []string{
		"reject_writer_lease_completion_receipt_mutation",
		"reject_artifact_promotion_delivery_delete",
		"reject_artifact_promotion_delivery_mutation",
	} {
		assertC3b2FunctionExists(t, ctx, conn, schema, function)
	}
	for _, constraint := range []string{
		"writer_lease_completion_receipt_target_digest_chk",
		"writer_lease_completion_receipt_snapshot_array_chk",
		"writer_lease_completion_receipt_proof_digest_chk",
		"writer_lease_completion_receipt_digest_chk",
		"artifact_promotion_delivery_state_chk",
		"artifact_promotion_delivery_promotion_id_chk",
		"artifact_promotion_delivery_payload_digest_chk",
		"artifact_promotion_delivery_target_digest_chk",
		"artifact_promotion_delivery_receipt_digest_chk",
		"artifact_promotion_delivery_binding_all_or_none_chk",
		"artifact_promotion_delivery_payload_object_chk",
		"artifact_promotion_delivery_claim_token_chk",
		"artifact_promotion_delivery_state_receipt_chk",
		"artifact_promotion_delivery_response_object_chk",
		"artifact_promotion_delivery_readback_object_chk",
		"artifact_promotion_claim_writer_target_digest_chk",
		"artifact_promotion_claim_completion_receipt_digest_chk",
		"artifact_promotion_claim_binding_all_or_none_chk",
	} {
		assertC3b2ConstraintExists(t, ctx, conn, schema, constraint)
	}
	assertC3b2ColumnExists(t, ctx, conn, schema, "artifact_event", "actor_user_id")
	assertC3b2ConstraintExists(t, ctx, conn, schema, "artifact_event_event_type_check")
}

func assertC3b2MigrationObjectsAbsent(t *testing.T, ctx context.Context, conn *pgx.Conn, schema string) {
	t.Helper()
	for _, table := range []string{"writer_lease_completion_receipt", "artifact_promotion_delivery"} {
		var relation *string
		if err := conn.QueryRow(ctx, `SELECT to_regclass($1)::text`, schema+"."+table).Scan(&relation); err != nil {
			t.Fatalf("inspect rolled-back table %s: %v", table, err)
		}
		if relation != nil {
			t.Fatalf("rolled-back table %s still exists as %s", table, *relation)
		}
	}
	for _, index := range []string{
		"writer_lease_completion_receipt_task_uidx",
		"artifact_promotion_delivery_promotion_uidx",
		"artifact_event_approval_candidate_idx",
	} {
		var exists bool
		if err := conn.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM pg_class c
				JOIN pg_namespace n ON n.oid = c.relnamespace
				WHERE n.nspname = $1 AND c.relname = $2
			)`, schema, index).Scan(&exists); err != nil {
			t.Fatalf("inspect rolled-back index %s: %v", index, err)
		}
		if exists {
			t.Fatalf("rolled-back index %s still exists", index)
		}
	}
	for _, column := range []struct{ table, column string }{
		{"artifact_event", "actor_user_id"},
		{"artifact_promotion_claim", "source_task_id"},
		{"artifact_promotion_claim", "writer_lease_target_digest"},
		{"artifact_promotion_claim", "completion_receipt_digest"},
	} {
		assertC3b2ColumnAbsent(t, ctx, conn, schema, column.table, column.column)
	}
	for _, function := range []string{
		"reject_writer_lease_completion_receipt_mutation",
		"reject_artifact_promotion_delivery_delete",
		"reject_artifact_promotion_delivery_mutation",
	} {
		assertC3b2FunctionAbsent(t, ctx, conn, schema, function)
	}
	assertC3b2ConstraintExists(t, ctx, conn, schema, "artifact_event_event_type_check")
}

func assertC3b2ReceiptConstraints(t *testing.T, ctx context.Context, conn *pgx.Conn) {
	t.Helper()
	workspaceID := uuid.New()
	taskID := uuid.New()
	targetDigest := repeatedC3b2Hex('a')
	proofDigest := "sha256:" + repeatedC3b2Hex('b')
	receiptDigest := "sha256:" + repeatedC3b2Hex('c')
	validInsert := `
		INSERT INTO writer_lease_completion_receipt
			(workspace_id, task_id, target_digest, proof_snapshot, proof_digest, receipt_digest)
		VALUES ($1, $2, $3, '[{"resource_id":"resource"}]'::jsonb, $4, $5)`
	if _, err := conn.Exec(ctx, validInsert, workspaceID, taskID, targetDigest, proofDigest, receiptDigest); err != nil {
		t.Fatalf("insert valid writer lease receipt: %v", err)
	}
	assertC3b2ExecFails(t, ctx, conn, `
		INSERT INTO writer_lease_completion_receipt
			(workspace_id, task_id, target_digest, proof_snapshot, proof_digest, receipt_digest)
		VALUES ($1, $2, 'bad', '[]'::jsonb, $3, $4)`, uuid.New(), uuid.New(), proofDigest, receiptDigest)
	assertC3b2ExecFails(t, ctx, conn, `
		INSERT INTO writer_lease_completion_receipt
			(workspace_id, task_id, target_digest, proof_snapshot, proof_digest, receipt_digest)
		VALUES ($1, $2, $3, '{}'::jsonb, $4, $5)`, uuid.New(), uuid.New(), targetDigest, proofDigest, receiptDigest)
	assertC3b2ExecFails(t, ctx, conn, `
		INSERT INTO writer_lease_completion_receipt
			(workspace_id, task_id, target_digest, proof_snapshot, proof_digest, receipt_digest)
		VALUES ($1, $2, $3, '[]'::jsonb, 'bad', $4)`, uuid.New(), uuid.New(), targetDigest, receiptDigest)
	assertC3b2ExecFails(t, ctx, conn, `
		INSERT INTO writer_lease_completion_receipt
			(workspace_id, task_id, target_digest, proof_snapshot, proof_digest, receipt_digest)
		VALUES ($1, $2, $3, '[]'::jsonb, $4, 'bad')`, uuid.New(), uuid.New(), targetDigest, proofDigest)
	assertC3b2ExecFails(t, ctx, conn, validInsert, workspaceID, taskID, targetDigest, proofDigest, receiptDigest)
	assertC3b2ExecFails(t, ctx, conn, `UPDATE writer_lease_completion_receipt SET target_digest = $1 WHERE task_id = $2`, targetDigest, taskID)
	assertC3b2ExecFails(t, ctx, conn, `DELETE FROM writer_lease_completion_receipt WHERE task_id = $1`, taskID)
}

func assertC3b2DeliveryConstraints(t *testing.T, ctx context.Context, conn *pgx.Conn) {
	t.Helper()
	workspaceID := uuid.New()
	validPromotionID := uuid.NewString()
	validCandidateID := uuid.New()
	validLineageID := uuid.New()
	validDigest := "sha256:" + repeatedC3b2Hex('d')
	insert := func(promotionID, state, payloadDigest string) {
		t.Helper()
		_, err := conn.Exec(ctx, `
			INSERT INTO artifact_promotion_delivery
				(workspace_id, promotion_id, candidate_id, lineage_id, payload_digest, state, request_payload)
			VALUES ($1, $2, $3, $4, $5, $6, '{}'::jsonb)`, workspaceID, promotionID, validCandidateID, validLineageID, payloadDigest, state)
		if err != nil {
			t.Fatalf("insert valid delivery state %s: %v", state, err)
		}
	}
	insert(validPromotionID, "pending", validDigest)

	assertC3b2ExecFails(t, ctx, conn, `
		INSERT INTO artifact_promotion_delivery
			(workspace_id, promotion_id, candidate_id, lineage_id, payload_digest, state, request_payload)
		VALUES ($1, $2, $3, $4, 'bad', 'pending', '{}'::jsonb)`, workspaceID, uuid.NewString(), uuid.New(), uuid.New())
	assertC3b2ExecFails(t, ctx, conn, `
		INSERT INTO artifact_promotion_delivery
			(workspace_id, promotion_id, candidate_id, lineage_id, payload_digest, state, request_payload)
		VALUES ($1, $2, $3, $4, $5, 'bogus', '{}'::jsonb)`, workspaceID, uuid.NewString(), uuid.New(), uuid.New(), validDigest)
	assertC3b2ExecFails(t, ctx, conn, `
		INSERT INTO artifact_promotion_delivery
			(workspace_id, promotion_id, candidate_id, lineage_id, payload_digest, state, request_payload)
		VALUES ($1, 'not-a-uuid', $2, $3, $4, 'pending', '{}'::jsonb)`, workspaceID, uuid.New(), uuid.New(), validDigest)
	assertC3b2ExecFails(t, ctx, conn, `
		INSERT INTO artifact_promotion_delivery
			(workspace_id, promotion_id, candidate_id, lineage_id, payload_digest, state, request_payload, dispatch_token, lease_until)
		VALUES ($1, $2, $3, $4, $5, 'pending', '{}'::jsonb, $6, now())`, workspaceID, uuid.NewString(), uuid.New(), uuid.New(), validDigest, uuid.New())
	assertC3b2ExecFails(t, ctx, conn, `
		INSERT INTO artifact_promotion_delivery
			(workspace_id, promotion_id, candidate_id, lineage_id, payload_digest, state, request_payload, dispatch_token)
		VALUES ($1, $2, $3, $4, $5, 'pending', '{}'::jsonb, $6)`, workspaceID, uuid.NewString(), uuid.New(), uuid.New(), validDigest, uuid.New())
	assertC3b2ExecFails(t, ctx, conn, `
		INSERT INTO artifact_promotion_delivery
			(workspace_id, promotion_id, candidate_id, lineage_id, payload_digest, state, request_payload, lease_until)
		VALUES ($1, $2, $3, $4, $5, 'pending', '{}'::jsonb, now())`, workspaceID, uuid.NewString(), uuid.New(), uuid.New(), validDigest)
	assertC3b2ExecFails(t, ctx, conn, `
		INSERT INTO artifact_promotion_delivery
			(workspace_id, promotion_id, candidate_id, lineage_id, payload_digest, state, request_payload)
		VALUES ($1, $2, $3, $4, $5, 'dispatching', '{}'::jsonb)`, workspaceID, uuid.NewString(), uuid.New(), uuid.New(), validDigest)
	assertC3b2ExecFails(t, ctx, conn, `
		INSERT INTO artifact_promotion_delivery
			(workspace_id, promotion_id, candidate_id, lineage_id, source_task_id, payload_digest, state, request_payload)
		VALUES ($1, $2, $3, $4, $5, $6, 'pending', '{}'::jsonb)`, workspaceID, uuid.NewString(), uuid.New(), uuid.New(), uuid.New(), validDigest)
	assertC3b2ExecFails(t, ctx, conn, `
		INSERT INTO artifact_promotion_delivery
			(workspace_id, promotion_id, candidate_id, lineage_id, payload_digest, state, request_payload)
		VALUES ($1, $2, $3, $4, $5, 'succeeded', '{}'::jsonb)`, workspaceID, uuid.NewString(), uuid.New(), uuid.New(), validDigest)
	assertC3b2ExecFails(t, ctx, conn, `
		INSERT INTO artifact_promotion_delivery
			(workspace_id, promotion_id, candidate_id, lineage_id, payload_digest, state, request_payload, response_receipt)
		VALUES ($1, $2, $3, $4, $5, 'readback_confirmed', '{}'::jsonb, '{}'::jsonb)`, workspaceID, uuid.NewString(), uuid.New(), uuid.New(), validDigest)
	assertC3b2ExecFails(t, ctx, conn, `
		INSERT INTO artifact_promotion_delivery
			(workspace_id, promotion_id, candidate_id, lineage_id, payload_digest, state, request_payload, response_receipt)
		VALUES ($1, $2, $3, $4, $5, 'pending', '{}'::jsonb, '[]'::jsonb)`, workspaceID, uuid.NewString(), uuid.New(), uuid.New(), validDigest)
	assertC3b2ExecFails(t, ctx, conn, `
		INSERT INTO artifact_promotion_delivery
			(workspace_id, promotion_id, candidate_id, lineage_id, payload_digest, state, request_payload, readback_receipt)
		VALUES ($1, $2, $3, $4, $5, 'pending', '{}'::jsonb, '"bad"'::jsonb)`, workspaceID, uuid.NewString(), uuid.New(), uuid.New(), validDigest)
	for _, columns := range []string{"source_task_id", "writer_lease_target_digest", "completion_receipt_digest"} {
		assertC3b2ExecFails(t, ctx, conn, `
			INSERT INTO artifact_promotion_claim
				(workspace_id, promotion_id, candidate_id, lineage_id, payload_digest, `+columns+`)
			VALUES ($1, $2, $3, $4, $5, $6)`, workspaceID, uuid.NewString(), uuid.New(), uuid.New(), validDigest, func() any {
			if columns == "source_task_id" {
				return uuid.New()
			}
			if columns == "writer_lease_target_digest" {
				return repeatedC3b2Hex('e')
			}
			return "sha256:" + repeatedC3b2Hex('f')
		}())
	}
	assertC3b2ExecFails(t, ctx, conn, `DELETE FROM artifact_promotion_delivery WHERE workspace_id = $1 AND promotion_id = $2`, workspaceID, validPromotionID)
	assertC3b2ExecFails(t, ctx, conn, `UPDATE artifact_promotion_delivery SET request_payload = '[1]'::jsonb WHERE workspace_id = $1 AND promotion_id = $2`, workspaceID, validPromotionID)
	assertC3b2ExecFails(t, ctx, conn, `
		INSERT INTO artifact_promotion_delivery
			(workspace_id, promotion_id, candidate_id, lineage_id, payload_digest, state, request_payload)
		VALUES ($1, $2, $3, $4, $5, 'pending', '{}'::jsonb)`, workspaceID, validPromotionID, uuid.New(), uuid.New(), validDigest)
}

func assertC3b2ExecFails(t *testing.T, ctx context.Context, conn *pgx.Conn, statement string, args ...any) {
	t.Helper()
	if _, err := conn.Exec(ctx, statement, args...); err == nil {
		t.Fatalf("expected SQL statement to fail: %s", statement)
	}
}

func assertC3b2IndexExists(t *testing.T, ctx context.Context, conn *pgx.Conn, schema, index string) {
	t.Helper()
	var valid bool
	if err := conn.QueryRow(ctx, `
		SELECT COALESCE((
			SELECT c.indisvalid
			FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = $1 AND c.relname = $2 AND c.relkind = 'i'
		), false)`, schema, index).Scan(&valid); err != nil {
		t.Fatalf("inspect index %s: %v", index, err)
	}
	if !valid {
		t.Fatalf("index %s is missing or invalid", index)
	}
}

func assertC3b2ConstraintExists(t *testing.T, ctx context.Context, conn *pgx.Conn, schema, constraint string) {
	t.Helper()
	var exists bool
	if err := conn.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM pg_constraint c
			JOIN pg_namespace n ON n.oid = c.connamespace
			WHERE n.nspname = $1 AND c.conname = $2
		)`, schema, constraint).Scan(&exists); err != nil {
		t.Fatalf("inspect constraint %s: %v", constraint, err)
	}
	if !exists {
		t.Fatalf("constraint %s is missing", constraint)
	}
}

func assertC3b2FunctionExists(t *testing.T, ctx context.Context, conn *pgx.Conn, schema, function string) {
	t.Helper()
	var exists bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace WHERE n.nspname = $1 AND p.proname = $2)`, schema, function).Scan(&exists); err != nil {
		t.Fatalf("inspect function %s: %v", function, err)
	}
	if !exists {
		t.Fatalf("function %s is missing", function)
	}
}

func assertC3b2FunctionAbsent(t *testing.T, ctx context.Context, conn *pgx.Conn, schema, function string) {
	t.Helper()
	var exists bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace WHERE n.nspname = $1 AND p.proname = $2)`, schema, function).Scan(&exists); err != nil {
		t.Fatalf("inspect rolled-back function %s: %v", function, err)
	}
	if exists {
		t.Fatalf("rolled-back function %s still exists", function)
	}
}

func assertC3b2ColumnExists(t *testing.T, ctx context.Context, conn *pgx.Conn, schema, table, column string) {
	t.Helper()
	var exists bool
	if err := conn.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = $1 AND table_name = $2 AND column_name = $3
		)`, schema, table, column).Scan(&exists); err != nil {
		t.Fatalf("inspect column %s.%s: %v", table, column, err)
	}
	if !exists {
		t.Fatalf("column %s.%s is missing", table, column)
	}
}

func assertC3b2ColumnAbsent(t *testing.T, ctx context.Context, conn *pgx.Conn, schema, table, column string) {
	t.Helper()
	var exists bool
	if err := conn.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = $1 AND table_name = $2 AND column_name = $3
		)`, schema, table, column).Scan(&exists); err != nil {
		t.Fatalf("inspect rolled-back column %s.%s: %v", table, column, err)
	}
	if exists {
		t.Fatalf("rolled-back column %s.%s still exists", table, column)
	}
}

func repeatedC3b2Hex(ch byte) string {
	buf := make([]byte, 64)
	for i := range buf {
		buf[i] = ch
	}
	return string(buf)
}

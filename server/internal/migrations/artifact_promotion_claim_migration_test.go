package migrations

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestArtifactPromotionClaimForwardMigrationUpDownUp(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not set; migration rehearsal is opt-in")
	}
	config, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	if config.Host != "127.0.0.1" || config.Port != 55432 || config.Database != "multica_hivecrew_b2_design_602" {
		t.Skipf("migration rehearsal requires exact isolated database target, got %s:%d/%s", config.Host, config.Port, config.Database)
	}

	ctx := context.Background()
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect migration rehearsal database: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })

	schema := "artifact_claim_rehearsal_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
	if _, err := conn.Exec(ctx, `
		CREATE FUNCTION reject_companyops_artifact_mutation() RETURNS trigger
		LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'immutable'; END $$
	`); err != nil {
		t.Fatalf("create rehearsal immutable trigger function: %v", err)
	}

	applyArtifactClaimMigration(t, ctx, conn, "251_artifact_promotion_claim.up.sql")
	legacyWorkspace := uuid.New()
	legacyPromotion := uuid.NewString()
	legacyCandidate := uuid.New()
	legacyLineage := uuid.New()
	if _, err := conn.Exec(ctx, `
		INSERT INTO artifact_promotion_claim (workspace_id, promotion_id, candidate_id, lineage_id)
		VALUES ($1, $2, $3, $4)
	`, legacyWorkspace, legacyPromotion, legacyCandidate, legacyLineage); err != nil {
		t.Fatalf("insert 251 legacy claim: %v", err)
	}

	for _, file := range []string{
		"252_artifact_promotion_claim_payload_digest.up.sql",
		"253_artifact_promotion_claim_payload_digest_check.up.sql",
		"254_artifact_promotion_claim_promotion_id_check.up.sql",
	} {
		applyArtifactClaimMigration(t, ctx, conn, file)
	}
	assertLegacyArtifactClaimDigestIsNull(t, ctx, conn, legacyWorkspace, legacyPromotion)
	assertArtifactClaimNewWriteConstraints(t, ctx, conn)

	for _, file := range []string{
		"254_artifact_promotion_claim_promotion_id_check.down.sql",
		"253_artifact_promotion_claim_payload_digest_check.down.sql",
		"252_artifact_promotion_claim_payload_digest.down.sql",
	} {
		applyArtifactClaimMigration(t, ctx, conn, file)
	}
	var payloadColumnCount int
	if err := conn.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = 'artifact_promotion_claim' AND column_name = 'payload_digest'
	`, schema).Scan(&payloadColumnCount); err != nil {
		t.Fatalf("inspect payload_digest after down: %v", err)
	}
	if payloadColumnCount != 0 {
		t.Fatalf("payload_digest column count after down = %d, want 0", payloadColumnCount)
	}

	for _, file := range []string{
		"252_artifact_promotion_claim_payload_digest.up.sql",
		"253_artifact_promotion_claim_payload_digest_check.up.sql",
		"254_artifact_promotion_claim_promotion_id_check.up.sql",
	} {
		applyArtifactClaimMigration(t, ctx, conn, file)
	}
	assertLegacyArtifactClaimDigestIsNull(t, ctx, conn, legacyWorkspace, legacyPromotion)
	assertArtifactClaimNewWriteConstraints(t, ctx, conn)
}

func applyArtifactClaimMigration(t *testing.T, ctx context.Context, conn *pgx.Conn, name string) {
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

func assertLegacyArtifactClaimDigestIsNull(t *testing.T, ctx context.Context, conn *pgx.Conn, workspace uuid.UUID, promotionID string) {
	t.Helper()
	var isNull bool
	if err := conn.QueryRow(ctx, `
		SELECT payload_digest IS NULL FROM artifact_promotion_claim
		WHERE workspace_id = $1 AND promotion_id = $2
	`, workspace, promotionID).Scan(&isNull); err != nil {
		t.Fatalf("read upgraded legacy claim: %v", err)
	}
	if !isNull {
		t.Fatal("legacy claim received an invented payload digest")
	}
}

func assertArtifactClaimNewWriteConstraints(t *testing.T, ctx context.Context, conn *pgx.Conn) {
	t.Helper()
	if _, err := conn.Exec(ctx, `
		INSERT INTO artifact_promotion_claim (workspace_id, promotion_id, candidate_id, lineage_id, payload_digest)
		VALUES ($1, $2, $3, $4, NULL)
	`, uuid.New(), uuid.NewString(), uuid.New(), uuid.New()); err == nil {
		t.Fatal("upgraded schema accepted a new NULL payload_digest")
	}
	canonicalDigest := "sha256:" + strings.Repeat("a", 64)
	if _, err := conn.Exec(ctx, `
		INSERT INTO artifact_promotion_claim (workspace_id, promotion_id, candidate_id, lineage_id, payload_digest)
		VALUES ($1, 'NOT-A-CANONICAL-UUID', $2, $3, $4)
	`, uuid.New(), uuid.New(), uuid.New(), canonicalDigest); err == nil {
		t.Fatal("upgraded schema accepted a non-canonical promotion_id")
	}
	if _, err := conn.Exec(ctx, `
		INSERT INTO artifact_promotion_claim (workspace_id, promotion_id, candidate_id, lineage_id, payload_digest)
		VALUES ($1, $2, $3, $4, $5)
	`, uuid.New(), uuid.NewString(), uuid.New(), uuid.New(), canonicalDigest); err != nil {
		t.Fatalf("upgraded schema rejected a canonical new claim: %v", err)
	}
}

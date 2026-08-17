package companyops

import (
	"context"
	"errors"
	"net"
	"os"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestArtifactPromotionDeliveryPostgresConcurrencyAndRecovery(t *testing.T) {
	ctx := context.Background()
	pool := openArtifactPromotionDeliveryTestPool(t)
	workspaceID := uuid.NewString()
	payload := artifactPromotionDeliveryTestPayload()

	promotionID := uuid.NewString()
	candidateID := uuid.NewString()
	lineageID := uuid.NewString()
	if err := withArtifactPromotionDeliveryRepo(ctx, pool, func(repo *ArtifactPersistenceRepository) error {
		_, err := repo.EnsureArtifactPromotionDelivery(ctx, workspaceID, promotionID, candidateID, lineageID, payload, []byte(`{"request":"exact"}`))
		return err
	}); err != nil {
		t.Fatalf("ensure delivery: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE artifact_promotion_delivery SET lease_until = now() - interval '1 second' WHERE workspace_id = $1 AND promotion_id = $2`, workspaceID, promotionID); err == nil {
		t.Fatal("unclaimed pending delivery accepted a forged lease without a dispatch token")
	}

	// Two independent transactions race for the same exact delivery. The
	// UPDATE ... state IN (...) predicate and row lock must produce one owner.
	type claimResult struct {
		row dbArtifactPromotionDelivery
		err error
	}
	start := make(chan struct{})
	results := make(chan claimResult, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			var result claimResult
			result.err = withArtifactPromotionDeliveryRepo(ctx, pool, func(repo *ArtifactPersistenceRepository) error {
				row, err := repo.ClaimArtifactPromotionDelivery(ctx, workspaceID, promotionID, payload.Digest())
				result.row = dbArtifactPromotionDelivery{DispatchToken: row.DispatchToken, Attempt: row.Attempt}
				return err
			})
			results <- result
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	owners := 0
	var ownerToken pgtype.UUID
	for result := range results {
		if result.err == nil {
			owners++
			ownerToken = result.row.DispatchToken
			continue
		}
		if !errors.Is(result.err, ErrArtifactPromotionInProgress) {
			t.Fatalf("losing concurrent claim error = %v, want ErrArtifactPromotionInProgress", result.err)
		}
	}
	if owners != 1 || !ownerToken.Valid {
		t.Fatalf("concurrent claim owners = %d token=%v, want one valid owner", owners, ownerToken)
	}

	stored, err := getArtifactPromotionDeliveryTestRow(ctx, pool, workspaceID, promotionID)
	if err != nil {
		t.Fatal(err)
	}
	stale := stored
	stale.DispatchToken = pgtype.UUID{Bytes: uuid.New(), Valid: true}
	if err := withArtifactPromotionDeliveryRepo(ctx, pool, func(repo *ArtifactPersistenceRepository) error {
		return repo.MarkArtifactPromotionDeliverySucceeded(ctx, stale, []byte(`{"response":true}`))
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("stale dispatch token CAS error = %v, want pgx.ErrNoRows", err)
	}

	if _, err := pool.Exec(ctx, `UPDATE artifact_promotion_delivery SET lease_until = now() - interval '1 second' WHERE workspace_id = $1 AND promotion_id = $2`, workspaceID, promotionID); err != nil {
		t.Fatalf("expire dispatch lease: %v", err)
	}
	if err := withArtifactPromotionDeliveryRepo(ctx, pool, func(repo *ArtifactPersistenceRepository) error {
		return repo.MarkArtifactPromotionDeliveryDefiniteAbsent(ctx, stored, "exact GET proved absence")
	}); err != nil {
		t.Fatalf("mark definite absent: %v", err)
	}
	if err := withArtifactPromotionDeliveryRepo(ctx, pool, func(repo *ArtifactPersistenceRepository) error {
		row, err := repo.ClaimArtifactPromotionDelivery(ctx, workspaceID, promotionID, payload.Digest())
		if err != nil {
			return err
		}
		if row.Attempt != 2 {
			t.Fatalf("reclaimed attempt = %d, want 2", row.Attempt)
		}
		return repo.MarkArtifactPromotionDeliveryFailed(ctx, row, "test cleanup")
	}); err != nil {
		t.Fatalf("reclaim after definite absence: %v", err)
	}

	recoveryPromotionID := uuid.NewString()
	if err := withArtifactPromotionDeliveryRepo(ctx, pool, func(repo *ArtifactPersistenceRepository) error {
		_, err := repo.EnsureArtifactPromotionDelivery(ctx, workspaceID, recoveryPromotionID, uuid.NewString(), uuid.NewString(), payload, []byte(`{"request":"recovery"}`))
		return err
	}); err != nil {
		t.Fatalf("ensure recovery delivery: %v", err)
	}
	recovery, err := claimArtifactPromotionDeliveryTestRow(ctx, pool, workspaceID, recoveryPromotionID, payload.Digest())
	if err != nil {
		t.Fatalf("claim recovery delivery: %v", err)
	}
	if recovery.State != "dispatching" || !recovery.DispatchToken.Valid || recovery.Attempt != 1 {
		t.Fatalf("recovery claim = state %q token_valid=%v attempt=%d, want dispatching/token/1", recovery.State, recovery.DispatchToken.Valid, recovery.Attempt)
	}
	if _, err := pool.Exec(ctx, `UPDATE artifact_promotion_delivery SET lease_until = now() - interval '1 second' WHERE workspace_id = $1 AND promotion_id = $2`, workspaceID, recoveryPromotionID); err != nil {
		t.Fatalf("expire recovery lease: %v", err)
	}
	response := []byte(`{"PromotionID":"` + recoveryPromotionID + `","WritePerformed":true,"Artifact":{}}`)
	readback := []byte(`{"PromotionID":"` + recoveryPromotionID + `","WritePerformed":true,"Artifact":{}}`)
	if err := withArtifactPromotionDeliveryRepo(ctx, pool, func(repo *ArtifactPersistenceRepository) error {
		return repo.RecoverArtifactPromotionDeliveryFromReadback(ctx, recovery, response, readback)
	}); err != nil {
		t.Fatalf("exact GET recovery after lease expiry: %v", err)
	}
	recovered, err := getArtifactPromotionDeliveryTestRow(ctx, pool, workspaceID, recoveryPromotionID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.State != "readback_confirmed" || recovered.Attempt != 1 || len(recovered.ResponseReceipt) == 0 || len(recovered.ReadbackReceipt) == 0 {
		t.Fatalf("recovered delivery = state %q attempt=%d response=%d readback=%d", recovered.State, recovered.Attempt, len(recovered.ResponseReceipt), len(recovered.ReadbackReceipt))
	}
}

func TestArtifactPromotionDeliveryPostgresEvidenceIsImmutable(t *testing.T) {
	ctx := context.Background()
	pool := openArtifactPromotionDeliveryTestPool(t)
	workspaceID, promotionID := uuid.NewString(), uuid.NewString()
	payload := artifactPromotionDeliveryTestPayload()
	if err := withArtifactPromotionDeliveryRepo(ctx, pool, func(repo *ArtifactPersistenceRepository) error {
		_, err := repo.EnsureArtifactPromotionDelivery(ctx, workspaceID, promotionID, uuid.NewString(), uuid.NewString(), payload, []byte(`{"request":"immutable"}`))
		return err
	}); err != nil {
		t.Fatalf("ensure immutable delivery: %v", err)
	}
	if _, err := withArtifactPromotionDeliveryRow(ctx, pool, workspaceID, promotionID, payload.Digest()); err != nil {
		t.Fatal(err)
	}
	claimed, err := claimArtifactPromotionDeliveryTestRow(ctx, pool, workspaceID, promotionID, payload.Digest())
	if err != nil {
		t.Fatal(err)
	}
	if err := withArtifactPromotionDeliveryRepo(ctx, pool, func(repo *ArtifactPersistenceRepository) error {
		return repo.MarkArtifactPromotionDeliverySucceeded(ctx, claimed, []byte(`{"response":true}`))
	}); err != nil {
		t.Fatalf("mark succeeded: %v", err)
	}
	assertArtifactPromotionDeliverySQLFails(t, ctx, pool, `UPDATE artifact_promotion_delivery SET response_receipt = '{"tampered":true}'::jsonb WHERE workspace_id = $1 AND promotion_id = $2`, workspaceID, promotionID)
	succeeded, err := getArtifactPromotionDeliveryTestRow(ctx, pool, workspaceID, promotionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := withArtifactPromotionDeliveryRepo(ctx, pool, func(repo *ArtifactPersistenceRepository) error {
		return repo.MarkArtifactPromotionDeliveryReadbackConfirmed(ctx, succeeded, []byte(`{"readback":true}`))
	}); err != nil {
		t.Fatalf("mark readback confirmed: %v", err)
	}
	assertArtifactPromotionDeliverySQLFails(t, ctx, pool, `UPDATE artifact_promotion_delivery SET readback_receipt = '{"tampered":true}'::jsonb WHERE workspace_id = $1 AND promotion_id = $2`, workspaceID, promotionID)
	assertArtifactPromotionDeliverySQLFails(t, ctx, pool, `UPDATE artifact_promotion_delivery SET state = 'dispatching' WHERE workspace_id = $1 AND promotion_id = $2`, workspaceID, promotionID)
}

// dbArtifactPromotionDelivery is the small concurrency result needed by this
// test; repository rows remain private to the generated db package boundary.
type dbArtifactPromotionDelivery struct {
	DispatchToken pgtype.UUID
	Attempt       int32
}

func artifactPromotionDeliveryTestPayload() PromotionClaimPayload {
	return PromotionClaimPayload{
		SourceTaskID:            uuid.NewString(),
		WriterLeaseTargetDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		CompletionReceiptDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}
}

func openArtifactPromotionDeliveryTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL is not set; delivery PostgreSQL integration is opt-in")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	if config.ConnConfig.Port == 5432 {
		t.Skip("delivery PostgreSQL integration refuses shared port 5432")
	}
	host := config.ConnConfig.Host
	parsed := net.ParseIP(host)
	if host != "localhost" && host != "::1" && (parsed == nil || !parsed.IsLoopback()) {
		t.Skipf("delivery PostgreSQL integration requires loopback host, got %q", host)
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatalf("open delivery PostgreSQL pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(context.Background()); err != nil {
		t.Fatalf("ping delivery PostgreSQL: %v", err)
	}
	return pool
}

func withArtifactPromotionDeliveryRepo(ctx context.Context, pool *pgxpool.Pool, fn func(*ArtifactPersistenceRepository) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(NewArtifactPersistenceRepository(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func withArtifactPromotionDeliveryRow(ctx context.Context, pool *pgxpool.Pool, workspaceID, promotionID, payloadDigest string) (db.ArtifactPromotionDelivery, error) {
	var row db.ArtifactPromotionDelivery
	err := withArtifactPromotionDeliveryRepo(ctx, pool, func(repo *ArtifactPersistenceRepository) error {
		var err error
		row, err = repo.queries.GetArtifactPromotionDelivery(ctx, db.GetArtifactPromotionDeliveryParams{WorkspaceID: pgtype.UUID{Bytes: uuid.MustParse(workspaceID), Valid: true}, PromotionID: promotionID})
		if err == nil && payloadDigest != "" && row.PayloadDigest != payloadDigest {
			return ErrArtifactPromotionConflict
		}
		return err
	})
	return row, err
}

func getArtifactPromotionDeliveryTestRow(ctx context.Context, pool *pgxpool.Pool, workspaceID, promotionID string) (db.ArtifactPromotionDelivery, error) {
	return withArtifactPromotionDeliveryRow(ctx, pool, workspaceID, promotionID, "")
}

func claimArtifactPromotionDeliveryTestRow(ctx context.Context, pool *pgxpool.Pool, workspaceID, promotionID, payloadDigest string) (db.ArtifactPromotionDelivery, error) {
	var row db.ArtifactPromotionDelivery
	err := withArtifactPromotionDeliveryRepo(ctx, pool, func(repo *ArtifactPersistenceRepository) error {
		var err error
		row, err = repo.ClaimArtifactPromotionDelivery(ctx, workspaceID, promotionID, payloadDigest)
		return err
	})
	return row, err
}

func assertArtifactPromotionDeliverySQLFails(t *testing.T, ctx context.Context, pool *pgxpool.Pool, query string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(ctx, query, args...); err == nil {
		t.Fatal("artifact promotion delivery accepted an illegal SQL mutation")
	}
}

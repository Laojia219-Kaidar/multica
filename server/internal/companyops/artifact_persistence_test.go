package companyops

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestArtifactPersistenceCandidateIsImmutableRevisionedAndIdempotent(t *testing.T) {
	fixture := newArtifactPersistenceFixture(t)
	v1 := fixture.createCandidate(t, "candidate-v1")

	replayed, found, err := fixture.repo.FindArtifactCandidateByIdempotencyKey(fixture.ctx, fixture.workspaceID, "candidate-v1")
	if err != nil {
		t.Fatalf("FindArtifactCandidateByIdempotencyKey() error = %v", err)
	}
	if !found || !reflect.DeepEqual(replayed, v1) {
		t.Fatalf("idempotent candidate replay = (%+v, %v), want %+v", replayed, found, v1)
	}

	mutated := v1
	mutated.DurableObjectRef += "-mutated"
	mutated.Digest = "sha256:mutated"
	mutationIntent := fixture.intentFor(mutated, "candidate-v1-mutation")
	if _, err := fixture.repo.RecordArtifactMaterializationIntent(fixture.ctx, mutationIntent); err != nil {
		t.Fatalf("RecordArtifactMaterializationIntent(mutation) error = %v", err)
	}
	if _, err := fixture.repo.CommitArtifactCandidate(fixture.ctx, mutationIntent, mutated); !errors.Is(err, ErrArtifactCandidateImmutable) {
		t.Fatalf("CommitArtifactCandidate(mutation) error = %v, want %v", err, ErrArtifactCandidateImmutable)
	}
	fixture.assertSQLRejected(t, `UPDATE artifact_candidate SET digest = 'sha256:sql-mutation' WHERE id = $1`, v1.ID)

	fixture.appendEvent(t, v1, ArtifactEventChangesRequested, "changes-v1", "")
	v2ID := uuid.NewString()
	v2, err := ReviseArtifactCandidate(v1, ArtifactCandidateRevisionInput{
		ID:               v2ID,
		DurableObjectRef: "object://hivecrew/workspaces/" + fixture.workspaceID + "/artifact-candidates/" + v2ID,
		Digest:           "sha256:v2",
	})
	if err != nil {
		t.Fatalf("ReviseArtifactCandidate() error = %v", err)
	}
	fixture.commitCandidate(t, v2, "candidate-v2")

	storedV1, err := fixture.repo.GetArtifactCandidate(fixture.ctx, fixture.workspaceID, v1.ID)
	if err != nil {
		t.Fatalf("GetArtifactCandidate(v1) error = %v", err)
	}
	if !reflect.DeepEqual(storedV1, v1) {
		t.Fatalf("creating v2 rewrote v1: got=%+v want=%+v", storedV1, v1)
	}
}

func TestArtifactPersistenceEventsAreAppendOnlySequencedAndIdempotent(t *testing.T) {
	fixture := newArtifactPersistenceFixture(t)
	candidate := fixture.createCandidate(t, "candidate-events")

	events, err := fixture.repo.ListArtifactEvents(fixture.ctx, fixture.workspaceID, candidate.LineageID)
	if err != nil {
		t.Fatalf("ListArtifactEvents() error = %v", err)
	}
	if len(events) != 1 || events[0].Type != ArtifactEventSubmitted || events[0].Sequence != 1 {
		t.Fatalf("candidate commit events = %+v, want submitted sequence 1", events)
	}

	approvedInput := persistenceEventInput(candidate, ArtifactEventApproved, "approve-candidate", "")
	approved, err := fixture.repo.AppendArtifactEvent(fixture.ctx, fixture.workspaceID, candidate.LineageID, approvedInput)
	if err != nil {
		t.Fatalf("AppendArtifactEvent(approved) error = %v", err)
	}
	replayed, err := fixture.repo.AppendArtifactEvent(fixture.ctx, fixture.workspaceID, candidate.LineageID, approvedInput)
	if err != nil {
		t.Fatalf("AppendArtifactEvent(idempotent replay) error = %v", err)
	}
	if approved.ID != replayed.ID || approved.Sequence != replayed.Sequence {
		t.Fatalf("event replay changed identity/sequence: first=%+v replay=%+v", approved, replayed)
	}

	events, err = fixture.repo.ListArtifactEvents(fixture.ctx, fixture.workspaceID, candidate.LineageID)
	if err != nil {
		t.Fatalf("ListArtifactEvents() error = %v", err)
	}
	if len(events) != 2 || events[0].Sequence != 1 || events[1].Sequence != 2 {
		t.Fatalf("event sequence = %+v, want exactly [1, 2]", events)
	}
	events[0].Type = ArtifactEventPromotionSucceeded
	again, err := fixture.repo.ListArtifactEvents(fixture.ctx, fixture.workspaceID, candidate.LineageID)
	if err != nil {
		t.Fatalf("ListArtifactEvents(copy-out check) error = %v", err)
	}
	if again[0].Type != ArtifactEventSubmitted {
		t.Fatalf("caller mutation rewrote stored event: %+v", again[0])
	}
	fixture.assertSQLRejected(t, `UPDATE artifact_event SET event_type = 'promotion_succeeded' WHERE id = $1`, again[0].ID)
}

func TestArtifactPersistenceMaterializationIntentCleanupAndExactReferenceDecision(t *testing.T) {
	fixture := newArtifactPersistenceFixture(t)
	orphanCandidate := fixture.candidate("orphan-candidate")
	orphanIntent := fixture.intentFor(orphanCandidate, "orphan-intent")
	if _, err := fixture.repo.RecordArtifactMaterializationIntent(fixture.ctx, orphanIntent); err != nil {
		t.Fatalf("RecordArtifactMaterializationIntent() error = %v", err)
	}
	record, err := fixture.repo.GetArtifactMaterializationRecord(fixture.ctx, fixture.workspaceID, orphanIntent.StorageKey)
	if err != nil || record.State != ArtifactMaterializationPending {
		t.Fatalf("initial materialization record = (%+v, %v), want pending", record, err)
	}
	cleanupCause := errors.New("upload outcome ambiguous")
	if err := fixture.repo.MarkArtifactMaterializationCleanupPending(fixture.ctx, orphanIntent, cleanupCause); err != nil {
		t.Fatalf("MarkArtifactMaterializationCleanupPending() error = %v", err)
	}
	record, err = fixture.repo.GetArtifactMaterializationRecord(fixture.ctx, fixture.workspaceID, orphanIntent.StorageKey)
	if err != nil || record.State != ArtifactMaterializationCleanupPending {
		t.Fatalf("cleanup materialization record = (%+v, %v), want cleanup_pending", record, err)
	}
	if err := fixture.repo.TombstoneArtifactMaterializationIntent(fixture.ctx, fixture.workspaceID, orphanIntent.StorageKey); err != nil {
		t.Fatalf("TombstoneArtifactMaterializationIntent() error = %v", err)
	}
	record, err = fixture.repo.GetArtifactMaterializationRecord(fixture.ctx, fixture.workspaceID, orphanIntent.StorageKey)
	if err != nil || record.State != ArtifactMaterializationTombstoned {
		t.Fatalf("tombstoned materialization record = (%+v, %v), want tombstoned", record, err)
	}

	referenced := fixture.createCandidate(t, "referenced-candidate")
	referencedIntent := fixture.intentFor(referenced, "referenced-stale-intent")
	if _, err := fixture.repo.RecordArtifactMaterializationIntent(fixture.ctx, referencedIntent); err != nil {
		t.Fatalf("RecordArtifactMaterializationIntent(referenced) error = %v", err)
	}
	if err := fixture.repo.MarkArtifactMaterializationCleanupPending(fixture.ctx, referencedIntent, cleanupCause); err != nil {
		t.Fatalf("MarkArtifactMaterializationCleanupPending(referenced) error = %v", err)
	}
	record, err = fixture.repo.GetArtifactMaterializationRecord(fixture.ctx, fixture.workspaceID, referencedIntent.StorageKey)
	if err != nil {
		t.Fatalf("GetArtifactMaterializationRecord(referenced) error = %v", err)
	}
	decision, err := fixture.repo.DecideArtifactMaterializationCleanup(fixture.ctx, record)
	if err != nil {
		t.Fatalf("DecideArtifactMaterializationCleanup() error = %v", err)
	}
	if decision != ArtifactMaterializationKeepObject {
		t.Fatalf("exact candidate key/ref/digest decision = %q, want keep_object", decision)
	}
}

func TestArtifactPersistenceSourceDeletionDoesNotCascadeCandidateOrEvents(t *testing.T) {
	fixture := newArtifactPersistenceFixture(t)
	candidate := fixture.createCandidate(t, "source-independent-candidate")

	if _, err := fixture.tx.Exec(fixture.ctx, `DELETE FROM attachment WHERE id = $1`, candidate.SourceAttachmentID); err != nil {
		t.Fatalf("delete source attachment: %v", err)
	}
	if _, err := fixture.tx.Exec(fixture.ctx, `DELETE FROM comment WHERE id = $1`, candidate.SourceCommentID); err != nil {
		t.Fatalf("delete source comment: %v", err)
	}
	stored, err := fixture.repo.GetArtifactCandidate(fixture.ctx, fixture.workspaceID, candidate.ID)
	if err != nil {
		t.Fatalf("GetArtifactCandidate(after source delete) error = %v", err)
	}
	if !reflect.DeepEqual(stored, candidate) {
		t.Fatalf("source deletion changed candidate: got=%+v want=%+v", stored, candidate)
	}
	events, err := fixture.repo.ListArtifactEvents(fixture.ctx, fixture.workspaceID, candidate.LineageID)
	if err != nil || len(events) != 1 || events[0].Type != ArtifactEventSubmitted {
		t.Fatalf("source deletion changed candidate events: events=%+v err=%v", events, err)
	}
}

func TestArtifactPersistenceFormalVisibilityRequiresExactAuthorityReadback(t *testing.T) {
	fixture := newArtifactPersistenceFixture(t)
	candidate := fixture.createCandidate(t, "promotion-candidate")
	formalRef := "hivecosm://formal-artifacts/FA-1"
	fixture.appendEvent(t, candidate, ArtifactEventApproved, "approve", "")
	fixture.appendEvent(t, candidate, ArtifactEventPromotionRequested, "promotion-request", "")
	fixture.appendEvent(t, candidate, ArtifactEventPromotionSucceeded, "promotion-succeeded", formalRef)

	projection, err := fixture.repo.GetArtifactLifecycleProjection(fixture.ctx, fixture.workspaceID, candidate.LineageID)
	if err != nil {
		t.Fatalf("GetArtifactLifecycleProjection(after promotion) error = %v", err)
	}
	if projection.FormalVisible || projection.FormalArtifactRef != "" {
		t.Fatalf("promotion_succeeded became formal without readback: %+v", projection)
	}

	wrongDigest := persistenceEventInput(candidate, ArtifactEventAuthorityReadbackConfirmed, "bad-readback", formalRef)
	wrongDigest.CandidateDigest = "sha256:other"
	if _, err := fixture.repo.AppendArtifactEvent(fixture.ctx, fixture.workspaceID, candidate.LineageID, wrongDigest); !errors.Is(err, ErrArtifactDigestMismatch) {
		t.Fatalf("AppendArtifactEvent(bad readback) error = %v, want %v", err, ErrArtifactDigestMismatch)
	}
	projection, err = fixture.repo.GetArtifactLifecycleProjection(fixture.ctx, fixture.workspaceID, candidate.LineageID)
	if err != nil || projection.FormalVisible {
		t.Fatalf("bad readback escaped fail-closed gate: projection=%+v err=%v", projection, err)
	}

	readback := fixture.appendEvent(t, candidate, ArtifactEventAuthorityReadbackConfirmed, "authority-readback", formalRef)
	if readback.CandidateRevision != candidate.Revision || readback.CandidateDigest != candidate.Digest || readback.CandidateObjectRef != candidate.DurableObjectRef || readback.FormalArtifactRef != formalRef {
		t.Fatalf("authority readback lost exact candidate/ref binding: %+v", readback)
	}
	projection, err = fixture.repo.GetArtifactLifecycleProjection(fixture.ctx, fixture.workspaceID, candidate.LineageID)
	if err != nil {
		t.Fatalf("GetArtifactLifecycleProjection(after readback) error = %v", err)
	}
	if !projection.FormalVisible || projection.FormalArtifactRef != formalRef || projection.CandidateRevision != candidate.Revision {
		t.Fatalf("confirmed readback projection = %+v", projection)
	}
}

type artifactPersistenceFixture struct {
	ctx         context.Context
	tx          pgx.Tx
	repo        *ArtifactPersistenceRepository
	workspaceID string
}

func newArtifactPersistenceFixture(t *testing.T) artifactPersistenceFixture {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL is not set; artifact persistence contract is opt-in")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	if config.ConnConfig.Port == 5432 {
		t.Skip("artifact persistence contract refuses to connect to port 5432")
	}
	if config.ConnConfig.Port != 55432 {
		t.Skipf("artifact persistence contract requires isolated worktree port 55432, got %d", config.ConnConfig.Port)
	}
	if host := config.ConnConfig.Host; host != "127.0.0.1" && host != "localhost" && host != "::1" {
		t.Skipf("artifact persistence contract requires a loopback database host, got %q", host)
	}
	ctx := context.Background()
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("open artifact persistence test pool: %v", err)
	}
	t.Cleanup(pool.Close)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin artifact persistence test transaction: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	return artifactPersistenceFixture{
		ctx:         ctx,
		tx:          tx,
		repo:        NewArtifactPersistenceRepository(tx),
		workspaceID: uuid.NewString(),
	}
}

func (f artifactPersistenceFixture) candidate(label string) ArtifactCandidate {
	id := uuid.NewString()
	return ArtifactCandidate{
		ID:                 id,
		LineageID:          uuid.NewString(),
		Revision:           1,
		DurableObjectRef:   "object://hivecrew/workspaces/" + f.workspaceID + "/artifact-candidates/" + id,
		Digest:             "sha256:" + label,
		SourceAttachmentID: uuid.NewString(),
		SourceCommentID:    uuid.NewString(),
	}
}

func (f artifactPersistenceFixture) intentFor(candidate ArtifactCandidate, idempotencyKey string) ArtifactMaterializationIntent {
	return ArtifactMaterializationIntent{
		WorkspaceID:        f.workspaceID,
		CandidateID:        candidate.ID,
		LineageID:          candidate.LineageID,
		StorageKey:         "workspaces/" + f.workspaceID + "/artifact-candidates/" + candidate.ID,
		DurableObjectRef:   candidate.DurableObjectRef,
		Digest:             candidate.Digest,
		Filename:           "artifact.md",
		ContentType:        "text/markdown",
		SizeBytes:          8,
		SourceAttachmentID: candidate.SourceAttachmentID,
		SourceCommentID:    candidate.SourceCommentID,
		IdempotencyKey:     idempotencyKey,
	}
}

func (f artifactPersistenceFixture) createCandidate(t *testing.T, idempotencyKey string) ArtifactCandidate {
	t.Helper()
	return f.commitCandidate(t, f.candidate(idempotencyKey), idempotencyKey)
}

func (f artifactPersistenceFixture) commitCandidate(t *testing.T, candidate ArtifactCandidate, idempotencyKey string) ArtifactCandidate {
	t.Helper()
	intent := f.intentFor(candidate, idempotencyKey)
	if _, err := f.repo.RecordArtifactMaterializationIntent(f.ctx, intent); err != nil {
		t.Fatalf("RecordArtifactMaterializationIntent() error = %v", err)
	}
	stored, err := f.repo.CommitArtifactCandidate(f.ctx, intent, candidate)
	if err != nil {
		t.Fatalf("CommitArtifactCandidate() error = %v", err)
	}
	return stored
}

func (f artifactPersistenceFixture) appendEvent(t *testing.T, candidate ArtifactCandidate, eventType ArtifactEventType, idempotencyKey, formalRef string) ArtifactEvent {
	t.Helper()
	event, err := f.repo.AppendArtifactEvent(f.ctx, f.workspaceID, candidate.LineageID, persistenceEventInput(candidate, eventType, idempotencyKey, formalRef))
	if err != nil {
		t.Fatalf("AppendArtifactEvent(%s) error = %v", eventType, err)
	}
	return event
}

func (f artifactPersistenceFixture) assertSQLRejected(t *testing.T, query string, args ...any) {
	t.Helper()
	const savepoint = "artifact_persistence_expected_sql_error"
	if _, err := f.tx.Exec(f.ctx, "SAVEPOINT "+savepoint); err != nil {
		t.Fatalf("create savepoint for expected SQL rejection: %v", err)
	}
	_, rejectedErr := f.tx.Exec(f.ctx, query, args...)
	if _, err := f.tx.Exec(f.ctx, "ROLLBACK TO SAVEPOINT "+savepoint); err != nil {
		t.Fatalf("restore transaction after expected SQL rejection: %v (rejected error: %v)", err, rejectedErr)
	}
	if _, err := f.tx.Exec(f.ctx, "RELEASE SAVEPOINT "+savepoint); err != nil {
		t.Fatalf("release savepoint after expected SQL rejection: %v", err)
	}
	if rejectedErr == nil {
		t.Fatal("append-only persistence table accepted an UPDATE")
	}
}

func persistenceEventInput(candidate ArtifactCandidate, eventType ArtifactEventType, idempotencyKey, formalRef string) ArtifactEventInput {
	return ArtifactEventInput{
		Type:               eventType,
		CandidateID:        candidate.ID,
		CandidateRevision:  candidate.Revision,
		CandidateDigest:    candidate.Digest,
		CandidateObjectRef: candidate.DurableObjectRef,
		FormalArtifactRef:  formalRef,
		IdempotencyKey:     idempotencyKey,
	}
}

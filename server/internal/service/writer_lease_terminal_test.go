package service

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func terminalProofFixture(t *testing.T, count int) ([]WriterLeaseTarget, db.AgentRuntime, db.AgentTaskQueue, []db.LockWriterLeasesForCompletionRow, []WriterLeaseTerminalProof) {
	t.Helper()
	runtimeID := uuid.New()
	taskID := uuid.New()
	runtime := db.AgentRuntime{ID: pgtype.UUID{Bytes: runtimeID, Valid: true}, DaemonID: pgtype.Text{String: "daemon-a", Valid: true}}
	task := db.AgentTaskQueue{ID: pgtype.UUID{Bytes: taskID, Valid: true}, RuntimeID: runtime.ID}
	targets := make([]WriterLeaseTarget, 0, count)
	rows := make([]db.LockWriterLeasesForCompletionRow, 0, count)
	proof := make([]WriterLeaseTerminalProof, 0, count)
	for i := 0; i < count; i++ {
		resourceID := uuid.New()
		token := uuid.New()
		key := WriterLeaseMutexKey("workspace-a", resourceID.String(), "main")
		targets = append(targets, WriterLeaseTarget{ResourceID: resourceID.String(), MutexKey: key, Ref: "main"})
		rows = append(rows, db.LockWriterLeasesForCompletionRow{
			MutexKey:   key,
			HolderID:   pgtype.Text{String: WriterLeaseHolderID("daemon-a", runtimeID.String(), taskID.String()), Valid: true},
			LeaseToken: pgtype.UUID{Bytes: token, Valid: true}, FenceGeneration: 2,
			Status: string(WriteLeaseHeld), ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Minute), Valid: true}, NotExpired: true,
		})
		proof = append(proof, WriterLeaseTerminalProof{ResourceID: resourceID, LeaseToken: token, FenceGeneration: 2})
	}
	return targets, runtime, task, rows, proof
}

func TestValidateWriterLeaseProofRowsAcceptsExactCurrentProof(t *testing.T) {
	targets, runtime, task, rows, proof := terminalProofFixture(t, 2)
	if err := validateWriterLeaseProofRows(targets, runtime, task, proof, rows); err != nil {
		t.Fatalf("valid proof rejected: %v", err)
	}
}

func TestValidateWriterLeaseProofRowsRejectsOldGeneration(t *testing.T) {
	targets, runtime, task, rows, proof := terminalProofFixture(t, 1)
	proof[0].FenceGeneration = 1
	if err := validateWriterLeaseProofRows(targets, runtime, task, proof, rows); !errors.Is(err, ErrWriterLeaseFenceRejected) {
		t.Fatalf("old generation error = %v, want ErrWriterLeaseFenceRejected", err)
	}
}

func TestValidateWriterLeaseProofRowsRejectsDatabaseExpiredLease(t *testing.T) {
	targets, runtime, task, rows, proof := terminalProofFixture(t, 1)
	rows[0].NotExpired = false
	if err := validateWriterLeaseProofRows(targets, runtime, task, proof, rows); !errors.Is(err, ErrWriterLeaseFenceRejected) {
		t.Fatalf("database-expired lease error = %v, want ErrWriterLeaseFenceRejected", err)
	}
}

func TestValidateWriterLeaseProofRowsRejectsFreeLeaseWithNullExpiry(t *testing.T) {
	targets, runtime, task, rows, proof := terminalProofFixture(t, 1)
	rows[0].Status = string(WriteLeaseFree)
	rows[0].ExpiresAt = pgtype.Timestamptz{}
	rows[0].NotExpired = false
	if err := validateWriterLeaseProofRows(targets, runtime, task, proof, rows); !errors.Is(err, ErrWriterLeaseFenceRejected) {
		t.Fatalf("free lease with NULL expiry error = %v, want ErrWriterLeaseFenceRejected", err)
	}
}

func TestValidateWriterLeaseProofRowsRejectsAnyStaleTargetAtomically(t *testing.T) {
	targets, runtime, task, rows, proof := terminalProofFixture(t, 2)
	proof[1].FenceGeneration = 1
	if err := validateWriterLeaseProofRows(targets, runtime, task, proof, rows); !errors.Is(err, ErrWriterLeaseFenceRejected) {
		t.Fatalf("stale multi-target error = %v, want ErrWriterLeaseFenceRejected", err)
	}
}

func TestValidateWriterLeaseProofRowsRejectsForgedMissingAndDuplicateProofs(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]WriterLeaseTerminalProof)
	}{
		{name: "forged", mutate: func(proof []WriterLeaseTerminalProof) { proof[0].ResourceID = uuid.New() }},
		{name: "missing", mutate: func(proof []WriterLeaseTerminalProof) { proof[0].ResourceID = uuid.New() }},
		{name: "duplicate", mutate: func(proof []WriterLeaseTerminalProof) { proof[1].ResourceID = proof[0].ResourceID }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			targets, runtime, task, rows, proof := terminalProofFixture(t, 2)
			if tc.name == "missing" {
				proof = proof[:1]
			} else {
				tc.mutate(proof)
			}
			if err := validateWriterLeaseProofRows(targets, runtime, task, proof, rows); !errors.Is(err, ErrWriterLeaseFenceRejected) {
				t.Fatalf("%s proof error = %v, want ErrWriterLeaseFenceRejected", tc.name, err)
			}
		})
	}
}

func TestWriterLeaseCompletionRequiresTransactionInEnforce(t *testing.T) {
	if err := requireWriterLeaseCompletionTransaction(WriterLeaseModeEnforce, nil); !errors.Is(err, ErrWriterLeaseFenceRejected) {
		t.Fatalf("enforce nil TxStarter error = %v, want ErrWriterLeaseFenceRejected", err)
	}
	if err := requireWriterLeaseCompletionTransaction(WriterLeaseModeOff, nil); err != nil {
		t.Fatalf("off nil TxStarter rejected: %v", err)
	}
	if err := requireWriterLeaseCompletionTransaction(WriterLeaseModeShadow, nil); err != nil {
		t.Fatalf("shadow nil TxStarter rejected: %v", err)
	}
}

func TestValidateWriterLeaseTaskKindFailsClosed(t *testing.T) {
	for _, kind := range []string{"review", "", "unknown"} {
		if err := validateWriterLeaseTaskKind(kind); !errors.Is(err, ErrWriterLeaseFenceRejected) {
			t.Fatalf("task kind %q error = %v, want ErrWriterLeaseFenceRejected", kind, err)
		}
	}
	for _, kind := range []string{"work", "repair"} {
		if err := validateWriterLeaseTaskKind(kind); err != nil {
			t.Fatalf("task kind %q rejected: %v", kind, err)
		}
	}
}

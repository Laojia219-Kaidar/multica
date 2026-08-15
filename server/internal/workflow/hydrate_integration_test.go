//go:build integration

package workflow

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestEngine_HydrateResumesFromPersistence(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()
	q := db.New(pool)
	repo := NewRepository(q)

	def := ProjectLifecycleDefinition()
	if err := repo.SaveDefinition(ctx, def); err != nil {
		t.Fatalf("save def: %v", err)
	}
	inst := WorkflowInstance{
		ID: "hydrate-1", DefinitionID: def.ID, DefinitionVersion: 1,
		Context: ContextRef{ProjectID: "PRJ-1"}, StageIndex: 1, Status: StatusPaused,
	}
	if err := repo.SaveInstance(ctx, inst); err != nil {
		t.Fatalf("save inst: %v", err)
	}
	now := time.Now().UTC()
	for _, ev := range []Event{
		{InstanceID: "hydrate-1", Kind: "workflow.started", SourceRef: "instance://hydrate-1", Actor: "system", OccurredAt: now, ObservedAt: now, IdempotencyKey: "k1"},
		{InstanceID: "hydrate-1", Kind: "workflow.stage_advanced", SourceRef: "instance://hydrate-1", Actor: "a1", OccurredAt: now, ObservedAt: now, IdempotencyKey: "k2"},
	} {
		if err := repo.AppendEvent(ctx, ev); err != nil {
			t.Fatalf("append event: %v", err)
		}
	}

	// Fresh engine hydrates from DB.
	e := NewEngine()
	if err := e.Hydrate(ctx, repo, "hydrate-1"); err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	got, ok := e.Get("hydrate-1")
	if !ok || got.Status != StatusPaused || got.StageIndex != 1 || got.Context.ProjectID != "PRJ-1" {
		t.Fatalf("hydrated instance wrong: %+v ok=%v", got, ok)
	}
	evs := e.Events("hydrate-1")
	if len(evs) != 2 || evs[1].Kind != "workflow.stage_advanced" {
		t.Fatalf("hydrated events wrong: %+v", evs)
	}
}

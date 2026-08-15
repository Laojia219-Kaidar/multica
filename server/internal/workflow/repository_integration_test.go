//go:build integration

package workflow

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/memory"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestRepository_RoundTrip(t *testing.T) {
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

	// --- workflow ---
	wrepo := NewRepository(q)
	def := ProjectLifecycleDefinition()
	if err := wrepo.SaveDefinition(ctx, def); err != nil {
		t.Fatalf("save definition: %v", err)
	}
	got, err := wrepo.LoadDefinition(ctx, def.ID)
	if err != nil {
		t.Fatalf("load definition: %v", err)
	}
	if got.Risk != def.Risk || len(got.Stages) != len(def.Stages) || got.Stages[0].Name != "operate" {
		t.Fatalf("definition round-trip mismatch: %+v", got)
	}

	inst := WorkflowInstance{
		ID: "itest-1", DefinitionID: def.ID, DefinitionVersion: 1,
		Context: ContextRef{ProjectID: "PRJ-1"}, StageIndex: 0, Status: StatusRunning,
	}
	if err := wrepo.SaveInstance(ctx, inst); err != nil {
		t.Fatalf("save instance: %v", err)
	}
	inst.StageIndex = 1
	if err := wrepo.UpdateInstance(ctx, inst); err != nil {
		t.Fatalf("update instance: %v", err)
	}
	loaded, err := wrepo.LoadInstance(ctx, "itest-1")
	if err != nil || loaded.StageIndex != 1 || loaded.Context.ProjectID != "PRJ-1" {
		t.Fatalf("load instance: %+v err=%v", loaded, err)
	}

	now := time.Now().UTC()
	if err := wrepo.AppendEvent(ctx, Event{
		InstanceID: "itest-1", Kind: "workflow.started", SourceRef: "instance://itest-1",
		Actor: "system", OccurredAt: now, ObservedAt: now, IdempotencyKey: "ev-1",
	}); err != nil {
		t.Fatalf("append event: %v", err)
	}
	// duplicate key is a no-op
	if err := wrepo.AppendEvent(ctx, Event{
		InstanceID: "itest-1", Kind: "workflow.started", SourceRef: "x",
		Actor: "system", OccurredAt: now, ObservedAt: now, IdempotencyKey: "ev-1",
	}); err != nil {
		t.Fatalf("duplicate event: %v", err)
	}
	evs, err := wrepo.ListEvents(ctx, "itest-1")
	if err != nil || len(evs) != 1 {
		t.Fatalf("list events: %v err=%v", evs, err)
	}

	// --- memory (unique IDs per run so the persistent DB never accumulates) ---
	mrepo := memory.NewRepository(q)
	uid := time.Now().UnixNano()
	cid := fmt.Sprintf("mtest-%d", uid)
	eid := fmt.Sprintf("EMP-%d", uid)
	cand := memory.MemoryCandidate{
		ID: cid, EmployeeID: eid, PositionID: "SWE", Kind: memory.KindEpisodic,
		Content: "postgres queries tuned", Evidence: []memory.EvidenceRef{{Type: "run", ID: "r1"}},
		SourceRefs: []string{"run://r1"}, AuthorID: eid, Status: memory.StatusValidated,
	}
	if err := mrepo.SaveCandidate(ctx, cand); err != nil {
		t.Fatalf("save candidate: %v", err)
	}
	cloaded, err := mrepo.LoadCandidate(ctx, cid)
	if err != nil || cloaded.Content != "postgres queries tuned" || len(cloaded.Evidence) != 1 {
		t.Fatalf("load candidate: %+v err=%v", cloaded, err)
	}
	byEmp, err := mrepo.ListByEmployee(ctx, eid)
	if err != nil || len(byEmp) != 1 {
		t.Fatalf("list by employee: %v err=%v", byEmp, err)
	}
	if err := mrepo.SavePromotion(ctx, memory.MemoryPromotion{
		CandidateID: cid, Target: memory.TargetSkill, ReviewerID: "REV-01", Approved: true, Reason: "ok",
	}); err != nil {
		t.Fatalf("save promotion: %v", err)
	}
	promos, err := mrepo.ListPromotions(ctx, cid)
	if err != nil || len(promos) != 1 || promos[0].Target != memory.TargetSkill {
		t.Fatalf("list promotions: %v err=%v", promos, err)
	}
}

var _ = time.Second

package workflow

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func def(id string, risk RiskTier, stages ...Stage) WorkflowDefinition {
	return WorkflowDefinition{ID: id, Version: 1, Risk: risk, Stages: stages}
}

func threeStages() WorkflowDefinition {
	return def("d1", RiskFast, Stage{Name: "plan"}, Stage{Name: "build"}, Stage{Name: "close"})
}

func TestValidateDefinition(t *testing.T) {
	if err := ValidateDefinition(threeStages()); err != nil {
		t.Fatalf("valid def rejected: %v", err)
	}
	for name, d := range map[string]WorkflowDefinition{
		"empty stages":       def("x", RiskFast),
		"bad risk":           def("x", RiskTier("warp")),
		"negative sla":       def("x", RiskFast, Stage{Name: "a", SLA: -time.Second}),
		"missing id":         {Version: 1, Risk: RiskFast, Stages: []Stage{{Name: "a"}}},
		"missing stage name": def("x", RiskFast, Stage{Name: ""}),
	} {
		if err := ValidateDefinition(d); err == nil {
			t.Fatalf("%s: expected error", name)
		}
	}
}

func TestValidateGraphVersion(t *testing.T) {
	v := WorkflowDefinitionVersion{
		DefinitionID: "content.wechat-production-package", Version: 1, Risk: RiskOwner,
		Graph: WorkflowGraph{
			Nodes: []GraphNode{{ID: "draft", Kind: NodeAgentTask, Name: "Draft"}, {ID: "approve", Kind: NodeApproval, Name: "Approve"}},
			Edges: []GraphEdge{{ID: "draft-approve", From: "draft", To: "approve"}},
		},
	}
	if err := ValidateGraph(v); err != nil {
		t.Fatalf("valid graph rejected: %v", err)
	}

	bad := v
	bad.Graph.Edges = []GraphEdge{{ID: "cycle", From: "draft", To: "draft"}}
	if err := ValidateGraph(bad); err == nil {
		t.Fatal("self-cycle must be rejected")
	}
}

func TestStartIdempotent(t *testing.T) {
	e := NewEngine()
	_ = e.Register(threeStages())

	i1, r1, err := e.Start("d1", "inst-1", ContextRef{ProjectID: "p1"}, "key-1")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if !r1.Changed || i1.Status != StatusRunning || i1.StageIndex != 0 {
		t.Fatalf("first start: r1=%+v i1=%+v", r1, i1)
	}

	i2, r2, err := e.Start("d1", "inst-1", ContextRef{ProjectID: "p1"}, "key-1")
	if err != nil {
		t.Fatalf("replay start: %v", err)
	}
	if r2.Changed {
		t.Fatalf("replay start must not re-apply: %+v", r2)
	}
	if i2.ID != "inst-1" {
		t.Fatalf("replay returned wrong instance %q", i2.ID)
	}
	if n := len(e.Events("inst-1")); n != 1 {
		t.Fatalf("replay must not duplicate events, got %d", n)
	}
}

func TestStartForWorkspaceRejectsCrossWorkspaceReplay(t *testing.T) {
	e := NewEngine()
	_ = e.Register(threeStages())
	if _, _, err := e.StartForWorkspace("d1", "ws-a-instance", ContextRef{}, "00000000-0000-0000-0000-000000000001", "shared-key"); err != nil {
		t.Fatalf("first scoped start: %v", err)
	}
	if _, _, err := e.StartForWorkspace("d1", "ws-b-instance", ContextRef{}, "00000000-0000-0000-0000-000000000002", "shared-key"); err == nil {
		t.Fatal("cross-workspace idempotency replay must be rejected")
	}
}

func TestFastTierAutoAdvancesToCompletion(t *testing.T) {
	e := NewEngine()
	_ = e.Register(threeStages())

	_, _, _ = e.Start("d1", "inst-1", ContextRef{}, "k0")
	for i := 0; i < 3; i++ {
		inst, r, err := e.Advance("inst-1", AdvanceEvidence{}, "k"+string(rune('a'+i)))
		if err != nil {
			t.Fatalf("advance %d: %v", i, err)
		}
		if !r.Accepted {
			t.Fatalf("advance %d rejected", i)
		}
		if i == 2 && inst.Status != StatusCompleted {
			t.Fatalf("expected completed, got %s", inst.Status)
		}
	}
}

func TestStandardTierRequiresReview(t *testing.T) {
	e := NewEngine()
	_ = e.Register(def("std", RiskStandard, Stage{Name: "code"}))

	_, _, _ = e.Start("std", "i1", ContextRef{}, "k0")

	// Without review: rejected, no stage move.
	inst, r, err := e.Advance("i1", AdvanceEvidence{}, "k1")
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if r.Accepted || inst.StageIndex != 0 {
		t.Fatalf("standard without review must be rejected: r=%+v stage=%d", r, inst.StageIndex)
	}

	// With review: advances and completes.
	inst, r, err = e.Advance("i1", AdvanceEvidence{ReviewPassed: true}, "k2")
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if !r.Accepted || inst.Status != StatusCompleted {
		t.Fatalf("standard with review must complete: r=%+v status=%s", r, inst.Status)
	}
}

func TestOwnerTierRequiresApproval(t *testing.T) {
	e := NewEngine()
	_ = e.Register(def("own", RiskOwner, Stage{Name: "release"}))

	_, _, _ = e.Start("own", "i1", ContextRef{}, "k0")

	inst, r, _ := e.Advance("i1", AdvanceEvidence{ReviewPassed: true}, "k1")
	if r.Accepted || inst.StageIndex != 0 {
		t.Fatalf("owner without approval must be rejected (review is not a substitute)")
	}

	inst, r, _ = e.Advance("i1", AdvanceEvidence{OwnerApproved: true}, "k2")
	if !r.Accepted || inst.Status != StatusCompleted {
		t.Fatalf("owner with approval must complete")
	}
}

func TestPauseResumeStop(t *testing.T) {
	e := NewEngine()
	_ = e.Register(threeStages())
	_, _, _ = e.Start("d1", "i1", ContextRef{}, "k0")

	inst, r, err := e.Pause("i1", "k1")
	if err != nil || !r.Accepted || inst.Status != StatusPaused {
		t.Fatalf("pause failed: %v %+v", err, r)
	}
	inst, _, _ = e.Resume("i1", "k2")
	if inst.Status != StatusRunning {
		t.Fatalf("resume failed: %s", inst.Status)
	}
	inst, _, _ = e.Stop("i1", "k3")
	if inst.Status != StatusStopped {
		t.Fatalf("stop failed: %s", inst.Status)
	}

	// Stopped instance cannot pause.
	_, _, err = e.Pause("i1", "k4")
	if !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("expected ErrIllegalTransition, got %v", err)
	}
}

func TestFailRecover(t *testing.T) {
	e := NewEngine()
	_ = e.Register(threeStages())
	_, _, _ = e.Start("d1", "i1", ContextRef{}, "k0")

	_, _, _ = e.Advance("i1", AdvanceEvidence{}, "k1") // stage 0 -> 1
	inst, r, err := e.Fail("i1", "boom", "k2")
	if err != nil || inst.Status != StatusFailed {
		t.Fatalf("fail: %v %+v", err, r)
	}

	inst, _, err = e.Recover("i1", 1, "k3")
	if err != nil || inst.Status != StatusRunning || inst.StageIndex != 1 {
		t.Fatalf("recover: %v status=%s stage=%d", err, inst.Status, inst.StageIndex)
	}

	// Out-of-range recovery is rejected.
	_, _, err = e.Recover("i1", 99, "k4")
	if !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("expected out-of-range recovery rejection, got %v", err)
	}
}

func TestEventLogAppendOnlyAndOrdered(t *testing.T) {
	e := NewEngine()
	fixed := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	e.SetClock(func() time.Time { return fixed })
	_ = e.Register(threeStages())
	_, _, _ = e.Start("d1", "i1", ContextRef{}, "k0")
	_, _, _ = e.Advance("i1", AdvanceEvidence{}, "k1")

	evs := e.Events("i1")
	if len(evs) != 2 {
		t.Fatalf("expected 2 events, got %d", len(evs))
	}
	if evs[0].Kind != "workflow.started" || evs[1].Kind != "workflow.stage_advanced" {
		t.Fatalf("unexpected event order: %+v", evs)
	}
	if evs[0].Sequence != 0 || evs[1].Sequence != 1 {
		t.Fatalf("sequences not monotonic: %+v", evs)
	}
	if !evs[0].OccurredAt.Equal(fixed) {
		t.Fatalf("occurred_at not from clock: %v", evs[0].OccurredAt)
	}

	// Advance replay must not append a third event.
	_, r, _ := e.Advance("i1", AdvanceEvidence{}, "k1")
	if r.Changed || len(e.Events("i1")) != 2 {
		t.Fatalf("replay advance must not append: %+v", r)
	}
}

func TestStageExecutionRecordsEvidence(t *testing.T) {
	e := NewEngine()
	_ = e.Register(threeStages())
	_, _, _ = e.Start("d1", "i1", ContextRef{}, "k0")
	_, _, _ = e.Advance("i1", AdvanceEvidence{
		TaskID:    "t1",
		RunID:     "r1",
		ActorID:   "a1",
		RuntimeID: "rt1",
		Notes:     []string{"test passed"},
	}, "k1")

	se := e.StageExecutions("i1")
	if len(se) != 1 {
		t.Fatalf("expected 1 stage execution, got %d", len(se))
	}
	if se[0].TaskID != "t1" || se[0].RunID != "r1" || se[0].StageName != "plan" {
		t.Fatalf("stage execution wrong: %+v", se[0])
	}
}

func TestOverdue(t *testing.T) {
	base := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	e := NewEngine()
	e.SetClock(func() time.Time { return base })
	_ = e.Register(def("d1", RiskFast, Stage{Name: "plan", SLA: time.Minute}, Stage{Name: "build"}))
	_, _, _ = e.Start("d1", "i1", ContextRef{}, "k0")

	if _, _, ok := e.Overdue("i1", base.Add(30*time.Second)); ok {
		t.Fatal("should not be overdue at 30s")
	}
	name, by, ok := e.Overdue("i1", base.Add(2*time.Minute))
	if !ok || name != "plan" || by != time.Minute {
		t.Fatalf("overdue = (%q, %v, %v), want (plan, 1m0s, true)", name, by, ok)
	}

	_, _, _ = e.Pause("i1", "k1")
	if _, _, ok := e.Overdue("i1", base.Add(2*time.Minute)); ok {
		t.Fatal("paused instance must not be overdue")
	}
}

func TestConcurrentCommandsAreRaceFree(t *testing.T) {
	e := NewEngine()
	_ = e.Register(threeStages())
	_, _, _ = e.Start("d1", "i1", ContextRef{}, "k0")

	var wg sync.WaitGroup
	for i := 0; i < 60; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			key := fmt.Sprintf("key-%d", n)
			switch n % 3 {
			case 0:
				_, _, _ = e.Advance("i1", AdvanceEvidence{ReviewPassed: true, OwnerApproved: true}, key)
			case 1:
				_, _, _ = e.Pause("i1", key)
				_, _, _ = e.Resume("i1", key)
			case 2:
				_, _ = e.Get("i1")
				_ = e.Events("i1")
			}
		}(i)
	}
	wg.Wait()

	inst, ok := e.Get("i1")
	if !ok || inst.Status == "" {
		t.Fatalf("instance must exist with a valid status, got %+v ok=%v", inst, ok)
	}
}

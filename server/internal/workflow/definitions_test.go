package workflow

import "testing"

func TestProjectLifecycleDefinitionDrivesToClosed(t *testing.T) {
	e := NewEngine()
	if err := e.Register(ProjectLifecycleDefinition()); err != nil {
		t.Fatalf("register: %v", err)
	}

	inst, _, err := e.Start("hivecrew.project-lifecycle", "plc-1",
		ContextRef{ProjectID: "PRJ-1"}, "start-1")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if inst.StageIndex != 0 {
		t.Fatalf("start stage = %d", inst.StageIndex)
	}

	// STANDARD risk: each advance requires independent review evidence.
	steps := []string{"review-1", "review-2", "review-3", "review-4"}
	for i, key := range steps {
		var r Receipt
		var err error
		inst, r, err = e.Advance("plc-1", AdvanceEvidence{
			ReviewPassed: true,
			TaskID:       "t" + string(rune('1'+i)),
			RunID:        "r" + string(rune('1'+i)),
		}, key)
		if err != nil {
			t.Fatalf("advance %d: %v", i, err)
		}
		if !r.Accepted {
			t.Fatalf("advance %d rejected", i)
		}
	}

	if inst.Status != StatusCompleted {
		t.Fatalf("expected completed (CLOSED), got %s", inst.Status)
	}

	se := e.StageExecutions("plc-1")
	if len(se) != 4 {
		t.Fatalf("expected 4 stage executions, got %d", len(se))
	}
	for i, want := range []string{"operate", "review_repair", "closure_pending", "close"} {
		if se[i].StageName != want {
			t.Fatalf("stage %d = %q, want %q", i, se[i].StageName, want)
		}
	}
}

func TestProjectLifecycleDefinitionRequiresReviewEvidence(t *testing.T) {
	e := NewEngine()
	_ = e.Register(ProjectLifecycleDefinition())
	_, _, _ = e.Start("hivecrew.project-lifecycle", "plc-2", ContextRef{ProjectID: "PRJ-2"}, "s1")

	// Without review evidence, the first advance is rejected.
	inst, r, _ := e.Advance("plc-2", AdvanceEvidence{}, "a1")
	if r.Accepted || inst.StageIndex != 0 {
		t.Fatalf("STANDARD lifecycle must require review: r=%+v stage=%d", r, inst.StageIndex)
	}
}

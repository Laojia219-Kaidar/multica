package workwall

import (
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/workflow"
)

func TestRecentEventFromWorkflow(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	ev := workflow.Event{
		Sequence:   3,
		InstanceID: "i1",
		Kind:       "workflow.stage_advanced",
		SourceRef:  "instance://i1",
		OccurredAt: now,
	}

	re := RecentEventFromWorkflow(ev)
	if re.EventID != "wf-3" {
		t.Fatalf("event_id = %q, want wf-3", re.EventID)
	}
	if re.Kind != "workflow.stage_advanced" {
		t.Fatalf("kind = %q", re.Kind)
	}
	if re.SafeSummary != "阶段推进" {
		t.Fatalf("safe_summary = %q", re.SafeSummary)
	}
	if !re.OccurredAt.Equal(now) {
		t.Fatalf("occurred_at = %v", re.OccurredAt)
	}
	if re.SourceRef != "instance://i1" {
		t.Fatalf("source_ref = %q", re.SourceRef)
	}
}

func TestWorkflowEventSummary_AllKindsMapped(t *testing.T) {
	for _, kind := range []string{
		"workflow.started", "workflow.stage_advanced", "workflow.pause",
		"workflow.resume", "workflow.stop", "workflow.fail", "workflow.recovered",
	} {
		if got := workflowEventSummary(kind); got == kind {
			t.Fatalf("kind %q not mapped to a safe summary", kind)
		}
	}
}

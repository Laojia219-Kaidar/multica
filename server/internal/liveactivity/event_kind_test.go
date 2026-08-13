package liveactivity

import "testing"

func TestActivityEventKind_Valid(t *testing.T) {
	valid := []ActivityEventKind{
		EventTaskQueued, EventTaskDispatched, EventRunStarted, EventRunHeartbeat,
		EventToolStarted, EventToolCompleted, EventCommandStarted, EventCommandCompleted,
		EventTestStarted, EventTestResult, EventArtifactCreated, EventReviewRequested,
		EventReviewVerdict, EventRepairRequested, EventRunWaiting, EventRunBlocked,
		EventRunCompleted, EventRunFailed, EventRuntimeOffline,
	}
	if len(valid) != 19 {
		t.Fatalf("expected 19 protocol kinds, got %d", len(valid))
	}
	for _, k := range valid {
		if !k.Valid() {
			t.Fatalf("%q should be valid", k)
		}
	}
	for _, bad := range []ActivityEventKind{"", "task.queued.extra", "run.started2", "unknown"} {
		if bad.Valid() {
			t.Fatalf("%q should be invalid", bad)
		}
	}
}

func TestParseActivityEventKind(t *testing.T) {
	if k, ok := ParseActivityEventKind("run.completed"); !ok || k != EventRunCompleted {
		t.Fatalf("parse run.completed = %q %v", k, ok)
	}
	if _, ok := ParseActivityEventKind("run.stopped"); ok {
		t.Fatalf("run.stopped is not in the protocol")
	}
}

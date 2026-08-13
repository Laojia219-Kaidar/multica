package taskrecovery

import "testing"

// TestTransitionHappyPath walks the full recovery lifecycle from none to
// repaired and back to none.
func TestTransitionHappyPath(t *testing.T) {
	t.Parallel()

	steps := []struct {
		from State
		ev   Event
		want State
	}{
		{StateNone, EventClassified, StatePlanned},
		{StatePlanned, EventRetryStarted, StateRetrying},
		{StateRetrying, EventRetrySucceeded, StateRepaired},
		{StateRepaired, EventReset, StateNone},
	}
	cur := StateNone
	for _, s := range steps {
		next, err := Transition(cur, s.ev)
		if err != nil {
			t.Fatalf("Transition(%q, %q) error: %v", cur, s.ev, err)
		}
		if next != s.want {
			t.Errorf("Transition(%q, %q) = %q, want %q", cur, s.ev, next, s.want)
		}
		cur = next
	}
}

// TestTransitionRetryLoop pins the retry loop: a failed retry returns to
// planned, and exhaustion escalates to manual.
func TestTransitionRetryLoop(t *testing.T) {
	t.Parallel()

	// none -> planned -> retrying -> planned (retry failed) -> retrying -> exhausted -> manual.
	cur := StateNone
	cur = mustTransition(t, cur, EventClassified)
	cur = mustTransition(t, cur, EventRetryStarted)
	cur = mustTransition(t, cur, EventRetryFailed)
	if cur != StatePlanned {
		t.Fatalf("after retry_failed state = %q, want planned", cur)
	}
	cur = mustTransition(t, cur, EventRetryStarted)
	cur = mustTransition(t, cur, EventAttemptsExhausted)
	if cur != StateExhausted {
		t.Fatalf("after attempts_exhausted state = %q, want exhausted", cur)
	}
	cur = mustTransition(t, cur, EventEscalated)
	if cur != StateManual {
		t.Fatalf("after escalated state = %q, want manual", cur)
	}
}

// TestTransitionEscalateFromPlanned pins that a non-retryable plan escalates
// straight from planned to manual.
func TestTransitionEscalateFromPlanned(t *testing.T) {
	t.Parallel()

	cur := mustTransition(t, StateNone, EventClassified)
	cur = mustTransition(t, cur, EventEscalated)
	if cur != StateManual {
		t.Fatalf("state = %q, want manual", cur)
	}
}

// TestTransitionInvalid pins that invalid transitions fail loudly instead of
// silently dropping a recovery step.
func TestTransitionInvalid(t *testing.T) {
	t.Parallel()

	cases := []struct {
		from State
		ev   Event
	}{
		// A retry cannot start before a plan exists.
		{StateNone, EventRetryStarted},
		// A task cannot succeed before a retry is in flight.
		{StatePlanned, EventRetrySucceeded},
		// A repaired task cannot be retried again without a reset.
		{StateRepaired, EventRetryStarted},
		// A manual task cannot be re-escalated.
		{StateManual, EventEscalated},
		// Classifying an already-planned task is a no-op guard.
		{StatePlanned, EventClassified},
	}
	for _, c := range cases {
		t.Run(string(c.from)+"/"+string(c.ev), func(t *testing.T) {
			if _, err := Transition(c.from, c.ev); err == nil {
				t.Errorf("Transition(%q, %q) should error", c.from, c.ev)
			}
		})
	}
}

// TestTransitionResetFromAny pins that reset is always valid and returns to
// none.
func TestTransitionResetFromAny(t *testing.T) {
	t.Parallel()

	for _, s := range []State{StateNone, StatePlanned, StateRetrying, StateRepaired, StateExhausted, StateManual} {
		next, err := Transition(s, EventReset)
		if err != nil {
			t.Fatalf("Transition(%q, reset) error: %v", s, err)
		}
		if next != StateNone {
			t.Errorf("Transition(%q, reset) = %q, want none", s, next)
		}
	}
}

func mustTransition(t *testing.T, s State, e Event) State {
	t.Helper()
	next, err := Transition(s, e)
	if err != nil {
		t.Fatalf("Transition(%q, %q) error: %v", s, e, err)
	}
	return next
}

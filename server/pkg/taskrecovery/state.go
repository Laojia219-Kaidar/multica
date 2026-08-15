package taskrecovery

import "fmt"

// State is the recovery lifecycle state for a task lineage. It is a pure
// value advanced by Transition; this package never persists it.
type State string

const (
	// StateNone: no recovery planned.
	StateNone State = "none"
	// StatePlanned: a plan has been generated but not yet executed.
	StatePlanned State = "planned"
	// StateRetrying: a retry/repair attempt is in flight.
	StateRetrying State = "retrying"
	// StateRepaired: the task recovered.
	StateRepaired State = "repaired"
	// StateExhausted: the attempt budget is exhausted.
	StateExhausted State = "exhausted"
	// StateManual: handed to a human owner.
	StateManual State = "manual"
)

// Event is a state-machine input.
type Event string

const (
	// EventClassified: a plan was generated for the failure.
	EventClassified Event = "classified"
	// EventRetryStarted: a retry/repair attempt was dispatched.
	EventRetryStarted Event = "retry_started"
	// EventRetrySucceeded: the retry completed successfully.
	EventRetrySucceeded Event = "retry_succeeded"
	// EventRetryFailed: the retry failed again.
	EventRetryFailed Event = "retry_failed"
	// EventAttemptsExhausted: the attempt budget is exhausted.
	EventAttemptsExhausted Event = "attempts_exhausted"
	// EventEscalated: handed to a human owner.
	EventEscalated Event = "escalated"
	// EventReset: recovery state cleared (e.g. task completed normally).
	EventReset Event = "reset"
)

// transitionTable maps (state, event) -> next state. Missing entries are
// invalid transitions.
var transitionTable = map[State]map[Event]State{
	StateNone: {
		EventClassified: StatePlanned,
		EventReset:      StateNone,
	},
	StatePlanned: {
		EventRetryStarted:      StateRetrying,
		EventAttemptsExhausted: StateExhausted,
		EventEscalated:         StateManual,
		EventReset:             StateNone,
	},
	StateRetrying: {
		EventRetrySucceeded:    StateRepaired,
		EventRetryFailed:       StatePlanned,
		EventAttemptsExhausted: StateExhausted,
		EventEscalated:         StateManual,
		EventReset:             StateNone,
	},
	StateRepaired: {
		EventReset: StateNone,
	},
	StateExhausted: {
		EventEscalated: StateManual,
		EventReset:     StateNone,
	},
	StateManual: {
		EventReset: StateNone,
	},
}

// Transition applies an event to a state and returns the next state. It
// returns an error for invalid transitions so callers fail loudly rather than
// silently dropping a recovery step.
func Transition(s State, e Event) (State, error) {
	next, ok := transitionTable[s][e]
	if !ok {
		return s, fmt.Errorf("taskrecovery: invalid transition %q from state %q", e, s)
	}
	return next, nil
}

package liveactivity

// ActivityEventKind is the closed LIVE-WORKSITE activity event protocol
// (HIVECREW-LIVE-WORKSITE-V1 §六). These are the at-least event kinds the
// work wall must support; activity.*/workflow.* are additional bridge kinds
// produced from the existing ActivityLog and workflow engine.
type ActivityEventKind string

const (
	EventTaskQueued       ActivityEventKind = "task.queued"
	EventTaskDispatched   ActivityEventKind = "task.dispatched"
	EventRunStarted       ActivityEventKind = "run.started"
	EventRunHeartbeat     ActivityEventKind = "run.heartbeat"
	EventToolStarted      ActivityEventKind = "tool.started"
	EventToolCompleted    ActivityEventKind = "tool.completed"
	EventCommandStarted   ActivityEventKind = "command.started"
	EventCommandCompleted ActivityEventKind = "command.completed"
	EventTestStarted      ActivityEventKind = "test.started"
	EventTestResult       ActivityEventKind = "test.result"
	EventArtifactCreated  ActivityEventKind = "artifact.created"
	EventReviewRequested  ActivityEventKind = "review.requested"
	EventReviewVerdict    ActivityEventKind = "review.verdict"
	EventRepairRequested  ActivityEventKind = "repair.requested"
	EventRunWaiting       ActivityEventKind = "run.waiting"
	EventRunBlocked       ActivityEventKind = "run.blocked"
	EventRunCompleted     ActivityEventKind = "run.completed"
	EventRunFailed        ActivityEventKind = "run.failed"
	EventRuntimeOffline   ActivityEventKind = "runtime.offline"
)

// Valid reports whether the kind is in the closed protocol.
func (k ActivityEventKind) Valid() bool {
	switch k {
	case EventTaskQueued, EventTaskDispatched, EventRunStarted, EventRunHeartbeat,
		EventToolStarted, EventToolCompleted, EventCommandStarted, EventCommandCompleted,
		EventTestStarted, EventTestResult, EventArtifactCreated, EventReviewRequested,
		EventReviewVerdict, EventRepairRequested, EventRunWaiting, EventRunBlocked,
		EventRunCompleted, EventRunFailed, EventRuntimeOffline:
		return true
	default:
		return false
	}
}

// ParseActivityEventKind returns ok=false for any kind outside the protocol.
func ParseActivityEventKind(s string) (ActivityEventKind, bool) {
	k := ActivityEventKind(s)
	return k, k.Valid()
}

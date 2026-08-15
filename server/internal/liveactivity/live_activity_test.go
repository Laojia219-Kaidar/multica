package liveactivity

import "testing"

func TestDerivePresence_Matrix(t *testing.T) {
	tests := []struct {
		name string
		in   Inputs
		want PresenceState
	}{
		{
			name: "online + no open task = idle",
			in:   Inputs{RuntimeOnline: true, HasOpenTask: false},
			want: PresenceIdle,
		},
		{
			name: "queued task = queued",
			in:   Inputs{RuntimeOnline: true, HasOpenTask: true, TaskQueued: true, RunStarted: false},
			want: PresenceQueued,
		},
		{
			name: "running + fresh heartbeat = working",
			in:   Inputs{RuntimeOnline: true, HasOpenTask: true, RunStarted: true, RunExecuting: true, HeartbeatFresh: true},
			want: PresenceWorking,
		},
		{
			name: "running + stale heartbeat = unknown",
			in:   Inputs{RuntimeOnline: true, HasOpenTask: true, RunStarted: true, RunExecuting: true, HeartbeatFresh: false},
			want: PresenceUnknown,
		},
		{
			name: "waiting reason = waiting",
			in:   Inputs{RuntimeOnline: true, HasOpenTask: true, RunStarted: true, RunExecuting: true, HeartbeatFresh: true, WaitingReason: "external dependency"},
			want: PresenceWaiting,
		},
		{
			name: "blocked reason = blocked",
			in:   Inputs{RuntimeOnline: true, HasOpenTask: true, RunStarted: true, RunExecuting: true, HeartbeatFresh: true, BlockedReason: "lead missing"},
			want: PresenceBlocked,
		},
		{
			name: "blocked overrides waiting",
			in:   Inputs{RuntimeOnline: true, HasOpenTask: true, RunStarted: true, RunExecuting: true, HeartbeatFresh: true, WaitingReason: "x", BlockedReason: "y"},
			want: PresenceBlocked,
		},
		{
			name: "offline runtime = offline (even with a running task)",
			in:   Inputs{RuntimeOnline: false, HasOpenTask: true, RunStarted: true, RunExecuting: true, HeartbeatFresh: true},
			want: PresenceOffline,
		},
		{
			name: "recently completed within TTL",
			in:   Inputs{RuntimeOnline: true, HasOpenTask: false, RecentlyCompleted: true},
			want: PresenceRecentlyCompleted,
		},
		{
			name: "agent online without task != working",
			in:   Inputs{RuntimeOnline: true, HasOpenTask: false},
			want: PresenceIdle,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DerivePresence(tt.in); got != tt.want {
				t.Fatalf("DerivePresence() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDeriveWorkStage_FailClosed(t *testing.T) {
	tests := []struct {
		name string
		in   Inputs
		p    PresenceState
		want WorkStage
	}{
		{
			name: "reviewing requires exact review evidence",
			in:   Inputs{ReviewEvidence: true},
			p:    PresenceWorking,
			want: StageReviewing,
		},
		{
			name: "issue in_review without review evidence != reviewing",
			in:   Inputs{StageHint: StageReviewing}, // generic hint is NOT evidence
			p:    PresenceWorking,
			want: StageUnknown,
		},
		{
			name: "repairing requires exact repair evidence",
			in:   Inputs{RepairEvidence: true},
			p:    PresenceWorking,
			want: StageRepairing,
		},
		{
			name: "valid stage hint passes through",
			in:   Inputs{StageHint: StageTesting},
			p:    PresenceWorking,
			want: StageTesting,
		},
		{
			name: "working without any stage evidence = unknown",
			in:   Inputs{},
			p:    PresenceWorking,
			want: StageUnknown,
		},
		{
			name: "idle stage = none",
			in:   Inputs{},
			p:    PresenceIdle,
			want: StageNone,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DeriveWorkStage(tt.in, tt.p); got != tt.want {
				t.Fatalf("DeriveWorkStage() = %q, want %q", got, tt.want)
			}
		})
	}
}

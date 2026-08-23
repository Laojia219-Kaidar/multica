package workwall

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func mustUUID(b byte) pgtype.UUID {
	return pgtype.UUID{Bytes: [16]byte{b}, Valid: true}
}

func selTask(id byte, status string) *db.AgentTaskQueue {
	return &db.AgentTaskQueue{
		ID:      mustUUID(id),
		AgentID: mustUUID(0xAA),
		Status:  status,
	}
}

func TestSelectActiveTask(t *testing.T) {
	tests := []struct {
		name       string
		holder     *db.AgentTaskQueue
		challenger *db.AgentTaskQueue
		wantID     byte
	}{
		{
			name:       "nil_holder_challenger_wins",
			holder:     nil,
			challenger: selTask(1, "queued"),
			wantID:     1,
		},
		{
			name:       "running_beats_dispatched",
			holder:     selTask(1, "dispatched"),
			challenger: selTask(2, "running"),
			wantID:     2,
		},
		{
			name:       "dispatched_beats_waiting_local_directory",
			holder:     selTask(1, "waiting_local_directory"),
			challenger: selTask(2, "dispatched"),
			wantID:     2,
		},
		{
			name:       "waiting_local_directory_beats_queued",
			holder:     selTask(1, "queued"),
			challenger: selTask(2, "waiting_local_directory"),
			wantID:     2,
		},
		{
			name:       "running_beats_queued",
			holder:     selTask(1, "queued"),
			challenger: selTask(2, "running"),
			wantID:     2,
		},
		{
			name:       "holder_running_beats_challenger_queued",
			holder:     selTask(1, "running"),
			challenger: selTask(2, "queued"),
			wantID:     1,
		},
		{
			name:       "same_status_smaller_uuid_wins",
			holder:     selTask(2, "running"),
			challenger: selTask(1, "running"),
			wantID:     1,
		},
		{
			name:       "same_status_holder_smaller_uuid_kept",
			holder:     selTask(1, "dispatched"),
			challenger: selTask(2, "dispatched"),
			wantID:     1,
		},
		{
			name:       "same_status_queued_smaller_uuid_wins",
			holder:     selTask(5, "queued"),
			challenger: selTask(3, "queued"),
			wantID:     3,
		},
		{
			name:       "priority_overrides_uuid_order",
			holder:     selTask(1, "running"),
			challenger: selTask(2, "dispatched"),
			wantID:     1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := selectActiveTask(tc.holder, tc.challenger)
			if got == nil {
				t.Fatal("expected non-nil result")
			}
			if got.ID.Bytes[0] != tc.wantID {
				t.Errorf("got task ID byte %d, want %d", got.ID.Bytes[0], tc.wantID)
			}
		})
	}
}

func TestSelectActiveTask_TieBreakOrderIndependent(t *testing.T) {
	a := selTask(1, "running")
	b := selTask(2, "running")

	ab := selectActiveTask(a, b)
	ba := selectActiveTask(b, a)

	if ab.ID.Bytes[0] != 1 {
		t.Errorf("a then b: got ID byte %d, want 1 (smaller UUID)", ab.ID.Bytes[0])
	}
	if ba.ID.Bytes[0] != 1 {
		t.Errorf("b then a: got ID byte %d, want 1 (smaller UUID)", ba.ID.Bytes[0])
	}
}

func TestActiveTaskSelectionLoop(t *testing.T) {
	agentID := mustUUID(0xAA)

	tests := []struct {
		name       string
		tasks      []db.AgentTaskQueue
		wantStatus string
		wantIDByte byte
		wantNil    bool
	}{
		{
			name:    "empty_input",
			tasks:   nil,
			wantNil: true,
		},
		{
			name: "single_running",
			tasks: []db.AgentTaskQueue{
				{ID: mustUUID(1), AgentID: agentID, Status: "running"},
			},
			wantStatus: "running",
			wantIDByte: 1,
		},
		{
			name: "priority_order_running_wins",
			tasks: []db.AgentTaskQueue{
				{ID: mustUUID(1), AgentID: agentID, Status: "queued"},
				{ID: mustUUID(2), AgentID: agentID, Status: "dispatched"},
				{ID: mustUUID(3), AgentID: agentID, Status: "running"},
				{ID: mustUUID(4), AgentID: agentID, Status: "waiting_local_directory"},
			},
			wantStatus: "running",
			wantIDByte: 3,
		},
		{
			name: "priority_order_reverse_input",
			tasks: []db.AgentTaskQueue{
				{ID: mustUUID(4), AgentID: agentID, Status: "waiting_local_directory"},
				{ID: mustUUID(3), AgentID: agentID, Status: "running"},
				{ID: mustUUID(2), AgentID: agentID, Status: "dispatched"},
				{ID: mustUUID(1), AgentID: agentID, Status: "queued"},
			},
			wantStatus: "running",
			wantIDByte: 3,
		},
		{
			name: "completed_only_no_active",
			tasks: []db.AgentTaskQueue{
				{ID: mustUUID(1), AgentID: agentID, Status: "completed"},
				{ID: mustUUID(2), AgentID: agentID, Status: "completed"},
			},
			wantNil: true,
		},
		{
			name: "failed_only_no_active",
			tasks: []db.AgentTaskQueue{
				{ID: mustUUID(1), AgentID: agentID, Status: "failed"},
			},
			wantNil: true,
		},
		{
			name: "cancelled_only_no_active",
			tasks: []db.AgentTaskQueue{
				{ID: mustUUID(1), AgentID: agentID, Status: "cancelled"},
			},
			wantNil: true,
		},
		{
			name: "mixed_active_and_terminal",
			tasks: []db.AgentTaskQueue{
				{ID: mustUUID(1), AgentID: agentID, Status: "completed"},
				{ID: mustUUID(2), AgentID: agentID, Status: "queued"},
				{ID: mustUUID(3), AgentID: agentID, Status: "failed"},
				{ID: mustUUID(4), AgentID: agentID, Status: "dispatched"},
				{ID: mustUUID(5), AgentID: agentID, Status: "cancelled"},
			},
			wantStatus: "dispatched",
			wantIDByte: 4,
		},
		{
			name: "same_status_tiebreak_forward_order",
			tasks: []db.AgentTaskQueue{
				{ID: mustUUID(5), AgentID: agentID, Status: "queued"},
				{ID: mustUUID(3), AgentID: agentID, Status: "queued"},
				{ID: mustUUID(7), AgentID: agentID, Status: "queued"},
			},
			wantStatus: "queued",
			wantIDByte: 3,
		},
		{
			name: "same_status_tiebreak_reverse_order",
			tasks: []db.AgentTaskQueue{
				{ID: mustUUID(7), AgentID: agentID, Status: "queued"},
				{ID: mustUUID(3), AgentID: agentID, Status: "queued"},
				{ID: mustUUID(5), AgentID: agentID, Status: "queued"},
			},
			wantStatus: "queued",
			wantIDByte: 3,
		},
		{
			name: "same_status_tiebreak_random_order",
			tasks: []db.AgentTaskQueue{
				{ID: mustUUID(9), AgentID: agentID, Status: "dispatched"},
				{ID: mustUUID(1), AgentID: agentID, Status: "dispatched"},
				{ID: mustUUID(5), AgentID: agentID, Status: "dispatched"},
				{ID: mustUUID(3), AgentID: agentID, Status: "dispatched"},
			},
			wantStatus: "dispatched",
			wantIDByte: 1,
		},
		{
			name: "terminal_before_active_ignored",
			tasks: []db.AgentTaskQueue{
				{ID: mustUUID(1), AgentID: agentID, Status: "completed"},
				{ID: mustUUID(2), AgentID: agentID, Status: "failed"},
				{ID: mustUUID(3), AgentID: agentID, Status: "cancelled"},
				{ID: mustUUID(4), AgentID: agentID, Status: "running"},
			},
			wantStatus: "running",
			wantIDByte: 4,
		},
		{
			name: "priority_beats_smaller_uuid",
			tasks: []db.AgentTaskQueue{
				{ID: mustUUID(1), AgentID: agentID, Status: "dispatched"},
				{ID: mustUUID(2), AgentID: agentID, Status: "running"},
			},
			wantStatus: "running",
			wantIDByte: 2,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got *db.AgentTaskQueue
			for i := range tc.tasks {
				task := &tc.tasks[i]
				if isActiveTaskStatus(task.Status) {
					got = selectActiveTask(got, task)
				}
			}
			if tc.wantNil {
				if got != nil {
					t.Fatalf("expected nil, got status=%s id=%d", got.Status, got.ID.Bytes[0])
				}
				return
			}
			if got == nil {
				t.Fatal("expected non-nil result")
			}
			if got.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", got.Status, tc.wantStatus)
			}
			if got.ID.Bytes[0] != tc.wantIDByte {
				t.Errorf("ID byte = %d, want %d", got.ID.Bytes[0], tc.wantIDByte)
			}
		})
	}
}

func TestTaskActivePriority(t *testing.T) {
	tests := []struct {
		status string
		want   int
	}{
		{"running", 4},
		{"dispatched", 3},
		{"waiting_local_directory", 2},
		{"queued", 1},
		{"completed", 0},
		{"failed", 0},
		{"cancelled", 0},
		{"", 0},
	}
	for _, tc := range tests {
		t.Run(tc.status, func(t *testing.T) {
			if got := taskActivePriority(tc.status); got != tc.want {
				t.Errorf("taskActivePriority(%q) = %d, want %d", tc.status, got, tc.want)
			}
		})
	}
}

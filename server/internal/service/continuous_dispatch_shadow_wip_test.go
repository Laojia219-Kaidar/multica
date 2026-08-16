package service

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// An idle agent whose runtime heartbeat has lapsed is a known zero-load
// worker, not unknown WIP: counting it froze all dispatch in production
// (8 stale idle agents on 2026-08-16). Only a working agent needs a live
// runtime for WIP truth.
func TestComposeShadowWIPStaleIdleAgentIsKnownZeroLoad(t *testing.T) {
	now := time.Date(2026, 8, 16, 5, 0, 0, 0, time.UTC)
	staleSeen := pgtype.Timestamptz{Time: now.Add(-10 * time.Minute), Valid: true}
	agents := []db.Agent{
		{ID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, Status: "idle", RuntimeID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}},
		{ID: pgtype.UUID{Bytes: [16]byte{2}, Valid: true}, Status: "working", RuntimeID: pgtype.UUID{Bytes: [16]byte{2}, Valid: true}},
	}
	runtimes := map[string]db.AgentRuntime{
		shadowUUIDString(pgtype.UUID{Bytes: [16]byte{1}, Valid: true}): {LastSeenAt: staleSeen},
		shadowUUIDString(pgtype.UUID{Bytes: [16]byte{2}, Valid: true}): {LastSeenAt: staleSeen},
	}
	evidence := composeShadowWIP(agents, runtimes, nil, now)
	if evidence.UnknownWorkers != 1 {
		t.Fatalf("UnknownWorkers = %d, want 1 (stale idle agent excluded, stale working agent counted)", evidence.UnknownWorkers)
	}
}

// A working agent with a fresh heartbeat contributes no unknown workers.
func TestComposeShadowWIPWorkingAgentFreshRuntime(t *testing.T) {
	now := time.Date(2026, 8, 16, 5, 0, 0, 0, time.UTC)
	fresh := pgtype.Timestamptz{Time: now.Add(-30 * time.Second), Valid: true}
	agents := []db.Agent{
		{ID: pgtype.UUID{Bytes: [16]byte{2}, Valid: true}, Status: "working", RuntimeID: pgtype.UUID{Bytes: [16]byte{2}, Valid: true}},
	}
	runtimes := map[string]db.AgentRuntime{
		shadowUUIDString(pgtype.UUID{Bytes: [16]byte{2}, Valid: true}): {LastSeenAt: fresh},
	}
	evidence := composeShadowWIP(agents, runtimes, nil, now)
	if evidence.UnknownWorkers != 0 {
		t.Fatalf("UnknownWorkers = %d, want 0", evidence.UnknownWorkers)
	}
}

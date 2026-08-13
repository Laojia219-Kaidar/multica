package service

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func projWithLead(hasLead bool, status string) db.Project {
	p := db.Project{Status: status}
	if hasLead {
		p.LeadType = pgtype.Text{String: "agent", Valid: true}
		p.LeadID = pgtype.UUID{Valid: true}
	}
	return p
}

// C2: continue with a missing lead fails closed with ACCOUNTABLE_LEAD_REQUIRED.
func TestValidateProjectControl_ContinueMissingLead(t *testing.T) {
	blockers := validateProjectControl(projWithLead(false, "in_progress"), ActionContinue)
	if !containsStr(blockers, "ACCOUNTABLE_LEAD_REQUIRED") {
		t.Fatalf("blockers = %v, want ACCOUNTABLE_LEAD_REQUIRED", blockers)
	}
}

// C12: a frozen duplicate always blocks every control action.
func TestValidateProjectControl_DuplicateBlocks(t *testing.T) {
	p := projWithLead(true, "in_progress")
	// frozenSupersessions seeds this project id as a duplicate.
	p.ID = pgtype.UUID{Valid: true}
	// (frozenSupersessions key is a string; we assert on the shared gate helper
	// by temporarily overriding the seed in a clean way.)
	orig := frozenSupersessions
	frozenSupersessions = map[string]string{"seed-dup": "other"}
	defer func() { frozenSupersessions = orig }()
	p2 := projWithLead(true, "in_progress")
	p2.ID = mustUUID(t, "00000000-0000-0000-0000-000000000001")
	// use a project whose string id is in the seed:
	blockers := validateProjectControlAt(p2, ActionContinue, map[string]string{"00000000-0000-0000-0000-000000000001": "x"})
	if !containsStr(blockers, "DUPLICATE_AUTHORITY_OWNER_DECISION") {
		t.Fatalf("blockers = %v, want DUPLICATE_AUTHORITY_OWNER_DECISION", blockers)
	}
}

// C4: resume on a non-paused project is refused.
func TestValidateProjectControl_ResumeRequiresPaused(t *testing.T) {
	blockers := validateProjectControl(projWithLead(true, "in_progress"), ActionResume)
	if !containsStr(blockers, "PROJECT_NOT_PAUSED") {
		t.Fatalf("blockers = %v, want PROJECT_NOT_PAUSED", blockers)
	}
}

// continue on a paused project asks to resume first.
func TestValidateProjectControl_ContinuePaused(t *testing.T) {
	blockers := validateProjectControl(projWithLead(true, "paused"), ActionContinue)
	if !containsStr(blockers, "PROJECT_PAUSED_RESUME_FIRST") {
		t.Fatalf("blockers = %v, want PROJECT_PAUSED_RESUME_FIRST", blockers)
	}
}

// A healthy lead + in_progress project passes all gates for continue.
func TestValidateProjectControl_ContinueOk(t *testing.T) {
	blockers := validateProjectControl(projWithLead(true, "in_progress"), ActionContinue)
	if len(blockers) != 0 {
		t.Fatalf("blockers = %v, want none", blockers)
	}
}

func mustUUID(t *testing.T, s string) pgtype.UUID {
	u, err := parseTestUUID(s)
	if err != nil {
		t.Fatalf("parse uuid: %v", err)
	}
	return u
}

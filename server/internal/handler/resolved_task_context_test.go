package handler

import (
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func ownerAuthorizationTask() db.AgentTaskQueue {
	return db.AgentTaskQueue{
		OriginatorUserID:     pgtype.UUID{Bytes: uuid.MustParse("00000000-0000-0000-0000-000000000101"), Valid: true},
		OriginatorSource:     pgtype.Text{String: "direct_human", Valid: true},
		HandoffNote:          pgtype.Text{String: "Authorize only replacement canary 002; no RUN-06.", Valid: true},
		TriggerEvidenceKind:  pgtype.Text{String: "issue_assignment", Valid: true},
		TriggerEvidenceRefID: pgtype.UUID{Bytes: uuid.MustParse("00000000-0000-0000-0000-000000000201"), Valid: true},
	}
}

func TestResolvedTaskContextRequiresDirectWorkspaceOwnerEvidence(t *testing.T) {
	valid := ownerAuthorizationTask()
	resolved := resolvedTaskContextForOwner(valid, "owner")
	if resolved == nil || resolved.OwnerAuthorization == nil {
		t.Fatal("direct owner authorization was not resolved")
	}
	if resolved.OwnerAuthorization.Authorization != valid.HandoffNote.String {
		t.Fatalf("authorization = %q", resolved.OwnerAuthorization.Authorization)
	}

	tests := []struct {
		name string
		role string
		edit func(*db.AgentTaskQueue)
	}{
		{name: "non owner", role: "admin"},
		{name: "non human source", role: "owner", edit: func(task *db.AgentTaskQueue) { task.OriginatorSource.String = "rule_owner" }},
		{name: "missing authorization", role: "owner", edit: func(task *db.AgentTaskQueue) { task.HandoffNote = pgtype.Text{} }},
		{name: "untrusted evidence", role: "owner", edit: func(task *db.AgentTaskQueue) { task.TriggerEvidenceKind.String = "comment_source" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			task := valid
			if tc.edit != nil {
				tc.edit(&task)
			}
			if got := resolvedTaskContextForOwner(task, tc.role); got != nil {
				t.Fatalf("unexpected resolved authorization: %+v", got)
			}
		})
	}
}

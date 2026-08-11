package companyops

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var ErrInvalidHiveCrewAgentAuthority = errors.New("invalid HiveCrew Agent authority")

type hiveCrewAgentAuthorityDigest struct {
	ID                 string `json:"id"`
	WorkspaceID        string `json:"workspace_id"`
	RuntimeID          string `json:"runtime_id"`
	RuntimeMode        string `json:"runtime_mode"`
	Model              string `json:"model"`
	Status             string `json:"status"`
	MaxConcurrentTasks int32  `json:"max_concurrent_tasks"`
	PermissionMode     string `json:"permission_mode"`
	Kind               string `json:"kind"`
	SystemKey          string `json:"system_key"`
	UpdatedAt          string `json:"updated_at"`
}

// BuildHiveCrewAgentAuthoritySnapshot observes an Agent directly from the
// HiveCrew-owned database row. The digest contains only stable execution fields
// and deliberately excludes display metadata, configuration blobs, and secrets.
func BuildHiveCrewAgentAuthoritySnapshot(
	agent db.Agent,
	expectedWorkspaceID string,
	expectedAgentID string,
) (AuthoritySnapshot, error) {
	expectedWorkspace, err := authorityExpectedUUID(expectedWorkspaceID, "expected workspace id")
	if err != nil {
		return AuthoritySnapshot{}, err
	}
	expectedAgent, err := authorityExpectedUUID(expectedAgentID, "expected agent id")
	if err != nil {
		return AuthoritySnapshot{}, err
	}
	if !authorityUUIDEqual(agent.WorkspaceID, expectedWorkspace) {
		return AuthoritySnapshot{}, fmt.Errorf("%w: workspace id does not match expected authority", ErrInvalidHiveCrewAgentAuthority)
	}
	if !authorityUUIDEqual(agent.ID, expectedAgent) {
		return AuthoritySnapshot{}, fmt.Errorf("%w: agent id does not match expected authority", ErrInvalidHiveCrewAgentAuthority)
	}
	if agent.ArchivedAt.Valid {
		return AuthoritySnapshot{}, fmt.Errorf("%w: agent is archived", ErrInvalidHiveCrewAgentAuthority)
	}
	if !isExecutableHiveCrewAgentStatus(agent.Status) {
		return AuthoritySnapshot{}, fmt.Errorf("%w: agent status %q is not executable", ErrInvalidHiveCrewAgentAuthority, agent.Status)
	}
	if !authorityUUIDValid(agent.RuntimeID) {
		return AuthoritySnapshot{}, fmt.Errorf("%w: runtime id is missing or invalid", ErrInvalidHiveCrewAgentAuthority)
	}
	if !agent.UpdatedAt.Valid || agent.UpdatedAt.InfinityModifier != pgtype.Finite || agent.UpdatedAt.Time.IsZero() {
		return AuthoritySnapshot{}, fmt.Errorf("%w: updated_at is missing or invalid", ErrInvalidHiveCrewAgentAuthority)
	}

	agentID := util.UUIDToString(agent.ID)
	updatedAt := agent.UpdatedAt.Time.UTC().Format(time.RFC3339Nano)
	canonical := hiveCrewAgentAuthorityDigest{
		ID:                 agentID,
		WorkspaceID:        util.UUIDToString(agent.WorkspaceID),
		RuntimeID:          util.UUIDToString(agent.RuntimeID),
		RuntimeMode:        agent.RuntimeMode,
		Model:              authorityText(agent.Model),
		Status:             agent.Status,
		MaxConcurrentTasks: agent.MaxConcurrentTasks,
		PermissionMode:     agent.PermissionMode,
		Kind:               agent.Kind,
		SystemKey:          authorityText(agent.SystemKey),
		UpdatedAt:          updatedAt,
	}
	canonicalJSON, err := json.Marshal(canonical)
	if err != nil {
		return AuthoritySnapshot{}, fmt.Errorf("%w: canonical digest JSON: %w", ErrInvalidHiveCrewAgentAuthority, err)
	}
	sum := sha256.Sum256(canonicalJSON)

	return AuthoritySnapshot{
		Kind:          authorityKindAgent,
		SourceRef:     "/api/agents/" + agentID,
		Revision:      "updated_at:" + updatedAt,
		ContentDigest: "sha256:" + hex.EncodeToString(sum[:]),
		Freshness:     currentFreshness,
		DisplayName:   agent.Name,
		Model:         authorityText(agent.Model),
	}, nil
}

func authorityExpectedUUID(value string, field string) (pgtype.UUID, error) {
	parsed, err := util.ParseUUID(value)
	if err != nil || !authorityUUIDValid(parsed) {
		return pgtype.UUID{}, fmt.Errorf("%w: %s is invalid", ErrInvalidHiveCrewAgentAuthority, field)
	}
	return parsed, nil
}

func authorityUUIDValid(value pgtype.UUID) bool {
	return value.Valid && value.Bytes != [16]byte{}
}

func authorityUUIDEqual(actual pgtype.UUID, expected pgtype.UUID) bool {
	return authorityUUIDValid(actual) && actual.Bytes == expected.Bytes
}

func isExecutableHiveCrewAgentStatus(status string) bool {
	return status == "idle" || status == "working"
}

func authorityText(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

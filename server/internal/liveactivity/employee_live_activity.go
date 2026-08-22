package liveactivity

import "time"

// SchemaVersionV1 is the wire schema version for EmployeeLiveActivityV1.
const SchemaVersionV1 = "hivecrew.employee-live-activity.v1"

// FreshnessState is a closed enum describing provenance confidence.
type FreshnessState string

const (
	FreshnessFresh    FreshnessState = "fresh"
	FreshnessStale    FreshnessState = "stale"
	FreshnessMissing  FreshnessState = "missing"
	FreshnessConflict FreshnessState = "conflict"
)

// RecentEvent is one sanitized, structured activity event for the work wall.
// safe_summary is derived from structured event kind/summary only; raw
// stdout/stderr/chain-of-thought are never present here.
type RecentEvent struct {
	EventID     string    `json:"event_id"`
	Kind        string    `json:"kind"`
	SafeSummary string    `json:"safe_summary"`
	OccurredAt  time.Time `json:"occurred_at"`
	SourceRef   string    `json:"source_ref,omitempty"`
}

// EmployeeLiveActivityV1 is the public wire DTO for one work-wall terminal
// window. It is a strict field allowlist: no raw logs, secrets, env values,
// or chain-of-thought may ever be copied into it.
type EmployeeLiveActivityV1 struct {
	SchemaVersion  string `json:"schema_version"`
	WorkspaceID    string `json:"workspace_id"`
	EmployeeID     string `json:"employee_id"`
	AgentID        string `json:"agent_id"`
	DisplayName    string `json:"display_name"`
	AvatarURL      string `json:"avatar_url,omitempty"`
	DepartmentID   string `json:"department_id,omitempty"`
	DepartmentName string `json:"department_name,omitempty"`
	PositionName   string `json:"position_name,omitempty"`

	ProjectID          string `json:"project_id,omitempty"`
	ProjectTitle       string `json:"project_title,omitempty"`
	WorkflowInstanceID string `json:"workflow_instance_id,omitempty"`
	WorkflowTitle      string `json:"workflow_title,omitempty"`
	IssueID            string `json:"issue_id,omitempty"`
	IssueIdentifier    string `json:"issue_identifier,omitempty"`
	IssueTitle         string `json:"issue_title,omitempty"`
	TaskID             string `json:"task_id,omitempty"`
	RunID              string `json:"run_id,omitempty"`

	PresenceState   PresenceState `json:"presence_state"`
	WorkStage       WorkStage     `json:"work_stage"`
	ActivityKind    string        `json:"activity_kind,omitempty"`
	ActivitySummary string        `json:"activity_summary,omitempty"`
	RecentEvents    []RecentEvent `json:"recent_events"`

	BaseID          string `json:"base_id,omitempty"`
	BaseName        string `json:"base_name,omitempty"`
	RuntimeID       string `json:"runtime_id,omitempty"`
	RuntimeProvider string `json:"runtime_provider,omitempty"`
	ModelName       string `json:"model_name,omitempty"`

	// Execution-chain projection (HIV-797). RuntimeProfileID/Name are the
	// registered runtime_profile row bound to the agent's runtime; they are
	// empty when no profile applies (built-in runtime) or the referenced row
	// is missing. ExecutionReceiptRef/Status expose only a safe reference to
	// the authoritative execution_receipt row and its closed terminal status;
	// snapshots, digests, errors and other receipt payloads are never copied.
	RuntimeProfileID       string `json:"runtime_profile_id,omitempty"`
	RuntimeProfileName     string `json:"runtime_profile_name,omitempty"`
	ExecutionReceiptRef    string `json:"execution_receipt_ref,omitempty"`
	ExecutionReceiptStatus string `json:"execution_receipt_status,omitempty"`

	QueuedAt        *time.Time `json:"queued_at,omitempty"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	LastHeartbeatAt *time.Time `json:"last_heartbeat_at,omitempty"`
	LastEventAt     *time.Time `json:"last_event_at,omitempty"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`

	TokenUsage    *int64   `json:"token_usage,omitempty"`
	CostAmount    *float64 `json:"cost_amount,omitempty"`
	BlockedReason string   `json:"blocked_reason,omitempty"`
	NextAction    string   `json:"next_action,omitempty"`

	SourceRefs     []string       `json:"source_refs"`
	ObservedAt     time.Time      `json:"observed_at"`
	FreshnessState FreshnessState `json:"freshness_state"`
}

// SnapshotInput is the internal, authoritative snapshot consumed by BuildDTO.
// Unsafe fields (RawStdout, RawChainOfThought, EnvVars) exist only to prove
// the allowlist: they are NEVER copied into the DTO.
type SnapshotInput struct {
	WorkspaceID string
	EmployeeID  string
	AgentID     string
	DisplayName string
	AvatarURL   string

	DepartmentID   string
	DepartmentName string
	PositionName   string

	ProjectID       string
	ProjectTitle    string
	IssueID         string
	IssueIdentifier string
	IssueTitle      string
	TaskID          string
	RunID           string

	RuntimeID          string
	RuntimeProvider    string
	ModelName          string
	BaseID             string
	BaseName           string
	RuntimeProfileID   string
	RuntimeProfileName string

	ExecutionReceiptRef    string
	ExecutionReceiptStatus string

	Derivation    Inputs
	StageHint     WorkStage
	ActivityKind  string
	ActivityNotes string
	RecentEvents  []RecentEvent
	TokenUsage    *int64
	CostAmount    *float64
	NextAction    string

	QueuedAt        *time.Time
	StartedAt       *time.Time
	LastHeartbeatAt *time.Time
	LastEventAt     *time.Time
	CompletedAt     *time.Time

	SourceRefs     []string
	FreshnessState FreshnessState

	// Unsafe internals — must never reach the DTO.
	RawStdout         string
	RawChainOfThought string
	EnvVars           map[string]string
}

// BuildDTO assembles a sanitized EmployeeLiveActivityV1 from an authoritative
// snapshot. It derives presence/stage and copies ONLY allowlisted fields.
func BuildDTO(s SnapshotInput, observedAt time.Time) EmployeeLiveActivityV1 {
	deriv := s.Derivation
	if deriv.StageHint == "" {
		deriv.StageHint = s.StageHint
	}
	presence := DerivePresence(deriv)
	stage := DeriveWorkStage(deriv, presence)

	events := make([]RecentEvent, 0, len(s.RecentEvents))
	for _, e := range s.RecentEvents {
		events = append(events, RecentEvent{
			EventID:     e.EventID,
			Kind:        e.Kind,
			SafeSummary: e.SafeSummary,
			OccurredAt:  e.OccurredAt,
			SourceRef:   e.SourceRef,
		})
	}

	refs := make([]string, 0, len(s.SourceRefs))
	refs = append(refs, s.SourceRefs...)

	dto := EmployeeLiveActivityV1{
		SchemaVersion:   SchemaVersionV1,
		WorkspaceID:     s.WorkspaceID,
		EmployeeID:      s.EmployeeID,
		AgentID:         s.AgentID,
		DisplayName:     s.DisplayName,
		AvatarURL:       s.AvatarURL,
		DepartmentID:    s.DepartmentID,
		DepartmentName:  s.DepartmentName,
		PositionName:    s.PositionName,
		ProjectID:       s.ProjectID,
		ProjectTitle:    s.ProjectTitle,
		IssueID:         s.IssueID,
		IssueIdentifier: s.IssueIdentifier,
		IssueTitle:      s.IssueTitle,
		TaskID:          s.TaskID,
		RunID:           s.RunID,
		PresenceState:   presence,
		WorkStage:       stage,
		ActivityKind:    s.ActivityKind,
		ActivitySummary: s.ActivityNotes,
		RecentEvents:    events,
		BaseID:          s.BaseID,
		BaseName:        s.BaseName,
		RuntimeID:       s.RuntimeID,
		RuntimeProvider: s.RuntimeProvider,
		ModelName:       s.ModelName,

		RuntimeProfileID:       s.RuntimeProfileID,
		RuntimeProfileName:     s.RuntimeProfileName,
		ExecutionReceiptRef:    s.ExecutionReceiptRef,
		ExecutionReceiptStatus: s.ExecutionReceiptStatus,
		QueuedAt:               s.QueuedAt,
		StartedAt:              s.StartedAt,
		LastHeartbeatAt:        s.LastHeartbeatAt,
		LastEventAt:            s.LastEventAt,
		CompletedAt:            s.CompletedAt,
		TokenUsage:             s.TokenUsage,
		CostAmount:             s.CostAmount,
		BlockedReason:          deriv.BlockedReason,
		NextAction:             s.NextAction,
		SourceRefs:             refs,
		ObservedAt:             observedAt,
		FreshnessState:         s.FreshnessState,
	}
	if dto.FreshnessState == "" {
		dto.FreshnessState = FreshnessFresh
	}
	return dto
}

package workentry

// MCP tool manifest + dispatcher for the Universal Work Registration Kernel.
//
// This file exports the `work.*` tool set (JSON-schema argument definitions +
// a thin dispatcher over the Service methods) so a headless MCP server shim,
// the daemon MCP overlay (server/internal/handler/mcp_overlay.go,
// merge-by-server-name), or any external transport can expose the kernel
// without a second truth source. Tool names follow API-AND-ADAPTER-CONTRACT
// §7.3 / GOAL verb set; `work.register` is included alongside the nine GOAL
// verbs so the session-start journey (resolve -> register -> start) is
// end-to-end usable and matches the HTTP /api/work/* surface.
//
// Every tool returns the same structured receipt the HTTP layer returns and
// never carries secrets or chain-of-thought (event_payload is auditable fields
// only). WorkMCPServerName is the overlay server name.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// WorkMCPServerName is the MCP server name the daemon overlay merges by.
const WorkMCPServerName = "work"

// WorkMCPToolName is the stable tool name under the `work.*` namespace.
type WorkMCPToolName string

const (
	MCPWorkResolve   WorkMCPToolName = "work.resolve"
	MCPWorkRegister  WorkMCPToolName = "work.register"
	MCPWorkStart     WorkMCPToolName = "work.start"
	MCPWorkStatus    WorkMCPToolName = "work.status"
	MCPWorkHeartbeat WorkMCPToolName = "work.heartbeat"
	MCPWorkEvent     WorkMCPToolName = "work.event"
	MCPWorkHandoff   WorkMCPToolName = "work.handoff"
	MCPWorkFinish    WorkMCPToolName = "work.finish"
	MCPWorkSync      WorkMCPToolName = "work.sync"
	MCPWorkDoctor    WorkMCPToolName = "work.doctor"
)

// ErrUnknownMCPTool is returned when CallMCPTool receives a name outside the
// exported manifest.
var ErrUnknownMCPTool = errors.New("unknown work MCP tool")

// MCPPropertySchema is one JSON-Schema property descriptor inside an MCP tool
// inputSchema. It stays a small JSON-only shape so the manifest serializes
// directly into an MCP tools/list response.
type MCPPropertySchema struct {
	Type                 string                       `json:"type"`
	Description          string                       `json:"description,omitempty"`
	Enum                 []string                     `json:"enum,omitempty"`
	Properties           map[string]MCPPropertySchema `json:"properties,omitempty"`
	Items                *MCPPropertySchema           `json:"items,omitempty"`
	AdditionalProperties any                          `json:"additionalProperties,omitempty"`
	Required             []string                     `json:"required,omitempty"`
}

// MCPInputSchema is the JSON-Schema envelope for one tool's arguments.
type MCPInputSchema struct {
	Type       string                       `json:"type"`
	Properties map[string]MCPPropertySchema `json:"properties,omitempty"`
	Required   []string                     `json:"required,omitempty"`
}

// WorkMCPTool is one tools/list entry.
type WorkMCPTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema MCPInputSchema `json:"inputSchema"`
}

// WorkMCPTools returns the ordered set of work.* tool descriptors. The schemas
// reuse the exact JSON field names of WorkActorIdentityV1 / WorkIntentV1 /
// WorkEventV1 / WorkHandoffV1 / WorkCompletionV1 so the MCP path and the
// HTTP/CLI path stay on one contract.
func WorkMCPTools() []WorkMCPTool {
	tools := []WorkMCPTool{
		workResolveTool(),
		workRegisterTool(),
		workStartTool(),
		workStatusTool(),
		workHeartbeatTool(),
		workEventTool(),
		workHandoffTool(),
		workFinishTool(),
		workSyncTool(),
		workDoctorTool(),
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	return tools
}

// ---------------------------------------------------------------------------
// manifest builders
// ---------------------------------------------------------------------------

func mcpObject(desc string, props map[string]MCPPropertySchema, required ...string) MCPPropertySchema {
	return MCPPropertySchema{
		Type:                 "object",
		Description:          desc,
		Properties:           props,
		Required:             required,
		AdditionalProperties: true,
	}
}

func mcpStr(desc string) MCPPropertySchema {
	return MCPPropertySchema{Type: "string", Description: desc}
}

func mcpInt(desc string) MCPPropertySchema {
	return MCPPropertySchema{Type: "integer", Description: desc}
}

func mcpStrArray(desc string) MCPPropertySchema {
	return MCPPropertySchema{Type: "array", Description: desc, Items: &MCPPropertySchema{Type: "string"}}
}

func mcpObjArray(desc string) MCPPropertySchema {
	return MCPPropertySchema{Type: "array", Description: desc, Items: &MCPPropertySchema{Type: "object", AdditionalProperties: true}}
}

func mcpEnum(desc string, values []string) MCPPropertySchema {
	return MCPPropertySchema{Type: "string", Description: desc, Enum: values}
}

func mcpActorIdentitySchema() MCPPropertySchema {
	return mcpObject(
		"Immutable actor identity snapshot (WORK-ACTOR-CONTRACT §4.1). external_agent does NOT require employee_id (VC-02).",
		map[string]MCPPropertySchema{
			"actor_type": mcpEnum("Closed five-value actor type", []string{
				string(ActorRegisteredEmployee), string(ActorExternalAgent),
				string(ActorHumanOperator), string(ActorAutomationService),
				string(ActorObservedUnclaimedActor),
			}),
			"actor_id":       mcpStr("Stable actor id (DE-* / EXT-* / service id / member id)"),
			"employee_id":    mcpStr("DE-* employee id; required only for registered_employee"),
			"human_sponsor":  mcpStr("Human sponsor or authorizing Goal/work-order reference"),
			"carrier_id":     mcpStr("Carrier identifier (protocol family or runtime_profile id)"),
			"runtime_id":     mcpStr("HiveCrew agent_runtime id (UUID)"),
			"model_ref":      mcpStr("Model reference (per-runtime, never normalized)"),
			"base_id":        mcpStr("Base id (BASE-* registry)"),
			"host_id":        mcpStr("Physical host / machine title"),
			"session_id":     mcpStr("Executing session id"),
			"workspace_id":   mcpStr("Workspace (tenant) scope"),
			"observed_at":    mcpStr("Identity observation timestamp (RFC3339)"),
		},
		"actor_type", "carrier_id", "session_id", "workspace_id", "observed_at",
	)
}

func mcpIntentSchema() MCPPropertySchema {
	return mcpObject(
		"Work intent declaration used for dedupe/ownership classification (WORK-ACTOR-CONTRACT §4.2).",
		map[string]MCPPropertySchema{
			"owner_intent":          mcpStr("Owner intent summary / human-result context"),
			"goal_ref":              mcpStr("Goal id or WorkOrder source_ref"),
			"external_campaign_ref": mcpStr("External campaign / work-order reference"),
			"objective":             mcpStr("Objective (what to do)"),
			"expected_human_result": mcpStr("Expected human-visible result"),
			"repo":                  mcpStr("Repository path or URL"),
			"baseline_revision":     mcpStr("Baseline revision (full or short sha)"),
			"branch_or_worktree":    mcpStr("Branch name or isolated worktree path"),
			"read_scope":            mcpStrArray("Read-only scope paths"),
			"write_scope":           mcpStrArray("Writable scope paths"),
			"mutex_keys":            mcpStrArray("Mutex keys for conflict detection"),
			"expected_outcomes":     mcpStrArray("Expected artifact outcomes"),
			"candidate_formal_boundary": mcpEnum("candidate (default) or formal", []string{
				string(BoundaryCandidate), string(BoundaryFormal),
			}),
		},
		"owner_intent", "goal_ref", "objective", "expected_human_result", "repo",
		"baseline_revision", "branch_or_worktree", "read_scope", "write_scope", "expected_outcomes",
	)
}

func workResolveTool() WorkMCPTool {
	return WorkMCPTool{
		Name:        string(MCPWorkResolve),
		Description: "Resolve ownership and dedupe disposition for an intent (read-only). Returns resolution_decision (created|continued|classification_required), matches, similar and a suggestion without writing. classification_required never auto-creates (VC-07).",
		InputSchema: MCPInputSchema{
			Type: "object",
			Properties: map[string]MCPPropertySchema{
				"actor_identity": mcpActorIdentitySchema(),
				"intent":         mcpIntentSchema(),
				"project_id":     mcpStr("Optional explicit project lineage selector"),
				"issue_id":       mcpStr("Optional explicit issue lineage selector"),
			},
			Required: []string{"actor_identity", "intent"},
		},
	}
}

func workRegisterTool() WorkMCPTool {
	return WorkMCPTool{
		Name:        string(MCPWorkRegister),
		Description: "Idempotently register/continue work and return a WorkRegistrationReceiptV1 with a work_ref. Same key+digest replays; different digest returns conflict. Set confirm_create=true only when ownership could not be confirmed.",
		InputSchema: MCPInputSchema{
			Type: "object",
			Properties: map[string]MCPPropertySchema{
				"actor_identity": mcpActorIdentitySchema(),
				"intent":         mcpIntentSchema(),
				"project_id":     mcpStr("Optional explicit project lineage selector"),
				"issue_id":       mcpStr("Optional explicit issue lineage selector"),
				"confirm_create": MCPPropertySchema{Type: "boolean", Description: "Authorize step-7 creation when ownership is not confirmed"},
			},
			Required: []string{"actor_identity", "intent"},
		},
	}
}

func workStartTool() WorkMCPTool {
	return WorkMCPTool{
		Name:        string(MCPWorkStart),
		Description: "Mark execution start for a work_ref (appends a started event; idempotent by work_ref+session_id+run_id).",
		InputSchema: MCPInputSchema{
			Type: "object",
			Properties: map[string]MCPPropertySchema{
				"work_ref":     mcpStr("Work reference to start"),
				"session_id":   mcpStr("Executing session id"),
				"run_id":       mcpStr("Run id (task id)"),
				"actor_id":     mcpStr("Actor id"),
				"workspace_id": mcpStr("Workspace (tenant) scope"),
			},
			Required: []string{"work_ref"},
		},
	}
}

func workStatusTool() WorkMCPTool {
	return WorkMCPTool{
		Name:        string(MCPWorkStatus),
		Description: "Read the current status for a work_ref (read-only projection).",
		InputSchema: MCPInputSchema{
			Type: "object",
			Properties: map[string]MCPPropertySchema{
				"work_ref":     mcpStr("Work reference to describe"),
				"workspace_id": mcpStr("Workspace (tenant) scope"),
			},
			Required: []string{"work_ref"},
		},
	}
}

func workHeartbeatTool() WorkMCPTool {
	return WorkMCPTool{
		Name:        string(MCPWorkHeartbeat),
		Description: "Report terminal/presence heartbeat (upsert).",
		InputSchema: MCPInputSchema{
			Type: "object",
			Properties: map[string]MCPPropertySchema{
				"workspace_id":    mcpStr("Workspace (tenant) scope"),
				"actor_id":        mcpStr("Actor id"),
				"session_id":      mcpStr("Session id"),
				"host":            mcpStr("Physical host / machine title"),
				"session_name":    mcpStr("Terminal session name"),
				"window_index":    mcpInt("Terminal window index"),
				"pane_index":      mcpInt("Terminal pane index"),
				"current_command": mcpStr("Current command"),
				"agent_hint":      mcpStr("Agent hint"),
				"heartbeat_at":    mcpStr("Heartbeat timestamp (RFC3339; defaults to now)"),
			},
			Required: []string{"workspace_id", "actor_id", "session_id"},
		},
	}
}

func workEventTool() WorkMCPTool {
	return WorkMCPTool{
		Name:        string(MCPWorkEvent),
		Description: "Append one structured work event (append-only, idempotent). event_payload must never carry secrets or chain-of-thought.",
		InputSchema: MCPInputSchema{
			Type: "object",
			Properties: map[string]MCPPropertySchema{
				"event_id":   mcpStr("Event id (optional; auto-generated when empty)"),
				"work_ref":   mcpStr("Owning work reference"),
				"session_id": mcpStr("Triggering session id"),
				"run_id":     mcpStr("Associated run id (task id)"),
				"event_type": mcpEnum("Closed work event type", []string{
					string(EventStarted), string(EventProgress), string(EventToolFileScope),
					string(EventCheckpoint), string(EventBlocked), string(EventResumed),
					string(EventCandidateReady), string(EventReviewRequested),
					string(EventRepairRequested), string(EventHandoff), string(EventFinished),
					string(EventAbandonedRecovered),
				}),
				"event_payload":   mcpObject("Auditable event payload (no secrets / no chain-of-thought)", nil),
				"blocker_reason":  mcpStr("Required when event_type=blocked; stable machine-readable reason"),
				"receiver":        mcpStr("Receiver / wake target for handoff or blocked events"),
				"idempotency_key": mcpStr("Idempotency key (unique per work_ref)"),
				"occurred_at":     mcpStr("Occurrence timestamp (RFC3339)"),
				"observed_at":     mcpStr("Observation timestamp (RFC3339)"),
			},
			Required: []string{"work_ref", "session_id", "event_type", "event_payload", "idempotency_key", "occurred_at", "observed_at"},
		},
	}
}

func workHandoffTool() WorkMCPTool {
	return WorkMCPTool{
		Name:        string(MCPWorkHandoff),
		Description: "Submit a candidate handoff package with executed evidence. Candidate-only; never auto-passes (review_routed=true, auto_passed=false).",
		InputSchema: MCPInputSchema{
			Type: "object",
			Properties: map[string]MCPPropertySchema{
				"work_ref":           mcpStr("Owning work reference"),
				"revision":           mcpStr("Final revision (commit or worktree state)"),
				"branch_or_worktree": mcpStr("Branch name or worktree path"),
				"diff_files":         mcpStrArray("Changed file paths"),
				"tests":              mcpObjArray("Executed tests: {command,result,evidence_ref}"),
				"browser_evidence":   mcpObjArray("Browser evidence items"),
				"api_evidence":       mcpObjArray("API evidence items"),
				"db_evidence":        mcpObjArray("DB evidence items"),
				"artifact_refs":      mcpStrArray("Candidate artifact references"),
				"remaining_blockers": mcpStrArray("Remaining blockers"),
				"receiver":           mcpStr("Receiver / wake target"),
				"next_action":        mcpStr("Next executable action"),
			},
			Required: []string{"work_ref", "revision", "branch_or_worktree", "diff_files", "tests", "artifact_refs", "next_action"},
		},
	}
}

func workFinishTool() WorkMCPTool {
	return WorkMCPTool{
		Name:        string(MCPWorkFinish),
		Description: "Submit a completion candidate and route it to independent review. Never auto-passes (review_routed=true, auto_passed=false).",
		InputSchema: MCPInputSchema{
			Type: "object",
			Properties: map[string]MCPPropertySchema{
				"work_ref": mcpStr("Owning work reference"),
				"completion_candidate": mcpObject("Candidate artifact under review", map[string]MCPPropertySchema{
					"artifact_ref": mcpStr("Candidate artifact reference"),
					"digest":       mcpStr("sha256:<64hex> digest of the candidate"),
					"revision":     mcpStr("Candidate revision"),
				}, "artifact_ref", "digest"),
				"review": mcpObject("Independent review decision block", map[string]MCPPropertySchema{
					"reviewer_actor_id": mcpStr("Reviewer actor id"),
					"decision":          mcpEnum("PASS or REVISE", []string{string(ReviewPass), string(ReviewRevise)}),
					"evidence_refs":     mcpStrArray("Review evidence references"),
					"reviewed_at":       mcpStr("Review timestamp (RFC3339)"),
				}, "reviewer_actor_id", "decision"),
				"project_lifecycle_consequence": mcpEnum("Post-completion lifecycle action", []string{
					string(LifecycleContinue), string(LifecyclePauseDispatch),
					string(LifecycleResume), string(LifecycleClose), string(LifecycleSupersede),
				}),
			},
			Required: []string{"work_ref", "completion_candidate", "review", "project_lifecycle_consequence"},
		},
	}
}

func workSyncTool() WorkMCPTool {
	return WorkMCPTool{
		Name:        string(MCPWorkSync),
		Description: "Replay an ordered offline spool (idempotent). Conflicts are reported per entry and do not abort the rest of the batch.",
		InputSchema: MCPInputSchema{
			Type: "object",
			Properties: map[string]MCPPropertySchema{
				"entries": MCPPropertySchema{
					Type:        "array",
					Description: "Ordered offline spool entries",
					Items: &MCPPropertySchema{Type: "object", AdditionalProperties: true, Properties: map[string]MCPPropertySchema{
						"verb":             mcpEnum("Spool verb", []string{"register", "event"}),
						"idempotency_key":  mcpStr("Idempotency key carried verbatim from the offline entry"),
						"payload_digest":   mcpStr("sha256:<64hex> payload digest"),
						"canonical_payload": mcpObject("Canonical register or event payload", nil),
					}},
				},
			},
			Required: []string{"entries"},
		},
	}
}

func workDoctorTool() WorkMCPTool {
	return WorkMCPTool{
		Name:        string(MCPWorkDoctor),
		Description: "Diagnose the unclaimed inbox (read-only reconcile): lists inbox entries awaiting attach/ignore.",
		InputSchema: MCPInputSchema{
			Type: "object",
			Properties: map[string]MCPPropertySchema{
				"workspace_id": mcpStr("Workspace (tenant) scope"),
			},
			Required: []string{"workspace_id"},
		},
	}
}

// ---------------------------------------------------------------------------
// dispatcher
// ---------------------------------------------------------------------------

// CallMCPTool forwards one tools/call argument map to the matching Service
// method. args is the raw JSON-decoded arguments object from the MCP request;
// each case re-encodes and decodes into the typed request so the MCP boundary
// stays a thin JSON wrapper over the same contracts the HTTP layer uses.
func (s *Service) CallMCPTool(ctx context.Context, toolName string, args map[string]any) (any, error) {
	if s == nil || s.store == nil {
		return nil, ErrUnavailable
	}
	switch toolName {
	case string(MCPWorkResolve):
		req, err := decodeMCPArgs[ResolveRequest](args)
		if err != nil {
			return nil, err
		}
		return s.ResolvePreview(ctx, req)
	case string(MCPWorkRegister):
		req, err := decodeMCPArgs[RegisterRequest](args)
		if err != nil {
			return nil, err
		}
		return s.Register(ctx, req)
	case string(MCPWorkStart):
		req, err := decodeMCPArgs[StartRequest](args)
		if err != nil {
			return nil, err
		}
		return s.Start(ctx, req)
	case string(MCPWorkStatus):
		req, err := decodeMCPArgs[StatusRequest](args)
		if err != nil {
			return nil, err
		}
		return s.Status(ctx, req)
	case string(MCPWorkHeartbeat):
		return s.Heartbeat(ctx, decodeMCPHeartbeat(args))
	case string(MCPWorkEvent):
		event, err := decodeMCPArgs[WorkEventV1](args)
		if err != nil {
			return nil, err
		}
		return s.Event(ctx, event)
	case string(MCPWorkHandoff):
		pkg, err := decodeMCPArgs[WorkHandoffV1](args)
		if err != nil {
			return nil, err
		}
		return s.Handoff(ctx, pkg)
	case string(MCPWorkFinish):
		completion, err := decodeMCPArgs[WorkCompletionV1](args)
		if err != nil {
			return nil, err
		}
		return s.Finish(ctx, completion)
	case string(MCPWorkSync):
		req, err := decodeMCPArgs[mcpSyncRequest](args)
		if err != nil {
			return nil, err
		}
		return s.Sync(ctx, req.Entries)
	case string(MCPWorkDoctor):
		var req mcpDoctorRequest
		if err := decodeMCPArgsInto(args, &req); err != nil {
			return nil, err
		}
		items, err := s.Reconcile(ctx, req.WorkspaceID)
		if items == nil {
			items = []InboxItem{}
		}
		return items, err
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnknownMCPTool, toolName)
	}
}

// mcpSyncRequest mirrors the HTTP sync envelope ({entries:[...]}).
type mcpSyncRequest struct {
	Entries []SyncEntry `json:"entries"`
}

// mcpDoctorRequest mirrors the doctor/reconcile workspace selector.
type mcpDoctorRequest struct {
	WorkspaceID string `json:"workspace_id"`
}

func decodeMCPArgs[T any](args map[string]any) (T, error) {
	var dst T
	if err := decodeMCPArgsInto(args, &dst); err != nil {
		return dst, err
	}
	return dst, nil
}

func decodeMCPArgsInto(args map[string]any, dst any) error {
	b, err := json.Marshal(args)
	if err != nil {
		return invalid(fmt.Errorf("encode mcp arguments: %w", err))
	}
	if err := json.Unmarshal(b, dst); err != nil {
		return invalid(fmt.Errorf("decode mcp arguments: %w", err))
	}
	return nil
}

// decodeMCPHeartbeat maps the snake_case MCP schema onto HeartbeatRecord,
// which intentionally carries no JSON tags (its HTTP shape is owned by the
// CLI flag layer, not by a wire contract).
func decodeMCPHeartbeat(args map[string]any) HeartbeatRecord {
	return HeartbeatRecord{
		WorkspaceID:    mcpStringArg(args, "workspace_id"),
		ActorID:        mcpStringArg(args, "actor_id"),
		SessionID:      mcpStringArg(args, "session_id"),
		Host:           mcpStringArg(args, "host"),
		SessionName:    mcpStringArg(args, "session_name"),
		WindowIndex:    mcpIntArg(args, "window_index"),
		PaneIndex:      mcpIntArg(args, "pane_index"),
		CurrentCommand: mcpStringArg(args, "current_command"),
		AgentHint:      mcpStringArg(args, "agent_hint"),
		HeartbeatAt:    mcpStringArg(args, "heartbeat_at"),
	}
}

func mcpStringArg(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return strings.TrimSpace(v)
}

func mcpIntArg(args map[string]any, key string) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	default:
		return 0
	}
}

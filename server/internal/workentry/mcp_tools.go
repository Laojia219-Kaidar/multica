package workentry

// MCP tool definitions for the Universal Work Registration Kernel.
//
// These are the JSON-schema tool descriptors a headless MCP server exposes so
// carriers (Claude Desktop, Codex, Cursor, generic MCP clients) can register
// work without a browser login. The actual transport wiring into the daemon
// MCP overlay (server/internal/handler/mcp_overlay.go merge-by-server-name) is
// a follow-up; this file is the stable tool contract the overlay and any HTTP
// MCP shim must honor.

import (
	"sort"
)

// WorkMCPToolName is the stable tool name exposed under the `work.*` namespace.
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

// WorkMCPTool is one tool descriptor. Schema is expressed as a map so it can be
// rendered directly into a tool-calling payload; InputSchema must always carry
// actor identity + work_ref context so every write traces to actor/session/work_ref.
type WorkMCPTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// WorkMCPTools returns the ordered set of work.* tool descriptors. The schemas
// intentionally reuse the exact JSON field names of WorkActorIdentityV1 /
// WorkIntentV1 / WorkEventV1 so the MCP path and the HTTP/CLI path stay on one
// contract (WORK-ACTOR-CONTRACT.md / API-AND-ADAPTER-CONTRACT.md).
func WorkMCPTools() []WorkMCPTool {
	tools := []WorkMCPTool{
		{Name: string(MCPWorkResolve), Description: "Read-only dedupe/ownership resolution for a work intent (no writes).",
			InputSchema: objectSchema("resolve", []string{"actor_identity", "intent"})},
		{Name: string(MCPWorkRegister), Description: "Idempotently register/continue work and return a work_ref receipt.",
			InputSchema: objectSchema("register", []string{"actor_identity", "intent"})},
		{Name: string(MCPWorkStart), Description: "Mark execution start for a work_ref (appends started event).",
			InputSchema: objectSchema("start", []string{"work_ref", "actor_id", "session_id"})},
		{Name: string(MCPWorkStatus), Description: "Read current status for a work_ref.",
			InputSchema: objectSchema("status", []string{"work_ref"})},
		{Name: string(MCPWorkHeartbeat), Description: "Report a terminal/presence heartbeat.",
			InputSchema: objectSchema("heartbeat", []string{"actor_id", "session_id", "host"})},
		{Name: string(MCPWorkEvent), Description: "Append a structured work event (started/progress/checkpoint/blocked/resumed/candidate_ready/review_requested/repair_requested/handoff/finished/abandoned/recovered).",
			InputSchema: objectSchema("event", []string{"work_ref", "type", "occurred_at"})},
		{Name: string(MCPWorkHandoff), Description: "Submit a candidate handoff package.",
			InputSchema: objectSchema("handoff", []string{"work_ref", "revision"})},
		{Name: string(MCPWorkFinish), Description: "Submit a completion candidate for independent review (never auto-pass).",
			InputSchema: objectSchema("finish", []string{"work_ref", "revision"})},
		{Name: string(MCPWorkSync), Description: "Replay the local offline pending spool idempotently.",
			InputSchema: objectSchema("sync", []string{})},
		{Name: string(MCPWorkDoctor), Description: "Diagnose the unclaimed inbox (list/attach/ignore).",
			InputSchema: objectSchema("doctor", []string{})},
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	return tools
}

func objectSchema(name string, required []string) map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"name": name,
		},
		"required": required,
	}
}

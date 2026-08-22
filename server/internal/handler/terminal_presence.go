package handler

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Terminal presence: read-only projection of what each digital employee is
// actually doing inside host terminal panes right now. A host-side collector
// (scripts/terminal-presence-collector.sh + launchd agent) upserts sanitized
// pane tails; the work wall renders them as the "Terminal 现场" strip.

type terminalPresenceDTO struct {
	Host           string `json:"host"`
	SessionName    string `json:"session_name"`
	WindowIndex    int    `json:"window_index"`
	PaneIndex      int    `json:"pane_index"`
	CurrentCommand string `json:"current_command"`
	AgentHint      string `json:"agent_hint"`
	TailText       string `json:"tail_text"`
	HeartbeatAt    string `json:"heartbeat_at"`
}

type upsertTerminalPresenceRequest struct {
	WorkspaceSlug string                 `json:"workspace_slug"`
	Host          string                 `json:"host"`
	Sessions      []terminalPresencePane `json:"sessions"`
}

type terminalPresencePane struct {
	SessionName    string `json:"session_name"`
	WindowIndex    int    `json:"window_index"`
	PaneIndex      int    `json:"pane_index"`
	PanePID        int    `json:"pane_pid"`
	CurrentCommand string `json:"current_command"`
	AgentHint      string `json:"agent_hint"`
	TailText       string `json:"tail_text"`
}

// ReportTerminalPresence POST /api/work-wall/terminal-presence — collector
// ingest. Authenticated as a workspace member (loopback owner session covers
// the host collector); each request replaces the host's panes wholesale.
//
// Workspace authority: the authenticated workspace comes from
// resolveWorkspaceID (middleware / X-Workspace-ID / task-token binding). The
// request-body WorkspaceSlug is IGNORED for write authorization — accepting
// it would let a member of workspace A upsert presence rows for workspace B
// by setting a different slug in the JSON body.
func (h *Handler) ReportTerminalPresence(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if workspaceID == "" {
		writeError(w, http.StatusBadRequest, "workspace required")
		return
	}
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}
	var req upsertTerminalPresenceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Host == "" {
		writeError(w, http.StatusBadRequest, "host required")
		return
	}
	host := sanitizeTail(req.Host, 255)
	if strings.TrimSpace(host) == "" {
		writeError(w, http.StatusBadRequest, "valid host required")
		return
	}
	workspaceUUID := parseUUID(workspaceID)
	accepted := 0
	for _, p := range req.Sessions {
		if p.SessionName == "" || len(p.TailText) > 20000 {
			continue
		}
		sessionName := sanitizeTail(p.SessionName, 255)
		if strings.TrimSpace(sessionName) == "" {
			continue
		}
		rows, err := h.Queries.UpsertTerminalPresence(r.Context(), db.UpsertTerminalPresenceParams{
			WorkspaceID:    workspaceUUID,
			Host:           host,
			SessionName:    sessionName,
			WindowIndex:    int32(p.WindowIndex),
			PaneIndex:      int32(p.PaneIndex),
			PanePid:        int32(p.PanePID),
			CurrentCommand: sanitizeTail(p.CurrentCommand, 120),
			AgentHint:      sanitizeTail(p.AgentHint, 120),
			TailText:       sanitizeTail(p.TailText, 20000),
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to report terminal presence")
			return
		}
		if rows == 0 {
			writeError(w, http.StatusConflict, "terminal pane belongs to another workspace")
			return
		}
		accepted++
	}
	writeJSON(w, http.StatusOK, map[string]any{"panes": accepted})
}

// ListTerminalPresence GET /api/work-wall/terminal-presence — fresh panes only
// (heartbeat within 15 minutes), newest first.
func (h *Handler) ListTerminalPresence(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if workspaceID == "" {
		writeError(w, http.StatusBadRequest, "workspace required")
		return
	}
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}
	rows, err := h.Queries.ListFreshTerminalPresence(r.Context(), parseUUID(workspaceID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list terminal presence")
		return
	}
	out := make([]terminalPresenceDTO, 0, len(rows))
	for _, row := range rows {
		out = append(out, terminalPresenceDTO{
			Host: row.Host, SessionName: row.SessionName,
			WindowIndex: int(row.WindowIndex), PaneIndex: int(row.PaneIndex),
			CurrentCommand: row.CurrentCommand, AgentHint: row.AgentHint,
			TailText: row.TailText, HeartbeatAt: row.HeartbeatAt.Time.UTC().Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// terminalPresenceSecretPatterns matches obvious secret shapes that must never
// reach the database. Mirrors the collector-side patterns in
// scripts/terminal-presence-collector.py so the server is a true second-pass
// defense even if the collector is bypassed or misconfigured.
var terminalPresenceSecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\b(sk[-_][A-Za-z0-9_\-]{8,})`),
	regexp.MustCompile(`\b(gla[-_][A-Za-z0-9_\-]{8,})`),
	regexp.MustCompile(`\b((?:mul|mdt|mat)[-_][A-Za-z0-9_\-]{8,})`),
	regexp.MustCompile(`\b(AKIA[0-9A-Z]{16})`),
	regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._\-]{16,}`),
	regexp.MustCompile(`(?i)(password|passwd|secret|token)\s*[=:]\s*\S+`),
}

// sanitizeTail collapses control sequences and strips obvious secret shapes
// before storage. Defensive only — the collector sanitizes first; this is the
// server-side second pass.
func sanitizeTail(s string, max int) string {
	// Strip ANSI escape sequences.
	s = ansiEscapePattern.ReplaceAllString(s, "")
	// Drop control characters except newline and tab.
	var b strings.Builder
	for _, ch := range s {
		switch {
		case ch == '\n' || ch == '\t':
			b.WriteRune(ch)
		case ch < 0x20:
			// drop other control chars
		default:
			b.WriteRune(ch)
		}
	}
	out := b.String()
	// Redact obvious secret shapes.
	for _, pat := range terminalPresenceSecretPatterns {
		out = pat.ReplaceAllString(out, "[REDACTED]")
	}
	if len(out) > max {
		out = out[len(out)-max:]
	}
	return out
}

// ansiEscapePattern matches CSI/OSC ANSI escape sequences (e.g. tmux colour
// codes captured by pane tails).
var ansiEscapePattern = regexp.MustCompile(`\x1b(\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(\x07|\x1b\\))`)

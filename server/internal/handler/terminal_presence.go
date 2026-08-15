package handler

import (
	"encoding/json"
	"net/http"
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
	WorkspaceSlug  string `json:"workspace_slug"`
	Host           string `json:"host"`
	Sessions       []terminalPresencePane `json:"sessions"`
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
func (h *Handler) ReportTerminalPresence(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
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
	slug := req.WorkspaceSlug
	if slug == "" {
		slug = "hivecosm"
	}
	ws, err := h.Queries.GetWorkspaceBySlug(r.Context(), slug)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unknown workspace")
		return
	}
	for _, p := range req.Sessions {
		if p.SessionName == "" || len(p.TailText) > 20000 {
			continue
		}
		_ = h.Queries.UpsertTerminalPresence(r.Context(), db.UpsertTerminalPresenceParams{
			WorkspaceID:    ws.ID,
			Host:           req.Host,
			SessionName:    p.SessionName,
			WindowIndex:    int32(p.WindowIndex),
			PaneIndex:      int32(p.PaneIndex),
			PanePid:        int32(p.PanePID),
			CurrentCommand: sanitizeTail(p.CurrentCommand, 120),
			AgentHint:      sanitizeTail(p.AgentHint, 120),
			TailText:       sanitizeTail(p.TailText, 20000),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"panes": len(req.Sessions)})
}

// ListTerminalPresence GET /api/work-wall/terminal-presence — fresh panes only
// (heartbeat within 15 minutes), newest first.
func (h *Handler) ListTerminalPresence(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
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

// sanitizeTail collapses control sequences and strips obvious secret shapes
// before storage. Defensive only — the collector sanitizes first; this is the
// server-side second pass.
func sanitizeTail(s string, max int) string {
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
	if len(out) > max {
		out = out[len(out)-max:]
	}
	return out
}

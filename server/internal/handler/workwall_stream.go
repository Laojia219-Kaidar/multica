package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/liveactivity"
	"github.com/multica-ai/multica/server/internal/workwall"
)

const (
	// workWallStreamInterval is the default server-side poll cadence for the
	// workspace-wide SSE stream. One stream per workspace (NOT per employee).
	workWallStreamInterval = 5 * time.Second
	// workWallStreamMinInterval floors a per-request `?interval=` override so a
	// client cannot spin the stream into a hot loop. Tests use the override to
	// drive a controllable cadence.
	workWallStreamMinInterval = 100 * time.Millisecond
	// workWallStreamMaxInterval caps a per-request `?interval=` override so a
	// stale client cannot pin a workspace to stale snapshots forever.
	workWallStreamMaxInterval = 1 * time.Hour
)

// workWallStreamIntervalFor returns the SSE cadence for this request. A valid
// `?interval=` duration overrides the baseline, clamped to a safe range.
func workWallStreamIntervalFor(r *http.Request) time.Duration {
	if raw := r.URL.Query().Get("interval"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil {
			if d < workWallStreamMinInterval {
				d = workWallStreamMinInterval
			}
			if d > workWallStreamMaxInterval {
				d = workWallStreamMaxInterval
			}
			return d
		}
	}
	return workWallStreamInterval
}

// workWallSnapshotProvider is satisfied by *workwall.Service; the stream write
// path is exercised in tests with a stub so cadence/cancellation coverage does
// not depend on a live database.
type workWallSnapshotProvider interface {
	Snapshot(ctx context.Context, workspaceID pgtype.UUID) ([]liveactivity.EmployeeLiveActivityV1, error)
}

// GetWorkWallStream is the workspace-level SSE stream for the "工作现场"
// (work wall). It emits one access-filtered snapshot event as soon as the
// connection is established — no longer gated behind the first poll interval —
// and then one event every cadence until the client disconnects. Client
// reconnect is inherent (each connection starts a fresh loop); Last-Event-ID +
// delta compensation are left as a future enhancement over this v1 baseline.
//
// Route wiring (one line in router.go) is left to the mainline integrator.
func (h *Handler) GetWorkWallStream(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}
	_ = member

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	svc := workwall.NewService(h.Queries)

	// First frame right after the handshake, before the cadence ticker starts,
	// so clients see a workspace snapshot immediately instead of after a full
	// poll interval.
	if !h.writeWorkWallStreamFrame(w, flusher, r, svc, workspaceID, member.Role) {
		return
	}

	interval := workWallStreamIntervalFor(r)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if !h.writeWorkWallStreamFrame(w, flusher, r, svc, workspaceID, member.Role) {
				return
			}
		}
	}
}

// writeWorkWallStreamFrame resolves the caller's agent-access filter and emits
// one snapshot SSE event. Access-filter resolution is re-run per frame so a
// live connection picks up visibility changes while streaming (same discipline
// as the snapshot endpoint). It returns false when the request context is done,
// so the caller can terminate the stream.
func (h *Handler) writeWorkWallStreamFrame(w io.Writer, flusher http.Flusher, r *http.Request, svc workWallSnapshotProvider, workspaceID, role string) bool {
	actorType, actorID := h.resolveActor(r, requestUserID(r), workspaceID)
	allowed, ok := h.accessibleAgentIDs(r.Context(), workspaceID, actorType, actorID, role)
	if !ok {
		fmt.Fprintf(w, "event: error\ndata: %q\n\n", "failed to resolve agent access")
		flusher.Flush()
		return !requestContextDone(r)
	}
	return writeWorkWallSnapshotFrame(w, flusher, r, svc, workspaceID, allowed)
}

// writeWorkWallSnapshotFrame serialises one access-filtered snapshot to an SSE
// `event: snapshot` frame. Entries for agents outside the caller's `allowed`
// set are dropped (workspace access filter preserved). It returns false when
// the request context was cancelled while assembling or writing the frame.
func writeWorkWallSnapshotFrame(w io.Writer, flusher http.Flusher, r *http.Request, svc workWallSnapshotProvider, workspaceID string, allowed map[string]struct{}) bool {
	snapshot, err := svc.Snapshot(r.Context(), parseUUID(workspaceID))
	if err != nil {
		fmt.Fprintf(w, "event: error\ndata: %q\n\n", "failed to assemble snapshot")
		flusher.Flush()
		return !requestContextDone(r)
	}

	filtered := make([]json.RawMessage, 0, len(snapshot))
	for _, d := range snapshot {
		if _, visible := allowed[d.AgentID]; !visible {
			continue
		}
		b, err := json.Marshal(d)
		if err != nil {
			continue
		}
		filtered = append(filtered, b)
	}

	payload, err := json.Marshal(filtered)
	if err != nil {
		return !requestContextDone(r)
	}
	fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", payload)
	flusher.Flush()
	return !requestContextDone(r)
}

// requestContextDone reports whether the request context is already cancelled.
func requestContextDone(r *http.Request) bool {
	select {
	case <-r.Context().Done():
		return true
	default:
		return false
	}
}

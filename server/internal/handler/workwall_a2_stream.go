package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// A2 Work Wall SSE stream (GET /api/work-wall/a2/stream). Composes the
// snapshot assembly from workwall_a2.go into a server-sent-events loop:
// immediate full snapshot on connect AND on reconnect, a fixed retry hint,
// the stable cursor as the SSE event id, a keepalive comment while the pane
// content is unchanged, and a hard close when membership or visibility is
// revoked mid-stream. The v1 wall stream in workwall_stream.go is untouched.

const (
	// a2StreamInterval is the server-side poll cadence of the A2 stream,
	// matching the v1 wall stream.
	a2StreamInterval = 5 * time.Second

	// a2StreamRetryMs is the reconnect hint written to the client
	// (milliseconds, per the SSE spec's retry field).
	a2StreamRetryMs = 5000
)

// Fixed, redacted SSE error codes. They are stable wire identifiers only —
// no internal error text ever reaches the stream.
const (
	a2SSECodeSnapshotFailed    = "workwall_a2_snapshot_failed"
	a2SSECodeAccessRevoked     = "workwall_a2_access_revoked"
	a2SSECodeMembershipRevoked = "workwall_a2_membership_revoked"
)

// GetWorkWallA2Stream streams the A2 Work Wall over SSE. Every snapshot
// event carries the full filtered envelope with `id: <cursor>`; v1 has no
// deltas, so a reconnect — Last-Event-ID present or not — is always answered
// with a fresh full snapshot first, and an unchanged cursor afterwards
// yields only a keepalive comment per poll.
//
// Security: the connect-time membership gate mirrors the snapshot endpoint,
// and every poll re-resolves membership from the database plus agent
// visibility — a mid-stream revocation closes the connection.
func (h *Handler) GetWorkWallA2Stream(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}

	// The workspace identifier can arrive unvalidated from a header /
	// query; parse it with the error-returning validator before anything is
	// streamed (a 400 after the stream started would be useless).
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}

	// Strict Last-Event-ID: when present it must be exactly the cursor form
	// these endpoints emit (64 lowercase hex chars). v1 has no delta
	// compensation — the id is still validated so a malformed reconnect
	// cannot silently degrade into an unvalidated one.
	if id := r.Header.Get("Last-Event-ID"); id != "" && !isValidA2Cursor(id) {
		writeError(w, http.StatusBadRequest, "invalid last_event_id")
		return
	}

	eventLimit, ok := parseA2EventLimit(r.URL.Query().Get("event_limit"))
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid event_limit")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	writeA2SSERetry(w, a2StreamRetryMs)
	flusher.Flush()

	// lastCursor tracks the cursor actually written to this connection. It
	// deliberately starts empty even when the client presented a
	// Last-Event-ID: a reconnect always gets the full snapshot first.
	lastCursor := ""
	// lastVisible tracks the work_refs of the panes actually shown on this
	// connection (empty before the first poll). It is the baseline for the
	// per-poll access-revocation check: a pane that was visible before and
	// still projects but is now filtered out means the caller lost sight of
	// an employee it was watching — the stream must close, not shrink.
	var lastVisible map[string]struct{}

	// emit runs one poll: DB membership recheck, snapshot assembly with
	// visibility recheck, then the frame decision. It returns false only
	// when the connection must close (revocation).
	emit := func() bool {
		// Membership is re-resolved from the DB on EVERY poll: neither the
		// connect-time gate nor a middleware-injected member context may
		// mask a mid-stream revocation. Any failure here (row gone, user or
		// workspace id no longer valid) closes the stream.
		member, err := h.getWorkspaceMember(r.Context(), requestUserID(r), workspaceID)
		if err != nil {
			writeA2SSEError(w, a2SSECodeMembershipRevoked)
			return false
		}

		poll, err := h.buildA2SnapshotPoll(r.Context(), r, workspaceID, wsUUID, eventLimit, member)
		next, visible, keepOpen := emitA2StreamPoll(w, poll, err, lastCursor, lastVisible)
		lastCursor = next
		if keepOpen {
			lastVisible = visible
		}
		return keepOpen
	}

	if !emit() {
		flusher.Flush()
		return
	}
	flusher.Flush()

	ticker := time.NewTicker(a2StreamInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if !emit() {
				flusher.Flush()
				return
			}
			flusher.Flush()
		}
	}
}

// emitA2StreamPoll writes one poll's frames and reports the connection's
// next state. poll/err is the result from buildA2SnapshotPoll, lastCursor is
// the cursor last written to this connection and prevVisible the work_refs
// of the panes the connection showed on the previous poll (nil on connect).
// It returns the cursor to carry forward, the visible-work_ref baseline to
// carry forward (both unchanged on keepalive and on error) and whether the
// stream stays open. Decision table, in order:
//
//   - A previously visible pane still projecting but now filtered out is an
//     ACCESS REVOCATION (a2VisibilityRevocation): the terminal redacted
//     workwall_a2_access_revoked frame is written — never the shrunken
//     snapshot — and the stream closes.
//   - errA2Access (visibility could not even be resolved from the DB) is
//     treated identically: fail closed. The stream must not keep streaming
//     to a caller whose access cannot be proven.
//   - A transient assembly failure (errA2Assemble) is reported with the
//     redacted snapshot-failed code but keeps the stream open; nothing about
//     access changed.
//   - An unchanged cursor yields a keepalive comment instead of re-sending
//     the pane set; a changed cursor emits the full snapshot with its id.
func emitA2StreamPoll(w io.Writer, poll *a2SnapshotPoll, err error, lastCursor string, prevVisible map[string]struct{}) (string, map[string]struct{}, bool) {
	switch {
	case err == nil:
		if a2VisibilityRevocation(prevVisible, poll.rawPanes, poll.env.Panes) {
			writeA2SSEError(w, a2SSECodeAccessRevoked)
			return lastCursor, prevVisible, false
		}
		if poll.env.Cursor == lastCursor {
			writeA2SSEComment(w, "keepalive")
			return lastCursor, a2VisibleWorkRefs(poll.env.Panes), true
		}
		data, merr := json.Marshal(poll.env)
		if merr != nil {
			// The envelope holds no unmarshalable values; this branch is
			// defensive only. Report, do not close.
			writeA2SSEError(w, a2SSECodeSnapshotFailed)
			return lastCursor, prevVisible, true
		}
		writeA2SSEEvent(w, "snapshot", poll.env.Cursor, data)
		return poll.env.Cursor, a2VisibleWorkRefs(poll.env.Panes), true
	case errors.Is(err, errA2Access):
		writeA2SSEError(w, a2SSECodeAccessRevoked)
		return lastCursor, prevVisible, false
	default:
		writeA2SSEError(w, a2SSECodeSnapshotFailed)
		return lastCursor, prevVisible, true
	}
}

// writeA2SSERetry writes the SSE reconnect hint.
func writeA2SSERetry(w io.Writer, ms int) {
	fmt.Fprintf(w, "retry: %d\n\n", ms)
}

// writeA2SSEEvent writes one named event with its cursor id. data must be a
// single line (JSON marshal output is).
func writeA2SSEEvent(w io.Writer, event, id string, data []byte) {
	if id != "" {
		fmt.Fprintf(w, "id: %s\n", id)
	}
	fmt.Fprintf(w, "event: %s\n", event)
	fmt.Fprintf(w, "data: %s\n\n", data)
}

// writeA2SSEComment writes an SSE comment line — the canonical keepalive:
// it keeps intermediaries from timing the connection out without creating a
// MessageEvent on the client.
func writeA2SSEComment(w io.Writer, text string) {
	fmt.Fprintf(w, ": %s\n\n", text)
}

// writeA2SSEError writes an error frame with a fixed redacted code. The
// payload is one JSON object so clients parse a single error shape for both
// terminal and transient failures.
func writeA2SSEError(w io.Writer, code string) {
	data, _ := json.Marshal(map[string]string{"error": code})
	fmt.Fprintf(w, "event: error\ndata: %s\n\n", data)
}

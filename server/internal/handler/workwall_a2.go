package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/workwall"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// A2 Work Wall — read-only HTTP wiring for the A2 pane projection
// (workwall.Service.A2Snapshot). This file owns the plain snapshot endpoint
// plus the pure helpers (strict event_limit parsing, sha256 cursor,
// visibility filter, envelope stamping) that the SSE stream in
// workwall_a2_stream.go composes. The v1 work-wall endpoints are untouched.

// A2SnapshotSchemaV1 is the wire schema version of the A2 snapshot envelope.
const A2SnapshotSchemaV1 = "hivecrew.workwall.a2-snapshot.v1"

// a2DefaultEventLimit mirrors workwall's default ledger window: an absent
// event_limit means this value. Keeping the constant here lets the envelope
// echo the window that was actually used (the service applies the same
// default; the two must stay in sync).
const a2DefaultEventLimit = 200

// a2MaxEventLimit is the inclusive upper bound of event_limit. It mirrors
// workwall's hard cap; the endpoint rejects anything above it instead of
// silently clamping, so a caller can never mistake a clamped window for the
// one it asked for.
const a2MaxEventLimit = 1000

// Fixed, redacted assembly failures shared by the snapshot endpoint and the
// SSE stream. They never wrap or echo the underlying cause (SQL, driver or
// constraint text stays server-side); each endpoint maps them to its own
// fixed wire code.
var (
	errA2Assemble = errors.New("workwall a2 snapshot assembly failed")
	errA2Access   = errors.New("workwall a2 visibility resolution failed")
)

// A2SnapshotEnvelopeV1 is the envelope of GET /api/work-wall/a2/snapshot and
// of every `snapshot` event on GET /api/work-wall/a2/stream. Panes use the
// existing workwall.A2PaneV1 pane schema (hivecrew.workwall.a2-pane.v1).
type A2SnapshotEnvelopeV1 struct {
	SchemaVersion string `json:"schema_version"`
	WorkspaceID   string `json:"workspace_id"`
	// Cursor is the stable lowercase-hex sha256 digest of the pane set (see
	// a2SnapshotCursor). It doubles as the SSE event id: identical pane
	// content always yields the identical cursor, which is what lets the
	// stream answer an unchanged wall with a keepalive instead of a re-send.
	Cursor     string              `json:"cursor"`
	ObservedAt time.Time           `json:"observed_at"`
	EventLimit int32               `json:"event_limit"`
	Panes      []workwall.A2PaneV1 `json:"panes"`
}

// GetWorkWallA2Snapshot returns the A2 Work Wall snapshot: one
// hivecrew.workwall.a2-pane.v1 pane per visible, employee-owned work_ref in
// the newest window of the work_event ledger. Strictly read-only; the v1
// work-wall endpoints are unchanged. Access discipline mirrors
// GetWorkWallSnapshot: workspace membership first, then actor-resolved agent
// visibility, then the pane filter.
func (h *Handler) GetWorkWallA2Snapshot(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}

	// The workspace identifier can arrive unvalidated from the
	// X-Workspace-ID header / workspace_id query, so it is parsed with the
	// error-returning validator — never the panicking wrapper.
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}

	eventLimit, ok := parseA2EventLimit(r.URL.Query().Get("event_limit"))
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid event_limit")
		return
	}

	env, err := h.buildA2SnapshotEnvelope(r.Context(), r, workspaceID, wsUUID, eventLimit, member)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, env)
	case errors.Is(err, errA2Access):
		writeError(w, http.StatusInternalServerError, "failed to resolve agent access")
	default:
		writeError(w, http.StatusInternalServerError, "failed to assemble work wall a2 snapshot")
	}
}

// buildA2SnapshotEnvelope assembles the visibility-filtered envelope for one
// caller: A2 projection pane set -> agent-access filter -> envelope with the
// stable cursor. A failure returns one of the sentinel errors above, never
// the raw cause.
func (h *Handler) buildA2SnapshotEnvelope(ctx context.Context, r *http.Request, workspaceID string, wsUUID pgtype.UUID, eventLimit int32, member db.Member) (*A2SnapshotEnvelopeV1, error) {
	panes, err := workwall.NewService(h.Queries).A2Snapshot(ctx, wsUUID, eventLimit)
	if err != nil {
		return nil, errA2Assemble
	}

	// Same access discipline as the v1 wall snapshot: only employees
	// (agents) the caller may see.
	actorType, actorID := h.resolveActor(r, requestUserID(r), workspaceID)
	allowed, ok := h.accessibleAgentIDs(ctx, workspaceID, actorType, actorID, member.Role)
	if !ok {
		return nil, errA2Access
	}

	return newA2SnapshotEnvelope(workspaceID, eventLimit, filterA2VisiblePanes(panes, allowed), time.Now().UTC()), nil
}

// newA2SnapshotEnvelope stamps the envelope around an already
// visibility-filtered pane set. Pure: no I/O, no clock reads.
func newA2SnapshotEnvelope(workspaceID string, eventLimit int32, panes []workwall.A2PaneV1, observedAt time.Time) *A2SnapshotEnvelopeV1 {
	return &A2SnapshotEnvelopeV1{
		SchemaVersion: A2SnapshotSchemaV1,
		WorkspaceID:   workspaceID,
		Cursor:        a2SnapshotCursor(workspaceID, panes),
		ObservedAt:    observedAt,
		EventLimit:    eventLimit,
		Panes:         panes,
	}
}

// filterA2VisiblePanes keeps only panes whose employee ownership resolved to
// a non-empty employee_id that is visible to this caller. A pane without
// employee ownership can never be attributed to a visible employee, so it
// never leaves the server; a pane owned by a hidden / private employee is
// likewise dropped. Fail closed: an empty allowed set keeps nothing.
func filterA2VisiblePanes(panes []workwall.A2PaneV1, allowed map[string]struct{}) []workwall.A2PaneV1 {
	out := make([]workwall.A2PaneV1, 0, len(panes))
	for _, p := range panes {
		if p.EmployeeID == "" {
			continue
		}
		if _, visible := allowed[p.EmployeeID]; !visible {
			continue
		}
		out = append(out, p)
	}
	return out
}

// a2SnapshotCursor derives the stable cursor of one filtered pane set: the
// lowercase-hex sha256 digest over the envelope schema id, the workspace id
// and the pane JSON with each pane's observed_at stamp zeroed. observed_at
// is the only field the projection stamps from wall-clock Now at assembly
// time; zeroing it makes the cursor a function of pane CONTENT only, so two
// snapshots of an unchanged wall share one cursor — exactly the property the
// SSE stream needs to send a keepalive instead of re-sending panes. Any
// semantic pane change (state flip, new ledger event, ownership change, a
// freshness transition) changes the cursor.
func a2SnapshotCursor(workspaceID string, panes []workwall.A2PaneV1) string {
	stripped := make([]workwall.A2PaneV1, len(panes))
	copy(stripped, panes)
	for i := range stripped {
		stripped[i].ObservedAt = time.Time{}
	}
	sum := sha256.New()
	sum.Write([]byte(A2SnapshotSchemaV1))
	sum.Write([]byte{0})
	sum.Write([]byte(workspaceID))
	sum.Write([]byte{0})
	_ = json.NewEncoder(sum).Encode(stripped) // sha256 writes cannot fail
	return hex.EncodeToString(sum.Sum(nil))
}

// parseA2EventLimit parses the strict event_limit query parameter. Absent or
// empty means the default ledger window. Any provided value must be pure
// ASCII decimal digits denoting an integer in [1, a2MaxEventLimit]: signs,
// whitespace, exponent / hex / float forms and non-ASCII digit lookalikes
// are rejected, as are 0 and anything above the cap. Strict parsing keeps
// the ledger window from being negotiated by surprise — a clamped or
// float-rounded limit would silently answer a different question than the
// caller asked.
func parseA2EventLimit(raw string) (int32, bool) {
	if raw == "" {
		return a2DefaultEventLimit, true
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] < '0' || raw[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 1 || n > a2MaxEventLimit {
		return 0, false
	}
	return int32(n), true
}

// isValidA2Cursor reports whether s is exactly the id form these endpoints
// emit: 64 characters of lowercase hexadecimal (a sha256 digest). Used to
// strictly validate a reconnecting client's Last-Event-ID.
func isValidA2Cursor(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

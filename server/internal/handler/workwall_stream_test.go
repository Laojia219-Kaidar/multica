package handler

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/liveactivity"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// stubWorkWallProvider is a deterministic workWallSnapshotProvider usable
// without a database. When blocked is set, Snapshot waits for either the
// context or the released channel, modelling a slow/cancelled snapshot query.
type stubWorkWallProvider struct {
	snap     []liveactivity.EmployeeLiveActivityV1
	err      error
	blocked  chan struct{}
	released chan struct{}
}

func (s *stubWorkWallProvider) Snapshot(ctx context.Context, _ pgtype.UUID) ([]liveactivity.EmployeeLiveActivityV1, error) {
	if s.blocked != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-s.released:
		}
	}
	if s.err != nil {
		return nil, s.err
	}
	return s.snap, nil
}

func snapshotActivity(agentID, displayName string) liveactivity.EmployeeLiveActivityV1 {
	return liveactivity.EmployeeLiveActivityV1{
		SchemaVersion: "v1",
		AgentID:       agentID,
		DisplayName:   displayName,
	}
}

// sseFrame is one parsed SSE event block (event name + concatenated data lines).
type sseFrame struct {
	event string
	data  string
}

// readSSEFrame reads one SSE event block (terminated by a blank line).
func readSSEFrame(br *bufio.Reader) (sseFrame, error) {
	var f sseFrame
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return f, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			return f, nil
		}
		if strings.HasPrefix(line, "event: ") {
			f.event = strings.TrimPrefix(line, "event: ")
		} else if strings.HasPrefix(line, "data: ") {
			if f.data != "" {
				f.data += "\n"
			}
			f.data += strings.TrimPrefix(line, "data: ")
		}
	}
}

// TestWorkWallStreamIntervalFor covers the controllable-cadence knob: default,
// override, floor clamp, cap clamp, and invalid input fallback.
func TestWorkWallStreamIntervalFor(t *testing.T) {
	mkReq := func(q string) *http.Request {
		return httptest.NewRequest(http.MethodGet, "http://example.test/api/work-wall/stream?"+q, nil)
	}

	if got := workWallStreamIntervalFor(mkReq("")); got != workWallStreamInterval {
		t.Fatalf("default interval = %v, want %v", got, workWallStreamInterval)
	}
	if got := workWallStreamIntervalFor(mkReq("interval=150ms")); got != 150*time.Millisecond {
		t.Fatalf("override interval = %v, want 150ms", got)
	}
	if got := workWallStreamIntervalFor(mkReq("interval=1ns")); got != workWallStreamMinInterval {
		t.Fatalf("under-floor interval = %v, want floor %v", got, workWallStreamMinInterval)
	}
	if got := workWallStreamIntervalFor(mkReq("interval=1000h")); got != workWallStreamMaxInterval {
		t.Fatalf("over-cap interval = %v, want cap %v", got, workWallStreamMaxInterval)
	}
	if got := workWallStreamIntervalFor(mkReq("interval=not-a-duration")); got != workWallStreamInterval {
		t.Fatalf("invalid interval = %v, want default %v", got, workWallStreamInterval)
	}
}

// TestWriteWorkWallSnapshotFrame_ImmediateAndFiltered verifies one write call
// synchronously emits exactly one access-filtered snapshot frame (the "first
// frame" path used right after the SSE handshake) and that agents outside the
// caller's allowed set are dropped.
func TestWriteWorkWallSnapshotFrame_ImmediateAndFiltered(t *testing.T) {
	prov := &stubWorkWallProvider{
		snap: []liveactivity.EmployeeLiveActivityV1{
			snapshotActivity("11111111-1111-1111-1111-111111111111", "Visible Agent"),
			snapshotActivity("22222222-2222-2222-2222-222222222222", "Restricted Agent"),
		},
	}
	allowed := map[string]struct{}{"11111111-1111-1111-1111-111111111111": {}}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "http://example.test/api/work-wall/stream", nil)

	if cont := writeWorkWallSnapshotFrame(w, w, r, prov, "99999999-9999-9999-9999-999999999999", allowed); !cont {
		t.Fatalf("writeWorkWallSnapshotFrame returned continue=false with active context; body=%q", w.Body.String())
	}

	br := bufio.NewReader(strings.NewReader(w.Body.String()))
	frame, err := readSSEFrame(br)
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	if frame.event != "snapshot" {
		t.Fatalf("event = %q, want snapshot", frame.event)
	}
	var got []liveactivity.EmployeeLiveActivityV1
	if err := json.Unmarshal([]byte(frame.data), &got); err != nil {
		t.Fatalf("unmarshal snapshot data %q: %v", frame.data, err)
	}
	if len(got) != 1 || got[0].AgentID != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("filtered snapshot = %+v, want only the allowed visible agent", got)
	}

	// Nothing else follows the frame.
	if _, err := readSSEFrame(br); err != io.EOF {
		t.Fatalf("expected EOF after one frame, got %v", err)
	}
}

// TestWriteWorkWallSnapshotFrame_ProviderError verifies a snapshot-assembly
// failure surfaces as one `event: error` frame instead of dropping the stream.
func TestWriteWorkWallSnapshotFrame_ProviderError(t *testing.T) {
	prov := &stubWorkWallProvider{err: errors.New("boom")}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "http://example.test/api/work-wall/stream", nil)

	if cont := writeWorkWallSnapshotFrame(w, w, r, prov, "99999999-9999-9999-9999-999999999999", map[string]struct{}{}); !cont {
		t.Fatalf("continue=false with active context after provider error")
	}
	br := bufio.NewReader(strings.NewReader(w.Body.String()))
	frame, err := readSSEFrame(br)
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	if frame.event != "error" {
		t.Fatalf("event = %q, want error", frame.event)
	}
	if !strings.Contains(frame.data, "failed to assemble snapshot") {
		t.Fatalf("error data = %q, want failure message", frame.data)
	}
}

// TestWriteWorkWallSnapshotFrame_ContextCancelled verifies that a client
// disconnect during snapshot assembly terminates the frame write (simulating
// the context-cancellation path of the SSE loop).
func TestWriteWorkWallSnapshotFrame_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	prov := &stubWorkWallProvider{blocked: make(chan struct{}), released: make(chan struct{})}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "http://example.test/api/work-wall/stream", nil).WithContext(ctx)

	done := make(chan bool, 1)
	go func() {
		done <- writeWorkWallSnapshotFrame(w, w, r, prov, "99999999-9999-9999-9999-999999999999", map[string]struct{}{})
	}()

	select {
	case <-time.After(100 * time.Millisecond):
	case <-done:
		t.Fatalf("frame write returned before the provider was released")
	}

	cancel()
	select {
	case cont := <-done:
		if cont {
			t.Fatalf("frame write reported continue=true after context cancellation")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("frame write did not return after context cancellation")
	}
	// An error frame may or may not have been flushed, but the call must not
	// panic or spin.
	_ = w.Body.String()
}

// workWallStreamTestServer mounts GetWorkWallStream on an httptest server with
// a member context already injected, mirroring the router wiring.
func workWallStreamTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	memberRow, err := testHandler.Queries.GetMemberByUserAndWorkspace(context.Background(), db.GetMemberByUserAndWorkspaceParams{
		UserID:      util.MustParseUUID(testUserID),
		WorkspaceID: util.MustParseUUID(testWorkspaceID),
	})
	if err != nil {
		t.Fatalf("load owner member row: %v", err)
	}

	rt := chi.NewRouter()
	rt.Get("/api/work-wall/stream", func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(middleware.SetMemberContext(r.Context(), testWorkspaceID, memberRow))
		testHandler.GetWorkWallStream(w, r)
	})
	srv := httptest.NewServer(rt)
	t.Cleanup(srv.Close)
	return srv
}

// TestGetWorkWallStream_EmitsImmediately is the core regression test for the
// issue: with a 3s cadence the first snapshot must arrive well before the
// cadence elapses, proving the stream no longer waits for the first tick.
func TestGetWorkWallStream_EmitsImmediately(t *testing.T) {
	srv := workWallStreamTestServer(t)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/api/work-wall/stream?interval=3s", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("X-User-ID", testUserID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	start := time.Now()
	br := bufio.NewReader(resp.Body)
	frame, err := readSSEFrame(br)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("read first frame: %v", err)
	}
	if frame.event != "snapshot" {
		t.Fatalf("first event = %q, want snapshot (data=%q)", frame.event, frame.data)
	}
	if elapsed >= 1500*time.Millisecond {
		t.Fatalf("first snapshot arrived after %v with a 3s cadence — first frame is still gated behind the ticker", elapsed)
	}
}

// TestGetWorkWallStream_ControllableCadence verifies that subsequent snapshots
// follow the requested cadence (interval=150ms) rather than firing as fast as
// possible or hanging on the default interval.
func TestGetWorkWallStream_ControllableCadence(t *testing.T) {
	srv := workWallStreamTestServer(t)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/api/work-wall/stream?interval=150ms", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("X-User-ID", testUserID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()

	br := bufio.NewReader(resp.Body)
	const wantFrames = 4
	arrivals := make([]time.Time, 0, wantFrames)
	deadline := time.After(5 * time.Second)
	for len(arrivals) < wantFrames {
		select {
		case <-deadline:
			t.Fatalf("only got %d frames before timeout", len(arrivals))
		default:
		}
		frame, err := readSSEFrame(br)
		if err != nil {
			t.Fatalf("read frame %d: %v", len(arrivals)+1, err)
		}
		if frame.event != "snapshot" {
			t.Fatalf("event %d = %q, want snapshot", len(arrivals)+1, frame.event)
		}
		arrivals = append(arrivals, time.Now())
	}

	for i := 1; i < len(arrivals); i++ {
		gap := arrivals[i].Sub(arrivals[i-1])
		if gap < 120*time.Millisecond {
			t.Fatalf("frame gap %d = %v, shorter than the 150ms requested cadence", i, gap)
		}
		if gap > time.Second {
			t.Fatalf("frame gap %d = %v, much longer than the 150ms requested cadence", i, gap)
		}
	}
	_ = srv
}

// TestGetWorkWallStream_ContextCancellationClosesStream verifies that client
// disconnect (request context cancellation) terminates the stream: after the
// first frame, cancelling the request must close the body with no further
// snapshot events.
func TestGetWorkWallStream_ContextCancellationClosesStream(t *testing.T) {
	srv := workWallStreamTestServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/work-wall/stream", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("X-User-ID", testUserID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}

	br := bufio.NewReader(resp.Body)
	first, err := readSSEFrame(br)
	if err != nil {
		t.Fatalf("read first frame: %v", err)
	}
	if first.event != "snapshot" {
		t.Fatalf("first event = %q, want snapshot", first.event)
	}

	cancel()

	// The stream must terminate promptly: reading further must hit EOF (or a
	// transport error) without producing another snapshot event.
	readErrCh := make(chan error, 1)
	go func() {
		for {
			f, err := readSSEFrame(br)
			if err != nil {
				readErrCh <- err
				return
			}
			if f.event == "snapshot" {
				readErrCh <- errors.New("received a snapshot event after cancellation")
				return
			}
		}
	}()

	select {
	case err := <-readErrCh:
		// context.Canceled surfaces when the client's own cancellation aborts
		// the read; EOF / connection-reset are the server-side close signals.
		// All of them mean the stream ended, not that a snapshot leaked past
		// cancellation.
		if err == nil {
			t.Fatalf("read returned nil error, expected the stream to end")
		}
		if errors.Is(err, context.Canceled) {
			break
		}
		if err != io.EOF {
			if !strings.Contains(err.Error(), "EOF") &&
				!strings.Contains(err.Error(), "reset") &&
				!strings.Contains(err.Error(), "closed") {
				t.Fatalf("stream did not close cleanly after cancellation: %v", err)
			}
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("stream did not close within 3s after context cancellation")
	}
}

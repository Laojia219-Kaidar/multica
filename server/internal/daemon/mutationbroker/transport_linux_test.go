//go:build linux

package mutationbroker

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func preparedEndpoint(t *testing.T) (*Endpoint, PreparedRunner) {
	t.Helper()
	e := NewEndpoint()
	p, err := e.PrepareRunner()
	if err != nil {
		t.Fatal(err)
	}
	start, err := procStartTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err := e.BindPreparedRunner(p.Generation, os.Getpid(), start); err != nil {
		t.Fatal(err)
	}
	return e, p
}

func dialClient(t *testing.T, p PreparedRunner) *net.UnixConn {
	t.Helper()
	conn, err := Dial(context.Background(), p.Locator)
	if err != nil {
		t.Fatal(err)
	}
	u, ok := conn.(*net.UnixConn)
	if !ok {
		_ = conn.Close()
		t.Fatal("not UnixConn")
	}
	t.Cleanup(func() { _ = u.Close() })
	return u
}

func writeReq(t *testing.T, conn *net.UnixConn, req Request) {
	t.Helper()
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	oob := unix.UnixCredentials(&unix.Ucred{Pid: int32(os.Getpid()), Uid: uint32(os.Getuid()), Gid: uint32(os.Getgid())})
	if n, oobn, err := conn.WriteMsgUnix(b, oob, nil); err != nil || n != len(b) || oobn != len(oob) {
		t.Fatalf("write: n=%d err=%v", n, err)
	}
}

func expectClientClosed(t *testing.T, conn *net.UnixConn) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	if _, _, _, _, err := conn.ReadMsgUnix(buf, nil); err == nil {
		t.Fatal("client connection remained open")
	}
}

func readResp(t *testing.T, conn *net.UnixConn) Response {
	t.Helper()
	b := make([]byte, MaxFrameBytes+1)
	n, _, _, _, err := conn.ReadMsgUnix(b, nil)
	if err != nil {
		t.Fatal(err)
	}
	var resp Response
	if err := json.Unmarshal(b[:n], &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func serveTest(e *Endpoint, p PreparedRunner, h Handler) (context.CancelFunc, <-chan error) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan error, 1)
	go func() { ch <- e.Serve(p.Generation, ctx, h) }()
	return cancel, ch
}

func waitServing(t *testing.T, e *Endpoint, generation uint64) {
	t.Helper()
	state := e.state.(*linuxEndpoint)
	deadline := time.Now().Add(2 * time.Second)
	for {
		state.mu.Lock()
		serving := state.serving && state.generation == generation
		state.mu.Unlock()
		if serving {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("Serve did not enter serving state")
		}
		runtime.Gosched()
	}
}

func sendHello(t *testing.T, conn *net.UnixConn, id string) {
	t.Helper()
	writeReq(t, conn, Request{Version: ProtocolVersion, Kind: HelloKind, RequestID: id})
	if resp := readResp(t, conn); !resp.OK {
		t.Fatalf("hello: %+v", resp)
	}
}

func waitErr(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("Serve timeout")
		return nil
	}
}

func TestIndependentClientsDoNotCrossRoute(t *testing.T) {
	e, p := preparedEndpoint(t)
	var mu sync.Mutex
	seen := map[string]bool{}
	cancel, serveCh := serveTest(e, p, func(_ context.Context, req Request) (json.RawMessage, error) {
		mu.Lock()
		seen[req.RequestID] = true
		mu.Unlock()
		return json.RawMessage(`{"request":"` + req.RequestID + `"}`), nil
	})
	a, b := dialClient(t, p), dialClient(t, p)
	sendHello(t, a, "a-h")
	sendHello(t, b, "b-h")
	writeReq(t, a, Request{Version: ProtocolVersion, Kind: RequestKind, RequestID: "a-1", Operation: "checkout"})
	writeReq(t, b, Request{Version: ProtocolVersion, Kind: RequestKind, RequestID: "b-1", Operation: "checkout"})
	if got := readResp(t, a); got.RequestID != "a-1" {
		t.Fatalf("client A received %+v", got)
	}
	if got := readResp(t, b); got.RequestID != "b-1" {
		t.Fatalf("client B received %+v", got)
	}
	mu.Lock()
	if !seen["a-1"] || !seen["b-1"] {
		t.Fatalf("seen=%v", seen)
	}
	mu.Unlock()
	cancel()
	_ = e.Close()
	_ = waitErr(t, serveCh)
}

func TestOneClientEOFDoesNotRevokeOther(t *testing.T) {
	e, p := preparedEndpoint(t)
	entered := make(chan struct{})
	canceled := make(chan struct{})
	cancel, serveCh := serveTest(e, p, func(ctx context.Context, req Request) (json.RawMessage, error) {
		if req.RequestID == "a-1" {
			close(entered)
			<-ctx.Done()
			close(canceled)
		}
		return json.RawMessage(`{"id":"` + req.RequestID + `"}`), nil
	})
	a, b := dialClient(t, p), dialClient(t, p)
	sendHello(t, a, "a-h")
	sendHello(t, b, "b-h")
	writeReq(t, a, Request{Version: ProtocolVersion, Kind: RequestKind, RequestID: "a-1", Operation: "blocking"})
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("blocking handler did not start")
	}
	_ = a.Close()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("EOF did not cancel in-flight handler")
	}
	writeReq(t, b, Request{Version: ProtocolVersion, Kind: RequestKind, RequestID: "b-1", Operation: "checkout"})
	if got := readResp(t, b); got.RequestID != "b-1" || !got.OK {
		t.Fatalf("B after A EOF: %+v", got)
	}
	cancel()
	_ = e.Close()
	_ = waitErr(t, serveCh)
}

func TestBindBeforeDial(t *testing.T) {
	e := NewEndpoint()
	p, err := e.PrepareRunner()
	if err != nil {
		t.Fatal(err)
	}
	if conn, err := Dial(context.Background(), p.Locator); err == nil {
		_ = conn.Close()
		t.Fatal("dial before Bind unexpectedly succeeded")
	}
	start, _ := procStartTime(os.Getpid())
	if err := e.BindPreparedRunner(p.Generation, os.Getpid(), start); err != nil {
		t.Fatal(err)
	}
	conn, err := Dial(context.Background(), p.Locator)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	called := make(chan struct{}, 1)
	cancel, serveCh := serveTest(e, p, func(context.Context, Request) (json.RawMessage, error) { called <- struct{}{}; return nil, nil })
	writeReq(t, conn.(*net.UnixConn), Request{Version: ProtocolVersion, Kind: RequestKind, RequestID: "before", Operation: "x"})
	select {
	case <-called:
		t.Fatal("request before hello reached handler")
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	_ = e.Close()
	_ = waitErr(t, serveCh)
}

func TestPrepareEntropyFailureFailsClosed(t *testing.T) {
	old := randRead
	randRead = func([]byte) (int, error) { return 0, errors.New("entropy unavailable") }
	t.Cleanup(func() { randRead = old })
	e := NewEndpoint()
	if _, err := e.PrepareRunner(); !errors.Is(err, ErrEntropyUnavailable) {
		t.Fatalf("PrepareRunner error=%v, want entropy failure", err)
	}
}

func TestRotateInvalidatesOldLocator(t *testing.T) {
	e, p1 := preparedEndpoint(t)
	old := dialClient(t, p1)
	p2, err := e.PrepareRunner()
	if err != nil {
		t.Fatal(err)
	}
	if p1.Locator == p2.Locator || p1.Generation == p2.Generation {
		t.Fatalf("rotation reused locator/generation: %#v %#v", p1, p2)
	}
	if _, err := Dial(context.Background(), p1.Locator); err == nil {
		t.Fatal("old abstract locator still dialable")
	}
	_ = old.Close()
	_ = e.Close()
}

func TestStrictHelloAndStableErrors(t *testing.T) {
	e, p := preparedEndpoint(t)
	cancel, serveCh := serveTest(e, p, func(context.Context, Request) (json.RawMessage, error) { return nil, errors.New("secret-error") })
	conn := dialClient(t, p)
	writeReq(t, conn, Request{Version: ProtocolVersion, Kind: HelloKind, RequestID: "h", Operation: "bad"})
	expectClientClosed(t, conn)
	cancel()
	_ = e.Close()
	_ = waitErr(t, serveCh)

	e, p = preparedEndpoint(t)
	cancel, serveCh = serveTest(e, p, func(context.Context, Request) (json.RawMessage, error) { return nil, errors.New("secret-error") })
	conn = dialClient(t, p)
	sendHello(t, conn, "h")
	writeReq(t, conn, Request{Version: ProtocolVersion, Kind: RequestKind, RequestID: "r", Operation: "x"})
	resp := readResp(t, conn)
	if resp.Error != operationFailedCode || strings.Contains(resp.Error, "secret") {
		t.Fatalf("leaked error: %+v", resp)
	}
	cancel()
	_ = e.Close()
	_ = waitErr(t, serveCh)
}

func TestRightsAndOversizeFailClosed(t *testing.T) {
	e, p := preparedEndpoint(t)
	cancel, serveCh := serveTest(e, p, func(context.Context, Request) (json.RawMessage, error) {
		return nil, errors.New("handler must not receive SCM_RIGHTS")
	})
	conn := dialClient(t, p)
	fd1, _ := unix.Dup(1)
	fd2, _ := unix.Dup(1)
	b, _ := json.Marshal(Request{Version: ProtocolVersion, Kind: HelloKind, RequestID: "rights"})
	cred := unix.UnixCredentials(&unix.Ucred{Pid: int32(os.Getpid()), Uid: uint32(os.Getuid()), Gid: uint32(os.Getgid())})
	oob := append(append([]byte{}, cred...), unix.UnixRights(fd1)...)
	oob = append(oob, unix.UnixRights(fd2)...)
	if _, _, err := conn.WriteMsgUnix(b, oob, nil); err != nil {
		t.Fatal(err)
	}
	_ = unix.Close(fd1)
	_ = unix.Close(fd2)
	expectClientClosed(t, conn)
	cancel()
	_ = e.Close()
	_ = waitErr(t, serveCh)

	e, p = preparedEndpoint(t)
	cancel, serveCh = serveTest(e, p, func(context.Context, Request) (json.RawMessage, error) { return nil, nil })
	conn = dialClient(t, p)
	sendHello(t, conn, "h")
	oversize := make([]byte, MaxFrameBytes+1)
	if _, _, err := conn.WriteMsgUnix(oversize, unix.UnixCredentials(&unix.Ucred{Pid: int32(os.Getpid()), Uid: uint32(os.Getuid()), Gid: uint32(os.Getgid())}), nil); err != nil {
		t.Fatal(err)
	}
	expectClientClosed(t, conn)
	cancel()
	_ = e.Close()
	_ = waitErr(t, serveCh)
}

func TestSecondServeRejected(t *testing.T) {
	e, p := preparedEndpoint(t)
	cancel, serveCh := serveTest(e, p, func(context.Context, Request) (json.RawMessage, error) { return nil, nil })
	waitServing(t, e, p.Generation)
	if err := e.Serve(p.Generation, context.Background(), func(context.Context, Request) (json.RawMessage, error) { return nil, nil }); !errors.Is(err, ErrProtocol) {
		t.Fatalf("second Serve error=%v, want %v", err, ErrProtocol)
	}
	cancel()
	_ = e.Close()
	_ = waitErr(t, serveCh)
}

func TestRotationAdmissionBarrierDropsOldRequest(t *testing.T) {
	e, p1 := preparedEndpoint(t)
	state := e.state.(*linuxEndpoint)
	reached := make(chan struct{})
	release := make(chan struct{})
	state.dispatchBarrier = func() {
		close(reached)
		<-release
	}
	var callbacks atomic.Int32
	cancel1, serve1 := serveTest(e, p1, func(context.Context, Request) (json.RawMessage, error) {
		callbacks.Add(1)
		return json.RawMessage(`{"old":true}`), nil
	})
	old := dialClient(t, p1)
	sendHello(t, old, "old-h")
	writeReq(t, old, Request{Version: ProtocolVersion, Kind: RequestKind, RequestID: "old-r", Operation: "checkout"})
	select {
	case <-reached:
	case <-time.After(2 * time.Second):
		t.Fatal("request did not reach deterministic admission barrier")
	}

	preparedCh := make(chan struct {
		p   PreparedRunner
		err error
	}, 1)
	go func() {
		p, err := e.PrepareRunner()
		preparedCh <- struct {
			p   PreparedRunner
			err error
		}{p, err}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		state.mu.Lock()
		rotating := state.rotating
		state.mu.Unlock()
		if rotating {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("rotation did not enter rotating state")
		}
		runtime.Gosched()
	}
	close(release)
	rotated := <-preparedCh
	if rotated.err != nil {
		t.Fatal(rotated.err)
	}
	state.dispatchBarrier = nil
	if got := callbacks.Load(); got != 0 {
		t.Fatalf("old request callback count=%d, want zero", got)
	}
	state.mu.Lock()
	oldActive, oldClients := state.active, len(state.clients)
	state.mu.Unlock()
	if oldActive != 0 || oldClients != 0 {
		t.Fatalf("old generation state active=%d clients=%d", oldActive, oldClients)
	}
	cancel1()
	_ = waitErr(t, serve1)

	start, err := procStartTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err := e.BindPreparedRunner(rotated.p.Generation, os.Getpid(), start); err != nil {
		t.Fatal(err)
	}
	cancel2, serve2 := serveTest(e, rotated.p, func(context.Context, Request) (json.RawMessage, error) {
		return json.RawMessage(`{"new":true}`), nil
	})
	newClient := dialClient(t, rotated.p)
	sendHello(t, newClient, "new-h")
	writeReq(t, newClient, Request{Version: ProtocolVersion, Kind: RequestKind, RequestID: "new-r", Operation: "checkout"})
	if resp := readResp(t, newClient); !resp.OK || resp.RequestID != "new-r" {
		t.Fatalf("new generation request failed: %+v", resp)
	}
	cancel2()
	_ = e.Close()
	_ = waitErr(t, serve2)
}

func TestMutationBrokerSiblingHelper(t *testing.T) {
	mode := os.Getenv("MULTICA_MUTATIONBROKER_HELPER")
	if mode == "" {
		return
	}
	locator := os.Getenv("MULTICA_MUTATIONBROKER_LOCATOR")
	if locator == "" {
		t.Fatal("missing helper locator")
	}
	marker := os.Getenv("MULTICA_MUTATIONBROKER_MARKER")
	if marker == "" {
		t.Fatal("missing helper marker")
	}
	if err := os.WriteFile(marker, []byte("started"), 0600); err != nil {
		t.Fatal(err)
	}
	if mode != "runner" && mode != "sibling" {
		t.Fatalf("unknown helper mode %q", mode)
	}
	goFile := os.Getenv("MULTICA_MUTATIONBROKER_GO")
	deadline := time.Now().Add(5 * time.Second)
	if mode == "runner" {
		for {
			if _, err := os.Stat(goFile); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("runner release marker timeout")
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	var conn net.Conn
	var err error
	for conn == nil && time.Now().Before(deadline) {
		conn, err = Dial(context.Background(), locator)
		if conn == nil {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if err != nil && conn == nil {
		t.Fatal(err)
	}
	uconn := conn.(*net.UnixConn)
	defer uconn.Close()
	if mode == "sibling" {
		_ = uconn.SetReadDeadline(time.Now().Add(2 * time.Second))
		buf := make([]byte, 1)
		if _, _, _, _, readErr := uconn.ReadMsgUnix(buf, nil); readErr == nil {
			t.Fatal("sibling connection was not rejected by accept preflight")
		} else {
			var netErr net.Error
			if errors.As(readErr, &netErr) && netErr.Timeout() {
				t.Fatalf("sibling connection read timed out before server rejection: %v", readErr)
			}
		}
		if err := os.WriteFile(marker, []byte("rejected"), 0600); err != nil {
			t.Fatal(err)
		}
		releaseFile := os.Getenv("MULTICA_MUTATIONBROKER_SIBLING_RELEASE")
		releaseDeadline := time.Now().Add(10 * time.Second)
		for {
			if _, err := os.Stat(releaseFile); err == nil {
				return
			}
			if time.Now().After(releaseDeadline) {
				t.Fatal("sibling release marker timeout")
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	writeReq(t, uconn, Request{Version: ProtocolVersion, Kind: HelloKind, RequestID: "runner-h"})
	if resp := readResp(t, uconn); !resp.OK {
		t.Fatalf("runner hello: %+v", resp)
	}
	writeReq(t, uconn, Request{Version: ProtocolVersion, Kind: RequestKind, RequestID: "runner-r", Operation: "checkout"})
	if resp := readResp(t, uconn); !resp.OK || resp.RequestID != "runner-r" {
		t.Fatalf("runner request: %+v", resp)
	}
	if err := os.WriteFile(marker, []byte("success"), 0600); err != nil {
		t.Fatal(err)
	}
}

func waitHelperMarker(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("helper marker timeout: %s", path)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func waitHelperMarkerValue(t *testing.T, path, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if data, err := os.ReadFile(path); err == nil && string(data) == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("helper marker value timeout: %s=%q", path, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSiblingConnectionsDoNotConsumeClientSlots(t *testing.T) {
	e := NewEndpoint()
	p, err := e.PrepareRunner()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	runnerMarker := filepath.Join(dir, "runner")
	goMarker := filepath.Join(dir, "go")
	siblingRelease := filepath.Join(dir, "sibling-release")
	runner := exec.Command(os.Args[0], "-test.run=^TestMutationBrokerSiblingHelper$", "-test.v=false")
	runner.Env = append(os.Environ(),
		"MULTICA_MUTATIONBROKER_HELPER=runner",
		"MULTICA_MUTATIONBROKER_LOCATOR="+p.Locator,
		"MULTICA_MUTATIONBROKER_MARKER="+runnerMarker,
		"MULTICA_MUTATIONBROKER_GO="+goMarker,
	)
	if err := runner.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runner.Process.Kill(); _ = runner.Wait() })
	waitHelperMarker(t, runnerMarker)
	start, err := procStartTime(runner.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.BindPreparedRunner(p.Generation, runner.Process.Pid, start); err != nil {
		t.Fatal(err)
	}
	cancel, serveCh := serveTest(e, p, func(_ context.Context, req Request) (json.RawMessage, error) {
		if req.RequestID != "runner-r" {
			t.Errorf("unexpected callback from sibling or malformed request: %s", req.RequestID)
		}
		return json.RawMessage(`{"ok":true}`), nil
	})

	const siblingCount = maxClients
	siblings := make([]*exec.Cmd, 0, siblingCount)
	markers := make([]string, 0, siblingCount)
	for i := 0; i < siblingCount; i++ {
		marker := filepath.Join(dir, "sibling-"+strconv.Itoa(i))
		cmd := exec.Command(os.Args[0], "-test.run=^TestMutationBrokerSiblingHelper$", "-test.v=false")
		cmd.Env = append(os.Environ(),
			"MULTICA_MUTATIONBROKER_HELPER=sibling",
			"MULTICA_MUTATIONBROKER_LOCATOR="+p.Locator,
			"MULTICA_MUTATIONBROKER_MARKER="+marker,
			"MULTICA_MUTATIONBROKER_SIBLING_RELEASE="+siblingRelease,
		)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		siblingCmd := cmd
		t.Cleanup(func() { _ = siblingCmd.Process.Kill(); _ = siblingCmd.Wait() })
		siblings = append(siblings, siblingCmd)
		markers = append(markers, marker)
	}
	for _, marker := range markers {
		waitHelperMarkerValue(t, marker, "rejected")
	}
	state := e.state.(*linuxEndpoint)
	state.mu.Lock()
	clients := len(state.clients)
	state.mu.Unlock()
	if clients != 0 {
		t.Fatalf("same-UID siblings occupied client slots: total clients=%d, want zero before runner release", clients)
	}
	if err := os.WriteFile(goMarker, []byte("go"), 0600); err != nil {
		t.Fatal(err)
	}
	waitHelperMarkerValue(t, runnerMarker, "success")
	if err := runner.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(siblingRelease, []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range siblings {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("sibling helper: %v", err)
		}
	}
	cancel()
	_ = e.Close()
	_ = waitErr(t, serveCh)
}

func TestRevokeCancelsBlockingHandlerAndServeReturns(t *testing.T) {
	e, p := preparedEndpoint(t)
	started := make(chan struct{})
	canceled := make(chan struct{})
	cancel, serveCh := serveTest(e, p, func(ctx context.Context, _ Request) (json.RawMessage, error) {
		close(started)
		<-ctx.Done()
		close(canceled)
		return nil, ctx.Err()
	})
	conn := dialClient(t, p)
	sendHello(t, conn, "blocking-h")
	writeReq(t, conn, Request{Version: ProtocolVersion, Kind: RequestKind, RequestID: "blocking-r", Operation: "blocking"})
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("blocking handler did not start")
	}
	if err := e.Revoke(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("revoke did not cancel handler context")
	}
	cancel()
	err := waitErr(t, serveCh)
	if !errors.Is(err, ErrNotBound) && !errors.Is(err, context.Canceled) {
		t.Fatalf("Serve after revoke: %v", err)
	}
}

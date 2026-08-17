//go:build linux

package mutationbroker

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func preparedConn(t *testing.T, endpoint *Endpoint) (PreparedRunner, *net.UnixConn) {
	t.Helper()
	prepared, err := endpoint.PrepareRunner()
	if err != nil {
		t.Fatalf("PrepareRunner: %v", err)
	}
	start, err := procStartTime(os.Getpid())
	if err != nil {
		t.Fatalf("procStartTime: %v", err)
	}
	if err := endpoint.BindPreparedRunner(prepared.Generation, os.Getpid(), start); err != nil {
		t.Fatalf("BindPreparedRunner: %v", err)
	}
	raw, err := net.FileConn(prepared.File)
	_ = prepared.File.Close()
	if err != nil {
		t.Fatalf("FileConn: %v", err)
	}
	conn, ok := raw.(*net.UnixConn)
	if !ok {
		_ = raw.Close()
		t.Fatal("runner transport is not UnixConn")
	}
	t.Cleanup(func() { _ = conn.Close() })
	return prepared, conn
}

func writeRequest(t *testing.T, conn *net.UnixConn, request Request) {
	t.Helper()
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if n, _, err := conn.WriteMsgUnix(data, nil, nil); err != nil || n != len(data) {
		t.Fatalf("write request: n=%d err=%v", n, err)
	}
}

func readResponse(t *testing.T, conn *net.UnixConn) Response {
	t.Helper()
	data := make([]byte, MaxFrameBytes+1)
	n, _, _, _, err := conn.ReadMsgUnix(data, nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	var response Response
	if err := json.Unmarshal(data[:n], &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return response
}

func waitServe(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return")
		return nil
	}
}

func startServe(endpoint *Endpoint, generation uint64, handler Handler) (context.CancelFunc, <-chan error) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan error, 1)
	go func() { ch <- endpoint.Serve(generation, ctx, handler) }()
	return cancel, ch
}

func hello(t *testing.T, conn *net.UnixConn, id string) {
	t.Helper()
	writeRequest(t, conn, Request{Version: ProtocolVersion, Kind: HelloKind, RequestID: id})
	if response := readResponse(t, conn); !response.OK {
		t.Fatalf("hello response: %+v", response)
	}
}

func TestEndpointRoundTripAndGeneration(t *testing.T) {
	endpoint := NewEndpoint()
	prepared, conn := preparedConn(t, endpoint)
	cancel, serveCh := startServe(endpoint, prepared.Generation, func(_ context.Context, request Request) (json.RawMessage, error) {
		if request.Operation != "checkout" {
			return nil, errors.New("unexpected operation")
		}
		return json.RawMessage(`{"receipt":"ok"}`), nil
	})
	hello(t, conn, "hello")
	writeRequest(t, conn, Request{Version: ProtocolVersion, Kind: RequestKind, RequestID: "r1", Operation: "checkout"})
	response := readResponse(t, conn)
	if !response.OK || string(response.Payload) != `{"receipt":"ok"}` {
		t.Fatalf("request response: %+v", response)
	}
	cancel()
	_ = endpoint.Close()
	if err := waitServe(t, serveCh); err == nil {
		t.Fatal("Serve returned nil after close")
	}
}

func TestEndpointChildFDInheritance(t *testing.T) {
	if os.Getenv("MUTATIONBROKER_CHILD") == "1" {
		file := os.NewFile(uintptr(3), "mutation-broker-child")
		raw, err := net.FileConn(file)
		if err != nil {
			os.Exit(20)
		}
		conn := raw.(*net.UnixConn)
		defer conn.Close()
		time.Sleep(100 * time.Millisecond)
		data, _ := json.Marshal(Request{Version: ProtocolVersion, Kind: HelloKind, RequestID: "child-hello"})
		if n, _, err := conn.WriteMsgUnix(data, nil, nil); err != nil || n != len(data) {
			os.Exit(21)
		}
		buf := make([]byte, MaxFrameBytes+1)
		if _, _, _, _, err := conn.ReadMsgUnix(buf, nil); err != nil {
			os.Exit(22)
		}
		data, _ = json.Marshal(Request{Version: ProtocolVersion, Kind: RequestKind, RequestID: "child-request", Operation: "child"})
		if n, _, err := conn.WriteMsgUnix(data, nil, nil); err != nil || n != len(data) {
			os.Exit(23)
		}
		if _, _, _, _, err := conn.ReadMsgUnix(buf, nil); err != nil {
			os.Exit(24)
		}
		os.Exit(0)
	}
	endpoint := NewEndpoint()
	prepared, err := endpoint.PrepareRunner()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestEndpointChildFDInheritance", "--")
	cmd.Env = append(os.Environ(), "MUTATIONBROKER_CHILD=1")
	cmd.ExtraFiles = []*os.File{prepared.File}
	if err := cmd.Start(); err != nil {
		_ = prepared.File.Close()
		t.Fatal(err)
	}
	_ = prepared.File.Close()
	start, err := procStartTime(cmd.Process.Pid)
	if err != nil {
		_ = cmd.Process.Kill()
		t.Fatal(err)
	}
	if err := endpoint.BindPreparedRunner(prepared.Generation, cmd.Process.Pid, start); err != nil {
		_ = cmd.Process.Kill()
		t.Fatal(err)
	}
	cancel, serveCh := startServe(endpoint, prepared.Generation, func(_ context.Context, _ Request) (json.RawMessage, error) {
		return json.RawMessage(`{"child":true}`), nil
	})
	if err := cmd.Wait(); err != nil {
		cancel()
		_ = endpoint.Close()
		t.Fatalf("child: %v", err)
	}
	cancel()
	_ = endpoint.Close()
	_ = waitServe(t, serveCh)
}

func TestPrepareRotateCancelsOldAndRejectsStaleServe(t *testing.T) {
	endpoint := NewEndpoint()
	old, oldConn := preparedConn(t, endpoint)
	started, canceled := make(chan struct{}), make(chan struct{})
	oldCancel, oldServe := startServe(endpoint, old.Generation, func(ctx context.Context, _ Request) (json.RawMessage, error) {
		close(started)
		<-ctx.Done()
		close(canceled)
		return nil, ctx.Err()
	})
	hello(t, oldConn, "old-hello")
	writeRequest(t, oldConn, Request{Version: ProtocolVersion, Kind: RequestKind, RequestID: "old", Operation: "blocking"})
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("old handler did not start")
	}
	newPrepared, newConn := preparedConn(t, endpoint)
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("rotation did not cancel old handler")
	}
	oldCancel()
	_ = waitServe(t, oldServe)
	if err := endpoint.Serve(old.Generation, context.Background(), func(context.Context, Request) (json.RawMessage, error) { return nil, nil }); !errors.Is(err, ErrNotBound) {
		t.Fatalf("stale Serve = %v", err)
	}
	newCancel, newServe := startServe(endpoint, newPrepared.Generation, func(_ context.Context, _ Request) (json.RawMessage, error) {
		return json.RawMessage(`{"new":true}`), nil
	})
	hello(t, newConn, "new-hello")
	writeRequest(t, newConn, Request{Version: ProtocolVersion, Kind: RequestKind, RequestID: "new", Operation: "checkout"})
	if response := readResponse(t, newConn); !response.OK {
		t.Fatalf("new response: %+v", response)
	}
	newCancel()
	_ = endpoint.Close()
	_ = waitServe(t, newServe)
}

func TestHelloGateAndSingleServe(t *testing.T) {
	endpoint := NewEndpoint()
	prepared, conn := preparedConn(t, endpoint)
	cancel, serveCh := startServe(endpoint, prepared.Generation, func(context.Context, Request) (json.RawMessage, error) { return nil, nil })
	writeRequest(t, conn, Request{Version: ProtocolVersion, Kind: RequestKind, RequestID: "missing-hello"})
	if err := waitServe(t, serveCh); !errors.Is(err, ErrProtocol) {
		t.Fatalf("missing hello = %v", err)
	}
	cancel()
	_ = endpoint.Close()

	endpoint = NewEndpoint()
	prepared, conn = preparedConn(t, endpoint)
	cancel, serveCh = startServe(endpoint, prepared.Generation, func(context.Context, Request) (json.RawMessage, error) { return nil, nil })
	hello(t, conn, "hello")
	if err := endpoint.Serve(prepared.Generation, context.Background(), func(context.Context, Request) (json.RawMessage, error) { return nil, nil }); !errors.Is(err, ErrProtocol) {
		t.Fatalf("second Serve = %v", err)
	}
	writeRequest(t, conn, Request{Version: ProtocolVersion, Kind: HelloKind, RequestID: "duplicate"})
	if err := waitServe(t, serveCh); !errors.Is(err, ErrProtocol) {
		t.Fatalf("duplicate hello = %v", err)
	}
	cancel()
	_ = endpoint.Close()

	endpoint = NewEndpoint()
	prepared, conn = preparedConn(t, endpoint)
	cancel, serveCh = startServe(endpoint, prepared.Generation, func(context.Context, Request) (json.RawMessage, error) { return nil, nil })
	writeRaw(t, conn, `{"version":1,"kind":"hello","request_id":"h","payload":{"x":1}}`)
	if err := waitServe(t, serveCh); !errors.Is(err, ErrProtocol) {
		t.Fatalf("hello payload = %v", err)
	}
	cancel()
	_ = endpoint.Close()

	endpoint = NewEndpoint()
	prepared, conn = preparedConn(t, endpoint)
	cancel, serveCh = startServe(endpoint, prepared.Generation, func(context.Context, Request) (json.RawMessage, error) { return nil, nil })
	writeRaw(t, conn, `{"version":1,"kind":"hello","request_id":"h","unknown":true}`)
	if err := waitServe(t, serveCh); !errors.Is(err, ErrProtocol) {
		t.Fatalf("hello unknown field = %v", err)
	}
	cancel()
	_ = endpoint.Close()
}

func writeRaw(t *testing.T, conn *net.UnixConn, raw string) {
	t.Helper()
	data := []byte(raw)
	if n, _, err := conn.WriteMsgUnix(data, nil, nil); err != nil || n != len(data) {
		t.Fatalf("write raw: n=%d err=%v", n, err)
	}
}

func TestInFlightLimit(t *testing.T) {
	endpoint := NewEndpoint()
	prepared, conn := preparedConn(t, endpoint)
	state := endpoint.state.(*linuxEndpoint)
	state.mu.Lock()
	for i := 0; i < maxInFlight; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		_ = ctx
		state.active[string(rune(i+1))] = &activeRequest{cancel: cancel}
	}
	state.mu.Unlock()
	cancel, serveCh := startServe(endpoint, prepared.Generation, func(context.Context, Request) (json.RawMessage, error) { return nil, nil })
	hello(t, conn, "hello")
	writeRequest(t, conn, Request{Version: ProtocolVersion, Kind: RequestKind, RequestID: "limit", Operation: "x"})
	if err := waitServe(t, serveCh); !errors.Is(err, ErrInFlightLimit) {
		t.Fatalf("in-flight limit = %v", err)
	}
	cancel()
	_ = endpoint.Close()
}

func TestRotateBeforeDispatchHasZeroCallback(t *testing.T) {
	endpoint := NewEndpoint()
	prepared, conn := preparedConn(t, endpoint)
	var callbacks int
	cancel, serveCh := startServe(endpoint, prepared.Generation, func(context.Context, Request) (json.RawMessage, error) {
		callbacks++
		return nil, nil
	})
	hello(t, conn, "hello")
	state := endpoint.state.(*linuxEndpoint)
	state.dispatchGate.Lock()
	writeRequest(t, conn, Request{Version: ProtocolVersion, Kind: RequestKind, RequestID: "queued", Operation: "x"})
	deadline := time.Now().Add(3 * time.Second)
	for {
		state.mu.Lock()
		queued := len(state.active) == 1
		state.mu.Unlock()
		if queued || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	rotated := make(chan PreparedRunner, 1)
	go func() { next, _ := endpoint.PrepareRunner(); rotated <- next }()
	for {
		state.mu.Lock()
		rotating := state.rotating
		state.mu.Unlock()
		if rotating || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	state.dispatchGate.Unlock()
	next := <-rotated
	_ = next.File.Close()
	if callbacks != 0 {
		t.Fatalf("queued callback count = %d", callbacks)
	}
	cancel()
	_ = endpoint.Close()
	_ = waitServe(t, serveCh)
}

func TestRunnerEOFCancelsHandler(t *testing.T) {
	endpoint := NewEndpoint()
	prepared, conn := preparedConn(t, endpoint)
	started := make(chan struct{})
	canceled := make(chan struct{})
	cancel, serveCh := startServe(endpoint, prepared.Generation, func(ctx context.Context, _ Request) (json.RawMessage, error) {
		close(started)
		<-ctx.Done()
		close(canceled)
		return nil, ctx.Err()
	})
	hello(t, conn, "hello")
	writeRequest(t, conn, Request{Version: ProtocolVersion, Kind: RequestKind, RequestID: "eof", Operation: "x"})
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("handler did not start")
	}
	_ = conn.Close()
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("EOF did not cancel handler")
	}
	if err := waitServe(t, serveCh); err == nil {
		t.Fatal("Serve returned nil after EOF")
	}
	cancel()
	_ = endpoint.Close()
}

func TestAuthorizeRejectsNonDescendantAndUIDMismatch(t *testing.T) {
	endpoint := NewEndpoint()
	prepared, _ := preparedConn(t, endpoint)
	state := endpoint.state.(*linuxEndpoint)
	state.mu.Lock()
	conn := state.conn
	state.mu.Unlock()
	if err := state.authorize(prepared.Generation, conn, &unix.Ucred{Pid: 1, Uid: uint32(os.Getuid())}); !errors.Is(err, ErrRunnerUnauthorized) {
		t.Fatalf("non-descendant error = %v", err)
	}
	uid := uint32(os.Getuid() + 1)
	if err := state.authorize(prepared.Generation, conn, &unix.Ucred{Pid: int32(os.Getpid()), Uid: uid}); !errors.Is(err, ErrPeerUnauthorized) {
		t.Fatalf("uid mismatch error = %v", err)
	}
	_ = endpoint.Close()
}

func TestResponseOversizeRevokesGeneration(t *testing.T) {
	endpoint := NewEndpoint()
	prepared, conn := preparedConn(t, endpoint)
	cancel, serveCh := startServe(endpoint, prepared.Generation, func(context.Context, Request) (json.RawMessage, error) {
		return json.RawMessage(`"` + strings.Repeat("x", MaxFrameBytes) + `"`), nil
	})
	hello(t, conn, "hello")
	writeRequest(t, conn, Request{Version: ProtocolVersion, Kind: RequestKind, RequestID: "oversize", Operation: "x"})
	if err := waitServe(t, serveCh); err == nil {
		t.Fatal("oversize response left Serve alive")
	}
	cancel()
	_ = endpoint.Close()
}

func TestPeerIdentityAndAncillaryRejection(t *testing.T) {
	start, err := procStartTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	identity := RunnerIdentity{PID: os.Getpid(), StartTime: start}
	if !runnerIdentityStable(identity) || !runnerIsDescendantStable(os.Getpid(), identity) {
		t.Fatal("self identity rejected")
	}
	identity.StartTime++
	if runnerIdentityStable(identity) || runnerIsDescendantStable(os.Getpid(), identity) {
		t.Fatal("stale starttime accepted")
	}
	fd, err := unix.Dup(1)
	if err != nil {
		t.Fatal(err)
	}
	fd2, err := unix.Dup(1)
	if err != nil {
		t.Fatal(err)
	}
	oob := append(unix.UnixRights(fd), unix.UnixRights(fd2)...)
	if _, err := parseCredentials(oob); !errors.Is(err, ErrPeerUnauthorized) {
		t.Fatalf("SCM_RIGHTS error = %v", err)
	}
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err == nil {
		t.Fatal("received SCM_RIGHTS fd was not closed")
	}
	if _, err := unix.FcntlInt(uintptr(fd2), unix.F_GETFD, 0); err == nil {
		t.Fatal("second received SCM_RIGHTS fd was not closed")
	}

	endpoint := NewEndpoint()
	prepared, conn := preparedConn(t, endpoint)
	called := make(chan struct{})
	cancel, serveCh := startServe(endpoint, prepared.Generation, func(context.Context, Request) (json.RawMessage, error) {
		close(called)
		return nil, nil
	})
	sentFD, err := unix.Dup(1)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(Request{Version: ProtocolVersion, Kind: HelloKind, RequestID: "rights"})
	if n, _, err := conn.WriteMsgUnix(data, unix.UnixRights(sentFD), nil); err != nil || n != len(data) {
		t.Fatalf("socket SCM_RIGHTS write: n=%d err=%v", n, err)
	}
	_ = unix.Close(sentFD)
	if err := waitServe(t, serveCh); !errors.Is(err, ErrPeerUnauthorized) {
		t.Fatalf("socket SCM_RIGHTS = %v", err)
	}
	select {
	case <-called:
		t.Fatal("SCM_RIGHTS reached handler")
	default:
	}
	cancel()
	_ = endpoint.Close()
}

func TestHandlerErrorIsStableAndNonSensitive(t *testing.T) {
	endpoint := NewEndpoint()
	prepared, conn := preparedConn(t, endpoint)
	cancel, serveCh := startServe(endpoint, prepared.Generation, func(context.Context, Request) (json.RawMessage, error) {
		return nil, errors.New("secret-token-must-not-cross-wire")
	})
	hello(t, conn, "hello")
	writeRequest(t, conn, Request{Version: ProtocolVersion, Kind: RequestKind, RequestID: "error", Operation: "x"})
	response := readResponse(t, conn)
	if response.Error != operationFailedCode || strings.Contains(response.Error, "secret-token") {
		t.Fatalf("handler error leaked: %+v", response)
	}
	cancel()
	_ = endpoint.Close()
	_ = waitServe(t, serveCh)
}

func TestReadRequestRejectsPayloadTruncation(t *testing.T) {
	endpoint := NewEndpoint()
	prepared, conn := preparedConn(t, endpoint)
	cancel, serveCh := startServe(endpoint, prepared.Generation, func(context.Context, Request) (json.RawMessage, error) { return nil, nil })
	frame := make([]byte, MaxFrameBytes+1)
	if n, _, err := conn.WriteMsgUnix(frame, nil, nil); err != nil || n != len(frame) {
		t.Fatalf("oversize write: n=%d err=%v", n, err)
	}
	if err := waitServe(t, serveCh); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("oversize error = %v", err)
	}
	cancel()
	_ = endpoint.Close()
}

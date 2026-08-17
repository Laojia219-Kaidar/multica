//go:build linux

package mutationbroker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

const maxInFlight = 128

const operationFailedCode = "operation_failed"

func errorCode(err error) string {
	switch {
	case errors.Is(err, ErrProtocol):
		return "protocol_error"
	case errors.Is(err, ErrPeerUnauthorized), errors.Is(err, ErrRunnerUnauthorized):
		return "unauthorized"
	case errors.Is(err, ErrFrameTooLarge):
		return "frame_too_large"
	case errors.Is(err, ErrInFlightLimit):
		return "in_flight_limit"
	default:
		return operationFailedCode
	}
}

type activeRequest struct {
	cancel context.CancelFunc
}

type linuxEndpoint struct {
	mu           sync.Mutex
	lifecycleMu  sync.Mutex
	conn         *net.UnixConn
	runnerHold   *os.File
	closed       bool
	generation   uint64
	prepared     bool
	bound        bool
	identity     RunnerIdentity
	serving      bool
	helloSeen    bool
	rotating     bool
	active       map[string]*activeRequest
	writeMu      sync.Mutex
	dispatchGate sync.RWMutex
}

func linuxState(e *Endpoint) (*linuxEndpoint, error) {
	if e == nil {
		return nil, ErrClosed
	}
	state, ok := e.state.(*linuxEndpoint)
	if !ok || state == nil {
		return nil, ErrClosed
	}
	return state, nil
}

func newEndpoint() *Endpoint {
	return &Endpoint{state: &linuxEndpoint{active: make(map[string]*activeRequest)}}
}

func duplicateFile(file *os.File, name string) (*os.File, error) {
	fd, err := unix.Dup(int(file.Fd()))
	if err != nil {
		return nil, err
	}
	unix.CloseOnExec(fd)
	return os.NewFile(uintptr(fd), name), nil
}

func makeSocketPair() (*net.UnixConn, *os.File, *os.File, error) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, nil, nil, err
	}
	daemonFile := os.NewFile(uintptr(fds[0]), "mutation-broker-daemon")
	runnerFile := os.NewFile(uintptr(fds[1]), "mutation-broker-runner")
	if daemonFile == nil || runnerFile == nil {
		if daemonFile != nil {
			_ = daemonFile.Close()
		}
		if runnerFile != nil {
			_ = runnerFile.Close()
		}
		return nil, nil, nil, errors.New("create mutation broker socketpair")
	}
	if err := unix.SetsockoptInt(int(daemonFile.Fd()), unix.SOL_SOCKET, unix.SO_PASSCRED, 1); err != nil {
		_ = daemonFile.Close()
		_ = runnerFile.Close()
		return nil, nil, nil, fmt.Errorf("enable SCM_CREDENTIALS: %w", err)
	}
	raw, err := net.FileConn(daemonFile)
	_ = daemonFile.Close()
	if err != nil {
		_ = runnerFile.Close()
		return nil, nil, nil, fmt.Errorf("wrap mutation broker socket: %w", err)
	}
	conn, ok := raw.(*net.UnixConn)
	if !ok {
		_ = raw.Close()
		_ = runnerFile.Close()
		return nil, nil, nil, errors.New("mutation broker socket is not UnixConn")
	}
	hold, err := duplicateFile(runnerFile, "mutation-broker-runner-hold")
	if err != nil {
		_ = conn.Close()
		_ = runnerFile.Close()
		return nil, nil, nil, err
	}
	return conn, runnerFile, hold, nil
}

func (e *Endpoint) prepareRunner() (PreparedRunner, error) {
	state, err := linuxState(e)
	if err != nil {
		return PreparedRunner{}, err
	}
	state.lifecycleMu.Lock()
	defer state.lifecycleMu.Unlock()
	conn, runnerFile, hold, err := makeSocketPair()
	if err != nil {
		return PreparedRunner{}, err
	}
	state.mu.Lock()
	if state.closed || state.rotating || state.generation == ^uint64(0) {
		state.mu.Unlock()
		_ = conn.Close()
		_ = runnerFile.Close()
		_ = hold.Close()
		return PreparedRunner{}, ErrClosed
	}
	oldConn, oldHold := state.conn, state.runnerHold
	for id, request := range state.active {
		request.cancel()
		delete(state.active, id)
	}
	state.rotating = true
	state.conn = nil
	state.runnerHold = nil
	state.prepared = false
	state.bound = false
	state.identity = RunnerIdentity{}
	state.serving = false
	state.helloSeen = false
	state.mu.Unlock()
	if oldConn != nil {
		_ = oldConn.Close()
	}
	if oldHold != nil {
		_ = oldHold.Close()
	}
	state.dispatchGate.Lock()
	defer state.dispatchGate.Unlock()
	state.mu.Lock()
	if state.closed {
		state.rotating = false
		state.mu.Unlock()
		_ = conn.Close()
		_ = runnerFile.Close()
		_ = hold.Close()
		return PreparedRunner{}, ErrClosed
	}
	state.generation++
	state.conn = conn
	state.runnerHold = hold
	state.prepared = true
	state.rotating = false
	generation := state.generation
	state.mu.Unlock()
	return PreparedRunner{File: runnerFile, Generation: generation}, nil
}

func (e *Endpoint) bindPreparedRunner(generation uint64, rootPID int, rootStartTime uint64) error {
	state, err := linuxState(e)
	if err != nil {
		return err
	}
	state.lifecycleMu.Lock()
	defer state.lifecycleMu.Unlock()
	identity := RunnerIdentity{PID: rootPID, StartTime: rootStartTime}
	if !runnerIdentityStable(identity) {
		return ErrRunnerUnauthorized
	}
	state.mu.Lock()
	if state.closed {
		state.mu.Unlock()
		return ErrClosed
	}
	if state.rotating || !state.prepared || state.generation != generation || state.runnerHold == nil || state.conn == nil {
		state.mu.Unlock()
		return ErrNotBound
	}
	state.identity = identity
	state.bound = true
	state.prepared = false
	hold := state.runnerHold
	state.runnerHold = nil
	state.mu.Unlock()
	_ = hold.Close()
	return nil
}

func (e *Endpoint) serve(generation uint64, ctx context.Context, handler Handler) error {
	if ctx == nil || handler == nil {
		return ErrProtocol
	}
	state, err := linuxState(e)
	if err != nil {
		return err
	}
	state.mu.Lock()
	if state.closed {
		state.mu.Unlock()
		return ErrClosed
	}
	if generation == 0 || generation != state.generation || !state.bound || state.conn == nil {
		state.mu.Unlock()
		return ErrNotBound
	}
	if state.serving {
		state.mu.Unlock()
		return ErrProtocol
	}
	conn := state.conn
	state.serving = true
	state.helloSeen = false
	state.mu.Unlock()
	done := make(chan struct{})
	defer close(done)
	defer func() {
		state.mu.Lock()
		if state.generation == generation && state.conn == conn {
			state.serving = false
		}
		state.mu.Unlock()
	}()
	go func() {
		select {
		case <-ctx.Done():
			_ = e.revokeGeneration(generation, conn)
		case <-done:
		}
	}()

	for {
		req, cred, err := readRequest(conn)
		if err != nil {
			_ = e.revokeGeneration(generation, conn)
			return err
		}
		if err := state.authorize(generation, conn, cred); err != nil {
			_ = e.failGeneration(generation, conn, err)
			return err
		}
		if err := validateEnvelope(state, generation, conn, req); err != nil {
			_ = e.failGeneration(generation, conn, err)
			return err
		}
		if req.Kind == HelloKind {
			if err := writeResponse(state, conn, Response{Version: ProtocolVersion, RequestID: req.RequestID, OK: true}); err != nil {
				_ = e.revokeGeneration(generation, conn)
				return err
			}
			continue
		}
		state.mu.Lock()
		if state.generation != generation || state.conn != conn || !state.bound || state.rotating {
			state.mu.Unlock()
			return ErrNotBound
		}
		if len(state.active) >= maxInFlight {
			state.mu.Unlock()
			_ = e.failGeneration(generation, conn, ErrInFlightLimit)
			return ErrInFlightLimit
		}
		if _, exists := state.active[req.RequestID]; exists {
			state.mu.Unlock()
			_ = e.failGeneration(generation, conn, ErrProtocol)
			return ErrProtocol
		}
		reqCtx, cancel := context.WithCancel(ctx)
		active := &activeRequest{cancel: cancel}
		state.active[req.RequestID] = active
		state.mu.Unlock()
		go e.dispatch(generation, conn, handler, req, reqCtx, active)
	}
}

func validateEnvelope(state *linuxEndpoint, generation uint64, conn *net.UnixConn, req Request) error {
	if req.Version != ProtocolVersion {
		return ErrProtocol
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.generation != generation || state.conn != conn || !state.bound {
		return ErrNotBound
	}
	if !state.helloSeen {
		if req.Kind != HelloKind || req.RequestID == "" || req.Operation != "" || len(req.Payload) != 0 {
			return ErrProtocol
		}
		state.helloSeen = true
		return nil
	}
	if req.Kind == HelloKind || req.Kind != RequestKind || req.RequestID == "" {
		return ErrProtocol
	}
	return nil
}

func (e *Endpoint) dispatch(generation uint64, conn *net.UnixConn, handler Handler, req Request, reqCtx context.Context, active *activeRequest) {
	state, err := linuxState(e)
	if err != nil {
		active.cancel()
		return
	}
	state.dispatchGate.RLock()
	state.mu.Lock()
	current := state.generation == generation && state.conn == conn && state.bound && !state.rotating && state.active[req.RequestID] == active
	state.mu.Unlock()
	if !current {
		state.dispatchGate.RUnlock()
		active.cancel()
		return
	}
	payload, handlerErr := handler(reqCtx, req)
	state.dispatchGate.RUnlock()
	resp := Response{Version: ProtocolVersion, RequestID: req.RequestID, OK: handlerErr == nil, Payload: payload}
	if handlerErr != nil {
		resp.Error = operationFailedCode
	}
	if writeErr := writeResponse(state, conn, resp); writeErr != nil {
		_ = e.revokeGeneration(generation, conn)
	}
	active.cancel()
	state.mu.Lock()
	if state.active[req.RequestID] == active {
		delete(state.active, req.RequestID)
	}
	state.mu.Unlock()
}

func readRequest(conn *net.UnixConn) (Request, *unix.Ucred, error) {
	buf := make([]byte, MaxFrameBytes+1)
	oob := make([]byte, unix.CmsgSpace(unix.SizeofUcred)+unix.CmsgSpace(64*4))
	n, oobn, flags, _, err := conn.ReadMsgUnix(buf, oob)
	if err != nil {
		return Request{}, nil, err
	}
	if flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 {
		closeRights(oob[:oobn])
		if flags&unix.MSG_TRUNC != 0 {
			return Request{}, nil, ErrFrameTooLarge
		}
		return Request{}, nil, ErrPeerUnauthorized
	}
	if n > MaxFrameBytes {
		closeRights(oob[:oobn])
		return Request{}, nil, ErrFrameTooLarge
	}
	var req Request
	decoder := json.NewDecoder(bytes.NewReader(buf[:n]))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		closeRights(oob[:oobn])
		return Request{}, nil, ErrProtocol
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		closeRights(oob[:oobn])
		return Request{}, nil, ErrProtocol
	}
	cred, err := parseCredentials(oob[:oobn])
	if err != nil {
		return Request{}, nil, err
	}
	return req, cred, nil
}

func closeRights(oob []byte) {
	for len(oob) >= unix.CmsgLen(0) {
		header, data, rest, err := unix.ParseOneSocketControlMessage(oob)
		if err != nil {
			return
		}
		if header.Level == unix.SOL_SOCKET && header.Type == unix.SCM_RIGHTS {
			msg := unix.SocketControlMessage{Header: header, Data: data}
			fds, err := unix.ParseUnixRights(&msg)
			if err == nil {
				for _, fd := range fds {
					_ = unix.Close(fd)
				}
			}
		}
		if len(rest) == 0 {
			return
		}
		oob = rest
	}
}

func parseCredentials(oob []byte) (*unix.Ucred, error) {
	msgs, err := unix.ParseSocketControlMessage(oob)
	if err != nil {
		closeRights(oob)
		return nil, ErrPeerUnauthorized
	}
	var cred *unix.Ucred
	invalid := false
	for i := range msgs {
		msg := &msgs[i]
		if msg.Header.Level != unix.SOL_SOCKET {
			invalid = true
			continue
		}
		switch msg.Header.Type {
		case unix.SCM_CREDENTIALS:
			if cred != nil {
				invalid = true
				continue
			}
			parsed, err := unix.ParseUnixCredentials(msg)
			if err != nil {
				invalid = true
				continue
			}
			cred = parsed
		case unix.SCM_RIGHTS:
			fds, err := unix.ParseUnixRights(msg)
			if err == nil {
				for _, fd := range fds {
					_ = unix.Close(fd)
				}
			}
			invalid = true
		default:
			invalid = true
		}
	}
	if invalid || cred == nil {
		return nil, ErrPeerUnauthorized
	}
	return cred, nil
}

func writeResponse(state *linuxEndpoint, conn *net.UnixConn, resp Response) error {
	data, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	if len(data) > MaxFrameBytes {
		return ErrFrameTooLarge
	}
	if conn == nil {
		return ErrClosed
	}
	state.writeMu.Lock()
	defer state.writeMu.Unlock()
	n, oobn, err := conn.WriteMsgUnix(data, nil, nil)
	if err != nil {
		return err
	}
	if oobn != 0 || n != len(data) {
		return io.ErrShortWrite
	}
	return nil
}

func (s *linuxEndpoint) authorize(generation uint64, conn *net.UnixConn, cred *unix.Ucred) error {
	if cred == nil || int(cred.Uid) != os.Getuid() {
		return ErrPeerUnauthorized
	}
	s.mu.Lock()
	if s.generation != generation || s.conn != conn || !s.bound {
		s.mu.Unlock()
		return ErrNotBound
	}
	identity := s.identity
	s.mu.Unlock()
	if !runnerIsDescendantStable(int(cred.Pid), identity) {
		return ErrRunnerUnauthorized
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.generation != generation || s.conn != conn || s.identity != identity || !s.bound {
		return ErrNotBound
	}
	return nil
}

func (e *Endpoint) failGeneration(generation uint64, conn *net.UnixConn, cause error) error {
	state, err := linuxState(e)
	if err != nil {
		return err
	}
	_ = writeResponse(state, conn, Response{Version: ProtocolVersion, Error: errorCode(cause)})
	return e.revokeGeneration(generation, conn)
}

func (e *Endpoint) revoke() error {
	state, err := linuxState(e)
	if err != nil {
		return err
	}
	state.lifecycleMu.Lock()
	defer state.lifecycleMu.Unlock()
	return e.revokeLocked(state, 0, nil)
}

func (e *Endpoint) revokeGeneration(generation uint64, expected *net.UnixConn) error {
	state, err := linuxState(e)
	if err != nil {
		return err
	}
	state.lifecycleMu.Lock()
	defer state.lifecycleMu.Unlock()
	return e.revokeLocked(state, generation, expected)
}

func (e *Endpoint) revokeLocked(state *linuxEndpoint, generation uint64, expected *net.UnixConn) error {
	state.mu.Lock()
	if generation != 0 && (state.generation != generation || state.conn != expected) {
		state.mu.Unlock()
		return nil
	}
	if state.rotating {
		state.mu.Unlock()
		return nil
	}
	conn, hold := state.conn, state.runnerHold
	state.conn, state.runnerHold = nil, nil
	state.rotating = true
	state.prepared, state.bound, state.serving, state.helloSeen = false, false, false, false
	state.identity = RunnerIdentity{}
	for id, request := range state.active {
		request.cancel()
		delete(state.active, id)
	}
	state.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
	if hold != nil {
		_ = hold.Close()
	}
	state.dispatchGate.Lock()
	state.dispatchGate.Unlock()
	state.mu.Lock()
	state.rotating = false
	state.mu.Unlock()
	return nil
}

func (e *Endpoint) close() error {
	state, err := linuxState(e)
	if err != nil {
		return err
	}
	state.lifecycleMu.Lock()
	defer state.lifecycleMu.Unlock()
	state.mu.Lock()
	if state.closed {
		state.mu.Unlock()
		return nil
	}
	state.closed = true
	state.mu.Unlock()
	return e.revokeLocked(state, 0, nil)
}

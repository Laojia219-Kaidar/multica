//go:build linux

package mutationbroker

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const (
	maxInFlight       = 128
	maxClients        = 32
	handshakeDeadline = 2 * time.Second
)

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

var randRead = rand.Read

type activeRequest struct {
	cancel     context.CancelFunc
	generation uint64
	admitted   bool
}

type clientState struct {
	mu        sync.Mutex
	conn      *net.UnixConn
	peer      RunnerIdentity
	uid       uint32
	helloSeen bool
	active    map[string]*activeRequest
	writeMu   sync.Mutex
}

type linuxEndpoint struct {
	mu              sync.Mutex
	lifecycleMu     sync.Mutex
	listener        net.Listener
	locator         string
	closed          bool
	generation      uint64
	prepared        bool
	bound           bool
	identity        RunnerIdentity
	serving         bool
	rotating        bool
	clients         map[*clientState]struct{}
	active          int
	expectedUID     uint32
	dispatchGate    sync.RWMutex
	dispatchBarrier func()
}

func newEndpoint() *Endpoint {
	return &Endpoint{state: &linuxEndpoint{clients: make(map[*clientState]struct{}), expectedUID: uint32(os.Getuid())}}
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

func abstractName(name string) string { return "@" + name }

func dial(ctx context.Context, locator string) (net.Conn, error) {
	if locator == "" {
		return nil, ErrNotBound
	}
	return (&net.Dialer{}).DialContext(ctx, "unixpacket", abstractName(locator))
}

func makeListener(locator string) (net.Listener, error) {
	return net.Listen("unixpacket", abstractName(locator))
}

func newLocator(generation uint64) (string, error) {
	var raw [32]byte
	if _, err := randRead(raw[:]); err != nil {
		return "", ErrEntropyUnavailable
	}
	return fmt.Sprintf("multica-%d-%s", generation, hex.EncodeToString(raw[:])), nil
}

func socketControl(conn *net.UnixConn, fn func(int) error) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var controlErr error
	if err := raw.Control(func(fd uintptr) { controlErr = fn(int(fd)) }); err != nil {
		return err
	}
	return controlErr
}

func configureAccepted(conn *net.UnixConn) (RunnerIdentity, uint32, error) {
	var cred *unix.Ucred
	err := socketControl(conn, func(fd int) error {
		if err := unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_PASSCRED, 1); err != nil {
			return err
		}
		var err error
		cred, err = unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
		return err
	})
	if err != nil || cred == nil {
		return RunnerIdentity{}, 0, ErrPeerUnauthorized
	}
	start, err := procStartTime(int(cred.Pid))
	if err != nil || start == 0 {
		return RunnerIdentity{}, 0, ErrPeerUnauthorized
	}
	return RunnerIdentity{PID: int(cred.Pid), StartTime: start}, cred.Uid, nil
}

func (e *Endpoint) prepareRunner() (PreparedRunner, error) {
	state, err := linuxState(e)
	if err != nil {
		return PreparedRunner{}, err
	}
	state.lifecycleMu.Lock()
	defer state.lifecycleMu.Unlock()
	state.mu.Lock()
	if state.closed || state.rotating || state.generation == ^uint64(0) {
		state.mu.Unlock()
		return PreparedRunner{}, ErrClosed
	}
	nextGeneration := state.generation + 1
	state.mu.Unlock()
	locator, err := newLocator(nextGeneration)
	if err != nil {
		return PreparedRunner{}, err
	}
	state.mu.Lock()
	if state.closed || state.rotating {
		state.mu.Unlock()
		return PreparedRunner{}, ErrClosed
	}
	oldListener := state.listener
	oldClients := make([]*clientState, 0, len(state.clients))
	for client := range state.clients {
		oldClients = append(oldClients, client)
	}
	state.rotating = true
	state.listener = nil
	state.prepared = false
	state.bound = false
	state.identity = RunnerIdentity{}
	state.clients = make(map[*clientState]struct{})
	state.active = 0
	state.mu.Unlock()
	if oldListener != nil {
		_ = oldListener.Close()
	}
	for _, client := range oldClients {
		client.mu.Lock()
		for id, req := range client.active {
			req.cancel()
			delete(client.active, id)
		}
		client.mu.Unlock()
		_ = client.conn.Close()
	}
	state.dispatchGate.Lock()
	state.mu.Lock()
	if state.closed {
		state.rotating = false
		state.mu.Unlock()
		return PreparedRunner{}, ErrClosed
	}
	state.generation++
	state.listener = nil
	state.locator = locator
	state.prepared = true
	state.serving = false
	state.rotating = false
	generation := state.generation
	publishedLocator := state.locator
	state.mu.Unlock()
	state.dispatchGate.Unlock()
	return PreparedRunner{Locator: publishedLocator, Generation: generation}, nil
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
	if state.rotating || !state.prepared || state.generation != generation || state.listener != nil {
		state.mu.Unlock()
		return ErrNotBound
	}
	locator := state.locator
	state.mu.Unlock()
	listener, err := makeListener(locator)
	if err != nil {
		return ErrNotBound
	}
	if !runnerIdentityStable(identity) {
		_ = listener.Close()
		return ErrRunnerUnauthorized
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed {
		_ = listener.Close()
		return ErrClosed
	}
	if state.rotating || !state.prepared || state.generation != generation || state.listener != nil || state.locator != locator {
		_ = listener.Close()
		return ErrNotBound
	}
	state.listener = listener
	state.identity = identity
	state.bound = true
	state.prepared = false
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
	if generation == 0 || generation != state.generation || !state.bound || state.listener == nil {
		state.mu.Unlock()
		return ErrNotBound
	}
	if state.serving {
		state.mu.Unlock()
		return ErrProtocol
	}
	listener := state.listener
	state.serving = true
	state.mu.Unlock()
	defer func() {
		state.mu.Lock()
		if state.generation == generation {
			state.serving = false
		}
		state.mu.Unlock()
	}()
	go func() { <-ctx.Done(); _ = e.revokeGeneration(generation, listener) }()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			state.mu.Lock()
			current := state.generation == generation && state.listener == listener
			state.mu.Unlock()
			if !current {
				return ErrNotBound
			}
			return err
		}
		unixConn, ok := conn.(*net.UnixConn)
		if !ok {
			_ = conn.Close()
			continue
		}
		peer, uid, err := configureAccepted(unixConn)
		if err != nil {
			_ = unixConn.Close()
			continue
		}
		// Re-read the captured peer starttime before admitting the connection.
		// This is separate from the per-message check in authorizeClient: a PID
		// must not consume a client slot while it is already stale.
		peerStart, startErr := procStartTime(peer.PID)
		if startErr != nil || peerStart != peer.StartTime {
			_ = unixConn.Close()
			continue
		}
		state.mu.Lock()
		current := state.generation == generation && state.listener == listener && state.bound && !state.rotating && uid == state.expectedUID
		root := state.identity
		state.mu.Unlock()
		if !current || !runnerIsDescendantStable(peer.PID, root) {
			_ = unixConn.Close()
			continue
		}
		state.mu.Lock()
		allowed := state.generation == generation && state.listener == listener && state.bound && !state.rotating && uid == state.expectedUID && len(state.clients) < maxClients
		client := &clientState{conn: unixConn, peer: peer, uid: uid, active: make(map[string]*activeRequest)}
		if allowed {
			state.clients[client] = struct{}{}
		}
		state.mu.Unlock()
		if !allowed {
			_ = unixConn.Close()
			continue
		}
		_ = unixConn.SetReadDeadline(time.Now().Add(handshakeDeadline))
		go e.serveClient(generation, listener, state, client, handler, ctx)
	}
}

func (e *Endpoint) serveClient(generation uint64, listener net.Listener, state *linuxEndpoint, client *clientState, handler Handler, ctx context.Context) {
	defer func() {
		client.mu.Lock()
		for id, req := range client.active {
			req.cancel()
			delete(client.active, id)
		}
		client.mu.Unlock()
		_ = client.conn.Close()
		state.mu.Lock()
		delete(state.clients, client)
		state.mu.Unlock()
	}()
	for {
		req, cred, err := readRequest(client.conn)
		if err != nil {
			return
		}
		if err := authorizeClient(state, generation, listener, client, cred); err != nil {
			return
		}
		if err := validateClientEnvelope(client, req); err != nil {
			return
		}
		if req.Kind == HelloKind {
			_ = client.conn.SetReadDeadline(time.Time{})
			if writeResponse(client, Response{Version: ProtocolVersion, RequestID: req.RequestID, OK: true}) != nil {
				return
			}
			continue
		}
		client.mu.Lock()
		if len(client.active) >= maxInFlight {
			client.mu.Unlock()
			return
		}
		if _, exists := client.active[req.RequestID]; exists {
			client.mu.Unlock()
			return
		}
		reqCtx, cancel := context.WithCancel(ctx)
		active := &activeRequest{cancel: cancel, generation: generation}
		client.active[req.RequestID] = active
		client.mu.Unlock()
		// Hold the read side from admission through dispatch start. Rotation
		// takes the write side, so an admitted request cannot queue behind a
		// rotation writer and deadlock the generation transition.
		state.dispatchGate.RLock()
		state.mu.Lock()
		stale := state.generation != generation || state.listener != listener || !state.bound || state.rotating
		limitExceeded := !stale && state.active >= maxInFlight
		if !stale && !limitExceeded {
			state.active++
			active.admitted = true
		}
		state.mu.Unlock()
		if !stale && !limitExceeded && state.dispatchBarrier != nil {
			state.dispatchBarrier()
		}
		if stale || limitExceeded {
			state.dispatchGate.RUnlock()
			client.mu.Lock()
			delete(client.active, req.RequestID)
			client.mu.Unlock()
			active.cancel()
			if limitExceeded {
				_ = writeResponse(client, Response{Version: ProtocolVersion, RequestID: req.RequestID, OK: false, Error: errorCode(ErrInFlightLimit)})
			}
			return
		}
		go e.dispatchClient(generation, listener, state, client, handler, req, reqCtx, active, true)
	}
}

func authorizeClient(state *linuxEndpoint, generation uint64, listener net.Listener, client *clientState, cred *unix.Ucred) error {
	if cred == nil || uint32(cred.Uid) != client.uid || int(cred.Pid) != client.peer.PID {
		return ErrPeerUnauthorized
	}
	peerStart, err := procStartTime(client.peer.PID)
	if err != nil || peerStart != client.peer.StartTime {
		return ErrPeerUnauthorized
	}
	state.mu.Lock()
	current := state.generation == generation && state.listener == listener && state.bound
	root := state.identity
	expectedUID := state.expectedUID
	state.mu.Unlock()
	if !current || client.uid != expectedUID || uint32(cred.Uid) != expectedUID || !runnerIsDescendantStable(client.peer.PID, root) {
		return ErrRunnerUnauthorized
	}
	return nil
}

func validateClientEnvelope(client *clientState, req Request) error {
	if req.Version != ProtocolVersion {
		return ErrProtocol
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if !client.helloSeen {
		if req.Kind != HelloKind || req.RequestID == "" || req.Operation != "" || len(req.Payload) != 0 {
			return ErrProtocol
		}
		client.helloSeen = true
		return nil
	}
	if req.Kind == HelloKind || req.Kind != RequestKind || req.RequestID == "" {
		return ErrProtocol
	}
	return nil
}

func (e *Endpoint) dispatchClient(generation uint64, listener net.Listener, state *linuxEndpoint, client *clientState, handler Handler, req Request, reqCtx context.Context, active *activeRequest, gateHeld bool) {
	if !gateHeld {
		state.dispatchGate.RLock()
	}
	state.mu.Lock()
	current := state.generation == generation && state.listener == listener && state.bound && !state.rotating
	state.mu.Unlock()
	if !current {
		active.cancel()
		client.mu.Lock()
		if client.active[req.RequestID] == active {
			delete(client.active, req.RequestID)
		}
		client.mu.Unlock()
		state.mu.Lock()
		if active.admitted && active.generation == generation && state.generation == generation && state.active > 0 {
			state.active--
		}
		active.admitted = false
		state.mu.Unlock()
		state.dispatchGate.RUnlock()
		return
	}
	payload, handlerErr := handler(reqCtx, req)
	resp := Response{Version: ProtocolVersion, RequestID: req.RequestID, OK: handlerErr == nil, Payload: payload}
	if handlerErr != nil {
		resp.Error = operationFailedCode
	}
	writeErr := writeResponse(client, resp)
	active.cancel()
	client.mu.Lock()
	if client.active[req.RequestID] == active {
		delete(client.active, req.RequestID)
	}
	client.mu.Unlock()
	state.mu.Lock()
	if active.admitted && active.generation == generation && state.generation == generation && state.active > 0 {
		state.active--
	}
	active.admitted = false
	state.mu.Unlock()
	// Rotation takes the write side of this gate. Keep the read lock until
	// generation-local accounting is complete, otherwise an old dispatch can
	// decrement the active count of a freshly installed generation.
	state.dispatchGate.RUnlock()
	if writeErr != nil {
		_ = client.conn.Close()
	}
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
	decoder := json.NewDecoder(bytes.NewReader(buf[:n]))
	decoder.DisallowUnknownFields()
	var req Request
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
			if len(msg.Data) < unix.SizeofUcred {
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

func writeResponse(client *clientState, resp Response) error {
	data, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	if len(data) > MaxFrameBytes {
		return ErrFrameTooLarge
	}
	client.writeMu.Lock()
	defer client.writeMu.Unlock()
	n, oobn, err := client.conn.WriteMsgUnix(data, nil, nil)
	if err != nil {
		return err
	}
	if oobn != 0 || n != len(data) {
		return io.ErrShortWrite
	}
	return nil
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

func (e *Endpoint) revokeGenerationOnly(generation uint64) error {
	return e.revokeGeneration(generation, nil)
}
func (e *Endpoint) revokeGeneration(generation uint64, listener net.Listener) error {
	state, err := linuxState(e)
	if err != nil {
		return err
	}
	state.lifecycleMu.Lock()
	defer state.lifecycleMu.Unlock()
	return e.revokeLocked(state, generation, listener)
}

func (e *Endpoint) revokeLocked(state *linuxEndpoint, generation uint64, expected net.Listener) error {
	state.mu.Lock()
	if generation != 0 && (state.generation != generation || (expected != nil && state.listener != expected)) {
		state.mu.Unlock()
		return nil
	}
	if state.rotating {
		state.mu.Unlock()
		return nil
	}
	listener := state.listener
	state.listener = nil
	state.rotating = true
	state.prepared, state.bound, state.serving = false, false, false
	state.identity = RunnerIdentity{}
	clients := make([]*clientState, 0, len(state.clients))
	for client := range state.clients {
		clients = append(clients, client)
	}
	state.clients = make(map[*clientState]struct{})
	state.active = 0
	state.mu.Unlock()
	if listener != nil {
		_ = listener.Close()
	}
	for _, client := range clients {
		client.mu.Lock()
		for id, req := range client.active {
			req.cancel()
			delete(client.active, id)
		}
		client.mu.Unlock()
		_ = client.conn.Close()
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

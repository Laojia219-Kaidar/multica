package mutationbroker

import (
	"context"
	"encoding/json"
	"errors"
	"net"
)

const (
	ProtocolVersion = 1
	MaxFrameBytes   = 64 << 10
	HelloKind       = "hello"
	RequestKind     = "request"
)

var (
	ErrUnsupported        = errors.New("mutation broker transport unsupported")
	ErrClosed             = errors.New("mutation broker endpoint closed")
	ErrNotBound           = errors.New("mutation broker endpoint not bound")
	ErrPeerUnauthorized   = errors.New("mutation broker peer unauthorized")
	ErrRunnerUnauthorized = errors.New("mutation broker runner unauthorized")
	ErrFrameTooLarge      = errors.New("mutation broker frame too large")
	ErrProtocol           = errors.New("mutation broker protocol error")
	ErrInFlightLimit      = errors.New("mutation broker in-flight limit exceeded")
	ErrEntropyUnavailable = errors.New("mutation broker entropy unavailable")
)

// Request is the transport envelope. Task identity is bound to the endpoint;
// callers must not use request fields as authority.
type Request struct {
	Version   int             `json:"version"`
	Kind      string          `json:"kind"`
	RequestID string          `json:"request_id,omitempty"`
	Operation string          `json:"operation,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

// Response is deliberately bounded and never carries lease token or
// generation material. Payload is operation-specific receipt data.
type Response struct {
	Version   int             `json:"version"`
	RequestID string          `json:"request_id,omitempty"`
	OK        bool            `json:"ok"`
	Error     string          `json:"error,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

// Handler must honor ctx cancellation. Exact replay/idempotency is enforced
// by the upper Registry/receipt layer, not by this transport.
type Handler func(context.Context, Request) (json.RawMessage, error)

// PreparedRunner is the parent-side half of one transport generation. Locator
// is passed through the runner environment only as a non-authoritative
// rendezvous value; the daemon binds authority to peer credentials after the
// runner starts.
type PreparedRunner struct {
	// Locator is an abstract AF_UNIX address. It is a locator only; peer
	// credentials and the bound root ancestry are the authority.
	Locator    string
	Generation uint64
}

// RunnerIdentity identifies the process that owns a task endpoint. The
// start-time value prevents accepting a later process after PID reuse.
type RunnerIdentity struct {
	PID       int
	StartTime uint64
}

// Endpoint is a per-task transport authority. Linux provides the concrete
// implementation; non-Linux builds fail closed because C3b1 is Linux-only.
type Endpoint struct {
	state any
}

// NewEndpoint is implemented per platform.
func NewEndpoint() *Endpoint { return newEndpoint() }

// PrepareRunner rotates the endpoint and returns a fresh generation and
// abstract listener locator. Any prior connection and in-flight work is
// revoked.
func (e *Endpoint) PrepareRunner() (PreparedRunner, error) {
	return e.prepareRunner()
}

// BindPreparedRunner attaches a prepared generation to its registered root
// process. The PID and /proc starttime are checked before and after ancestry
// validation on every received message.
func (e *Endpoint) BindPreparedRunner(generation uint64, rootPID int, rootStartTime uint64) error {
	return e.bindPreparedRunner(generation, rootPID, rootStartTime)
}

func (e *Endpoint) Serve(generation uint64, ctx context.Context, handler Handler) error {
	return e.serve(generation, ctx, handler)
}

// Revoke cancels active requests and closes the current runner transport. A
// subsequent PrepareRunner starts a fresh generation.
func (e *Endpoint) Revoke() error { return e.revoke() }

func (e *Endpoint) RevokeGeneration(generation uint64) error {
	return e.revokeGenerationOnly(generation)
}

// Close permanently closes the endpoint and cancels all active requests.
func (e *Endpoint) Close() error { return e.close() }

// Dial connects one independent client to a prepared generation. The Linux
// implementation returns a SOCK_SEQPACKET UnixConn; unsupported platforms
// fail closed.
func Dial(ctx context.Context, locator string) (net.Conn, error) {
	return dial(ctx, locator)
}

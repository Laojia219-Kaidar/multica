//go:build !linux

package mutationbroker

import (
	"context"
	"net"
)

func newEndpoint() *Endpoint { return &Endpoint{} }

func (e *Endpoint) prepareRunner() (PreparedRunner, error)       { return PreparedRunner{}, ErrUnsupported }
func (e *Endpoint) bindPreparedRunner(uint64, int, uint64) error { return ErrUnsupported }
func (e *Endpoint) serve(uint64, context.Context, Handler) error { return ErrUnsupported }
func (e *Endpoint) revoke() error                                { return ErrUnsupported }
func (e *Endpoint) close() error                                 { return ErrUnsupported }
func (e *Endpoint) revokeGeneration(uint64) error                { return ErrUnsupported }
func (e *Endpoint) revokeGenerationOnly(uint64) error            { return ErrUnsupported }
func ProcessStartTime(int) (uint64, error)                       { return 0, ErrUnsupported }
func dial(context.Context, string) (net.Conn, error)             { return nil, ErrUnsupported }

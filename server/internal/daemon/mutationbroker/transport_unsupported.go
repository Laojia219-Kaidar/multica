//go:build !linux

package mutationbroker

import (
	"context"
)

func newEndpoint() *Endpoint { return &Endpoint{} }

func (e *Endpoint) prepareRunner() (PreparedRunner, error)       { return PreparedRunner{}, ErrUnsupported }
func (e *Endpoint) bindPreparedRunner(uint64, int, uint64) error { return ErrUnsupported }
func (e *Endpoint) serve(uint64, context.Context, Handler) error { return ErrUnsupported }
func (e *Endpoint) revoke() error                                { return ErrUnsupported }
func (e *Endpoint) close() error                                 { return ErrUnsupported }

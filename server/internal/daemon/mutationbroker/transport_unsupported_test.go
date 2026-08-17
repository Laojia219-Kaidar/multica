//go:build !linux

package mutationbroker

import (
	"context"
	"errors"
	"testing"
)

func TestUnsupportedTransportFailsClosed(t *testing.T) {
	endpoint := NewEndpoint()
	if _, err := endpoint.PrepareRunner(); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("PrepareRunner error = %v", err)
	}
	if err := endpoint.Serve(1, context.Background(), nil); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Serve error = %v", err)
	}
}

//go:build !linux

package main

import "errors"

func repoCheckoutCredentials() ([]byte, error) {
	return nil, errors.New("unix-seqpacket-v1 checkout transport is Linux-only")
}

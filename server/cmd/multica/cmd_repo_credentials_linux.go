//go:build linux

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

func repoCheckoutCredentials() ([]byte, error) {
	return unix.UnixCredentials(&unix.Ucred{Pid: int32(os.Getpid()), Uid: uint32(os.Getuid()), Gid: uint32(os.Getgid())}), nil
}

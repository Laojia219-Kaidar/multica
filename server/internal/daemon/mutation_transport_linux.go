//go:build linux

package daemon

import "syscall"

func killRunnerProcessGroup(pid int) error {
	if pid <= 0 {
		return nil
	}
	return syscall.Kill(-pid, syscall.SIGKILL)
}

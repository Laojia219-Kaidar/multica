//go:build !linux

package daemon

func killRunnerProcessGroup(int) error { return nil }

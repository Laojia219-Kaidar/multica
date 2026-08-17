//go:build linux

package mutationbroker

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

func procStartTime(pid int) (uint64, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	closeParen := strings.LastIndexByte(string(data), ')')
	if closeParen < 0 {
		return 0, fmt.Errorf("invalid proc stat")
	}
	fields := strings.Fields(string(data)[closeParen+1:])
	// fields[0] is state; field 22 (starttime) is index 19 here.
	if len(fields) <= 19 {
		return 0, fmt.Errorf("short proc stat")
	}
	return strconv.ParseUint(fields[19], 10, 64)
}

func ProcessStartTime(pid int) (uint64, error) { return procStartTime(pid) }

func procParent(pid int) (int, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	closeParen := strings.LastIndexByte(string(data), ')')
	if closeParen < 0 {
		return 0, fmt.Errorf("invalid proc stat")
	}
	fields := strings.Fields(string(data)[closeParen+1:])
	if len(fields) < 2 {
		return 0, fmt.Errorf("short proc stat")
	}
	return strconv.Atoi(fields[1])
}

func runnerIdentityAlive(identity RunnerIdentity) bool {
	if identity.PID <= 0 || identity.StartTime == 0 {
		return false
	}
	start, err := procStartTime(identity.PID)
	return err == nil && start == identity.StartTime
}

func runnerIdentityStable(identity RunnerIdentity) bool {
	if identity.PID <= 0 || identity.StartTime == 0 {
		return false
	}
	first, err := procStartTime(identity.PID)
	if err != nil || first != identity.StartTime {
		return false
	}
	second, err := procStartTime(identity.PID)
	return err == nil && second == first
}

func runnerIsDescendant(pid int, root RunnerIdentity) bool {
	if !runnerIdentityAlive(root) {
		return false
	}
	seen := make(map[int]struct{}, 64)
	for depth := 0; depth < 64 && pid > 0; depth++ {
		if _, ok := seen[pid]; ok {
			return false
		}
		seen[pid] = struct{}{}
		if pid == root.PID {
			start, err := procStartTime(pid)
			return err == nil && start == root.StartTime
		}
		parent, err := procParent(pid)
		if err != nil || parent == pid {
			return false
		}
		pid = parent
	}
	return false
}

func runnerIsDescendantStable(pid int, root RunnerIdentity) bool {
	if pid <= 0 || !runnerIdentityStable(root) {
		return false
	}
	type node struct {
		pid   int
		start uint64
	}
	chain := make([]node, 0, 64)
	seen := make(map[int]struct{}, 64)
	for depth := 0; depth < 64 && pid > 0; depth++ {
		if _, ok := seen[pid]; ok {
			return false
		}
		seen[pid] = struct{}{}
		start, err := procStartTime(pid)
		if err != nil {
			return false
		}
		chain = append(chain, node{pid: pid, start: start})
		if pid == root.PID {
			if start != root.StartTime {
				return false
			}
			break
		}
		parent, err := procParent(pid)
		if err != nil || parent == pid {
			return false
		}
		pid = parent
	}
	if len(chain) == 0 || chain[len(chain)-1].pid != root.PID {
		return false
	}
	for _, item := range chain {
		start, err := procStartTime(item.pid)
		if err != nil || start != item.start {
			return false
		}
	}
	return true
}

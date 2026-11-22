//go:build linux

package terminal

import (
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// sessionMembers is every process whose session is leader's, read from /proc:
// the jobs a shell started, whatever their process group.
func sessionMembers(leader int) []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var members []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == leader {
			continue
		}
		if sid, err := unix.Getsid(pid); err == nil && sid == leader {
			members = append(members, pid)
		}
	}
	return members
}

//go:build !windows && !darwin

package terminal

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// processStart reports whether pid is a running process and, where /proc says
// it, its start time in clock ticks since boot. Signal 0 tells whether the PID
// exists: ESRCH is gone, anything else (EPERM included) is alive.
func processStart(pid int) (alive bool, start string) {
	if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
		return false, ""
	}
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return true, ""
	}
	// The command name is in parentheses and may hold spaces; fields after the
	// closing parenthesis start at field 3, so starttime (field 22) is the
	// twentieth.
	rest := string(raw)
	if i := strings.LastIndexByte(rest, ')'); i >= 0 {
		rest = rest[i+1:]
	}
	fields := strings.Fields(rest)
	if len(fields) < 20 {
		return true, ""
	}
	return true, fields[19]
}

// pidNamespace names this process's PID namespace ("pid:[4026531836]"), so an
// owner record made in another one is never judged by PIDs it does not see.
// Empty where it cannot be read.
func pidNamespace() string {
	ns, err := os.Readlink("/proc/self/ns/pid")
	if err != nil {
		return ""
	}
	return ns
}

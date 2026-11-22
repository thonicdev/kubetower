//go:build darwin

package terminal

import (
	"errors"
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

// processStart reports whether pid is a running process and its start time,
// read with sysctl kern.proc.pid since macOS has no /proc; without the start
// time a reused PID would keep a dead owner's directory. Signal 0 tells whether
// the PID exists: ESRCH is gone, anything else (EPERM included) is alive.
func processStart(pid int) (alive bool, start string) {
	if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
		return false, ""
	}
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || info == nil {
		return true, ""
	}
	started := info.Proc.P_starttime
	return true, fmt.Sprintf("%d.%06d", started.Sec, started.Usec)
}

// pidNamespace is empty on macOS, which has no PID namespaces.
func pidNamespace() string { return "" }

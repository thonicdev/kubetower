//go:build windows

package terminal

import (
	"errors"
	"strconv"

	"golang.org/x/sys/windows"
)

// stillActive is GetExitCodeProcess's answer for a process that has not exited.
const stillActive = 259

// processStart reports whether pid is a running process and, when it can tell,
// its creation time as a string. A process this account may not open is
// reported alive with no start: not proof that it is gone.
func processStart(pid int) (alive bool, start string) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return false, ""
		}
		return true, ""
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err == nil && code != stillActive {
		return false, ""
	}
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return true, ""
	}
	return true, strconv.FormatInt(created.Nanoseconds(), 10)
}

// pidNamespace is empty on Windows: a process ID means one process there.
func pidNamespace() string { return "" }

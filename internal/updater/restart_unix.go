//go:build !windows

package updater

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// relaunch starts path with args once this process has exited, through a
// detached shell that waits for our pid: the new console binds the port this
// one holds, and the window comes back through it (the launcher secret).
// Then it asks this process to stop the way an operator's Ctrl+C does, so
// shells, forwards and the state are closed as on any quit.
func relaunch(path string, args []string) error {
	script := `while kill -0 "$1" 2>/dev/null; do sleep 0.2; done; shift; exec "$@"`
	argv := append([]string{"-c", script, "sh", strconv.Itoa(os.Getpid()), path}, args...)
	cmd := exec.Command("/bin/sh", argv...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = cmd.Process.Release()
	return stopSelf()
}

// restart is relaunch, held in a variable so a test replaces it rather than
// starting a process and stopping itself.
var restart = relaunch

// stopSelf is SIGTERM to this process: internal/launch's graceful shutdown.
var stopSelf = func() error { return syscall.Kill(os.Getpid(), syscall.SIGTERM) }

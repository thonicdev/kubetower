//go:build !windows

package updater

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// waitThenRun waits for pid $1 to exit, at most $2 checks 0.2 s apart (a pid
// reused, or never reaped, would otherwise hold it forever), then runs the
// rest of its arguments. Past the bound it gives up and starts nothing.
const waitThenRun = `n=0; while kill -0 "$1" 2>/dev/null; do n=$((n+1)); [ "$n" -gt "$2" ] && exit 1; sleep 0.2; done; shift 2; exec "$@"`

// relaunchChecks bounds the wait for this process to exit: two minutes.
const relaunchChecks = 600

// relaunch starts path with args once this process has exited, through a
// detached shell that waits for our pid: the new console binds the port this
// one holds, and the window comes back through it (the launcher secret).
// Then it asks this process to stop the way an operator's Ctrl+C does, so
// shells, forwards and the state are closed as on any quit.
func relaunch(path string, args []string) error {
	argv := append([]string{"-c", waitThenRun, "sh", strconv.Itoa(os.Getpid()), strconv.Itoa(relaunchChecks), path}, args...)
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

// startAgain is restart after the new build is in place: its failure is
// ErrNotRestarted, an installed update rather than a failed one.
func startAgain(path string) error {
	if err := restart(path, os.Args[1:]); err != nil {
		return fmt.Errorf("%w: %v", ErrNotRestarted, err)
	}
	return nil
}

// stopSelf is SIGTERM to this process: internal/launch's graceful shutdown.
var stopSelf = func() error { return syscall.Kill(os.Getpid(), syscall.SIGTERM) }

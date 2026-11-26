//go:build !windows

package updater

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
)

// waitThenRun waits for pid $1 to exit, at most $2 checks 0.2 s apart (a pid
// reused, or never reaped, would otherwise hold it forever), then runs the
// rest of its arguments from $4 on. Past the bound it gives up and starts
// nothing. Each step is appended to the install log $3, when one is named:
// the wait, the exit, the give-up and the program started - its path, never
// its arguments (#101).
const waitThenRun = `log="$3"; L() { [ -n "$log" ] && printf '%s helper: %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$1" >> "$log" 2>/dev/null; }; ` +
	`L "waiting for KubeTower (pid $1) to exit"; n=0; while kill -0 "$1" 2>/dev/null; do n=$((n+1)); ` +
	`if [ "$n" -gt "$2" ]; then L "gave up after $2 checks; nothing started"; exit 1; fi; sleep 0.2; done; ` +
	`L "KubeTower exited; starting $4"; shift 3; exec "$@"`

// relaunchChecks bounds the wait for this process to exit: two minutes.
const relaunchChecks = 600

// relaunch starts path with args once this process has exited, through a
// detached shell that waits for our pid: the new console binds the port this
// one holds, and the window comes back through it (the launcher secret).
// Then it asks this process to stop the way an operator's Ctrl+C does, so
// shells, forwards and the state are closed as on any quit.
func relaunch(path string, args []string) error {
	argv := append([]string{"-c", waitThenRun, "sh", strconv.Itoa(os.Getpid()), strconv.Itoa(relaunchChecks), currentLog(), path}, args...)
	cmd := exec.Command("/bin/sh", argv...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	relaunchHelper.Lock()
	relaunchHelper.pid = cmd.Process.Pid
	relaunchHelper.Unlock()
	_ = cmd.Process.Release()
	return stopSelf()
}

func init() { abandonHelper = stopRelaunch }

// relaunchHelper is the shell started last, kept so a console that did not
// stop can stop it: left waiting, it would start a second console whenever
// this one exits, or beside the next install's (#101).
var relaunchHelper struct {
	sync.Mutex
	pid int
}

// stopRelaunch kills the waiting shell's process group (Setsid made it its
// own), its sleep included. A group already gone is not an error.
func stopRelaunch() error {
	relaunchHelper.Lock()
	defer relaunchHelper.Unlock()
	if relaunchHelper.pid == 0 {
		return nil
	}
	err := syscall.Kill(-relaunchHelper.pid, syscall.SIGKILL)
	relaunchHelper.pid = 0
	if err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
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

// startAgainUnlessQuit is startAgain, except while KubeTower quits
// (InstallOnQuit): the new build is in place, and the next start runs it.
func startAgainUnlessQuit(plan Plan) error {
	if plan.Quit {
		return nil
	}
	return startAgain(plan.Installation.Path)
}

// stopSelf ends this process through internal/launch's shutdown (SetStop),
// or by SIGTERM to itself, which reaches the same path, where nothing was set.
var stopSelf = func() error {
	if stopper != nil {
		stopper()
		return nil
	}
	return syscall.Kill(os.Getpid(), syscall.SIGTERM)
}

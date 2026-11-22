//go:build unix

package health

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// ownGroup runs the plugin as the leader of its own process group, so that
// whatever it starts can be killed with it.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := signalGroup(cmd)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}

// killGroup kills what is left of the plugin's process group.
func killGroup(cmd *exec.Cmd) { _ = signalGroup(cmd) }

func signalGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil || cmd.Process.Pid <= 1 {
		return os.ErrProcessDone
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

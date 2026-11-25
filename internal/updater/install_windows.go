package updater

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

func init() {
	installers[WindowsSetup] = installSetup
	installers[WindowsZip] = installZip
	abandonHelper = stopHelper
	installsAfterExit = true
}

// helperProcess is the hidden PowerShell started last, kept so a console that
// did not stop can stop it rather than leave it waiting (abandonHelper), and
// the file it was to move into place, removed with it.
var helperProcess struct {
	sync.Mutex
	p    *os.Process
	left string
}

// stopHelper kills the waiting helper, if one was started, and removes the
// zip's new executable it was to move: nothing is left beside kubetower.exe
// (#101). A Kill that fails is returned, and the file is then kept, the
// helper possibly still about to move it.
func stopHelper() error {
	helperProcess.Lock()
	defer helperProcess.Unlock()
	if helperProcess.p == nil {
		return nil
	}
	if err := helperProcess.p.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	_ = helperProcess.p.Release()
	helperProcess.p = nil
	if helperProcess.left != "" {
		_ = os.Remove(helperProcess.left)
		helperProcess.left = ""
	}
	return nil
}

// createNoWindow is CREATE_NO_WINDOW: the helper shows no console.
const createNoWindow = 0x08000000

// helper starts the hidden PowerShell that finishes the update after exit,
// then stops this process.
var helper = func(script string) error { return finish(script, startPowerShell, stopper) }

// startPowerShell starts the script in a hidden PowerShell and lets it go.
func startPowerShell(script string) error {
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-WindowStyle", "Hidden", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow | syscall.CREATE_NEW_PROCESS_GROUP}
	if err := cmd.Start(); err != nil {
		return err
	}
	helperProcess.Lock()
	helperProcess.p = cmd.Process
	helperProcess.Unlock()
	return nil
}

// leave names the file stopHelper removes with the helper: the zip's new
// executable, or nothing.
func leave(file string) {
	helperProcess.Lock()
	helperProcess.left = file
	helperProcess.Unlock()
}

// installSetup runs the downloaded setup silently once the console has exited.
func installSetup(_ context.Context, plan Plan) error {
	leave("")
	return helper(setupScript(os.Getpid(), plan.File, plan.Installation.Path, os.Args[1:], plan.Log))
}

// installZip writes the zip's kubetower.exe beside the running one, and has it
// moved over it once the console has exited.
func installZip(_ context.Context, plan Plan) error {
	next, err := writeFromZip(plan.Installation.Path, plan.File, "kubetower.exe")
	if err != nil {
		return err
	}
	leave(next)
	if err := helper(swapScript(os.Getpid(), next, plan.Installation.Path, os.Args[1:], plan.Log)); err != nil {
		leave("")
		_ = os.Remove(next)
		return err
	}
	return nil
}

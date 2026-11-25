package updater

import (
	"context"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

func init() {
	installers[WindowsSetup] = installSetup
	installers[WindowsZip] = installZip
	abandonHelper = stopHelper
}

// helperProcess is the hidden PowerShell started last, kept so a console that
// did not stop can stop it rather than leave it waiting (abandonHelper).
var helperProcess struct {
	sync.Mutex
	p *os.Process
}

// stopHelper kills the waiting helper, if one was started.
func stopHelper() {
	helperProcess.Lock()
	defer helperProcess.Unlock()
	if helperProcess.p != nil {
		_ = helperProcess.p.Kill()
		_ = helperProcess.p.Release()
		helperProcess.p = nil
	}
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

// installSetup runs the downloaded setup silently once the console has exited.
func installSetup(_ context.Context, plan Plan) error {
	return helper(setupScript(os.Getpid(), plan.File, plan.Installation.Path, os.Args[1:], plan.Log))
}

// installZip writes the zip's kubetower.exe beside the running one, and has it
// moved over it once the console has exited.
func installZip(_ context.Context, plan Plan) error {
	next, err := writeFromZip(plan.Installation.Path, plan.File, "kubetower.exe")
	if err != nil {
		return err
	}
	if err := helper(swapScript(os.Getpid(), next, plan.Installation.Path, os.Args[1:], plan.Log)); err != nil {
		_ = os.Remove(next)
		return err
	}
	return nil
}

package updater

import (
	"context"
	"os"
	"os/exec"
	"syscall"
)

func init() {
	installers[WindowsSetup] = installSetup
	installers[WindowsZip] = installZip
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
	_ = cmd.Process.Release()
	return nil
}

// installSetup runs the downloaded setup silently once the console has exited.
func installSetup(_ context.Context, plan Plan) error {
	return helper(setupScript(os.Getpid(), plan.File, plan.Installation.Path, os.Args[1:]))
}

// installZip writes the zip's kubetower.exe beside the running one, and has it
// moved over it once the console has exited.
func installZip(_ context.Context, plan Plan) error {
	next, err := writeFromZip(plan.Installation.Path, plan.File, "kubetower.exe")
	if err != nil {
		return err
	}
	if err := helper(swapScript(os.Getpid(), next, plan.Installation.Path, os.Args[1:])); err != nil {
		_ = os.Remove(next)
		return err
	}
	return nil
}

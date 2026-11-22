//go:build windows

package terminal

import (
	"fmt"
	"os/exec"
	"syscall"
)

// Opening a real terminal window on Windows: Windows Terminal, a GUI
// application asked for a tab, or else a console program given a console of its
// own, which is what needs the care.

// createNewConsole is CREATE_NEW_CONSOLE. internal/launch/console_windows.go
// gives this process a hidden console, so the `aws` credential plugin's children
// inherit it instead of each opening a visible window. A console child started
// here would inherit that hidden console too and the window would never appear;
// this flag gives it a new one.
const createNewConsole = 0x00000010

func localTerminalSupported() bool { return true }

// launchTerminal opens a window running script, with workdir as its directory.
// Nothing is set in the child's environment: wt.exe has a running Windows
// Terminal create the tab from its own environment, so the session's KUBECONFIG
// comes from the script (see writeLaunchScript).
func launchTerminal(workdir, script string) error {
	shell, err := powerShell()
	if err != nil {
		return err
	}

	// `-NoLogo` drops the version banner. `-NoExit` leaves the operator at a
	// prompt when the script finishes. `-ExecutionPolicy Bypass` because the policy
	// applies to `-File` scripts and would refuse on an AllSigned machine; this file
	// was just written by this process, 0600, in a directory only this account
	// reads.
	args := []string{"-NoLogo", "-NoExit", "-ExecutionPolicy", "Bypass", "-File", script}

	// Windows Terminal first: the operator's own profile, font, scrollback and
	// key bindings are the reason for a real terminal.
	if wt, err := exec.LookPath("wt.exe"); err == nil {
		full := append([]string{"--title", "KubeTower", "-d", workdir, shell}, args...)
		cmd := exec.Command(wt, full...)
		if err := cmd.Start(); err == nil {
			// wt.exe hands the tab over and exits at once; reaped so it is not left a
			// zombie.
			go func() { _ = cmd.Wait() }()
			return nil
		}
		// Fall through rather than fail: a working PowerShell window beats an error
		// naming a terminal the operator never asked for.
	}

	cmd := exec.Command(shell, args...)
	cmd.Dir = workdir
	// NoInheritHandles, or the window opens and closes: os/exec passes standard
	// handles with STARTF_USESTDHANDLES, and with nil Stdin/Stdout/Stderr those are
	// the NUL device, so PowerShell would write into NUL and -NoExit would read EOF
	// and exit. With this Go omits the flag and the child takes the new console's
	// handles. Reached only on a Windows without Windows Terminal, so this is
	// reasoning, not verified behavior.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags:    createNewConsole,
		NoInheritHandles: true,
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("opening a terminal window: %w", err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// powerShell finds a shell to run the session script: PowerShell 7, then
// Windows PowerShell, which every Windows has. Not cmd.exe, which cannot run the
// script and would fill the window with syntax errors.
func powerShell() (string, error) {
	for _, candidate := range []string{"pwsh.exe", "powershell.exe"} {
		if path, err := exec.LookPath(candidate); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("neither pwsh.exe nor powershell.exe is on PATH, so there is no shell to open")
}

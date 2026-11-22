//go:build windows

package terminal

import "os/exec"

// resolveDefaultShell picks the shell a Windows session lands in; the POSIX
// default's path cannot exist there.
//
// PowerShell 7 first, then Windows PowerShell, which every Windows has, then
// cmd.exe, reached only on a machine with neither and the least verified path.
// localterm_windows.go's powerShell() uses the same order but refuses cmd.exe,
// since it runs a PowerShell script file; here a bare prompt is still a prompt.
//
// `-NoProfile` is the analogue of `--norc --noprofile`, for both its reasons:
//
//   - a profile that sets its own prompt (Oh My Posh, Starship) takes the
//     cluster name off the screen;
//   - the read-only guardrail: sessionEnv prepends the override directory to
//     PATH, which wins only if nothing re-points it later, and a profile that
//     appends to `$env:PATH` would put the real kubectl back in front (see
//     defaultSessionShell in terminal.go).
//
// `-NoLogo` drops the version banner. `cmd.exe /D` skips the AutoRun command in
// `HKCU\Software\Microsoft\Command Processor`, cmd's nearest thing to a profile
// and exactly the hook that could re-point PATH.
func resolveDefaultShell() []string {
	for _, candidate := range []string{"pwsh.exe", "powershell.exe"} {
		if path, err := exec.LookPath(candidate); err == nil {
			return []string{path, "-NoLogo", "-NoProfile"}
		}
	}
	// Falls back to a bare `cmd.exe`: there is nothing further to try, and a
	// session that fails says so in the pane, which beats New() refusing and taking
	// the pod shell, node shell and port forwards down with it.
	if path, err := exec.LookPath("cmd.exe"); err == nil {
		return []string{path, "/D"}
	}
	return []string{"cmd.exe", "/D"}
}

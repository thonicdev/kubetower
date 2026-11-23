package updater

import (
	"fmt"
	"strings"
)

// The Windows installers hand the work to a hidden PowerShell that waits for
// this process to exit: a running .exe can be neither replaced nor deleted,
// and the per-user setup refuses (silently, `/SD IDCANCEL`) while it runs.
// Composed here, without a build tag, so the scripts are tested everywhere.

// psQuote is a PowerShell single-quoted literal: nothing in it is expanded,
// and a quote is written twice.
func psQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// psArgs is `-ArgumentList` for the restarted console, or nothing.
func psArgs(args []string) string {
	if len(args) == 0 {
		return ""
	}
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = psQuote(a)
	}
	return " -ArgumentList " + strings.Join(quoted, ",")
}

// setupScript waits for pid, runs the per-user setup silently and waits for
// it, then starts KubeTower again from where the setup put it. A setup that
// failed leaves the old install, which is started again all the same.
func setupScript(pid int, setup, exe string, args []string) string {
	return fmt.Sprintf("$ErrorActionPreference='Continue'; Wait-Process -Id %d -ErrorAction SilentlyContinue; "+
		"Start-Process -FilePath %s -ArgumentList '/S' -Wait; Start-Process -FilePath %s%s",
		pid, psQuote(setup), psQuote(exe), psArgs(args))
}

// swapScript waits for pid, moves the new executable over the old one, then
// starts it. The new file was written beside the old one, on its volume.
func swapScript(pid int, next, exe string, args []string) string {
	return fmt.Sprintf("$ErrorActionPreference='Continue'; Wait-Process -Id %d -ErrorAction SilentlyContinue; "+
		"Move-Item -LiteralPath %s -Destination %s -Force; Start-Process -FilePath %s%s",
		pid, psQuote(next), psQuote(exe), psQuote(exe), psArgs(args))
}

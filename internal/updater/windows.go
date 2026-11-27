package updater

import (
	"errors"
	"fmt"
	"strings"
)

// The Windows installers hand the work to a hidden PowerShell that waits for
// this process to exit: a running .exe can be neither replaced nor deleted,
// and the per-user setup refuses (silently, `/SD IDCANCEL`) while it runs.
// Composed here, without a build tag, so the scripts are tested everywhere.

// psQuote is a PowerShell single-quoted literal: nothing in it is expanded,
// and a quote is written twice.
// PowerShell also ends such a literal on the typographic quotes U+2018 to
// U+201B (C:\Users\O’Neil), so each of them is written twice too.
func psQuote(s string) string { return "'" + psQuotes.Replace(s) + "'" }

var psQuotes = strings.NewReplacer(
	"'", "''",
	"\u2018", "\u2018\u2018",
	"\u2019", "\u2019\u2019",
	"\u201a", "\u201a\u201a",
	"\u201b", "\u201b\u201b",
)

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

// psLog opens a helper script: its steps are appended, stamped, to the
// install log (Plan.Log). A log that cannot be written never stops the update.
// What it writes is the script's own words, paths and exit codes.
func psLog(log string) string {
	return "$ErrorActionPreference='Continue'; $kt_log=" + psQuote(log) + "; " +
		"function L($m) { try { Add-Content -LiteralPath $kt_log -Value ((Get-Date).ToUniversalTime().ToString('o') + ' helper: ' + $m) } catch {} }; "
}

// setupScript waits for pid, runs the per-user setup silently into the folder
// this KubeTower runs from, and waits for it, then starts KubeTower again from
// there. `/D=` names that folder, last and unquoted as NSIS wants it (Windows
// PowerShell joins -ArgumentList with spaces): otherwise the setup installs
// where the registry says, the old exe starts again and the update is offered
// forever. A setup that failed leaves the old install, started again all the same.
//
// Every step goes to the install log: the wait, the setup's arguments and its
// exit code, the restart - so a real run that did not update says where it
// stopped (#96).
func setupScript(pid int, setup, exe string, args []string, log string, restart bool) string {
	dir := "/D=" + windowsDir(exe)
	return psLog(log) + fmt.Sprintf("L %s; Wait-Process -Id %d -ErrorAction SilentlyContinue; "+
		"L %s; try { $p = Start-Process -FilePath %s -ArgumentList '/S',%s -Wait -PassThru; L ('setup exit code ' + $p.ExitCode) } "+
		"catch { L ('setup did not run: ' + $_.Exception.Message) }",
		psQuote(fmt.Sprintf("waiting for KubeTower (pid %d) to exit", pid)), pid,
		psQuote("KubeTower exited; running "+setup+" /S "+dir), psQuote(setup), psQuote(dir)) +
		startScript(exe, args, restart)
}

// startScript is the helper's last step: start the new build, or, where
// KubeTower is quitting (InstallOnQuit), say that the next start runs it.
func startScript(exe string, args []string, restart bool) string {
	if !restart {
		return "; L 'installed on quit; nothing started'"
	}
	return fmt.Sprintf("; L %s; try { Start-Process -FilePath %s%s } catch { L ('restart failed: ' + $_.Exception.Message) }",
		psQuote("starting "+exe), psQuote(exe), psArgs(args))
}

// windowsDir is the folder of a Windows path, composed on any platform (the
// scripts are tested everywhere, where filepath does not split on `\`).
// A drive's root keeps its backslash: `C:\`.
func windowsDir(path string) string {
	i := strings.LastIndexAny(path, `\/`)
	switch {
	case i < 0:
		return path
	case i > 0 && path[i-1] == ':':
		return path[:i+1]
	}
	return path[:i]
}

// errNoStop: internal/launch gave no way to stop, so no helper is started.
var errNoStop = errors.New("this console cannot stop itself to finish the update")

// finish starts the helper script, then stops this process. Without a way to
// stop it starts nothing: a helper left waiting on a console that never exits
// would later move a deleted file, or run the setup unasked.
func finish(script string, start func(string) error, stop func()) error {
	if stop == nil {
		return errNoStop
	}
	if err := start(script); err != nil {
		return err
	}
	stop()
	return nil
}

// moveAttempts is how often the zip's helper tries to move the new
// executable over the old one.
const moveAttempts = 10

// swapScript waits for pid, moves the new executable over the old one, then
// starts it. The new file was written beside the old one, on its volume.
func swapScript(pid int, next, exe string, args []string, log string, restart bool) string {
	// The move is tried moveAttempts times, half a second apart: just after
	// KubeTower exits, an antivirus or the indexer may still hold the old
	// executable (#178, C14.4).
	return psLog(log) + fmt.Sprintf("L %s; Wait-Process -Id %d -ErrorAction SilentlyContinue; "+
		"L %s; $moved = $false; for ($try = 1; $try -le %d -and -not $moved; $try++) { "+
		"try { Move-Item -LiteralPath %s -Destination %s -Force -ErrorAction Stop; $moved = $true; L 'moved' } "+
		"catch { L ('move failed (try ' + $try + '): ' + $_.Exception.Message); Start-Sleep -Milliseconds 500 } }",
		psQuote(fmt.Sprintf("waiting for KubeTower (pid %d) to exit", pid)), pid,
		psQuote("KubeTower exited; moving "+next+" over "+exe), moveAttempts, psQuote(next), psQuote(exe)) +
		startScript(exe, args, restart)
}

// setupHelper is the setup's helper script for plan: it starts the new build
// afterwards, except while KubeTower quits (InstallOnQuit).
func setupHelper(plan Plan, pid int, args []string) string {
	return setupScript(pid, plan.File, plan.Installation.Path, args, plan.Log, !plan.Quit)
}

// swapHelper is the zip's helper script for plan, as setupHelper.
func swapHelper(plan Plan, next string, pid int, args []string) string {
	return swapScript(pid, next, plan.Installation.Path, args, plan.Log, !plan.Quit)
}

// handOver starts the helper the way the moment asks: while KubeTower quits
// (InstallOnQuit) quitting starts it and asks nothing more, since the process
// is exiting already; otherwise stopping starts it and stops this process.
func handOver(plan Plan, script string, stopping, quitting func(string) error) error {
	if plan.Quit {
		return quitting(script)
	}
	return stopping(script)
}

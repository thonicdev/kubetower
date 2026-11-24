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

// setupScript waits for pid, runs the per-user setup silently into the folder
// this KubeTower runs from, and waits for it, then starts KubeTower again from
// there. `/D=` names that folder, last and unquoted as NSIS wants it (Windows
// PowerShell joins -ArgumentList with spaces): otherwise the setup installs
// where the registry says, the old exe starts again and the update is offered
// forever. A setup that failed leaves the old install, started again all the same.
func setupScript(pid int, setup, exe string, args []string) string {
	return fmt.Sprintf("$ErrorActionPreference='Continue'; Wait-Process -Id %d -ErrorAction SilentlyContinue; "+
		"Start-Process -FilePath %s -ArgumentList '/S',%s -Wait; Start-Process -FilePath %s%s",
		pid, psQuote(setup), psQuote("/D="+windowsDir(exe)), psQuote(exe), psArgs(args))
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

// swapScript waits for pid, moves the new executable over the old one, then
// starts it. The new file was written beside the old one, on its volume.
func swapScript(pid int, next, exe string, args []string) string {
	return fmt.Sprintf("$ErrorActionPreference='Continue'; Wait-Process -Id %d -ErrorAction SilentlyContinue; "+
		"Move-Item -LiteralPath %s -Destination %s -Force; Start-Process -FilePath %s%s",
		pid, psQuote(next), psQuote(exe), psQuote(exe), psArgs(args))
}

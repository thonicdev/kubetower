package launch

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestTheWindowsBinaryGivesTheTerminalBack is a pairing test, and the pair is the
// whole point.
//
// `cmd/ktbuild` links the Windows target `-H windowsgui` so that the Start-menu
// shortcut opens an application rather than an application with a cmd window
// behind it. That flag alone **detaches the process from the terminal that
// started it**: `kubetower.exe --version` typed into PowerShell then prints
// nothing at all — no output, no error, a command that appears to do nothing.
// `console_windows.go` is the other half, and the two only work together.
//
// So this asserts them as one fact. Removing either on its own is the change that
// looks harmless in review: dropping the flag brings back the cmd window nobody
// wanted, and dropping AttachConsole makes every command-line use of the binary
// silent.
//
// The build tool is every build path's but the image's (the Dockerfile links
// its Linux binary itself): `task build:api`, `task build:setup`,
// `ci.yml`'s `platforms` job and `release.yml` all call it. It fails rather
// than skipping: the tool is in the tree, the image's build context included.
func TestTheWindowsBinaryGivesTheTerminalBack(t *testing.T) {
	tool, err := os.ReadFile(filepath.Join("..", "..", "cmd", "ktbuild", "main.go"))
	if err != nil {
		t.Fatalf("cmd/ktbuild/main.go is where the Windows link flags live, and it is missing: %v", err)
	}
	subsystem := strings.Contains(string(tool), `"-H windowsgui"`)

	source, err := os.ReadFile("console_windows.go")
	if err != nil {
		t.Fatalf("console_windows.go is missing: %v", err)
	}
	attaches := strings.Contains(string(source), "AttachConsole")

	switch {
	case subsystem && !attaches:
		t.Error("cmd/ktbuild links -H windowsgui and console_windows.go no longer attaches to " +
			"the parent console. Every command-line use of the binary — --version, --help, the " +
			"startup log — now prints into nothing, silently")
	case !subsystem && attaches:
		t.Error("console_windows.go still reattaches to the parent console but cmd/ktbuild no " +
			"longer links -H windowsgui, so the Start-menu shortcut opens a cmd window behind " +
			"the application again. If that is deliberate, say so here")
	}
}

// TestTheConsoleAnswerIsStable, which is as much as this can honestly assert.
//
// The property that matters is that the answer is "is there a console", not "did
// AttachConsole return" — the two differ under `go run` and in any build not
// linked -H windowsgui, where this process already owns a console and
// AttachConsole therefore **fails** with ERROR_ACCESS_DENIED, a process being
// allowed only one. Reading that failure as "no console" would send the log to a
// file on precisely the runs somebody is watching the terminal.
//
// **It cannot be asserted directly here.** `go test` need not have a console:
// under a CI harness or an automated shell the test binary is started with its
// output on pipes and no console at all, so asserting one would fail on a
// correct implementation.
//
// What is left is real: the answer must not move between two calls — a second
// call is where the ERROR_ACCESS_DENIED misreading shows up — and off Windows,
// where there is no GUI subsystem to be detached by, it must stay true.
func TestTheConsoleAnswerIsStable(t *testing.T) {
	previous := consoleAttached
	t.Cleanup(func() { consoleAttached = previous })

	initConsole()
	first := consoleAttached
	initConsole()
	if consoleAttached != first {
		t.Errorf("initConsole answered %v and then %v. On Windows that is AttachConsole's "+
			"ERROR_ACCESS_DENIED — refused because a console is already owned — being read as "+
			"an answer rather than as the confirmation it is", first, consoleAttached)
	}

	if runtime.GOOS != "windows" && !consoleAttached {
		t.Error("initConsole cleared consoleAttached off Windows, where nothing links " +
			"-H windowsgui and there is no console to lose. firstrun.go reads this flag to " +
			"decide whether it can show a drawn password at all, so a false here turns every " +
			"first run into a refusal")
	}
}

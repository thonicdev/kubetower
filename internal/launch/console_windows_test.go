//go:build windows

package launch

import (
	"os"
	"strings"
	"testing"
)

// TestARedirectIsNotReplacedByTheConsole.
//
// Pointing os.Stdout and os.Stderr at CONOUT$ unconditionally once attached
// would throw away whatever the parent set up: `kubetower --version >
// version.txt` would write to the console and leave an empty file, with an
// exit code of 0 — what output going somewhere else looks like rather than
// what a failing command looks like.
//
// `inherited` is the discrimination that prevents it, and this is the assertion that
// keeps it: under `go test` the standard handles are real — a pipe, a file, or a
// console — so they must all be recognized as inherited and left alone.
//
// The complementary case, a NULL handle from a double-click, cannot be
// constructed here: Go wraps it into an *os.File whose writes go nowhere and
// report no error, and there is no way to obtain one from a process that has real
// handles. The nil guard below is the closest reachable neighbor, and it is
// worth having on its own — `inherited` is called during startup before anything
// has checked that these are non-nil.
func TestARedirectIsNotReplacedByTheConsole(t *testing.T) {
	for name, handle := range map[string]*os.File{"stdout": os.Stdout, "stderr": os.Stderr} {
		if !inherited(handle) {
			t.Errorf("inherited(%s) is false under `go test`, where the handle is a real pipe or "+
				"file. initConsole would point it at CONOUT$ and every redirect this binary is "+
				"used with would silently write to a console instead of the file asked for", name)
		}
	}
	if inherited(nil) {
		t.Error("inherited(nil) is true, so a startup path with no handle at all would be left " +
			"pointing at nothing rather than at the console it just attached to")
	}
}

// TestTheGUIBuildStillGivesItsChildrenAConsole guards the worst defect this
// file could produce, and it is a source assertion because the behavior cannot
// be provoked from inside `go test`.
//
// A console process launched by a parent that has **no console** gets a new one
// allocated, with a visible window. KubeTower reaches an EKS cluster by running
// its kubeconfig's `exec` block — `aws eks get-token`, a console program — so
// linking `-H windowsgui` to remove the one cmd window behind the application
// would create one **per credential fetch**, on a machine with a dozen EKS
// contexts.
//
// The pairing this asserts: **if this binary can lose its console, it must make
// itself an invisible one.** Under `go test` the process already has real
// handles, so `hiddenConsole` is never reached and there is nothing to observe at
// runtime — which is exactly why it would be deleted as dead code by somebody
// tidying up.
func TestTheGUIBuildStillGivesItsChildrenAConsole(t *testing.T) {
	source, err := os.ReadFile("console_windows.go")
	if err != nil {
		t.Fatalf("console_windows.go is missing: %v", err)
	}
	text := string(source)

	for _, needed := range []string{"AllocConsole", "ShowWindow", "hiddenConsole()"} {
		if !strings.Contains(text, needed) {
			t.Errorf("console_windows.go no longer uses %s. Without it, a launch with no console "+
				"(the Start-menu shortcut) opens a cmd window for every `aws eks get-token` this "+
				"process runs — one per EKS context, every time the credential is refreshed", needed)
		}
	}

	// And the half that makes it a *hidden* console rather than a second cmd
	// window: allocating one without hiding it is the same defect with an extra
	// step.
	allocAt := strings.Index(text, "procAllocConsole.Call()")
	hideAt := strings.Index(text, "procShowWindow.Call(")
	if allocAt >= 0 && hideAt >= 0 && hideAt < allocAt {
		t.Error("the console is hidden before it is allocated, so the window it opens is never " +
			"hidden at all")
	}

	// The console it allocates must not be reported as a channel to a person.
	// Treating it as one sends the log and the first-run password to a window
	// nobody can see, which is worse than either failure above.
	if strings.Contains(text, "hiddenConsole()\n\t\tconsoleAttached = true") {
		t.Error("consoleAttached is set true around the hidden console. Nobody can read it, so " +
			"the log and the drawn password would go to a window that is not on screen")
	}
}

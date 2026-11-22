//go:build windows

package launch

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// A WebView2 that fails takes the window, and never the console.
//
// `openWindow` promises this — "Nothing here ever takes the server down by
// failing. A window that could not be opened is cosmetic; a console that
// stopped because of it would not be." The path below it is the hard one: `go-webview2`
// dereferences a nil pointer inside `CreateCoreWebView2ControllerCompleted`, a
// callback the browser runtime invokes *after* construction has returned a
// usable-looking view. That runs on the main goroutine, in `main.main`, where an
// unrecovered panic is the whole process — a console serving 5823 correctly
// would die because a window could not be drawn.
//
// The guard is a function taking a closure precisely so this test can exist:
// `nativeWindow` constructs a real WebView2 and blocks on its message loop, so
// a test that called it would open a window on the build machine and never
// return. That is why the guard is a function and not a defer — a guard whose
// only evidence is the comment above it has not been established.
func TestAFailedWebView2LosesTheWindowAndNotTheConsole(t *testing.T) {
	for _, tc := range []struct {
		name  string
		draw  func() bool
		panic bool
	}{
		{
			// The case that kills the process. A panic value, not an error return,
			// because there is no error return to have.
			name:  "a panic out of the runtime is not the caller's problem",
			draw:  func() bool { panic("nil pointer dereference in CreateCoreWebView2Controller") },
			panic: true,
		},
		{
			// The shape the real one takes: a nil dereference rather than a
			// string, so the recover is holding a `runtime.Error` and the log
			// line's `%v` has to survive it.
			name: "a genuine nil dereference is caught the same way",
			draw: func() bool {
				var view *struct{ ok bool }
				return view.ok
			},
			panic: true,
		},
		{
			// And the guard must not invent a failure. A window that opened has
			// to keep saying so, or `openWindow` draws a browser window on top of
			// a native one that is already there.
			name: "a window that opened still reports that it opened",
			draw: func() bool { return true },
		},
		{
			// The honest false: WebView2 absent. It must stay false and must not be
			// turned into a panic or a true by the wrapper.
			name: "a window that declined still reports that it declined",
			draw: func() bool { return false },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The assertion that matters most is that this line is reached at all: an
			// unrecovered panic here fails the test by taking the test binary down,
			// which is exactly what it does to the console.
			got := guardWindow("http://127.0.0.1:5823/", tc.draw)

			switch {
			case tc.panic && got:
				t.Error("guardWindow recovered a panic and then reported that the window opened. " +
					"openWindow would take that as success and draw nothing, leaving the operator " +
					"with a console that is running and no window anywhere")
			case !tc.panic:
				if want := tc.draw(); got != want {
					t.Errorf("guardWindow reported %v where the window itself said %v — the guard "+
						"must pass the answer through, not decide it", got, want)
				}
			}
		})
	}
}

// TestADamagedRuntimeIsRefusedBeforeTheLibraryIsCalled.
//
// "A WebView2 installation that is present but damaged" is precisely the case
// `go-webview2` reports with `log.Fatal` -- `os.Exit(1)`, no deferred function,
// nothing for `guardWindow` to recover. A console serving 5823 with terminals
// attached would simply stop.
//
// So the decision is made in front of the library, and this is it. The arms are
// unreachable through the real loader on this host, which has a healthy
// WebView2 -- which is exactly why `webView2Usable` takes the loader's two
// return values instead of calling it.
func TestADamagedRuntimeIsRefusedBeforeTheLibraryIsCalled(t *testing.T) {
	cases := []struct {
		name    string
		version string
		err     error
		wantOK  bool
		says    string
	}{
		{"a healthy runtime", "121.0.2277.98", nil, true, ""},
		{"the loader will not answer", "", errors.New("Access is denied."), false, "could not be read"},
		{"a version that came back with an error is still refused", "121.0.0.0",
			errors.New("Access is denied."), false, "could not be read"},
		{"no runtime at all", "", nil, false, "not installed"},
		{"a blank version string", "   ", nil, false, "not installed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			note, ok := webView2Usable(tc.version, tc.err)
			if ok != tc.wantOK {
				t.Fatalf("webView2Usable(%q, %v) ok = %v, want %v", tc.version, tc.err, ok, tc.wantOK)
			}
			if ok {
				if note != "" {
					t.Errorf("a healthy runtime produced a complaint: %q", note)
				}
				return
			}
			if !strings.Contains(note, tc.says) {
				t.Errorf("the refusal does not say %q: %q", tc.says, note)
			}
			// It has to name the fallback's existence somewhere, or the operator
			// reads a refusal as the console being broken. The caller supplies
			// the "falling back" half, so what is required here is that the
			// sentence ends cleanly enough to be joined to it.
			if !strings.HasSuffix(strings.TrimSpace(note), ".") {
				t.Errorf("the note does not end in a full stop, so the caller's sentence runs "+
					"into it: %q", note)
			}
		})
	}
}

// TestTheGuardDoesNotClaimToCoverAnExit.
//
// The comment is the artifact here: a guard is worth exactly the failures it
// can observe, and this one must not claim to cover "a WebView2 installation
// that is present but damaged" while `log.Fatal` walks straight past it. If
// somebody rewrites that paragraph into a broad promise, the pre-flight above
// starts looking redundant and gets deleted next.
func TestTheGuardDoesNotClaimToCoverAnExit(t *testing.T) {
	source, err := os.ReadFile("nativewindow_windows.go")
	if err != nil {
		t.Fatalf("reading nativewindow_windows.go: %v", err)
	}
	text := string(source)

	if !strings.Contains(text, "webView2Usable(webviewloader.GetInstalledVersion())") {
		t.Error("nothing asks the loader before the library is constructed, so a damaged " +
			"runtime exits the process again")
	}
	if !strings.Contains(text, "log.Fatal") {
		t.Error("the comments no longer name log.Fatal as what the recover cannot see; " +
			"without that, the pre-flight reads as redundant and gets deleted")
	}
}

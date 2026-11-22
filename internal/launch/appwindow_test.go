package launch

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/auth"
)

// TestABindAddressIsNotADestination is the one piece of arithmetic in this file
// and the one that would fail silently.
//
// ":5823" and "0.0.0.0:5823" say "listen on every interface"; neither is
// somewhere a client can connect to. Handing "http://0.0.0.0:5823/" to a browser
// is undefined — some stacks route it to loopback and some refuse it — so the
// window would open onto an error page on exactly the machines that behave
// correctly. The container sets KT_LISTEN=:5823, so this is not a corner.
func TestABindAddressIsNotADestination(t *testing.T) {
	cases := []struct{ listen, want string }{
		{"127.0.0.1:5823", "127.0.0.1:5823"},
		{":5823", "127.0.0.1:5823"},
		{"0.0.0.0:5823", "127.0.0.1:5823"},
		{"[::]:5823", "127.0.0.1:5823"},
		{"::", "::"}, // unparseable as host:port; handed back rather than guessed at
		{"192.0.2.10:5823", "192.0.2.10:5823"},
	}
	for _, tc := range cases {
		t.Run(tc.listen, func(t *testing.T) {
			if got := dialAddress(tc.listen); got != tc.want {
				t.Errorf("dialAddress(%q) = %q, want %q", tc.listen, got, tc.want)
			}
		})
	}
}

func TestTheURLPutInFrontOfAPersonIsOneTheyCanOpen(t *testing.T) {
	if got := consoleURL(":5823"); got != "http://127.0.0.1:5823/" {
		t.Errorf("consoleURL(\":5823\") = %q, want a loopback URL — a browser cannot open 0.0.0.0", got)
	}
}

// TestTheWindowWaitsForTheServer. Opening onto a connection refused is the whole
// reason this waits at all, and the failure it guards is a race that shows up on
// a slow machine and never on the developer's.
func TestTheWindowWaitsForTheServer(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("could not take a port: %v", err)
	}
	defer listener.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := waitForListener(ctx, listener.Addr().String()); err != nil {
		t.Errorf("a listening socket was reported unreachable: %v", err)
	}
}

// TestGivingUpNamesTheAddress. Ten seconds is the cap; the test uses a canceled
// context so it does not spend them, and asserts that giving up is an error
// rather than a hang — the server is fine either way and the operator needs the
// URL, which is why appWindow logs it on this path.
func TestGivingUpIsNotAHang(t *testing.T) {
	// Port 1 on loopback, which nothing listens on and which refuses fast.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := waitForListener(ctx, "127.0.0.1:1")
	if err == nil {
		t.Fatal("something answered on port 1")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("gave up after %v; the context was canceled at 300ms and this must honor it, "+
			"or Ctrl-C during startup waits for a browser that is never coming", elapsed)
	}
}

// TestTheAppFlagIsWhatMakesTheWindowBare. --app is the entire mechanism: without
// it Chromium opens an ordinary tab, which is what this feature exists not to
// do. Asserted on the argv rather than trusted, because a flag that stops
// working produces a window that looks nearly right.
func TestTheAppFlagIsWhatMakesTheWindowBare(t *testing.T) {
	_, args, ok := chromiumAppCommand("http://127.0.0.1:5823/")
	if !ok {
		t.Skip("no Chromium-based browser on this machine, so there is no argv to check")
	}
	if len(args) == 0 || !strings.HasPrefix(args[0], "--app=") {
		t.Fatalf("argv = %v; the first argument must be --app=<url> or the browser opens a tab", args)
	}
	if !strings.HasSuffix(args[0], "http://127.0.0.1:5823/") {
		t.Errorf("argv[0] = %q, which does not carry the URL", args[0])
	}
	// No --user-data-dir, and this is the assertion for it: a separate profile
	// means a separate cookie jar, and the session here *is* a cookie, so the
	// operator would log in again on every launch.
	for _, a := range args {
		if strings.HasPrefix(a, "--user-data-dir") {
			t.Errorf("argv carries %q. A separate profile logs the operator out on every launch, "+
				"because the session is a cookie", a)
		}
	}
}

// TestEveryPlatformHasSomewhereToLook. A candidate list that comes back empty on
// one GOOS is a feature that silently degrades to the default browser there,
// which is the kind of thing nobody notices until they are on that machine.
func TestEveryPlatformHasSomewhereToLook(t *testing.T) {
	if got := chromiumCandidates(); len(got) == 0 {
		t.Error("no browser candidates for this platform")
	}
}

// TestHelpNamesTheFlagsThatExist. A usage string is documentation that ships in
// the binary, and DEFINITION-OF-DONE §5 is about documentation that is wrong
// being worse than absent — here it is worse still, because it is the first
// thing a person reads.
func TestHelpNamesTheFlagsThatExist(t *testing.T) {
	help := usage()
	for _, flag := range []string{"--app", "--version", "--help", "--reset-password"} {
		if !strings.Contains(help, flag) {
			t.Errorf("--help does not mention %s", flag)
		}
	}
	if !strings.Contains(help, "KT_LISTEN") || !strings.Contains(help, ".env.example") {
		t.Error("--help does not say where the rest of the configuration lives, which is the " +
			"question a person asks next")
	}

	// And the question people actually ask: "how do I get my default
	// password back?". There is no default one —
	// the first run draws it and keeps only its hash — and --help is where
	// somebody locked out looks before they look anywhere else.
	if !strings.Contains(help, "credentials.json") {
		t.Error("--help does not name the file the password lives in, so the first thing an " +
			"operator who lost it reads does not tell them where to look")
	}
	if !strings.Contains(help, "no default") {
		t.Error("--help does not say there is no default password. That is the question people " +
			"ask, and the honest answer is what stops them hunting for one")
	}
}

// TestWindowsLooksSomewhereOtherThanPATH is worth its length because the defect
// it guards produces no error.
//
// Windows does not put browsers on PATH, so with bare names — "msedge.exe" and
// friends — exec.LookPath finds none of them even while Edge is running, and
// appWindow falls through to the default browser: the console opens as a
// **tab** in the window the operator already has, which is the one outcome
// --app exists to avoid, and the log line says no Chromium-based browser is
// installed, which is false.
//
// The property, and not the paths: bare names alone cannot find a Windows
// browser, so the list must contain absolute ones.
func TestWindowsLooksSomewhereOtherThanPATH(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the candidate list is per-GOOS and this asserts the Windows one")
	}
	absolute := 0
	for _, c := range chromiumCandidates() {
		if filepath.IsAbs(c) {
			absolute++
		}
	}
	if absolute == 0 {
		t.Fatal("every Windows candidate is a bare name, and Windows does not put browsers on " +
			"PATH. LookPath will find none of them and the console will open as a tab")
	}

	// And the one that will be there: Edge ships with the OS, so a list that
	// cannot name its install location is a list that finds nothing on a stock
	// machine with no Chrome.
	edge := false
	for _, c := range chromiumCandidates() {
		if strings.Contains(strings.ToLower(c), `edge\application\msedge.exe`) {
			edge = true
		}
	}
	if !edge {
		t.Error("no absolute path for Edge, which is the only browser guaranteed to be present")
	}
}

// TestTheCandidatePathsAreBuiltFromTheEnvironment. A hard-coded C:\Program Files
// is wrong on every machine that moved it, and moving it is what an enterprise
// image does.
func TestTheCandidatePathsAreBuiltFromTheEnvironment(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-only")
	}
	root := os.Getenv("ProgramFiles(x86)")
	if root == "" {
		t.Skip("no ProgramFiles(x86) on this machine")
	}
	found := false
	for _, c := range chromiumCandidates() {
		if strings.HasPrefix(c, root) {
			found = true
		}
	}
	if !found {
		t.Errorf("no candidate under %q, so the paths are not coming from the environment", root)
	}
}

// TestADoubleClickOpensSomething is the guard on the trap `-H windowsgui`
// introduces.
//
// A GUI-subsystem binary launched with no arguments has no console *and* no
// window: it serves on 5823 invisibly, and the only way to stop it is Task
// Manager. A console-subsystem binary at least produces a cmd window with a log
// in it, so linking `-H windowsgui` would silently make this launch invisible,
// on the most ordinary gesture there is.
//
// The other half is asserted here too, and it is the one a fix would break: a
// deliberate `kubetower` in a terminal must **not** open a window. That is what
// the container runs, and it is what somebody watching the log wants.
//
// **And the row for this table's fourth column**: a macOS bundle launch.
// `consoleAttached` is a hardcoded `true` off Windows, so the terminal case and
// the Finder case would be one case and the Finder would lose — a
// `KubeTower.app` that serves on 5823 and opens nothing at all. Both bundle
// rows below fail without the bundle check.
func TestADoubleClickOpensSomething(t *testing.T) {
	cases := []struct {
		name                                        string
		asked, refused, resetting, console, bundled bool
		want                                        bool
	}{
		{"double-click: no console, no flags", false, false, false, false, false, true},
		{"a terminal, no flags: serve and be watched", false, false, false, true, false, false},
		{"--app in a terminal", true, false, false, true, false, true},
		{"--app with no console", true, false, false, false, false, true},
		{"--reset-password from a shortcut opens nothing", false, false, true, false, false, false},
		{"--reset-password in a terminal", false, false, true, true, false, false},

		// The macOS defect. A bundle launch reports a console because nothing
		// off Windows measures one, so without the bundle input these two are
		// indistinguishable from the "serve and be watched" row above.
		{"KubeTower.app from the Finder", false, false, false, true, true, true},
		{"the bundle's own binary, --no-window in a terminal", false, true, false, true, true, false},
		{"--no-window beats --app", true, true, false, false, false, false},
		{"--reset-password inside a bundle still opens nothing", false, false, true, true, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, why := windowDecision(tc.asked, tc.refused, tc.resetting, tc.console, tc.bundled)
			if got != tc.want {
				t.Errorf("windowDecision(asked=%v, refused=%v, resetting=%v, console=%v, bundled=%v) = %v, want %v",
					tc.asked, tc.refused, tc.resetting, tc.console, tc.bundled, got, tc.want)
			}
			// The sentence is not decoration: the reason this function returns one is
			// that a decision made in silence, with no line about the window in the
			// start-up log, leaves nothing to debug from. An empty reason is that
			// defect.
			if strings.TrimSpace(why) == "" {
				t.Error("the decision came with no reason, which is how it was invisible in the log")
			}
			// And it has to say which way it went, because the operator reads
			// the sentence and not the boolean.
			if got && !strings.HasPrefix(why, "opening a window") {
				t.Errorf("a window is opened but the log line says %q", why)
			}
			if !got && !strings.HasPrefix(why, "no window") {
				t.Errorf("no window is opened but the log line says %q", why)
			}
		})
	}
}

// TestABundleLaunchIsRecognisedFromThePath. macOS has nowhere to put `--app`,
// so this path read is the entire signal — and it is the only thing standing
// between a Finder launch and a silent no-window start.
//
// The negative rows matter as much as the positive one. `launchedFromAppBundle`
// forced true would satisfy the case above and open a browser window inside the
// container, which is exactly what the `!console` rule exists to avoid.
func TestABundleLaunchIsRecognisedFromThePath(t *testing.T) {
	cases := []struct {
		name, goos, exe string
		want            bool
	}{
		{"the installed bundle", "darwin",
			"/Users/jean/Applications/KubeTower.app/Contents/MacOS/kubetower", true},
		{"the downloaded bundle, opened from anywhere", "darwin",
			"/Volumes/KubeTower/KubeTower.app/Contents/MacOS/kubetower", true},
		{"a bare binary on a Mac", "darwin", "/usr/local/bin/kubetower", false},
		{"a binary in a directory that merely ends in .app", "darwin",
			"/Users/jean/thing.app/kubetower", false},
		{"Contents/Resources is not the executable slot", "darwin",
			"/Users/jean/KubeTower.app/Contents/Resources/kubetower", false},
		{"a directory called just .app", "darwin", "/Users/jean/.app/Contents/MacOS/kubetower", false},
		{"the same layout on Linux is not a bundle", "linux",
			"/home/jean/KubeTower.app/Contents/MacOS/kubetower", false},
		{"the container", "linux", "/usr/local/bin/kubetower", false},
		{"Windows", "windows", `C:\Users\jean\AppData\Local\KubeTower\kubetower.exe`, false},
		{"no path at all", "darwin", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := launchedFromAppBundle(tc.goos, tc.exe); got != tc.want {
				t.Errorf("launchedFromAppBundle(%q, %q) = %v, want %v", tc.goos, tc.exe, got, tc.want)
			}
		})
	}
}

// TestTheBundleTheInstallerWritesIsTheBundleTheWindowRuleRecognises.
//
// The two halves live in different files and neither is wrong on its own:
// rename the executable in `installbundle.go` and the rule above stops firing,
// silently, on exactly the launch it exists for. This holds them together the
// way `TestTheBuildScriptAndTheBundleWriterAgreeOnTheAppName` holds the name.
func TestTheBundleTheInstallerWritesIsTheBundleTheWindowRuleRecognises(t *testing.T) {
	exe := "/Users/jean/Applications/" + bundleDirName + "/Contents/MacOS/" + bundleExecutableName
	if !launchedFromAppBundle("darwin", exe) {
		t.Fatalf("the installer writes %s, and the window rule does not recognize it as a bundle "+
			"launch — a Finder double-click would serve with no window", exe)
	}
}

// The native window's address carries the launch's one-time code, and the log
// never does.
func TestTheWindowsAddressCarriesTheCodeAndTheLogDoesNot(t *testing.T) {
	const pass = "c2VjcmV0LXRva2VuLWZvci10aGUtd2luZG93LW9ubHk"

	got := windowURL(":5823", pass)
	want := "http://127.0.0.1:5823" + auth.LaunchPath + "?" + auth.LaunchCodeParam + "=" + pass
	if got != want {
		t.Errorf("windowURL = %q; want %q", got, want)
	}
	if logged := loggableURL(got); strings.Contains(logged, pass) || logged != "http://127.0.0.1:5823"+auth.LaunchPath {
		t.Errorf("loggableURL(%q) = %q; want the address without its query", got, logged)
	}

	// No code, and the window opens the console's own address.
	if got := windowURL(":5823", ""); got != consoleURL(":5823") {
		t.Errorf("windowURL with no token = %q; want the console's address %q", got, consoleURL(":5823"))
	}
}

package launch

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/thonicdev/kubetower/internal/auth"
)

// Everything `--app` does when it cannot draw the window itself.
//
// The preferred window is the native one (WebView2 on Windows, WKWebView on
// macOS): a frame this process owns, with no browser needed, through pure-Go
// bindings so the binaries cross-compile without a C compiler. Linux has none
// (nativewindow_other.go says what is established about WebKitGTK).
//
// Where the native window cannot open, `--app` asks the operator's browser for
// one bare window: Chromium's `--app=`, with no tab strip or address bar and its
// own taskbar button. It looks the same but belongs to a browser this process
// does not own, so closing it leaves the server running.
//
// The last rung is an ordinary browser window, for a machine with only Firefox;
// hiding its chrome through a profile would reconfigure somebody else's
// browser.
//
// The permanent version on any platform is Install app from the browser's menu:
// the manifest and service worker are in the binary, so a PWA install gets its
// own window, entry and icon with no second runtime such as Electron or Tauri.

// openWindow puts the console in front of the operator, in the best window this
// machine can give it, and reports whether that window has since been closed.
//
// Three tiers, best first, differing in kind:
//
//  1. A native window this process draws (nativeWindow, Windows and macOS).
//     It blocks until closed and returns true: closing an application's window
//     ends the application.
//  2. A browser window with the browser hidden (Chromium's `--app=`). Returns
//     false at once: closing it must leave the server running.
//  3. An ordinary browser window. Also false.
//
// Failing to open a window never takes the server down.
//
// mint mints a one-time sign-in code (internal/auth/window.go) for the window
// about to open (`native` says which kind), or is nil. It is called once the
// server answers and once per tier tried, so each code's short life starts when
// its window is about to use it. Every tier takes one: with no password on the
// loopback desktop, a window without a code would have no way in. Only the
// native window's code marks its session as the window's.
//
// A browser tier's code never goes on a command line, readable by every local
// account through /proc on Linux: the browser gets the path of a 0600 page that
// carries it (browserTarget).
//
// feed is what a native window reads about the rest of the process
// (windowSources); only tier 1 on macOS reads it.
func openWindow(ctx context.Context, listen string, mint func(native bool) (string, error), feed windowSources) (closed bool) {
	url := consoleURL(listen)

	if err := waitForListener(ctx, listen); err != nil {
		log.Printf("[kubetower] the window was not opened: %v. The console is still at %s", err, url)
		return false
	}

	if drawNative(windowURL(listen, codeFor(mint, true)), feed) {
		return true
	}

	// Say there is no native window here, and why: the lines below describe the
	// window that was opened, not the one that was not. Not on Windows or
	// macOS, where nativeWindow logs its own more specific reason.
	if note := nativeWindowAbsentNote(runtime.GOOS); note != "" {
		log.Printf("[kubetower] %s", note)
	}

	// Each browser tier gets its own code, minted now, never on a command line:
	// the browser is handed a 0600 page that forwards with it, in the state
	// directory or where a snap or Flatpak can read it (confined.go). The log
	// names the bare address.
	browser, ok := findChromium()
	if !ok {
		// Not a failure: a window opens, with a browser around it.
		log.Printf("[kubetower] no Chromium-based browser found, so %s is opening in your default "+
			"browser instead of its own window. Chrome or Edge gives a window with no browser "+
			"around it; so does Install app, from the browser's own menu", url)
		openInDefaultBrowser(browserTarget(listen, mint, defaultBrowserPageDir()))
		return false
	}

	signedIn := browserTarget(listen, mint, chromiumPageDir(browser))
	if err := startProcess(browser, chromiumAppArgs(signedIn)...); err != nil {
		log.Printf("[kubetower] could not start %s (%v), so the console is opening in your default "+
			"browser instead. It is at %s", browser, err, url)
		openInDefaultBrowser(browserTarget(listen, mint, defaultBrowserPageDir()))
		return false
	}

	log.Printf("[kubetower] opened %s in its own window. Closing that window does not stop this "+
		"process — Ctrl-C does. For a permanent entry in your applications, use Install app "+
		"from the browser menu", url)
	return false
}

// drawNative and startProcess are the two ways openWindow reaches outside the
// process, as variables so a test can read every command line a window would be
// given without drawing one or starting a browser.
var (
	drawNative = nativeWindow

	// startProcess starts name with args and does not wait for it: closing the
	// browser's window must not stop the server.
	startProcess = func(name string, args ...string) error {
		cmd := exec.Command(name, args...)
		if err := cmd.Start(); err != nil {
			return err
		}
		go func() { _ = cmd.Wait() }()
		return nil
	}
)

// chromiumAppCommand finds a Chromium-based browser and builds the argv that
// makes it draw one bare window.
//
// The user-data-dir is not overridden: a separate profile would lose the
// operator's extensions and theme and start with an empty cookie jar, meaning a
// sign-in every time. Sharing their profile for a page on 127.0.0.1 is what
// they already had.
func chromiumAppCommand(url string) (string, []string, bool) {
	path, ok := findChromium()
	if !ok {
		return "", nil, false
	}
	return path, chromiumAppArgs(url), true
}

// chromiumAppArgs is the argv that makes a Chromium-based browser draw one
// bare window on url.
func chromiumAppArgs(url string) []string {
	// --app is the point. The other two: no "restore pages?" bar after a crash,
	// and no first-run tab stealing focus.
	return []string{"--app=" + url, "--no-first-run", "--disable-session-crashed-bubble"}
}

// findChromium is the first Chromium-based browser of chromiumCandidates that
// is installed.
func findChromium() (string, bool) {
	for _, candidate := range chromiumCandidates() {
		if path, err := exec.LookPath(candidate); err == nil {
			return path, true
		}
	}
	return "", false
}

// chromiumCandidates is the search order, most-likely-installed first per
// platform.
//
// Browsers on Windows are not on PATH: LookPath finds no msedge.exe even while
// Edge runs, and the fallback would silently open a tab in the operator's
// existing window, the outcome `--app` exists to avoid. The App Paths registry
// key is the exact lookup; this stats the install locations instead, built from
// the environment because `C:\Program Files` can move.
func chromiumCandidates() []string {
	switch runtime.GOOS {
	case "windows":
		// Edge first, on Windows only: every supported build has it. PATH
		// first anyway: an operator who put a browser there meant it.
		out := []string{"msedge.exe", "chrome.exe", "brave.exe", "vivaldi.exe"}
		for _, root := range []string{
			os.Getenv("ProgramFiles(x86)"), // where Edge actually installs, on 64-bit Windows
			os.Getenv("ProgramFiles"),
			os.Getenv("LocalAppData"), // Chrome's per-user install, which needs no admin
		} {
			if root == "" {
				continue
			}
			out = append(out,
				filepath.Join(root, `Microsoft\Edge\Application\msedge.exe`),
				filepath.Join(root, `Google\Chrome\Application\chrome.exe`),
				filepath.Join(root, `BraveSoftware\Brave-Browser\Application\brave.exe`),
				filepath.Join(root, `Vivaldi\Application\vivaldi.exe`),
			)
		}
		return out
	case "darwin":
		// On macOS the bundle is the installation and PATH rarely knows it.
		return []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
		}
	default:
		return []string{"google-chrome", "chromium", "chromium-browser", "microsoft-edge", "brave-browser"}
	}
}

// openInDefaultBrowser is the fallback, and it is per-OS because there is no
// portable way to say "open this".
func openInDefaultBrowser(url string) {
	var err error
	switch runtime.GOOS {
	case "windows":
		// Through cmd's start, because rundll32 mangles a URL with an
		// ampersand. The empty "" is start's title argument: without it start
		// reads the URL as the title and opens nothing.
		err = startProcess("cmd", "/c", "start", "", url)
	case "darwin":
		err = startProcess("open", url)
	default:
		err = startProcess("xdg-open", url)
	}
	if err != nil {
		log.Printf("[kubetower] could not open a browser (%v). The console is at %s", err, loggableURL(url))
	}
}

// waitForListener blocks until the server answers, so the window is not opened
// onto a connection refused.
//
// It dials rather than fetching: a GET without the session cookie would log an
// authentication failure caused by this process talking to itself.
func waitForListener(ctx context.Context, listen string) error {
	deadline := time.Now().Add(10 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", dialAddress(listen), time.Second)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the server did not answer on %s within ten seconds", listen)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// dialAddress turns a listen address into one that can be dialled.
//
// ":5823" and "0.0.0.0:5823" bind every interface and are not destinations;
// loopback is reachable in every case.
func dialAddress(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return listen
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		return net.JoinHostPort("127.0.0.1", port)
	}
	return listen
}

// consoleURL is the address to put in front of a person, which is not always the
// address the process bound.
func consoleURL(listen string) string {
	return "http://" + dialAddress(listen) + "/"
}

// codeFor mints a code for a window about to open, or answers "" (no mint, or a
// failed one). A failure is logged, not a refusal: the window opens on the
// sign-in page.
func codeFor(mint func(native bool) (string, error), native bool) string {
	if mint == nil {
		return ""
	}
	code, err := mint(native)
	if err != nil {
		log.Printf("[kubetower] the window opens on the sign-in page: its one-time sign-in code could "+
			"not be minted (%v)", err)
		return ""
	}
	return code
}

// windowURL is the address the native window opens: auth.LaunchPath with a
// one-time code when there is one, else the console's own address. It is never
// logged as it is: loggableURL drops the query the code is in.
func windowURL(listen, code string) string {
	if code == "" {
		return consoleURL(listen)
	}
	return "http://" + dialAddress(listen) + auth.LaunchPath + "?" +
		url.Values{auth.LaunchCodeParam: {code}}.Encode()
}

// loggableURL is an address as it may be written to the log: scheme, host and
// path, without the query or fragment.
//
// The native window's address carries a one-time sign-in code, and
// kubetower.log reaches bug reports whole. Every line naming the window's
// address goes through this one function, so a new line cannot forget to strip
// it. Untagged, so its test runs on every host.
func loggableURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "the console's address"
	}
	u.RawQuery, u.Fragment, u.User = "", "", nil
	return u.String()
}

// windowDecision decides whether this run opens a window, and says why in a
// sentence the log prints either way.
//
// One function returning both, because the log line is the point: a bundle
// double-click that serves and draws nothing must leave a sentence about the
// window, composed beside the answer.
//
// In order:
//
//   - `--no-window` wins: the bundle's own executable run from a terminal must
//     be able to say "serve and log".
//   - `--app` is the deliberate ask.
//   - A password reset serves nothing, so no window.
//   - An application-bundle launch is a double-click: consoleAttached is assumed
//     true off Windows, so it cannot tell a Finder launch from a terminal.
//   - Otherwise no console means a double-click, and a console means "serve,
//     I am watching the log", as in a container or while debugging.
func windowDecision(asked, refused, resetting, console, bundled bool) (open bool, why string) {
	switch {
	case refused:
		return false, "no window: --no-window was given, so this serves and logs here"
	case asked:
		return true, "opening a window: --app was given"
	case resetting:
		return false, "no window: --reset-password draws a password and stops without serving"
	case bundled:
		return true, "opening a window: this executable is inside an application bundle, so the " +
			"launch came from the Finder, the Dock or Launchpad. Run the binary inside the " +
			"bundle with --no-window to serve and watch the log instead"
	case !console:
		return true, "opening a window: there is no console, so this launch is a double-click"
	default:
		return false, "no window: started from a terminal, so this serves and logs here. --app " +
			"opens one"
	}
}

// launchedFromAppBundle reports whether this executable is the one inside a
// macOS `.app`.
//
// This is the macOS spelling of `--app`, which the Windows shortcut and the
// .desktop entry carry: Info.plist has no arguments key, so the fact is read off
// the path. os.Executable on darwin returns what the kernel executed, the file
// inside the bundle for a Finder, Dock, Launchpad or `open` launch.
//
// LSEnvironment in the plist would only reach bundles built with it; a path read
// also covers an installed bundle whose executable is replaced.
//
// Running the bundle's executable from a terminal therefore opens a window too;
// `--no-window` is the answer, and windowDecision's sentence names it.
//
// Untagged and taking goos, so it is tested on every host.
func launchedFromAppBundle(goos, exe string) bool {
	if goos != "darwin" || exe == "" {
		return false
	}
	// Split on the forward slash, not filepath: this is a macOS path, read on
	// any test host. Backslashes are folded first.
	parts := strings.Split(strings.ReplaceAll(exe, `\`, "/"), "/")
	// .../<Name>.app/Contents/MacOS/<executable>, all four segments checked:
	// a binary in a directory called `x.app`, or in Contents/Resources, is not
	// a bundle launch.
	if len(parts) < 4 {
		return false
	}
	n := len(parts)
	return parts[n-2] == "MacOS" &&
		parts[n-3] == "Contents" &&
		strings.HasSuffix(parts[n-4], ".app") &&
		len(parts[n-4]) > len(".app")
}

// executablePath is os.Executable with the error folded into the empty string.
//
// A path that cannot be determined is not a bundle launch.
func executablePath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return exe
}

// nativeWindowAbsentNote is why `--app` is a browser window on this platform.
//
// Untagged and taking goos, so it is tested on every host.
//
// Empty on Windows and macOS, which have a native window, and where a fallback
// to a browser logs its own, more specific reason.
//
// It must follow which platforms draw a native window: it feeds both the
// start-up log and the `--install` banner, where a stale sentence would sit
// under the window it is wrong about.
func nativeWindowAbsentNote(goos string) string {
	switch goos {
	case "windows", "darwin":
		return ""
	case "linux":
		return "there is no native window on this platform yet, so --app opens a browser " +
			"window with the browser hidden rather than a window this process draws itself. " +
			"The engine exists here (WebKitGTK); no pure-Go binding for it is in this " +
			"binary, and these binaries are cross-compiled with CGO_ENABLED=0. Install app, " +
			"from the browser's own menu, gives the permanent entry with the icon"
	default:
		// Some other Unix: the engine is not named, since naming the wrong one
		// is worse than none.
		return "there is no native window on this platform, so --app opens a browser window " +
			"with the browser hidden. Install app, from the browser's own menu, gives the " +
			"permanent entry with the icon"
	}
}

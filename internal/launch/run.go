// Package launch is the console's whole start-up. `app.Run` calls it, and
// `cmd/kubetower` reaches it only through `app.Run`.
//
// It is not a `package main` so that another binary can start the same console
// with its own extensions: a `main` cannot be imported, and `internal/` cannot
// be imported from another module, so `app` is its public door — one start-up
// path, whichever `main` calls it.
//
// This file is the assembly and nothing else: read the configuration, build the
// pieces, wire them, serve. Every decision worth explaining lives in the package
// that owns it.
//
// The order below is not arbitrary. The router is built first and the front door
// wraps it last, so that a route added later cannot accidentally be registered
// outside the middleware: the terminal and the API cannot be reached
// unauthenticated even when a proxy in front is misconfigured.
package launch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	// The IANA zone database, embedded in the binary (~450 KB).
	//
	// A CronJob's spec.timeZone is an IANA name, and time.LoadLocation resolves
	// one by reading /usr/share/zoneinfo — which the Alpine runtime image does
	// not install, and which Windows does not have at all. Without this import
	// every zoned CronJob's schedule fails to resolve, so the scheduled-jobs
	// page answers "cannot say" for precisely the CronJobs whose author was
	// careful enough to pin a zone. Silent, total, and invisible on a
	// developer's Linux host, which is the worst combination available.
	//
	// Embedded rather than `apk add tzdata`, because the answer has to be the
	// same wherever the binary runs — including the host build, where there is
	// no image to install a package into.
	_ "time/tzdata"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/archive"
	"github.com/thonicdev/kubetower/internal/audit"
	"github.com/thonicdev/kubetower/internal/auth"
	"github.com/thonicdev/kubetower/internal/binpath"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/columns"
	"github.com/thonicdev/kubetower/internal/extras"
	"github.com/thonicdev/kubetower/internal/forward"
	"github.com/thonicdev/kubetower/internal/health"
	"github.com/thonicdev/kubetower/internal/hotbars"
	"github.com/thonicdev/kubetower/internal/identity"
	"github.com/thonicdev/kubetower/internal/kindprefs"
	"github.com/thonicdev/kubetower/internal/logs"
	"github.com/thonicdev/kubetower/internal/metrics"
	"github.com/thonicdev/kubetower/internal/nameplates"
	"github.com/thonicdev/kubetower/internal/recorder"
	"github.com/thonicdev/kubetower/internal/seen"
	"github.com/thonicdev/kubetower/internal/sharedstate"
	"github.com/thonicdev/kubetower/internal/shellenv"
	"github.com/thonicdev/kubetower/internal/statefile"
	"github.com/thonicdev/kubetower/internal/terminal"
	"github.com/thonicdev/kubetower/internal/update"
	"github.com/thonicdev/kubetower/internal/updater"
	"github.com/thonicdev/kubetower/internal/web"
	"github.com/thonicdev/kubetower/safego"
)

// defaultListen is where the console answers when nothing says otherwise.
//
// 5823 stays out of the 8000–8100 band, where local development servers
// crowd and a console would some day fail to start with a bind error naming
// nothing an operator can act on. 5823 is `KUBE` on a telephone keypad; no port
// can be guaranteed unused everywhere, and this does not claim to be.
//
// Loopback and not `:5823`, deliberately: this process holds administrator
// credentials for production clusters, so the default must be the machine itself
// rather than every interface. A container overrides it with `:5823`, the only
// bind Docker can publish.
const defaultListen = "127.0.0.1:5823"

// envStatePersistent is the deployment saying whether the state directory
// survives a restart. **Told, because it cannot be seen**: an emptyDir and a
// PersistentVolumeClaim are both a directory that answers every write, and a
// guess here is a Settings page offering seven days of retention on a disk
// wiped at the next start.
//
// Unset is persistent. That is every desktop - a directory under the
// operator's home - and any deployment that does not set it; the Helm chart
// writes it from `persistence.enabled` on every install.
const envStatePersistent = "KT_STATE_PERSISTENT"

// readStatePersistent is envStatePersistent's answer, from getenv. openProfile
// calls it once and hands the boolean to every use, so a malformed value is
// warned about once and no two uses can read it differently.
func readStatePersistent(getenv func(string) string) bool {
	return envBoolFrom(getenv, envStatePersistent, true)
}

// Run is the console's whole start-up: read the command line and the
// environment, build the pieces, wire them, serve, and stop.
//
// args is the command line without the program name. It returns when ctx is
// done, when a signal arrives, or when the desktop window closes, and nil after
// an ordinary stop or a flag that answers and leaves. The extensions it mounts
// are whatever was registered before it was called — `app.Run` registers its
// own and then calls this. bundle is the front end served at `/`, nil for the
// one embedded in this binary; see web.Options.UI.
//
// It reads as the phases it runs, in order: the options the command line
// gives, the flags that answer and leave, the log, the profile and what it
// opens, the pieces that run beside the server, the entry gate, the routes,
// and serving until it stops. Each phase is a function below, in that order.
func Run(ctx context.Context, args []string, bundle fs.FS) error {
	// Before any output at all, because it decides where output goes.
	//
	// The Windows binary is linked `-H windowsgui` so that the Start-menu
	// shortcut opens an application and not an application with a cmd window
	// behind it; the cost is that it is detached from the terminal that started
	// it, and `--version` would print into nothing. initConsole reattaches when
	// there is a parent console and records when there is not — a fact
	// firstrun.go depends on to decide whether it can show a drawn password at
	// all. See console_windows.go.
	initConsole()
	log.SetFlags(0)

	// **Everything this starts ends with this context**: the update check,
	// single sign-on's discovery and the archive's workers run on it, and it is
	// canceled when Run returns, whichever way it returns. A binary that embeds
	// the console, or a test that started it, gets back a process with nothing
	// of the console's still running in it.
	ctx, endRun := context.WithCancel(ctx)
	defer endRun()

	// Before anything else, and deliberately not through `flag`: this process
	// takes its configuration from the environment everywhere else, and adding a
	// flag set for one query would give it two ways to be configured. The one
	// thing a person needs from the command line is which build they are holding.
	o, answered, err := readRunOptions(args, bundle)
	if answered {
		return err
	}
	inCluster := o.inCluster

	// `--bundle=DIR`, `--install` and `--uninstall` answer and leave, before the
	// log file. See leaveEarly.
	if left, err := leaveEarly(args, o.setup); left {
		return err
	}

	// The log file, the window decision and the first two lines. See startLog.
	window, windowWhy := startLog(o)

	// Where this will listen, read here rather than beside the server because
	// a second launch is decided by it before anything else is built.
	listen := envOr("KT_LISTEN", defaultListen)

	// **A second launch hands off to the first**: on the
	// loopback desktop, a launch that would open a window onto a port already
	// answering asks that instance for a code and opens its own window on it,
	// rather than failing on the port. Nothing below runs for it - no
	// kubeconfig, no clusters, no second server.
	if secondLaunch(window, listen, inCluster, cluster.InContainer(), openWindow) {
		return nil
	}

	// PATH, then the launch report, in that order. See augmentAndReport.
	augmentAndReport(args, windowWhy)

	// Before anything is built, and before any cluster is touched: this asks for
	// a credential and nothing else, so a kubeconfig that will not load must not
	// stand between an operator and getting back into their own console.
	if o.reset {
		if err := resetPassword(); err != nil {
			return err
		}
		return nil
	}

	// The profile: the state directory's version note, the update checker, the
	// clusters and the one gate in front of them, the stores, and whether this
	// process is one of several replicas. See openProfile.
	p, err := openProfile(inCluster)
	if err != nil {
		return err
	}

	// What runs beside the server - the archive, the terminals, the forwards,
	// the recorder and the probe - and what is undone on the way out, last opened
	// first closed. See startWorkers.
	followMax, err := logFollowMax(inCluster)
	if err != nil {
		return err
	}
	var undo cleanups
	defer undo.run()
	// The metrics pool holds a tunnel into a cluster for each live session; it
	// is released on the way out, after the workers that query through it.
	undo.add(p.measure.Close)
	w, err := startWorkers(ctx, p, inCluster, &undo)
	if err != nil {
		return err
	}
	opts := consoleOptions(bundle, inCluster, p, w)
	opts.LogFollowMax = followMax

	// The entry gate. See openFrontDoor.
	front, shared, err := openFrontDoor(inCluster, p.replicas)
	if err != nil {
		return err
	}
	// **The port is bound here, before any window can open.** openWindow dials
	// the address, and if something else answered there the window would open
	// on its page, at the console's origin, carrying a code and then the
	// window's stored cookie. Bound first, a port somebody else holds stops the
	// start with a sentence and no window at all.
	//
	// **And from here on the address is the one bound, never KT_LISTEN again.**
	// `KT_LISTEN=localhost:5823` names two
	// addresses on a dual-stack machine, and Go binds one: resolving the name a
	// second time - for the handoff's MACs, for a window's address - could
	// reach whatever holds the other, `[::1]:5823`, which is not this process.
	ln, err := listenFirst(listen)
	if err != nil {
		return err
	}
	bound := ln.Addr().String()

	// The launcher secret, and the front door's half of the console's options.
	// See wireEntryGate.
	ready := wireEntryGate(ctx, front, shared, &opts, listen, bound, inCluster, p, w)

	// The routes, inside the session guard. See mountRoutes.
	ui, outer, err := mountRoutes(opts, front, w.shells, ready, inCluster)
	if err != nil {
		return err
	}

	// The security headers wrap **everything**, above the session guard and above
	// the outer router, for the same reason that split exists at all: a header set
	// inside the guard is a header the next route registered outside it silently
	// does not get. No route escapes the guard today; this stays outermost so
	// that the next one to escape is covered on the day it lands rather than on
	// the day somebody notices.
	//
	// The login page's source goes with it because the Content-Security-Policy
	// names every inline script by hash, and that page is the one document
	// outside internal/web's embedded template tree — it is served before there
	// is a session, so it cannot reference `/static/`, which is behind the guard.
	//
	// **And the bundle is the one the router serves, asked of it rather than
	// passed a second time**: the policy names the shell's inline script by
	// hash, so a policy hashed from another `index.html` would block the script
	// of the page it is in front of.
	//
	// **And inside the headers, the Host allowlist**: on the
	// desktop, a request for a name that is not this machine's is refused before
	// the session guard is asked anything, because that is what a DNS-rebound
	// page sends. Inside rather than around the headers so the refusal carries
	// them too. See hostguard.go for where it applies and why it checks names
	// rather than ports.
	handler, hostWhy := frontHandler(ui.Bundle(), outer, listen, inCluster, cluster.InContainer())
	log.Printf("[kubetower] %s", hostWhy)

	// A server that stops serving on its own ends the run with its error,
	// rather than the process: see serve.
	served := make(chan error, 1)
	srv := serve(ln, bound, listen, handler, served, endRun)

	// **After the port, on a goroutine of its own, and it can take as long as it
	// likes.** The console's front door is the health page — *"the page opened
	// when nothing works"* — and a version check hanging on a DNS timeout must
	// never be between an operator and that page. Nothing below waits for this,
	// nothing fails because of it, and its whole output is a log line and the
	// update line on the Infos tab.
	//
	// It makes no request at all when the check is off, in a container, or on a
	// development build; that is decided inside `Check`, before a socket is
	// opened, and `TestATurnedOffCheckMakesNoRequestAtAll` is what holds it.
	//
	// Then it keeps watching: a console left open for days
	// is asked again once its answer has aged past the cache's window, and never
	// sooner however often the tick comes.
	//
	// On Run's context, so it ends with the run.
	go func() {
		p.updates.Check(ctx)
		p.updates.Watch(ctx, update.RecheckEvery)
	}()
	// "Install updates automatically" (#122): downloads what the check found
	// and, set to install now, installs once nothing a restart closes is open.
	// On quit is quit's own step, below.
	if p.installer != nil {
		open := openWorkOf(w)
		safego.Go("update: automatic", func() error {
			p.installer.Auto(ctx, updater.AutoEvery, func() string { return newerOf(p.updates.Report()) }, open)
			return nil
		}, nil)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stop)
	// An installed update ends this process the same way, once its helper
	// waits to start the new version (internal/updater, #49 to #51).
	updater.SetStop(func() {
		select {
		case stop <- os.Interrupt:
		default:
		}
	})

	if window {
		// The window is opened onto a server that answers: openWindow dials
		// first and gives up after ten seconds rather than hanging.
		//
		// **It runs on this goroutine and not in one of its own**, and that is a
		// requirement rather than a style: a native window is an event loop
		// bound to the thread that created it — WebView2's message loop on
		// Windows, AppKit's on macOS, which insists on the process's first
		// thread — and Go will move a goroutine between OS threads whenever it
		// likes. Only the main goroutine can be pinned to the thread the process
		// started on.
		//
		// So this blocks until the window closes, and closing it stops KubeTower
		// — which is what a desktop application does. The browser fallback
		// inside openWindow does the opposite and returns immediately, leaving
		// the wait below to hold the process up, because closing a browser tab
		// must not take down a server the operator may still be using.
		//
		// While it blocks, a goroutine reads `stop`: see watchWindowStop.
		interrupted, windowDone, watcherDone := watchWindowStop(ctx, stop)

		// What a native window reads about the rest of this process, in-process:
		// the sessions and forwards ⌘Q counts, the log
		// follows the App Nap guard also counts, the update check's last answer
		// for the Dock badge — read, never asked again — and the console's own
		// cluster list for the Cluster menu. See `windowSources`.
		//
		// The watcher that pushes the badge and the guard to the window runs on
		// macOS only: it is the only window that reads them, and a goroutine
		// polling for nobody on Windows would be a cost with no reader.
		feed := windowSources{
			Terminals:  w.shells.Open,
			Forwards:   w.countForwards,
			LogStreams: ui.OpenLogStreams,
			Update:     p.updates.Report,
			Clusters:   ui.Clusters,
			Tools:      w.tools,
		}
		// The tool windows close with their tunnel or their cluster (#65),
		// for as long as the native window can hold them.
		if w.tools != nil {
			safego.Go("tool windows: closing with their tunnel or cluster", func() error {
				w.tools.watch(windowDone, toolWindowSweepEvery)
				return nil
			}, nil)
		}
		if runtime.GOOS == "darwin" {
			go watchWindowFacts(windowDone, feed, windowWatchEvery, refreshNativeWindow)
		}

		// **The window arrives signed in**: a one-time code goes into its
		// address, whichever tier opens it - see openWindow, which mints it once
		// the server answers, so the code's minute starts when the window is
		// about to use it. Only where the route that spends it is registered;
		// elsewhere the window opens on the sign-in page. The macOS window and
		// the Windows one are handed the same address. See windowMint for why
		// this follows the launch offer and not the password.
		mint := windowMint(front, inCluster)
		openCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		closed := openWindow(openCtx, bound, mint, feed)
		cancel()
		close(windowDone)
		<-watcherDone
		if closed {
			p.finish(ctx, func() { shutdown(srv, w.shells) })
			return serveFailure(served)
		}
		select {
		case <-interrupted:
			p.finish(ctx, func() { shutdown(srv, w.shells) })
			return serveFailure(served)
		default:
		}
	}

	// A signal, or the caller's context: the second is how a binary that
	// embeds the console, or a test that started it, stops it without sending
	// the process a signal.
	select {
	case <-stop:
	case <-ctx.Done():
	}
	p.finish(ctx, func() { shutdown(srv, w.shells) })
	return serveFailure(served)
}

// newerOf is the release the update check found newer than this build, or
// empty.
func newerOf(report update.Report) string {
	if report.State != update.Newer {
		return ""
	}
	return report.Latest
}

// finish is every quit's last step: stop the server and the shells, then
// install a verified update where "Install updates automatically" says on
// quit (#122), bounded by updater.QuitWait; the next start runs it.
func (p *profile) finish(ctx context.Context, stop func()) {
	stop()
	if p.onQuit != nil {
		p.onQuit(ctx)
	}
}

// installOnQuitOf is profile.onQuit: nil where nothing installs or nobody
// quits (--unattended).
func installOnQuitOf(u *updater.Updater) func(context.Context) {
	if u == nil || unattended {
		return nil
	}
	return func(ctx context.Context) { u.InstallOnQuit(ctx) }
}

// serveFailure is the error the server stopped with on its own, or nil when
// it was stopped: by a signal, the window, or the caller's context.
func serveFailure(served <-chan error) error {
	select {
	case err := <-served:
		return err
	default:
		return nil
	}
}

// runOptions is what the command line says, read once by readRunOptions.
type runOptions struct {
	// window is set by --app: serve, then open the console in a window with no
	// browser furniture around it. Off by default, because the container has no
	// browser to open and would log a failed lookup on every start.
	window bool
	// noWindow is set by --no-window: serve, and open nothing. See the case below
	// and windowDecision in appwindow.go for the one situation that needs it.
	noWindow bool
	// reset is set by --reset-password: draw a new one, show it, and stop. It
	// serves nothing and reaches no cluster — the console is not started, because
	// the operator asking for a new password is not asking for a running console.
	reset bool
	// setup is --install or --uninstall: place this executable where the machine
	// keeps applications, or take it back out - on Windows, refuse in favor of
	// the setup wizard, or launch its uninstaller (installsetup.go). Like
	// --version and --help it answers and leaves, and unlike --reset-password it
	// never reads the state directory — an uninstall must work on a machine
	// whose kubeconfig, state directory and clusters are all long gone.
	setup string
	// inCluster is set by --incluster: the server profile, a console
	// shared by several people inside a cluster rather than one person's
	// application on their own machine.
	//
	// **It takes routes away and adds nothing**, so without it the desktop's
	// routes are all there — asserted rather than assumed, in
	// internal/launch/profileroutes_test.go.
	inCluster bool
}

// readRunOptions reads the command line into runOptions. answered is true when
// a flag answered and leaves (`--version`, `--notices`, `--help`); err is then
// what Run returns.
func readRunOptions(args []string, bundle fs.FS) (o runOptions, answered bool, err error) {
	for _, arg := range args {
		switch arg {
		case "--version", "-version", "version", "-v":
			fmt.Println(versionLine())
			return o, true, nil
		case "--notices", "-notices", "notices":
			// The licenses of what this binary is built from, which it carries
			// in its bundle (cmd/ktnotices) and also serves at
			// /THIRD_PARTY_NOTICES.txt (see mountOuter for who may read it).
			// Here for the copy that has no file beside it: the Windows
			// installer, and an installed binary.
			return o, true, printNotices(os.Stdout, bundle)
		case "--app", "-app", "app":
			o.window = true
		case "open", "--open", "-open":
			// `kubetower open`: a signed-in window, on
			// the instance already running when there is one - see handOff -
			// and otherwise exactly `--app`.
			o.window = true
		case "--no-window", "-no-window", "no-window":
			// The opposite of --app, needed on a Mac: an executable inside a
			// `.app` opens a window whoever started it, so somebody running that same file from
			// a terminal to read the log needs a way to say so. See
			// windowDecision in appwindow.go.
			o.noWindow = true
		case "--reset-password", "-reset-password", "reset-password":
			o.reset = true
		case "--unattended", "-unattended", "unattended":
			// **Nobody is in front of this machine**, so the first-run box is not
			// attempted and the drawn password is announced on stderr instead.
			// See `unattended` in firstrun.go, which carries the whole argument
			// and the measurement behind it.
			//
			// It writes the package variable rather than a field of runOptions like
			// the four above, because the code that reads it — `announce` — is four calls
			// down inside `frontDoor` and has no parameter to carry it. That is
			// the same shape `consoleAttached` already has, for the same reason.
			unattended = true
		case "--incluster", "-incluster", "incluster":
			// What it removes is registered in two places, and both are below:
			// the three `/kt-api/` upgrades in this file, and — through
			// `web.Options.InCluster` — the desktop-terminal route plus three of
			// the four socket channels inside `internal/api`.
			o.inCluster = true
		case "--install", "-install", "install":
			o.setup = "install"
		case "--uninstall", "-uninstall", "uninstall":
			o.setup = "uninstall"
		case "--help", "-help", "-h", "help":
			fmt.Println(usage())
			return o, true, nil
		}
	}
	return o, false, nil
}

// leaveEarly answers `--bundle=DIR`, `--install` and `--uninstall`, and says
// whether it did. A failure is said where a person will read it and returned,
// so the caller decides how the process ends.
func leaveEarly(args []string, setup string) (bool, error) {
	// `--bundle=DIR` assembles a macOS `KubeTower.app` and is
	// `.github/workflows/dmg.yml`'s alone, whether a release or `ci.yml`'s full
	// tier calls it: it is how a host that is not a Mac — that workflow's Linux
	// runner — produces the same bundle `--install` builds on a Mac, out of the
	// same writer rather than a second copy of the plist.
	// Before the log file for the reason the block below gives — it answers
	// and leaves, and a release build has no business creating a log in
	// somebody's state directory. See installbundle.go.
	if dir := bundleTarget(args); dir != "" {
		if err := runWriteBundle(dir); err != nil {
			return true, fmt.Errorf("KubeTower could not write the bundle: %w", err)
		}
		return true, nil
	}

	// Before the log file, deliberately, and for the same reason --version and
	// --help are: neither of these serves anything, and an uninstall that created
	// a log inside the state directory on its way out would be leaving a file
	// behind as its last act.
	//
	// **A failure here may have no console to be read in**: on Windows the
	// binary is linked `-H windowsgui`, and a shortcut that still runs
	// `kubetower.exe --uninstall` starts it without one. That is why `report` in
	// installhelp.go writes a dialog when there is no console, and not a log
	// line: a log line here would go to a file inside the state directory. A
	// success says nothing on Windows - the setup's uninstaller is the window.
	if setup != "" {
		var err error
		if setup == "install" {
			err = runInstall()
		} else {
			err = runUninstall()
		}
		if err != nil {
			report("KubeTower could not " + setup + ":\n\n    " + err.Error())
			return true, fmt.Errorf("KubeTower could not %s: %w", setup, err)
		}
		return true, nil
	}
	return false, nil
}

// startLog opens the log file, decides whether a window opens, and logs the
// version and that decision.
func startLog(o runOptions) (window bool, windowWhy string) {
	// The log gets a file, on every start that serves anything. Placed after the
	// argument loop on purpose: `--version` and `--help` answer and leave, and
	// neither should create a file as a side effect of being asked a question.
	//
	// **Unconditional, not gated on there being no console**: a macOS `.app`
	// opened from the Finder has a console flag of `true` and a stderr that goes
	// to /dev/null, exactly as a Start-menu shortcut's does, so a gate would
	// leave it writing its diagnostics nowhere. logfile.go carries the argument
	// for two channels rather than one, and stderr still gets every line
	// wherever it reaches anybody — the container included.
	//
	// The handle is deliberately not closed: this file is the process's log for
	// as long as the process runs, and closing it before exit would silence
	// exactly the shutdown an operator is trying to read.
	if path, _ := logToFile(); path != "" {
		log.Printf("[kubetower] logging to %s", path)
	}

	// A launch with no console and no flags is a double-click, and it must open
	// something.
	//
	// This is the trap `-H windowsgui` introduces and it is worth closing here
	// rather than documenting: a GUI-linked binary run with no arguments would
	// otherwise be a process serving on 5823 with no window, no console and no
	// icon, which the operator can only find in Task Manager. Anybody who
	// double-clicks an application expects the application.
	//
	// Only when there is no console: a deliberate `kubetower` typed into a
	// terminal still means "serve, and I am watching the log", which is what the
	// container does and what a person debugging wants.
	// **And it says which way it went, on every start**: a `KubeTower.app` that
	// serves and draws nothing must leave a line about the window in its log.
	// windowDecision composes the answer and the reason together for that reason.
	//
	// `launchedFromAppBundle` is the macOS half: a bundle has nowhere to carry
	// `--app`, so the fact is read off this executable's own path.
	window, windowWhy = windowDecision(o.window, o.noWindow, o.reset, consoleAttached,
		launchedFromAppBundle(runtime.GOOS, executablePath()))

	log.Printf("[kubetower] %s", versionLine())
	log.Printf("[kubetower] %s", windowWhy)
	return window, windowWhy
}

// augmentAndReport imports the login shell's environment, appends what PATH
// still lacks, then prints the launch report.
func augmentAndReport(args []string, windowWhy string) {
	// **First, and before any kubeconfig is read**: on macOS a window opened
	// from the Finder or the Dock has launchd's environment, so AWS_PROFILE,
	// AWS_CONFIG_FILE and KUBECONFIG from the shell are missing and an EKS
	// context that names no profile is signed as another one. Names only in the
	// log: the values can be credentials.
	if got := shellenv.Import(); got.Err != nil {
		log.Printf("[kubetower] environment: the login shell's could not be read, "+
			"so this process keeps launchd's: %v", got.Err)
	} else if len(got.Imported) > 0 || got.Path {
		taken := strings.Join(got.Imported, ", ")
		if got.Path {
			taken = strings.TrimPrefix(taken+", PATH", ", ")
		}
		log.Printf("[kubetower] environment: took %s from %s", taken, got.Shell)
	}

	// **Before any client is built and before the terminal looks for kubectl.**
	//
	// A GUI-launched process on macOS gets launchd's PATH — about four
	// directories — and not the operator's shell's, so `aws`,
	// `gke-gcloud-auth-plugin`, `kubelogin` and `kubectl` are all invisible to
	// this process while working perfectly in a dock terminal, which is a login
	// shell and re-derives its own. Nothing is added on
	// Windows or in the container, so this is silent everywhere it is not needed
	// — which is why it only speaks when it did something.
	//
	// It has to be here rather than in an exec block's environment:
	// `exec.Command` resolves a bare name against *this* process's PATH before
	// `cmd.Env` is ever read, so a PATH written next to `HOME` in
	// internal/cluster/awscache.go would be a no-op. internal/binpath's package
	// comment carries the measurement.
	if added := binpath.Augment(); len(added) > 0 {
		log.Printf("[kubetower] PATH: appended %s. A window opened from the Finder or the Dock "+
			"inherits launchd's PATH, not your shell's, so the credential plugin and kubectl "+
			"would otherwise be invisible to this process", strings.Join(added, ", "))
	}

	// **The launch report, on purpose, while this is a 0.x build.**
	//
	// The 0.x versions are noisy so that a log somebody sends is already the
	// diagnosis. diagnostics.go carries the argument, including why the
	// gate is the version itself — it expires with no decision to remember — and
	// why an environment variable's value is printed only from a closed list.
	//
	// **After `binpath.Augment`, never before it**: `toolReport` resolves
	// `kubectl`, `helm`, `aws` and `claude` against this process's PATH, and
	// before the augmentation a `KubeTower.app` opened from the Dock would report
	// `PATH: 4 entries` and four `not found`s — asserting, as fact, the exact
	// misdiagnosis `internal/binpath` exists to prevent. `Environ` is captured
	// here for the same reason.
	//
	// And after the window decision, which is the other fact it has to carry.
	if verboseLaunch(version, os.Getenv(diagKey)) {
		for _, line := range launchReport(launchFacts{
			Release:   version,
			Version:   versionLine(),
			Exe:       executablePath(),
			GOOS:      runtime.GOOS,
			Args:      args,
			Console:   consoleAttached,
			Bundled:   launchedFromAppBundle(runtime.GOOS, executablePath()),
			WindowWhy: windowWhy,
			StateDir:  statefile.Dir(),
			Environ:   os.Environ(),
		}) {
			log.Printf("[kubetower] %s", line)
		}
	}
}

// profile is what openProfile reads and opens: the state directory's version
// note, the update checker, the clusters and the one gate in front of them, the
// stores in the state directory, and whether this process is one of several.
type profile struct {
	stateWrittenBy string
	stateIsNewer   bool
	updates        *update.Checker
	// installer downloads and installs an update (#47): the desktop only, nil
	// in a cluster and in a container, which are upgraded by their image tag.
	installer *updater.Updater
	// onQuit installs a verified update as KubeTower quits (#122): nil where
	// nothing installs, and under --unattended, where a service stop or
	// restart is nobody quitting.
	onQuit     func(context.Context)
	kubeconfig cluster.Kubeconfig
	kubefiles  cluster.KubeconfigSurvey
	clusters   *cluster.Manager
	gate       *access.Gate
	readOnly   *cluster.ReadOnly
	measure    *metrics.Metrics
	bars       *hotbars.Store
	cols       *columns.Store
	plates     *nameplates.Store
	xtra       *extras.Store
	// kinds is each person's choices about the kinds column, nil where they
	// would not last. See openKindPrefs.
	kinds *kindprefs.Store
	// seen is the last release whose "What's new" was seen (#80).
	seen     *seen.Store
	replicas replication
	// statePersistent is KT_STATE_PERSISTENT, as the deployment said it.
	statePersistent bool
}

// openProfile is the profile phase: desktop or server, one replica or several,
// and the state directory. See profile.
func openProfile(inCluster bool) (*profile, error) {
	// **Which version last wrote this state directory**, before anything reads it.
	//
	// Two binaries of different versions share one
	// `~/.kubetower` happily, and the failure is not a refusal but a *rewrite* —
	// every store here loads defensively, so an older build meeting a newer format
	// replaces the operator's arrangement with the defaults and says so in a log
	// nobody is reading.
	//
	// Warned about rather than refused: a console that will not open because of a
	// version note is one an operator cannot use to find out what went wrong, and
	// downgrading on purpose to check whether a defect is new is a legitimate
	// thing to do. What it must not be is silent — so it is said here.
	stateWrittenBy, stateIsNewer := statefile.Older(version)
	if stateIsNewer {
		log.Printf("[kubetower] WARNING: %s was last written by KubeTower %s, which is newer than this "+
			"build (%s). Two versions sharing one state directory is how an arrangement gets "+
			"silently replaced by defaults — close one of them, or point this one somewhere "+
			"else with %s", statefile.Dir(), stateWrittenBy, version, statefile.EnvDir)
	} else if err := statefile.Record(version); err != nil {
		// Not fatal: the note is a diagnostic, not a lock, and a console that
		// refuses to start because it could not write one would be worse than a
		// stale note.
		log.Printf("[kubetower] could not record this version in %s: %v", statefile.Dir(), err)
	}

	// Whether a newer release exists — asked of GitHub, once a day at most, and
	// **never on the path between this process starting and its port answering.**
	// The checker is built here so that the Settings page can be handed a reader
	// for it; the request itself is made on a goroutine of its own, after the
	// server is listening, further down.
	//
	// Every one of the four decisions it takes is taken here rather than inside
	// the package, so that there is one reading of each and a test can set the
	// answer without setting the machine:
	//
	//   - the off switch, which is honored **before any request is made**;
	//   - `cluster.InContainer()`, because the in-cluster build is upgraded by its
	//     image tag and a pod calling github.com on boot is a decision nobody
	//     took;
	//   - `developmentBuild()`, because `dev` cannot be ordered against a release
	//     and a development build is never told it is out of date;
	//   - `version` and not `versionLine()`: this one is compared, and the line
	//     the page prints is the opaque string beside it.
	updates := update.New(update.Options{
		Version:     version,
		Development: developmentBuild(),
		InContainer: cluster.InContainer(),
		Enabled:     update.EnabledFromEnv(),
		Dir:         statefile.Dir(),
		Logf:        logf,
	})
	// The two clicks after "a newer release exists": on the desktop only, for
	// the installation this binary was started from (internal/updater).
	var installer *updater.Updater
	if !inCluster && !cluster.InContainer() {
		installer = updater.New(updater.Options{
			Version:      version,
			GOOS:         runtime.GOOS,
			GOARCH:       runtime.GOARCH,
			Installation: updater.DetectRunning(),
			Dir:          statefile.Dir(),
			Logf:         logf,
		})
	}

	// **Where the clusters come from is the profile's first decision.** The
	// desktop reads the operator's kubeconfig, a list of files; the server
	// profile reads the pod it runs in, and `KUBECONFIG` is not consulted at all
	// (see internal/cluster/incluster.go for why). What the two hand on is the
	// same three things, and under the profile the list and its survey are
	// empty, which every reader below already treats as "nothing to check"
	// rather than as a failure.
	kubeconfig, kubefiles, clusters, err := openClusters(inCluster)
	if err != nil {
		return nil, err
	}

	// **The one access gate**, before anything that can write is built: every
	// write, every exec and every forward is reviewed through it, for the
	// person behind the request, and the pages ask it before they offer a
	// verb. One for the process, so a page and the write it offers share the
	// held answers - and so there is one cache to reason about when a grant is
	// revoked (its minute is the revocation delay). See internal/access.
	//
	// **And the one audit log, which the gate writes** (internal/audit):
	// two records per act, to stdout, the attempt before the request is sent
	// and refusing it when it cannot be written. See auditLog.
	gate := processGate(clusters, inCluster, logf)

	// The read-only guardrail, resolved against the kubeconfig and announced.
	//
	// Three things, each needed: *resolved*, so the DNS-friendly form of a
	// context name — `…cluster--example` for `…cluster/example` — marks the
	// cluster it names; *validated*, so a typo does not silently protect
	// nothing; and *announced*, because an operator believing a production
	// cluster is read-only when it is not is the worst state this variable can
	// be in, and it must not be a silent one.
	//
	// Read once here rather than once per reader below.
	readOnly := clusters.ReadOnlyMarkings(os.Getenv("KT_READONLY_CLUSTERS"))

	// metrics-server for "what now" and Prometheus for "what at 03:00",
	// discovered per cluster. One object in two roles below — Options.Metrics for
	// the tables' instant figures, Options.Series for the overview's history —
	// because a cluster commonly has one backend and not the other.
	//
	// Its tunnels to Prometheus are the console's own (access.AsConsole) and are
	// reviewed as such through the one gate.
	//
	// Under the server profile, which Prometheus is the deployment's choice
	// (KT_PROMETHEUS_SERVICE) and nothing is discovered: a label in the cluster
	// is written by whoever creates the Service. See
	// prometheus.go.
	promConfigured, promTarget, err := readPrometheus(inCluster, os.Getenv)
	if err != nil {
		return nil, err
	}
	log.Printf("[kubetower] Prometheus: %s", prometheusSummary(promConfigured, promTarget))
	measure := metrics.New(metrics.Options{
		Clusters: clusters, Access: gate, Logf: logf,
		Configured: promConfigured, Prometheus: promTarget,
	})

	// The stores in the state directory. See openPreferences.
	bars, cols, plates, xtra := openPreferences(clusters, inCluster)

	// Several replicas, and the store they share - read before anything that
	// depends on whether this process is one of several. See replicas.go for
	// what it refuses and what it takes away.
	replicas, err := readReplication(inCluster, passwordOfferedByEnv(), os.Getenv)
	if err != nil {
		return nil, err
	}
	// Whether the state directory survives a restart, read once for the
	// archive (openArchive), the extras line and the kinds column below and
	// the web server's StateEphemeral (consoleOptions), so they cannot
	// disagree and a malformed value is warned about once.
	statePersistent := readStatePersistent(os.Getenv)
	// After the replicas, because one of several has no per-cluster switch to
	// name. Through extrasStartupLine, whose test holds the profile's polarity;
	// openprofile_test.go holds this call.
	log.Printf("[kubetower] extras enabled: %s", extrasStartupLine(xtra, clusters.Contexts(),
		inCluster, replicas.Several, statePersistent))
	if pinned := xtra.PinnedSettings(); len(pinned) > 0 {
		// Said once at start-up, beside the extras: KT_EXTRA_SETTINGS decides
		// these on every cluster, and the Settings page draws them with no control.
		log.Printf("[kubetower] extra settings pinned by %s: %s", extras.EnvExtraSettings, strings.Join(pinned, ", "))
	}

	kinds := openKindPrefs(inCluster, replicas.Several, statePersistent)

	return &profile{
		stateWrittenBy: stateWrittenBy, stateIsNewer: stateIsNewer, updates: updates, installer: installer, onQuit: installOnQuitOf(installer),
		kubeconfig: kubeconfig, kubefiles: kubefiles, clusters: clusters,
		gate: gate, readOnly: readOnly, measure: measure,
		bars: bars, cols: cols, plates: plates, xtra: xtra, kinds: kinds, replicas: replicas,
		seen:            seen.New(statefile.Dir(), logf),
		statePersistent: statePersistent,
	}, nil
}

// openKindPrefs opens each person's choices about the kinds column
// where they last: on the desktop, and under the server
// profile with one replica and a state volume, the extras switch's rule
// (extensionSwitch). Elsewhere nil, and the browser keeps them; the file is not even
// read, as the other preferences are not under several replicas.
func openKindPrefs(inCluster, several, statePersistent bool) *kindprefs.Store {
	if several || (inCluster && !statePersistent) {
		return nil
	}
	kinds := kindprefs.New(statefile.Dir(), logf)
	log.Printf("[kubetower] kinds column choices remembered per person in %s", kinds.Path())
	return kinds
}

// openPreferences opens the operator's arrangement in the state directory -
// the hotbars, the column layouts, the cluster names and the extras - and says
// where each is kept.
func openPreferences(clusters *cluster.Manager, inCluster bool) (*hotbars.Store, *columns.Store, *nameplates.Store, *extras.Store) {
	// The rail's arrangement: which hotbar each cluster is on. The one thing this
	// process writes to disk, and it is said out loud at startup because "my
	// hotbars reset" is otherwise diagnosed by guesswork — in the container the
	// directory must be a volume, or the file goes with the container.
	bars := hotbars.New(hotbars.Dir(), logf)
	if bars.Path() != "" {
		log.Printf("[kubetower] hotbars remembered in %s", bars.Path())
	}

	// The tables' own arrangement: how wide each column is and what order they
	// come in, per kind. The same state directory and the same bargain — without
	// a volume under it the file goes with the container, and "my column widths
	// reset" is otherwise diagnosed by guesswork.
	cols := columns.New(columns.Dir(), logf)
	if cols.Path() != "" {
		log.Printf("[kubetower] column layouts remembered in %s", cols.Path())
	}

	// What the operator calls each cluster and what color they gave it. The
	// third file in the same state directory, on the same bargain — and worth
	// saying out loud for one more reason than the other two: this is the one
	// preference that changes what a cluster is *called* on screen, so "why does
	// the rail say Prod Europe" needs a file somebody can open and a line in the
	// log pointing at it. The kubeconfig is untouched and stays read-only.
	plates := nameplates.New(nameplates.Dir(), logf)
	if plates.Path() != "" {
		log.Printf("[kubetower] cluster names and colors remembered in %s", plates.Path())
	}

	// Which of the additive pages this installation runs. The fifth file in
	// the same state directory. **Every extra is off until somebody turns it
	// on**, on every profile, on the Settings page's Extra tab. **Under the
	// server profile KT_EXTRAS is the chart's values**: it names the extras this
	// installation may turn on, and a person granted the verb `use` on one (API
	// group `extras.kubetower.dev`) turns it on per cluster (extras.InCluster).
	//
	// Said out loud at startup like the four beside it, and for a sharper reason
	// than any of them: "an additive page disappeared" has three possible
	// causes — a state directory that is not a volume, a KT_EXTRAS that pins it
	// off (or, under the server profile, does not name it), and nobody having
	// turned it on — and the log line plus the settings page are what separate
	// them.
	// The cluster list is passed for one reason: converting a state file in the
	// older format, where a page was on globally and clusters were subtracted
	// from it. "On everywhere" is only nameable from the kubeconfig, so without
	// this list an existing file would read as nothing ticked — which is six
	// pages taken away from an operator at a restart they did not connect to it.
	// See extras.Store.migrate.
	xtra := extras.New(extras.Dir(), clusters.Contexts(), logf, extrasOptions(inCluster)...)
	if xtra.Path() != "" {
		log.Printf("[kubetower] extras remembered in %s", xtra.Path())
	}
	return bars, cols, plates, xtra
}

// cleanups is what Run undoes on its way out, run last added first, as defers
// would.
type cleanups []func()

func (c *cleanups) add(f func()) { *c = append(*c, f) }

// run runs every cleanup, last added first. Each runs from a defer of its own,
// so one that panics does not stop the
// ones added before it: a panic in the forwards' Shutdown still stops the
// archive and closes the store.
func (c *cleanups) run() { runLastFirst(*c) }

func runLastFirst(fs []func()) {
	if len(fs) == 0 {
		return
	}
	defer runLastFirst(fs[:len(fs)-1])
	fs[len(fs)-1]()
}

// workers is what startWorkers starts beside the server.
type workers struct {
	shells        *terminal.Terminal
	forwards      web.Forwards
	countForwards func() int
	// tools is the desktop's tool windows (#65): nil where no native window
	// can be drawn or there are no forwards.
	tools    *toolWindows
	archives web.Archives
	feeds    web.Recorders
	probe    *health.Health
}

// startWorkers opens the archive, the terminals, the port forwards, the
// recorder and the health probe. What has to be closed on the way out is added
// to undo, last opened first closed.
func startWorkers(ctx context.Context, p *profile, inCluster bool, undo *cleanups) (*workers, error) {
	clusters, kubeconfig, gate, readOnly, measure := p.clusters, p.kubeconfig, p.gate, p.readOnly, p.measure

	// A kubeconfig edited while the console runs is read again, without a
	// restart (internal/cluster/reload.go). Nothing to watch in the cluster.
	// Through safego: it parses files the operator edits, and a panic there is
	// a line in the log, not the end of the console.
	safego.Go("kubeconfig: watching for changes", func() error {
		clusters.Watch(ctx, cluster.WatchEvery, logf)
		return nil
	}, func(err error) {
		if err != nil {
			logf("kubeconfig: no longer watched for changes: %v", err)
		}
	})

	// The archive, and the context everything started beside it runs under.
	// See openArchive.
	store, archiveCtx := openArchive(ctx, p.statePersistent, p.replicas, undo)

	// The terminals: the dock's shell, pod exec, the node shell, and Claude Code
	// on the cluster the drawer was open on.
	//
	// A Claude session here is handed no `--mcp-config`, so it has no KubeTower
	// tools and "this pod" does not resolve without the operator naming it. What
	// it has is the same PTY, and a per-session kubeconfig narrowed to the
	// cluster on screen, so `kubectl` and `helm` need no `--context`.
	shells, err := terminal.New(clusters, terminal.Options{
		Kubeconfig: kubeconfig,
		ReadOnly:   readOnly.Has,
		Access:     gate,
		// Claude sessions run behind the mutating-verb overrides on every
		// cluster unless this is turned off. The default is not timidity: this
		// process holds administrator credentials for production clusters, and a
		// model asked to fix something will run `kubectl delete` to fix it.
		ClaudeReadOnly: envBool("KT_CLAUDE_READONLY", true),
		// One person's share of the sixteen, under the server profile only:
		// there the sixteen are every person's, and one person holding them all
		// would refuse everybody else's exec.
		MaxSessionsPerPerson: sharePerPerson(inCluster, terminal.PerPersonSessions),
		// And an idle exec ends there: pongs would keep one alive for ever,
		// holding a slot every person shares.
		IdleExec: idleExec(inCluster),
		Logf:     logf,
	})
	if err != nil {
		return nil, fmt.Errorf("terminal: %w", err)
	}

	if store == nil {
		// Said once, at startup, because the symptom otherwise is a Settings
		// page with less on it than the last one somebody saw.
		log.Printf("[kubetower] %s=false: %s does not survive a restart, so there is no "+
			"archive and no node-shell image setting; the rail, the columns and the names "+
			"last as long as this process", envStatePersistent, statefile.Dir())
	} else if status := store.Status(); status.Root != "" {
		log.Printf("[kubetower] archive in %s (events %v, logs %v, %d days)",
			status.Root, status.Enabled, status.Logs, status.RetentionDays)
	}

	// Port forwards. See startForwards.
	forwards, countForwards := startForwards(clusters, readOnly, gate, archiveCtx, inCluster, p.replicas.Several, undo)

	// What feeds the archive. See startRecorder.
	archives, feeds := startRecorder(store, clusters, archiveCtx)

	// Which contexts answer, and — when they do not — which of three different
	// people fixes it. A Kubernetes client failure is four unrelated problems
	// wearing the same coat; classifying them is the whole value of the package,
	// and it is also what gives /kt-healthz something to mean.
	probe := health.New(health.Options{
		Clusters:   clusters,
		Metrics:    metricsKind{measure},
		Kubeconfig: kubeconfig,
		ReadOnly:   readOnly.Has,
	})

	return &workers{
		shells:   shells,
		forwards: forwards, countForwards: countForwards,
		tools:    startToolWindows(forwards, clusters.Known),
		archives: archives, feeds: feeds, probe: probe,
	}, nil
}

// openArchive opens the archive where the state directory survives a restart,
// and starts it under the context it returns, which is canceled on the way out.
// store is nil where there is no archive.
func openArchive(ctx context.Context, statePersistent bool, replicas replication, undo *cleanups) (store *archive.Archive, archiveCtx context.Context) {
	// The archive: a window of events and, if the operator turns it on, logs.
	//
	// Kubernetes forgets — events go after about an hour, and a pod's logs go
	// with the pod, which is exactly when they are wanted. Off for logs by
	// default, because logs are the part that can fill a disk and because they
	// are production application output at rest: see the note in
	// internal/archive on what that means before anyone enables it.
	//
	// Started before the server, so a prune runs at boot rather than an hour in.
	//
	// **Not opened at all where the state directory does not survive a
	// restart**, which the deployment says and this process cannot see: an
	// emptyDir and a PersistentVolumeClaim are both a directory that answers
	// every write. Seven days of retention on a disk wiped at the next start is
	// a control that lies, so the archive, its settings and the recorder that
	// feeds it are absent rather than running and forgetting - and the Settings
	// page says why rather than drawing "this build has no archive".
	//
	// **And off where several replicas share one address** (replicas.go): each
	// pod would record into its own state directory and serve its own half, so
	// the window an operator reads would depend on which pod answered. The
	// settings writes that could turn it back on are not registered there either.
	if statePersistent {
		archiveDefaults := archive.FromEnv()
		if replicas.Several {
			archiveDefaults.Enabled, archiveDefaults.Logs = false, false
		}
		store = archive.Open(archive.Options{
			Root:     archive.Dir(),
			Defaults: archiveDefaults,
			// And whatever a settings file left on the volume says: Defaults
			// apply only where there is none.
			ForceOff: replicas.Several,
			Logf:     logf,
		})
		undo.add(func() { _ = store.Close() })
	}

	archiveCtx, stopArchive := context.WithCancel(ctx)
	undo.add(stopArchive)
	if store != nil {
		store.Start(archiveCtx)
	}
	return store, archiveCtx
}

// startForwards starts the managed set of port forwards, where this profile has
// one. Both results are nil where it has none.
func startForwards(clusters *cluster.Manager, readOnly *cluster.ReadOnly, gate *access.Gate,
	archiveCtx context.Context, inCluster, several bool, undo *cleanups) (web.Forwards, func() int) {
	// Port forwards, as a managed set rather than one-shot tunnels. Loopback
	// only, bounded, and reaped the instant the pod behind one goes away — a dead
	// forward still accepts connections and then resets them, which reads as a
	// broken workload rather than as a dead tunnel.
	// The Redis viewer, which reaches a workload rather than an API server and so
	// needs a tunnel. The same adapter internal/metrics uses for Prometheus:
	// internal/terminal's forwarder, which drains the forwarder's output — one
	// whose output nobody drains blocks every new connection after about 64 KB,
	// which is a tunnel that works for twenty minutes and then stops.

	//
	// **None under the server profile, and the reason is where the port is.** A
	// forward binds a port inside this process and the page links to it as
	// `http://127.0.0.1:<port>` - which, from a browser talking to a pod, is the
	// person's own machine, where nothing listens. So it is broken even where
	// the ServiceAccount may `pods/portforward`, and if it worked every person
	// signed in would share the pod's one range of ports. The page draws the
	// `kubectl port-forward` command instead, which opens the tunnel on the
	// person's machine with their own credentials; `capabilities.portForward`
	// false is what tells it to, and `internal/api` registers none of the three
	// forward operations. A nil interface, not a nil `*forward.Manager`: the
	// capability reads `forwards != nil`, and a typed nil is not nil there.
	//
	// The in-process tunnels an extension or the Prometheus panel digs are a
	// different thing - `terminal.NewForwarder` below, bound to this process
	// and never handed to a browser - and they stay.
	//
	// **And none where several replicas share one address**: a forward is a
	// listener inside this pod, and the list, the link and the stop that follow
	// it land wherever the Service sends them. Nil is what `internal/web` reads
	// as "this console has no forwarder", which is the refusal and the reason
	// every Ports panel then draws.
	var forwards web.Forwards
	// countForwards is the same set counted in-process, for the macOS window's
	// ⌘Q question and App Nap guard; nil where there is no set, which the window
	// reads as none open.
	var countForwards func() int
	if !inCluster && !several {
		set := forward.New(forward.Options{
			Clusters: clusters,
			ReadOnly: readOnly.Has,
			Logf:     logf,
			Access:   gate,
			// A forward that stopped without the operator stopping it, for
			// the macOS window's notification. Queued
			// and dropped at the door unless that window can notify.
			Lost: forwardLostNotice,
		})
		set.Start(archiveCtx)
		undo.add(set.Shutdown)
		forwards = set
		countForwards = func() int { return len(set.List()) }
	}
	return forwards, countForwards
}

// startRecorder starts what asks the clusters for the events the archive keeps.
// Both results are nil where there is no archive.
func startRecorder(store *archive.Archive, clusters *cluster.Manager, archiveCtx context.Context) (web.Archives, web.Recorders) {
	// What feeds it. The archive knows how to keep events; this is what asks the
	// clusters for them. It checks the setting every round, so turning the
	// archive on from the settings page takes effect without a restart.
	//
	// The pointer is kept and handed to the UI, so this unattended work has an
	// operator-visible surface: it polls every cluster on a timer and says each
	// failure exactly once, and without the UI an archive that stopped
	// collecting from eleven of fourteen clusters overnight would look, from
	// every screen, like one collecting from all of them.
	//
	// None without an archive: a poller writing into nothing would be a
	// collector the health page reports as running for a store that does not
	// exist. Both interfaces stay nil rather than holding a typed nil, which
	// `internal/web` would read as present.
	var archives web.Archives
	var feeds web.Recorders
	if store != nil {
		events := recorder.New(recorder.Options{
			Clusters: clusters,
			Store:    store,
			Logf:     logf,
		})
		events.Start(archiveCtx)
		archives, feeds = store, events
	}
	return archives, feeds
}

// consoleOptions assembles what internal/web is handed, from the profile and
// the workers. The entry gate fills in the rest (wireEntryGate).
func consoleOptions(bundle fs.FS, inCluster bool, p *profile, w *workers) web.Options {
	clusters, gate, readOnly, measure := p.clusters, p.gate, p.readOnly, p.measure
	stateWrittenBy, stateIsNewer, statePersistent := p.stateWrittenBy, p.stateIsNewer, p.statePersistent
	updates, kubefiles := p.updates, p.kubefiles
	opts := web.Options{
		// The front end at `/`. Nil is the embedded one; a binary carrying
		// extras hands its own build, which holds their pages.
		UI: bundle,
		// The server profile, handed over so that `internal/web` and
		// `internal/api` can decide their own registrations. False is the
		// desktop.
		InCluster: inCluster,
		// What the Settings page and its tabs answer with. Assembled here because
		// this is where the facts are known: `statefile.Dir()` decided where the
		// state went, the version comparison happened at startup, and the version
		// itself is stamped into this package by `cmd/ktbuild`, and by the
		// Dockerfile's own `-ldflags`. internal/web is told rather than left to
		// work it out.
		//
		// **`versionLine()` and not the four variables behind it.** The Settings
		// page prints exactly what `--version` prints because it is handed the
		// output of the one function that composes it — two spellings of one fact
		// drift, and there is nothing here to spell twice.
		// `TestTheSettingsPageIsHandedTheSameLineTheFlagPrints` holds that.
		Setup: web.SetupInfo{
			StateDir: statefile.Dir(),
			// As the deployment said it, never as measured. See envStatePersistent.
			StateEphemeral: !statePersistent,
			StateWrittenBy: stateWrittenBy,
			StateIsNewer:   stateIsNewer,
			Version:        versionLine(),
			Development:    developmentBuild(),
			Release:        releaseOf(),
			// Where KubeTower lives, beside the version. From the package that
			// asks the same repository, so the link and the endpoint cannot name
			// two different addresses.
			RepositoryURL: update.Repository,
			// The documentation Help ▸ KubeTower Help opens, the same constant.
			DocumentationURL: helpURL,
			// **A reader and not a value.** The check runs after the port opens,
			// so a snapshot taken here would be the pre-check answer for the life
			// of the process — the page would report "the check has not run yet"
			// forever.
			Updates: func() web.UpdateReport {
				// Checking first: the checker stores its answer before it
				// clears the flag, so "not checking" read first means the
				// report read second is the settled one (update.Checking).
				checking := updates.Checking()
				return webUpdateReport(updates.Report(), checking)
			},
			// Settings › Infos' Check now, past the cache.
			CheckUpdates: func(ctx context.Context) web.UpdateReport {
				return webUpdateReport(updates.CheckNow(ctx), updates.Checking())
			},
			// Download, then Restart to update (#47), and what the restart ends.
			Installer: setupInstaller(p.installer),
			// A forward's flow UI in a window of its own (#65), where a
			// native window can be drawn. A nil interface otherwise.
			ToolWindows: setupToolWindows(w.tools),
			OpenWork:    openWorkOf(w),
			// The kubeconfig files, in precedence order, as they were read at
			// **startup** and not as they are now.
			//
			// That is a decision rather than a shortcut. Every client in this
			// process, every URL and the whole rail were built from this merge;
			// a page that re-read the files would describe a kubeconfig nothing
			// is using and report a shadow as resolved while the rail still
			// showed the winner. The page says a change needs a restart.
			//
			// Handed over as the survey itself rather than copied into a shape
			// of internal/web's own: it is already a struct of plain facts, and
			// a second spelling would be free to drift from the log line, which
			// composes its sentences from the same place.
			Kubeconfig: kubefiles,
		},
		Clusters:   clusters,
		Metrics:    measure,
		Hotbars:    p.bars,
		Columns:    p.cols,
		Nameplates: p.plates,
		Extras:     p.xtra,
		KindPrefs:  p.kinds,
		Seen:       p.seen,
		Archive:    w.archives,
		Forwards:   w.forwards,
		// The same object, in its other role. Usage is metrics-server's instant
		// figures for the table columns; Series is Prometheus for the overview's
		// history charts. Two fields because they are two backends, and a
		// cluster commonly has one and not the other.
		Series:   measure,
		Terminal: w.shells,
		Health:   w.probe,
		Recorder: w.feeds,
		// The same guardrail the terminal takes, handed to the
		// one part of the UI that can change a cluster. Passed rather than read
		// from the environment inside internal/web, so there is exactly one
		// reading of KT_READONLY_CLUSTERS in this process and four users of it.
		ReadOnly: readOnly.Has,
		// And the entries that named no context, for the page.
		ReadOnlyUnmatched: readOnly.Unmatched(),
		// The same gate the terminal and the forwards were handed.
		Access: gate,
	}

	// What an extension reaches a workload through, rather than an API server.
	//
	// **Counted and capped in internal/web**, which puts its own ceiling of
	// sixteen in front of this (tunnelcap.go). It is not the `forward.Manager` cap
	// above: that one counts the forwards a browser opens, through a forwarder of
	// its own; the two do not share one budget. The same adapter
	// `internal/metrics` uses for Prometheus — `internal/terminal`'s forwarder,
	// which drains the output of a forwarder that nobody reads, so that it does
	// not block every new connection after about 64 KB.
	//
	// This package names no viewer: it hands the capability over once, and the
	// extensions that want it are registered through `app.Register` and ask for
	// it through `extension.Host`.
	//
	// Through the same gate: a tunnel an extension opens with Host.Forward is
	// reviewed for the person whose request carries it.
	opts.Tunnels = metrics.Tunnels(terminal.NewForwarder(clusters, gate))
	return opts
}

// openFrontDoor builds the front door, and offers the desktop's windows their
// launch codes.
func openFrontDoor(inCluster bool, replicas replication) (*auth.Auth, sharedstate.Store, error) {
	// The front door is built **before** the console rather than after it,
	// because the JSON contract's three session operations are mounted by
	// internal/api and answered by this package. It owns the cookie, the
	// constant-time comparison and the failure delay; a second login handler
	// somewhere else would be a second set of those decisions, and the one that
	// drifts is the one nobody is looking at.
	// **Single sign-on is the server profile's and nobody else's**, so
	// the profile is handed over: frontDoor refuses to start with an issuer and
	// no --incluster. See singleSignOn.
	front, shared, err := frontDoor(inCluster, replicas.Store)
	if err != nil {
		return nil, nil, err
	}
	// **The desktop's windows arrive signed in**: its one-time launch
	// codes, and the route that spends them, which internal/api registers only
	// where this was said. Never under --incluster.
	if !inCluster {
		front.OfferLaunches()
	}
	return front, shared, nil
}

// wireEntryGate takes the handoffs on the loopback desktop, hands the console's
// options what the front door decides, starts single sign-on's discovery, and
// returns the readiness `/readyz` and `/kt-healthz` answer with.
func wireEntryGate(ctx context.Context, front *auth.Auth, shared sharedstate.Store, opts *web.Options, listen, bound string,
	inCluster bool, p *profile, w *workers) readiness {
	replicas, probe, gate := p.replicas, w.probe, p.gate
	// **The launcher secret, on the loopback desktop only**:
	// drawn into the state directory the first time, and what a second launch
	// presents to be handed a code. A secret that cannot be written costs the
	// handoff and nothing else, so it is a sentence rather than a refusal. The
	// address bound is written beside it, so a second launch reaches this
	// process at that address and not at a name resolved again.
	if loopbackDesktop(listen, inCluster, cluster.InContainer()) {
		if secret, err := launcherSecret(true); err != nil {
			log.Printf("[kubetower] a second launch will not be able to open a window on this one: %v", err)
		} else {
			front.AcceptHandoffs(secret, bound)
			if err := writeInstanceAddress(bound); err != nil {
				log.Printf("[kubetower] a second launch may not find this one: %v", err)
			}
		}
	}
	// The sealed store, when there is one, is the plan store's too: a preview
	// and its confirmation are two requests, and two replicas may answer them.
	opts.Shared = shared
	opts.Replicated = replicas.Several
	// The address bound, and whether anybody is asked for a password, sent with
	// the settings; no page draws them today. See signedInWithoutPassword.
	opts.Setup.ListenAddress = bound
	opts.Setup.WithoutPassword = signedInWithoutPassword(inCluster, front)
	opts.Sessions = front
	// The same object in its other role: what the sign-in page offers, which
	// internal/api reads once to decide which session routes exist.
	opts.SignIn = front
	// Single sign-on's discovery runs beside the server rather than in front of
	// it: **an unreachable identity provider does not take the console down**
	// - it is a sentence on the sign-in page while this keeps asking, until
	// Run's context ends.
	front.Start(ctx)
	// `/readyz` is the same answer `/kt-healthz` gives, in the shape
	// `api/openapi.yaml` declares. Both paths, and that is deliberate — see the
	// registration below.
	//
	// **And not ready while the audit log is broken**: see readiness.
	ready := newReadiness(probe, gate, inCluster)
	opts.Probe = ready
	return ready
}

// mountRoutes builds the console and its routers: every route, inside the
// session guard, on outer.
func mountRoutes(opts web.Options, front *auth.Auth, shells *terminal.Terminal, ready readiness,
	inCluster bool) (*web.Server, *http.ServeMux, error) {
	ui, err := web.New(opts)
	if err != nil {
		return nil, nil, err
	}
	// **A person who signs out loses what they had open**: their shells and pod
	// execs, and the log streams they follow. Stopping new requests only would
	// leave an open exec - the most dangerous thing the server profile hands
	// out - running until its tab was closed. On this replica only: another
	// replica's sockets are its own.
	front.OnSignOut(func(who identity.Who) {
		closed := shells.EndSessionsOf(who)
		streams := ui.EndStreamsOf(who)
		if closed+streams > 0 {
			logf("%s", identity.Line(identity.With(context.Background(), who),
				"signed out: %d terminal session(s) and %d log stream(s) closed", closed, streams))
		}
	})

	mux := http.NewServeMux()
	ui.Mount(mux)
	// The three shell upgrades, and **the server profile does not register them at
	// all** rather than registering handlers that refuse.
	//
	// `/kt-api/terminal` serves a pseudo-terminal on the machine this process
	// runs on. In a pod that machine is the pod, and the shell inherits the
	// process's environment — the mounted ServiceAccount token and the
	// kubeconfig built from it. A shell opened this way on a deployment reads
	// every Secret in the cluster even with the chart's `rbac.exec` and
	// `rbac.write` both false: RBAC bounds the pod, and this shell *is* the pod.
	// `/kt-api/claude` is the same shell with an assistant in front of it, and
	// the server profile ships no assistant control at all - absent, not refused.
	//
	// Not registering them is what makes that checkable: `mux.Handler(req)`
	// returns no pattern, which is a statement about the whole router rather
	// than about one function. See profileroutes_test.go.
	mountShellUpgrades(mux, shells, inCluster)
	if inCluster {
		// Said here, and said *after* the lines above, because the log is read in
		// order and `internal/terminal` has already announced what the image
		// carries — "claude terminal available (…)" is true of the build and
		// says nothing about what is served. Without this line an operator reads
		// that one, concludes the assistant is reachable, and is wrong.
		//
		// **And the answer is not one status.** The removed addresses sit
		// behind two different misses. The
		// four under `/api/` reach the API mount, which has no catch-all, so they
		// answer 404 `no operation at ...` - byte for byte what `/api/never-existed`
		// answers. The three `/kt-api/` upgrades are not under that mount, so they
		// fall through to the single-page catch-all and answer 200 with index.html -
		// again byte for byte what any unknown path answers, 2251 bytes of it.
		// Saying "the ordinary 404" of all seven would send an operator who curls
		// `/kt-api/terminal`, sees 200, and concludes the removal did not work.
		// **The guarantee is indistinguishability from a path that was never
		// registered, not a particular status**, and that is what this says.
		log.Printf("[kubetower] server profile: no local shell, no node shell and no assistant. " +
			"Those addresses are not registered at all, so each answers exactly what a path that " +
			"never existed answers - 404 under /api/, the single-page shell under /kt-api/ - rather " +
			"than a handler that refuses. Pod exec is unchanged and is bounded by this pod's RBAC")
	}

	mux.HandleFunc("GET "+auth.LoginPath, front.LoginPage)
	// **The form's POST exists only where a password is offered**, which
	// is the same decision internal/api makes for `POST /api/auth/login` - not
	// registered rather than refusing, so that walking this router is the test.
	// See signinroutes_test.go.
	mountPasswordForm(mux, front)
	// The native window's one-time sign-in: the desktop's, and only
	// where the form above exists. See signinroutes.go.
	mux.HandleFunc(auth.LogoutPath, front.Logout)

	// Three probe paths and two meanings, which is the reconciliation rather
	// than a duplication.
	//
	// `api/openapi.yaml` declares `/healthz` (the process is up) and `/readyz`
	// (the configuration is readable, and **this one has to be able to fail**).
	// Both are registered by internal/api, through web.Mount.
	//
	// `/kt-healthz` is what `compose.dev.yaml`'s healthcheck calls, and its
	// meaning is readiness — it degrades when the kubeconfig stops reading or when
	// no context answers. **It answers plain text**, because a probe is the one
	// endpoint where changing the body is changing the contract with whatever is
	// watching. What it is not is a second implementation: both it and `/readyz`
	// read `health.Readiness`, so the two cannot disagree about whether this
	// process can reach anything.
	mux.HandleFunc("GET "+auth.HealthPath, healthz(ready))

	// Two layers, and the split is kept on purpose although nothing escapes the
	// guard today: `outer` has exactly one registration and could be collapsed
	// into `handler := web.SecurityHeaders(front.Middleware(mux), …)` with no
	// change in behavior.
	//
	// It is not collapsed, and that is a decision rather than an oversight. Two
	// routers make "outside the guard" a thing a reader can see rather than a
	// thing a comment claims: a route registered on the *guarded* mux under a
	// comment saying it is outside answers "session missing or expired" to every
	// call, and the comment stops anyone looking. An endpoint that authenticates
	// itself goes on `outer`.
	outer := http.NewServeMux()
	// Inside the guard, Standing gives every request a way to ask later whether
	// its sign-in still stands, which an open pod exec or log follow does while
	// it runs: those are authenticated once, at the upgrade.
	mountOuter(outer, front.Middleware(front.Standing(mux)), ui.NoticesHandler(), inCluster)
	return ui, outer, nil
}

// newServer is the console's HTTP server, its limits set and nothing started.
//
// **No ReadTimeout and no WriteTimeout**: log streams, SSE and the terminal's
// websocket hold a request open for as long as the operator is watching, and
// either timeout would cut them. IdleTimeout applies only between requests on
// a keep-alive connection, never to a request in flight or a hijacked socket,
// so it closes forgotten connections without touching a stream.
// MaxHeaderBytes is well above any browser's request headers, cookies
// included, and well below the 1 MiB net/http accepts by default.
func newServer(listen string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
	}
}

// serve serves handler on the listener already bound, on a goroutine of its
// own, and returns the server so that Run can shut it down.
//
// A server that stops on its own - anything but Shutdown - posts its error on
// served and calls stop, which ends Run's context, so Run shuts the rest down
// and returns that error instead of the process ending under it.
func serve(ln net.Listener, bound, listen string, handler http.Handler, served chan<- error, stop func()) *http.Server {
	srv := newServer(listen, handler)

	go func() {
		announceListen(bound, cluster.InContainer())
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			served <- fmt.Errorf("the console stopped serving: %w", err)
			stop()
		}
	}()
	return srv
}

// watchWindowStop reads `stop` while the window blocks Run's goroutine.
//
// **While it blocks, nothing on this goroutine reads `stop`**, so a
// goroutine does: a signal, or the caller's context, asks the window to
// close, and the window's own way out then takes the server down
// gracefully. Without it `kubetower --app` from a macOS terminal would ignore
// Ctrl-C and need `kill -9`. A signal it took is remembered in
// `interrupted`, because when openWindow fell back to a browser there is
// no window to close and the wait in Run must not then wait for a second
// signal. The goroutine is waited for before `interrupted` is read, so a
// signal that arrives as the window closes is seen by exactly one side.
//
// It reads `stop` until the window has closed, not once: each signal asks the
// window again, so a second update's stop is not left in the channel's buffer
// behind a first that did not close it (#101).
//
// windowDone is Run's to close when the window has closed, and watcherDone is
// closed when the goroutine has finished.
// interruptWindow is interruptNativeWindow, held in a variable so a test
// counts the asks.
var interruptWindow = func() { interruptNativeWindow() }

func watchWindowStop(ctx context.Context, stop <-chan os.Signal) (interrupted, windowDone, watcherDone chan struct{}) {
	interrupted = make(chan struct{})
	windowDone = make(chan struct{})
	watcherDone = make(chan struct{})
	go func() {
		defer close(watcherDone)
		once := false
		for {
			select {
			case <-stop:
				// Every stop asks again: a window the first ask did not close
				// (#101) must close on the next, an update installed a second
				// time among them.
				if !once {
					close(interrupted)
					once = true
				}
				interruptWindow()
			case <-ctx.Done():
				interruptWindow()
				return
			case <-windowDone:
				return
			}
		}
	}()
	return interrupted, windowDone, watcherDone
}

// frontHandler is everything the server answers with: the security headers,
// inside them the Host allowlist where hostPolicy asks for it, and inside that
// outer - the session guard and every route. One function so Run and its test
// build the same thing - so a test fails if Run stops wiring the guard - and it
// returns the start-up sentence saying which Host policy is in force.
//
// Under --incluster, and only there, Strict-Transport-Security goes outermost:
// the server profile is reached
// through a TLS ingress, the desktop over plain-HTTP loopback. See
// web.StrictTransport for the parameters.
func frontHandler(bundle fs.FS, outer http.Handler, listen string, inCluster, inContainer bool) (http.Handler, string) {
	guarded := outer
	checkHost, why := hostPolicy(listen, inCluster, inContainer)
	if checkHost {
		guarded = guardHost(outer)
	}
	handler := web.SecurityHeadersFor(bundle, guarded, auth.LoginPageHTML())
	if inCluster {
		handler = web.StrictTransport(handler)
	}
	return handler, why
}

// listenFirst binds listen, or says why it could not in a sentence that names
// the way out. Called before any window is opened (see Run).
func listenFirst(listen string) (net.Listener, error) {
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return nil, fmt.Errorf("KubeTower cannot listen on %s (%v). Something else holds that address, and "+
			"no window was opened on it. Close it, or start KubeTower on another address with KT_LISTEN",
			listen, err)
	}
	return ln, nil
}

// windowMint is what this process's own window mints its one-time codes with,
// or nil where no code can be minted: under --incluster, or where the shared
// account a code signs in as does not exist (single sign-on without the shared
// password).
//
// **It follows the launch offer, never the password.** On the loopback desktop
// the password is withdrawn, and that is exactly where the window must arrive
// signed in: gated on the password, the first window of a double-click would
// open with no code onto a page that has no other way in.
func windowMint(front *auth.Auth, inCluster bool) func(native bool) (string, error) {
	if inCluster || !front.OffersLaunch() {
		return nil
	}
	return front.IssueLaunchCode
}

// shutdown gives the server five seconds to finish what it is doing.
//
// Its own function because there are two ways out — a closed window and a
// signal — and two copies of a graceful shutdown is how one of them comes to be
// the ungraceful one.
func shutdown(srv *http.Server, shells *terminal.Terminal) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	// Shutdown does not wait for a hijacked connection, so the shells and
	// assistant sessions still open are ended here, and their directories - a
	// kubeconfig each - removed before the process goes.
	shells.Close()
}

// healthz is the container's probe, and it means something: a process whose
// kubeconfig mount has gone, or whose `aws` binary cannot run, must not stay
// `healthy` and in service while the one thing it exists to do is impossible.
//
// Degraded means one of two things and nothing else: the kubeconfig no longer
// reads, or not one context answered its last probe. **Some clusters
// unreachable is still 200** — that is the ordinary state of this installation,
// it is what the health page is for, and restarting the container over it would
// take away the one screen that explains what happened at the moment somebody
// opened it.
//
// The answer never probes inline. The compose healthcheck allows five seconds
// and a single cluster is allowed ten; see internal/health/readiness.go.
func healthz(probe readiness) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ok, reason := probe.Ready(r.Context())

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		// No caching: a probe reading a cached "ok" is a probe that cannot fail.
		w.Header().Set("Cache-Control", "no-store")
		if !ok {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("degraded: " + reason + "\n"))
			return
		}
		_, _ = w.Write([]byte("ok: " + reason + "\n"))
	}
}

// readiness adapts the probe to the one method the JSON contract asks for.
//
// `health.Readiness` already answers in a shape a handler can turn into a status
// and one line of text, and that line is not decoration: a probe that says only
// "ok" is a probe nobody can debug. Both `/readyz` and `/kt-healthz` are handed
// the same struct, so the JSON answer and the plain-text one are the same
// judgment rendered twice rather than two judgments.
//
// # Not ready while the audit log is broken
//
// Once a record could not be written - a failed, short or stalled write - the log
// is broken for the rest of the process (internal/audit) and the gate refuses
// every act. A replica in that state still serves pages and refuses every
// write, exec, forward and reveal it is asked for, so it is taken out of its
// Service: replicas whose logs still write take the traffic, and the reason
// names why.
//
// **Readiness only, never liveness.** `/healthz` stays "the process is up". A
// restart would recover the log, and whether to restart a pod whose stdout
// broke - losing the state of whatever broke it - is the operator's call, not
// an automatic one.
//
// # Not unready over an API-server blip, under the server profile
//
// On the desktop the probe counts contexts, and "not one of them answered"
// means this process can do nothing. Under --incluster there is one context,
// the API server every replica shares, so without a grace a blip in it would
// make every replica unready at once and empty the Service: the console would
// go from degraded to unreachable, and its own health page with it.
//
// **For apiServerGrace, and no longer.** A failure that outlasts it is not a
// blip, and may be this replica's own - a ServiceAccount token refused, node
// egress or DNS gone, a NetworkPolicy that drops this pod - and a replica that
// cannot reach the API server must not stay in its Service answering errors
// behind a green probe. So the probe's failure is reported
// and tolerated for the grace, then answered 503 until the API server answers
// again; each transition is logged. The audit log above is not graced: it is
// this replica's own, and 503 at once.
type readiness struct {
	probe interface {
		Readiness(context.Context) health.Readiness
	}
	gate      *access.Gate
	inCluster bool
	// outage is when the probe first failed, shared by every copy of this
	// value; now is time.Now outside a test.
	outage *outage
	now    func() time.Time
}

// apiServerGrace is how long a server-profile replica stays ready while the API
// server does not answer. Two minutes: longer than a control-plane restart,
// shorter than anybody waits for a console that answers errors.
const apiServerGrace = 2 * time.Minute

func newReadiness(probe interface {
	Readiness(context.Context) health.Readiness
}, gate *access.Gate, inCluster bool) readiness {
	return readiness{probe: probe, gate: gate, inCluster: inCluster, outage: &outage{}, now: time.Now}
}

// outage is the current run of failed probes.
type outage struct {
	mu      sync.Mutex
	since   time.Time
	gaveUp  bool
	failing bool
}

func (r readiness) Ready(ctx context.Context) (bool, string) {
	if err := r.gate.AuditBroken(); err != nil {
		return false, "the audit log stopped writing, so this replica refuses every act until it is restarted: " + err.Error()
	}
	answer := r.probe.Readiness(ctx)
	if !r.inCluster || r.outage == nil {
		return answer.OK, answer.Reason
	}
	now := r.now()
	o := r.outage
	o.mu.Lock()
	defer o.mu.Unlock()
	if answer.OK {
		if o.failing {
			log.Printf("[kubetower] readiness: the API server answers again after %s; ready", now.Sub(o.since).Round(time.Second))
		}
		o.failing, o.gaveUp = false, false
		return true, answer.Reason
	}
	if !o.failing {
		o.failing, o.since = true, now
		log.Printf("[kubetower] readiness: %s; staying ready for up to %s, since every replica shares that API server",
			answer.Reason, apiServerGrace)
	}
	if elapsed := now.Sub(o.since); elapsed < apiServerGrace {
		return true, fmt.Sprintf("serving, though %s for %s of a %s grace; every replica shares that API server, "+
			"so this replica stays in its Service and the health page says what is wrong",
			answer.Reason, elapsed.Round(time.Second), apiServerGrace)
	}
	if !o.gaveUp {
		o.gaveUp = true
		log.Printf("[kubetower] readiness: %s for longer than %s; not ready until it answers", answer.Reason, apiServerGrace)
	}
	return false, fmt.Sprintf("%s for longer than %s", answer.Reason, apiServerGrace)
}

// metricsKind adapts the metrics layer to the one method internal/health wants.
//
// health asks for a string because all it does with the answer is print it in a
// cell; metrics answers with a Discovery because everything else about it
// matters somewhere. The conversion is the interesting part: a discovery that
// *could not ask* the cluster reports nothing at all rather than its kind:
// rendering "none" there would be a claim about the cluster made from a
// failure to reach it.
type metricsKind struct{ *metrics.Metrics }

func (m metricsKind) Discover(ctx context.Context, name string, force bool) string {
	found := m.Metrics.Discover(ctx, name, force)
	if found == nil || found.CouldNotAsk() {
		return ""
	}
	return found.Kind
}

// Cached is the same projection over the cache-only read, and it takes no context
// because it contacts nothing. The health page's background round uses it so that
// warming a row does not stand up a port-forward to find Prometheus; the same
// "could not ask reports nothing" rule applies, for the same reason.
func (m metricsKind) Cached(name string) string {
	found := m.Metrics.Cached(name)
	if found == nil || found.CouldNotAsk() {
		return ""
	}
	return found.Kind
}

// frontDoor builds the session guard.
//
// A missing password is fatal, not a warning that starts an open server. This
// process holds administrator credentials for production clusters; the failure
// mode of getting that wrong is not "less secure", it is "anyone on this machine
// has production".
//
// **It also seals the shared store**, when there is one, because the key it
// seals with is the session key and this is where that is decided: the store's
// values are opened only by a process holding the key every replica shares
// (`sharedstate.Sealed` says why a store's own password is not enough). The
// sealed store comes back so the plan store uses the same one; nil means each
// keeps its own memory, as one replica always has.
// mountOuter fills the router in front of the session guard: everything goes
// to the guard, except, on the desktop only, a GET or HEAD of the third-party
// notices.
//
// **Desktop: public, GET only.** The console listens on loopback, the file is
// the licenses of what the binary is built from, and a person who has lost the
// password still has a right to read them. Mounted here, on an exact pattern
// with a method, so a POST, PUT or OPTIONS on the same path falls through to
// the guard and gets the same 401 as any other route - an exemption in the
// guard itself would let every method past it.
//
// **Server profile: behind sign-in.** Anyone who can reach
// a shared console would otherwise read the exact version of every dependency
// without signing in, which is a fingerprint. The guarded router still serves
// the file to a signed-in person (internal/web, Mount), and the image carries
// it on disk under /usr/share/licenses/kubetower/.
func mountOuter(outer *http.ServeMux, guarded, notices http.Handler, inCluster bool) {
	outer.Handle("/", guarded)
	if !inCluster {
		outer.Handle("GET "+web.NoticesPath, notices)
	}
}

// signedInWithoutPassword is what the settings say about the front door: this
// console asks nobody for a password. The install report says the same fact
// in words of its own.
//
// **The front door decides, not the listen address.** It is the one place that
// knows whether a password is offered: KT_PASSWORD_HASH keeps one on the
// loopback desktop on purpose, a container or a wider listen draws one, and
// single sign-on takes it away for a reason that has nothing to do with this
// machine - which is why the server profile is excluded outright.
func signedInWithoutPassword(inCluster bool, front interface{ OffersPassword() bool }) bool {
	return !inCluster && !front.OffersPassword()
}

func frontDoor(inCluster bool, kv sharedstate.Store) (*auth.Auth, sharedstate.Store, error) {
	sso, keepLocal, err := singleSignOn(inCluster)
	if err != nil {
		return nil, nil, err
	}
	if sso != nil && os.Getenv("KT_SESSION_SECRET") == "" {
		log.Printf("[kubetower] %s", ssoKeyNotShared)
	}
	if sso != nil && !keepLocal {
		// **No password at all, and none drawn.** Drawing one here would
		// write a credential into the state directory that opens nothing, and a
		// login page pointing at it would be a lie. The session key is still
		// needed, for the cookie that carries a single sign-on session's id.
		key, err := sessionKey()
		if err != nil {
			return nil, nil, err
		}
		sealed, err := sealShared(kv, key)
		if err != nil {
			return nil, nil, err
		}
		log.Printf("[kubetower] single sign-on through %s; the shared password is not offered, and its "+
			"routes are not registered. Set KT_AUTH_LOCAL_ACCOUNT=true to keep it", sso.Issuer)
		front, err := auth.New(auth.Config{
			SessionKey:  key,
			SessionIdle: envDuration("KT_SESSION_TTL", auth.DefaultSessionIdle),
			SessionMax:  envDuration("KT_SESSION_MAX", auth.DefaultSessionMax),
			OIDC:        sso,
			Store:       sealed,
		})
		if err != nil {
			return nil, nil, err
		}
		log.Printf("[kubetower] %s", front.SessionPolicy())
		if names := front.NamePolicy(); names != "" {
			log.Printf("[kubetower] %s", names)
		}
		return front, sealed, nil
	}
	if sso != nil {
		log.Printf("[kubetower] single sign-on through %s, and the shared password stays available beside "+
			"it because KT_AUTH_LOCAL_ACCOUNT is set. Anybody holding that password bypasses the identity "+
			"provider, and the write log cannot say who they are", sso.Issuer)
	}

	hash := os.Getenv("KT_PASSWORD_HASH")
	// Which branch supplied the password, read before the branch runs because
	// `hash` is about to be overwritten by the file's. See the login-page hint
	// at the bottom of this function for what it decides.
	configured := hash != ""
	key, err := sessionKey()
	if err != nil {
		return nil, nil, err
	}

	// **The environment wins, and the file is the fallback.**
	//
	// In the container there is one source and it is `.env`; that is right for a
	// deployment and it is why this is checked first. A binary somebody
	// downloaded has no `.env`, so it draws a password on first run, shows it
	// once and keeps only the hash — see firstrun.go. Outside the loopback
	// desktop below there is still no way to run without a password; this is a
	// second way to have one.
	//
	// **Except on the loopback desktop**: the desktop profile, outside a
	// container, on loopback - and with no KT_PASSWORD_HASH, which keeps the
	// password for whoever set one on purpose: the development loop and the
	// smoke suite. There, nothing is drawn and none is offered; firstrun.go says
	// what stands in front of the console instead.
	//
	// The file is consulted only when the environment is silent, so a stale
	// `credentials.json` in a mounted volume can never override what the
	// deployment set — which would make the container's front door depend on what
	// happened to be on the disk.
	passwordless := !configured && loopbackDesktop(envOr("KT_LISTEN", defaultListen), inCluster,
		cluster.InContainer())
	if hash == "" {
		stored, storedKey, err := storedCredentials(!passwordless)
		if err != nil {
			return nil, nil, err
		}
		hash = stored.PasswordHash
		if os.Getenv("KT_SESSION_SECRET") == "" {
			// The stored key too, so closing and reopening the binary does not
			// log the operator out. A drawn key is right for a server and wrong
			// for something with a window.
			key = storedKey
			log.Printf("[kubetower] sessions signed with the key in the credentials file, " +
				"so they survive a restart")
		}
	} else if os.Getenv("KT_SESSION_SECRET") == "" {
		// The environment set a password and no key, which is the container's
		// shape, and the one branch where a drawn key means sessions do not
		// survive a restart.
		log.Printf("[kubetower] no %s: sessions will not survive a restart", "KT_SESSION_SECRET")
	}

	// After the key is final: the credentials file may have replaced the drawn
	// one above, and a store sealed under a key this process then discards
	// would hold values nothing can open.
	sealed, err := sealShared(kv, key)
	if err != nil {
		return nil, nil, err
	}
	// Whose X-Forwarded-For a password attempt is counted and logged by:
	// the ingress's addresses, named by the deployment. Refused at
	// start rather than half-read, since a typo here is a proxy not believed.
	proxies, err := auth.ParseTrustedProxies(os.Getenv("KT_TRUSTED_PROXIES"))
	if err != nil {
		return nil, nil, fmt.Errorf("KT_TRUSTED_PROXIES: %w", err)
	}
	if inCluster && len(proxies) == 0 {
		// Said once at start, because the consequence is invisible otherwise:
		// every password attempt arrives from the ingress, so no attempt can be
		// counted against the person making it, and the log names the ingress
		// for every failure. The residual in numbers is internal/auth's
		// (attempts.go): a browser that signed in before keeps a lane of its
		// own, and a first-time browser shares five attempts a second with
		// whoever floods the queue.
		log.Printf(passwordBehindAnUnnamedIngress, "KT_TRUSTED_PROXIES")
	}
	front, err := auth.New(auth.Config{
		User:           envOr("KT_USER", "admin"),
		PasswordHash:   hash,
		SessionKey:     key,
		TrustedProxies: proxies,
		BehindProxy:    inCluster,
		// Behind the server profile's TLS ingress the browser arrives over
		// https, so the session cookie is Secure. Where single sign-on is
		// configured beside the password its redirect URL decides; the desktop
		// listens on plain HTTP and keeps neither.
		SecureCookies: inCluster && sso == nil,
		// **The variable is still called KT_SESSION_TTL and the field is not**,
		// and this is the one place both names are visible, so this is where it
		// is explained. It is the *idle* window: a session in use never runs
		// out, one left alone ends. The variable keeps its name because renaming
		// it would silently widen every installation that had deliberately
		// shortened it, and a security-relevant value that stops being read is
		// the worst way to change a default.
		//
		// The default is named rather than restated: the argument for thirty and
		// ninety days is written where the constants are, and envDuration puts
		// the fallback into its own "that is not a duration" log line, which
		// would otherwise read "using 0s".
		SessionIdle: envDuration("KT_SESSION_TTL", auth.DefaultSessionIdle),

		// The ceiling, counted from the login and not from the last request. The
		// token is stateless: Logout only drops the cookie, so a copy taken from
		// a browser profile or a screenshot opens every production cluster until
		// it expires — and an idle window alone never expires a cookie somebody
		// is using. This is the only bound on that short of rotating
		// KT_SESSION_SECRET.
		SessionMax: envDuration("KT_SESSION_MAX", auth.DefaultSessionMax),

		// The one-time file holding a drawn password in clear stops being needed
		// the instant somebody has logged in with it — see passwordonce.go.
		//
		// **Wired unconditionally, including on the branch where the password came
		// from `KT_PASSWORD_HASH`.** `removePasswordOnce` on a file that is not
		// there is silent, and the case where it *is* there and the environment
		// also set a hash is a real one: an operator who ran the binary once and
		// then put a hash in `.env` has a note on disk that nothing else would
		// ever clear. Making the wiring depend on which branch supplied the hash
		// would leave exactly that plaintext password behind, to save a nil check.
		OnLogin: removePasswordOnce,

		// The login page names the one-time file when there is one, because a
		// password nobody can find is a password nobody has — see
		// `drawnPasswordNote`: shown is not the same as obtainable.
		//
		// **Wired only on the branch where this process's own password is the
		// drawn one, which is the opposite of the line above and deliberately
		// so.** `removePasswordOnce` is harmless on a note that belongs to
		// nothing, so it runs unconditionally; a *hint* is not harmless. An
		// operator who ran the binary once and then set `KT_PASSWORD_HASH` has a
		// stale note on disk whose password no longer opens anything, and a login
		// page pointing at it would be sending them to a file that is wrong
		// rather than merely absent. It is still deleted at their first login by
		// the hook above.
		PasswordNote: passwordNoteFor(configured || passwordless),

		WithoutPassword: passwordless,

		// Nil unless KT_OIDC_ISSUER is set, and only reached here with the
		// password kept beside it.
		OIDC:             sso,
		KeepLocalAccount: keepLocal,
		Store:            sealed,
	})
	if err != nil {
		return nil, nil, err
	}
	// Said out loud because both halves have defaults and both are settable, so
	// "why am I being asked to sign in" has four possible answers and none of
	// them is visible from a browser. The line before this one already reports
	// which key signs the session; this reports how long one lasts.
	log.Printf("[kubetower] %s", front.SessionPolicy())
	if names := front.NamePolicy(); names != "" {
		log.Printf("[kubetower] %s", names)
	}
	// **Whose rights the password carries**, said where the operator
	// who set it reads: under the server profile the shared account is
	// reviewed as the console itself (internal/access, "Whose rights are
	// reviewed"), so each person holding the password acts with this pod's
	// ServiceAccount's rights, whatever their own RoleBindings say.
	if inCluster && front.OffersPassword() {
		source := "KT_PASSWORD_HASH"
		if !configured {
			source = "the password drawn into the state directory"
		}
		log.Print(sharedAccountRights(envOr("KT_USER", "admin"), source))
	}
	return front, sealed, nil
}

// sharedAccountRights is the server profile's start-up line naming the rights
// a holder of the shared password acts with.
func sharedAccountRights(user, source string) string {
	return fmt.Sprintf("[kubetower] server profile: everybody who signs in with the shared password (%s) "+
		"signs in as %q and acts with this pod's ServiceAccount's rights - reviewed by the console's own "+
		"SelfSubjectAccessReview, never as themselves - which the threat model puts close to cluster-admin "+
		"less impersonation. The write log records them as via=password and cannot say which of them it was. "+
		"Single sign-on (KT_OIDC_ISSUER) without KT_AUTH_LOCAL_ACCOUNT removes the shared password",
		source, user)
}

// sealShared wraps a shared store in the session key, or answers nil for none -
// which auth.New and the plan store each read as "keep your own memory".
func sealShared(kv sharedstate.Store, key []byte) (sharedstate.Store, error) {
	if kv == nil {
		return nil, nil
	}
	return sharedstate.Seal(kv, key)
}

// singleSignOn reads the identity provider's configuration, or nil when
// KT_OIDC_ISSUER is unset - every deployment without single sign-on, and every
// desktop.
//
// **A provider that is configured and unreachable is not an error here**: that
// is a state the console runs in and says so. What is an error is a
// configuration that could never work - an issuer with no client id, a redirect
// URL that is not this console's callback - because that console would retry
// discovery forever and then refuse every sign-in, which looks like a provider
// problem and is not one. auth.New makes those refusals; this only reads.
//
// **KT_AUTH_LOCAL_ACCOUNT is read here and nowhere else**, and it means nothing
// without an issuer: with no provider the shared password is the only way in and
// is always offered.
//
// # An issuer without --incluster is a refusal to start
//
// OIDC is the server profile's authentication only, and the reason is not
// tidiness. **The desktop profile registers what the server profile removes**:
// the local shell, the node shell, the desktop terminal and the assistant. Each
// of those runs as this process - its user, its mounted kubeconfig with every
// context in it, privileged node-shell pods, and the operator's own assistant
// subscription. With a password that is one person's machine. With an identity
// provider it is **every person the provider accepts**, and there is no
// allow-list of users or groups in this console: a broad provider (a whole
// tenant, Google, GitHub without an organization restriction) makes that anyone.
//
// So the combination is fatal rather than a warning, and fatal here, before a
// single route is registered: a log line nobody reads does not stop a shell
// being handed out. TestSingleSignOnIsRefusedOutsideTheServerProfile holds it.
func singleSignOn(inCluster bool) (*auth.OIDCConfig, bool, error) {
	issuer := strings.TrimSpace(os.Getenv("KT_OIDC_ISSUER"))
	keepLocal := envBool("KT_AUTH_LOCAL_ACCOUNT", false)
	if issuer == "" {
		if os.Getenv("KT_AUTH_LOCAL_ACCOUNT") != "" {
			log.Printf("[kubetower] KT_AUTH_LOCAL_ACCOUNT is set and no KT_OIDC_ISSUER is: with no identity " +
				"provider the shared password is the only way in, so the setting changes nothing")
		}
		return nil, false, nil
	}
	if !inCluster {
		return nil, false, errSSOOutsideServerProfile
	}
	cfg := &auth.OIDCConfig{
		Issuer:        issuer,
		DiscoveryURL:  strings.TrimSpace(os.Getenv("KT_OIDC_DISCOVERY_URL")),
		ClientID:      strings.TrimSpace(os.Getenv("KT_OIDC_CLIENT_ID")),
		ClientSecret:  os.Getenv("KT_OIDC_CLIENT_SECRET"),
		RedirectURL:   strings.TrimSpace(os.Getenv("KT_OIDC_REDIRECT_URL")),
		Scopes:        splitList(os.Getenv("KT_OIDC_SCOPES")),
		UsernameClaim: strings.TrimSpace(os.Getenv("KT_OIDC_USERNAME_CLAIM")),
		// Not trimmed, as the groups prefix is not: a prefix is spelled
		// exactly, and a stray line ending is refused by auth.New rather than
		// quietly removed.
		UsernamePrefix: os.Getenv("KT_OIDC_USERNAME_PREFIX"),
		GroupsClaim:    strings.TrimSpace(os.Getenv("KT_OIDC_GROUPS_CLAIM")),
		GroupsPrefix:   os.Getenv("KT_OIDC_GROUPS_PREFIX"),
		SessionMax:     envDuration("KT_OIDC_SESSION_MAX", auth.DefaultSSOSessionMax),
	}
	// The secret's presence and never its value, and the discovery address
	// only where it differs, because that is the pair the issuer trap is about.
	secret := "no client secret (a public client; PKCE protects the exchange)"
	if cfg.ClientSecret != "" {
		secret = "a client secret"
	}
	discovery := ""
	if cfg.DiscoveryURL != "" {
		discovery = ", discovered at " + cfg.DiscoveryURL
	}
	log.Printf("[kubetower] identity provider %s%s, client %q with %s, callback %s", cfg.Issuer, discovery,
		cfg.ClientID, secret, cfg.RedirectURL)
	return cfg, keepLocal, nil
}

// errSSOOutsideServerProfile is the refusal, as one sentence an operator reads
// in the only line the process writes before it exits.
var errSSOOutsideServerProfile = errors.New("KT_OIDC_ISSUER is set and --incluster is not. Single sign-on " +
	"is the server profile's only: without --incluster this process also serves a local shell, a " +
	"node shell, a desktop terminal and the assistant, all running as this process with its kubeconfig, " +
	"and every person the identity provider accepts would reach them. Start with --incluster, or unset " +
	"KT_OIDC_ISSUER to sign in with the password")

// splitList reads a list written with commas, spaces or both.
func splitList(raw string) []string {
	return strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' })
}

// passwordNoteFor is the login page's hint, or nothing.
//
// A named function rather than a conditional inside the literal above so that
// the decision has somewhere to be tested and somewhere to be read: "the hint is
// off when the deployment set the password" is the whole of it, and it is the
// difference between a page that helps a first run and a page that points at a
// password which stopped working when somebody wrote a hash into `.env`.
func passwordNoteFor(configured bool) func() string {
	if configured {
		return nil
	}
	return drawnPasswordNote
}

// ssoKeyNotShared is said when single sign-on is configured and
// KT_SESSION_SECRET is not. **The key is per process then**: it signs the
// session cookie and seals the sign-in flow, so a second replica - or this one
// after a restart - opens neither. With one replica that is a sign-in lost to a
// restart; behind a Service with two it is every other request signed out, and
// a sign-in that started on one pod and came back to the other read as
// "took too long".
const ssoKeyNotShared = "single sign-on is configured and KT_SESSION_SECRET is not: this process " +
	"signs sessions and seals the sign-in flow with a key of its own, so neither crosses to another " +
	"replica or survives a restart. Set KT_SESSION_SECRET to the same value on every replica"

// sessionKey reads the signing key from the environment, or draws one.
//
// **It does not say what a drawn key means**: `frontDoor` may replace the drawn
// key with the one stored in `credentials.json` *after* this function has run,
// so "sessions will not survive a restart" said here could contradict what the
// process does. A log line like that is worse than none - it is what somebody
// debugging "why am I logged out" reads first - so the announcement is
// `frontDoor`'s, where the answer is actually known.
func sessionKey() ([]byte, error) {
	if raw := os.Getenv("KT_SESSION_SECRET"); raw != "" {
		if decoded, err := hex.DecodeString(raw); err == nil && len(decoded) >= 32 {
			return decoded, nil
		}
		if len(raw) >= 32 {
			return []byte(raw), nil
		}
		log.Printf("[kubetower] KT_SESSION_SECRET is shorter than 32 bytes; drawing one instead")
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return key, nil
}

// envBool reads a flag that has a default of its own. An unparseable value is
// said out loud rather than silently taken as false — this one decides whether a
// model can run mutating commands on a production cluster.
func envBool(key string, fallback bool) bool {
	return envBoolFrom(os.Getenv, key, fallback)
}

// envBoolFrom is envBool over an environment of the caller's choosing, so a
// test can set the answer without setting the process's.
func envBoolFrom(getenv func(string) string, key string, fallback bool) bool {
	raw := strings.TrimSpace(getenv(key))
	if raw == "" {
		return fallback
	}
	switch strings.ToLower(raw) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	log.Printf("[kubetower] %s is not a yes/no value; using %t", key, fallback)
	return fallback
}

func logf(format string, args ...any) { log.Printf("[kubetower] "+format, args...) }

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if raw := os.Getenv(key); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			return d
		}
		log.Printf("[kubetower] %s is not a duration; using %s", key, fallback)
	}
	return fallback
}

// announceKubeconfig says which files this process read, which it skipped, and
// which names one file took from another.
//
// One function rather than four log calls at the call site, for the reason
// `announceListen` below gives: the warnings are the part worth having, and a
// warning nothing asserts is a warning that gets deleted by accident.
//
// **The shadow lines are the point of it.** Two kubeconfigs each carrying a
// `prod` context is what happens the first time somebody is handed a second
// file for a second account, and the merge keeps the first file's entry whole
// and discards the second's — so from the rail, from a URL and from the dock
// the second `prod` does not exist, with no error and nothing to click. An
// answer that could not be obtained must not be rendered as an answer
// as an answer; here it is not even rendered, so the only honest place left
// is out loud at startup.
//
// A *missing* path is a note and not a warning: `Precedence` skips it exactly
// as kubectl does, and a `KUBECONFIG` naming a file that lives on the
// operator's other laptop is deliberate. It is still said, because "my cluster
// is not in the rail" and "I typed that path wrong" look identical from the
// console.
func announceKubeconfig(files cluster.Kubeconfig, survey cluster.KubeconfigSurvey, contexts int) {
	// One file renders as just that file, so an operator with a single kubeconfig
	// reads a plain one-file line.
	log.Printf("[kubetower] %d contexts from %s", contexts, files)

	for _, file := range survey.Files {
		if file.Absent {
			log.Printf("[kubetower] %s names %s, which is not there: skipped, as kubectl does",
				cluster.EnvKubeconfig, file.Path)
		}
	}
	for _, shadow := range survey.Shadowed {
		// Composed in one place (`Shadow.Sentence`), so the log and the settings
		// answer cannot disagree about it.
		log.Printf("[kubetower] WARNING: %s", shadow.Sentence())
	}
}

// announceListen says where this is listening, and says more when that is not
// loopback.
//
// One function rather than two log calls at the call site, so a test can watch
// it: the warning is the part worth having, and a warning nothing asserts is a
// warning that can be deleted by accident.
func announceListen(listen string, inContainer bool) {
	log.Printf("[kubetower] listening on %s", listen)
	if !reachable(listen) || inContainer {
		// Inside a container this is not a finding: the container is the
		// boundary, every interface is the only workable binding, and the image
		// sets it deliberately. Warning here would fire on every start, and a
		// warning that always fires is one nobody reads on the day it means
		// something.
		//
		// Detection decides only whether to *print*, never where to bind — which is
		// why it is acceptable here and would not be for the default. Getting it
		// wrong costs a log line.
		return
	}
	// Said out loud, once, because the operator who did this on purpose should
	// see it confirmed and the one who did it by accident has no other way of
	// finding out. This process holds administrator credentials for every
	// cluster in the kubeconfig.
	log.Printf("[kubetower] %s is not loopback: this console is reachable from the network, "+
		"and it holds administrator credentials for every cluster in the kubeconfig. "+
		"Set KT_LISTEN=127.0.0.1:5823 if that was not deliberate", listen)
}

// reachable reports whether this address accepts connections from off the
// machine.
//
// An empty host — ":5823" — means every interface, and so does
// 0.0.0.0 and ::. Anything else is taken at face value: an operator who binds a
// specific LAN address has said what they meant, and this only exists to catch
// the two spellings that mean "everything" without looking like it.
func reachable(listen string) bool {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		// Unparseable addresses are the http package's problem, not this
		// warning's. Saying nothing beats saying something wrong.
		return false
	}
	switch strings.TrimSpace(host) {
	case "", "0.0.0.0", "::", "[::]":
		return true
	}
	return false
}

// extrasOptions is how the extras store is opened for a profile. Every extra is
// off where nobody chose, on every profile; what
// differs is KT_EXTRAS: a pin on the desktop and in a container, and under the
// server profile the chart's values naming what may be turned on
// (extras.InCluster). A function of its own so the server profile's answer is
// tested rather than read off a branch.
func extrasOptions(inCluster bool) []extras.Option {
	if inCluster {
		return []extras.Option{extras.InCluster()}
	}
	return nil
}

// extrasStartupLine is the start-up line's answer to "which extras are on",
// for the start-up's own inputs: whether it runs in a cluster, whether it is
// one of several replicas, and whether the state directory survives a restart
// (readStatePersistent's answer, as openProfile read it). openProfile logs it
// and passes nothing a test does not control, so the test of this function
// holds the line as the console prints it.
func extrasStartupLine(store *extras.Store, contexts []string, inCluster, several, statePersistent bool) string {
	return extrasSummary(store, contexts, extrasStartupProfile(inCluster, several, statePersistent))
}

// extrasStartupProfile is the profile the start-up line names. It takes
// whether the state directory is persistent and turns it into ProfileOf's
// ephemeral here, under a test: a persistent state directory is not an
// ephemeral one, and getting that backwards names a switch that is not
// registered.
func extrasStartupProfile(inCluster, several, statePersistent bool) extras.Profile {
	return extras.ProfileOf(inCluster, several, !statePersistent)
}

// extrasSummary is the startup line naming which extra pages are on, and where.
//
// **It counts clusters**: a page is ticked per cluster, so a page's name alone
// is not an answer — on which of fourteen? — and the honest summary is the page
// with the number of contexts behind it.
//
// It still says how to change the answer when it is "none", because "none" with
// nothing after it reads like a fault rather than the default this deliberately
// is - in extras.HowToTurnOn's words, the same the refusal in internal/web ends
// with.
func extrasSummary(store *extras.Store, contexts []string, profile extras.Profile) string {
	var on []string
	for _, e := range extras.Catalogue() {
		count := 0
		for _, context := range contexts {
			if store.EnabledOn(context, e.Key) {
				count++
			}
		}
		if count == 0 {
			continue
		}
		label := fmt.Sprintf("%s (%d/%d)", e.Label, count, len(contexts))
		if store.Pinned(e.Key) {
			// Which of the two sources decided matters more here than anywhere
			// else: an operator looking at a page they cannot turn off needs to
			// be sent to the deployment's KT_EXTRAS, not to a per-cluster switch.
			label = e.Label + " (" + extras.EnvExtras + ")"
		}
		on = append(on, label)
	}
	if len(on) == 0 {
		if len(extras.Catalogue()) == 0 {
			return "none — this build carries no extra pages"
		}
		return "none. " + extras.HowToTurnOn("<cluster>", "", profile)
	}
	return strings.Join(on, ", ")
}

// openClusters is the cluster manager, the kubeconfig list it was built from,
// and that list's survey.
//
// Under `--incluster` there is no list: the one cluster is the pod's own,
// reached with `rest.InClusterConfig()`, and the list and survey come back
// empty. The readiness probe then checks no file, the Settings page lists none,
// and the terminal layer - whose shells are not registered under the profile -
// is handed nothing to copy a context out of. It leaves the list empty under an
// in-cluster manager (`terminal.sessionKubeconfig`) rather than filling it from
// `KUBECONFIG`, so the startup line below stays true and a session it were ever
// asked for refuses rather than reading that file.
func openClusters(inCluster bool) (cluster.Kubeconfig, cluster.KubeconfigSurvey, *cluster.Manager, error) {
	if inCluster {
		clusters, err := cluster.NewInClusterManager()
		if err != nil {
			return nil, cluster.KubeconfigSurvey{}, nil, err
		}
		log.Printf("[kubetower] 1 context, %s, from this pod's ServiceAccount (--incluster); "+
			"%s is not read", cluster.InClusterContext, cluster.EnvKubeconfig)
		return nil, cluster.KubeconfigSurvey{}, clusters, nil
	}

	// **The kubeconfig is a list, and this is the one place it is resolved.**
	//
	// `KUBECONFIG` is an `os.PathListSeparator`-separated list of files, as
	// kubectl reads it — `KUBECONFIG=~/.kube/config;C:\work\extra.yaml` must not
	// be read as one filename with a semicolon in it.
	//
	// Resolved once here and handed on as a `cluster.Kubeconfig` to all three
	// readers below: the manager, the terminals and the readiness probe. It is a
	// type rather than a string precisely so that a fourth reader cannot treat
	// it as one file — see internal/cluster/kubeconfig.go: "the same correction
	// on every path" is not something to leave to whoever adds the path.
	kubeconfig := cluster.KubeconfigFromEnv()

	// **And then the ones nobody named.**
	//
	// The container mounts the operator's whole `~/.kube` and names exactly one
	// file in it. A directory whose kubeconfigs are called anything else — which
	// is ordinary the moment somebody is handed a second cluster — would make
	// *every* path in the list missing, and `Load` refuses that rather than
	// reporting an installation with no clusters in it, which is the right
	// refusal. Under compose's restart policy that would be a crash loop whose
	// only line named the file it wanted and not the four beside it.
	//
	// Discovery **extends the list** and merges nothing: the merge stays
	// client-go's, so it matches kubectl's exactly. Named files keep first-wins
	// precedence; discovered ones are appended after them, sorted, so two starts
	// resolve a duplicated context name the same way. `Survey` below then reports
	// the shadowing exactly as for named files, which is what makes a discovered
	// file that took a name from a named one visible instead of merely effective.
	//
	// Before the manager and before the terminals, so all three readers get the
	// same extended list — the property `cluster.Kubeconfig` exists to hold.
	discovered, scans := kubeconfig.Discover()
	for _, scan := range scans {
		if line := scan.Sentence(); line != "" {
			log.Printf("[kubetower] %s", line)
		}
	}
	kubeconfig = discovered

	clusters, err := cluster.NewManager(kubeconfig...)
	if err != nil {
		// **Say what was on disk, and in the container say what to do about it.**
		//
		// Refusing here is right: a kubeconfig that could not be read must never be
		// drawn as an installation with no clusters in it (`.env.example` spends a
		// paragraph on the empty bind mount that produces it). But a refusal saying
		// only `Config not found: /home/kubetower/.kube/config` — a file the operator
		// never had — is restarted by compose for ever with nothing to act on.
		//
		// Against the shipped image, a recipient who skips
		// *copy your ~/.kube here* gets a directory **Docker Desktop creates for
		// them**, empty, and four restarts with nothing naming the directory.
		// `WhyNothing` names it; the second line is the part `internal/cluster`
		// cannot write, because it does not know it is in a container.
		if why := cluster.WhyNothing(kubeconfig, scans); why != "" {
			log.Printf("[kubetower] %s", why)
			if cluster.InContainer() {
				// Said for any runtime, because `docker run -v` and Podman mount
				// it too; compose.dev.yaml is the one this repository ships.
				log.Printf("[kubetower] that directory is the container side of the kubeconfig " +
					"mount: mount the host directory that holds your kubeconfig there and start " +
					"again. With compose.dev.yaml that directory is HOST_KUBE_DIR in .env. " +
					"docs/DEVELOPMENT.md, \"The packaged back end\"")
			}
		}
		return nil, cluster.KubeconfigSurvey{}, nil, err
	}

	// What was read, what was skipped, and what the merge hid. Surveyed once and
	// used twice — the log now, the settings answer later — because reading the
	// files a second time would let the two disagree about which file a context
	// came from.
	kubefiles := kubeconfig.Survey()
	announceKubeconfig(kubeconfig, kubefiles, len(clusters.Contexts()))
	return kubeconfig, kubefiles, clusters, nil
}

// processGate is the process's one gate, with the audit log its profile
// keeps. A function of its own so that a test can hold the wiring - the log
// reaching the gate under the server profile - and not only auditLog.
func processGate(clusters *cluster.Manager, inCluster bool, logf func(string, ...any)) *access.Gate {
	return web.NewGate(clusters, inCluster, logf, auditLog(inCluster, logf))
}

// auditLog is where the audit records go: **stdout, under the server profile,
// and nowhere on the desktop.**
//
// Stdout because it is the one channel of a container that leaves the pod as
// it is written - the kubelet keeps it outside this process's reach, and the
// cluster's own log collection reads it - while the process log goes to a file
// in the state volume, which the audited process can rewrite (logfile.go keeps
// that log on file and stderr, so stdout carries the records and nothing else).
// Under the server profile the only other things this binary prints there are
// `--version` and the usage, which exit before this is reached.
//
// Nowhere on the desktop: one operator, acting with their own kubeconfig, so
// the API server's own audit already names them; and the Windows build's stdout
// under `-H windowsgui` is a handle whose every write fails, which would refuse
// every write the desktop makes. The gate keeps no record there, and the human
// `write:` lines in the process log are the record there.
//
// Said once at start, so an operator looking for the record knows where it is.
func auditLog(inCluster bool, logf func(string, ...any)) *audit.Log {
	if !inCluster {
		return nil
	}
	if logf != nil {
		logf("audit records go to stdout, one JSON object per line: an attempt before each write, exec, " +
			"port-forward and reveal, and its outcome after, joined by id. An act whose attempt cannot be " +
			"written is refused")
	}
	return audit.New(os.Stdout)
}

// sharePerPerson is one person's share of a ceiling: the share under the server
// profile, where a ceiling is every person's, and none on the desktop, where
// there is one person.
// idleExec is how long an open pod exec may receive nothing before it ends:
// terminal.IdleExecBound under the server profile, no bound on the desktop.
func idleExec(inCluster bool) time.Duration {
	if inCluster {
		return terminal.IdleExecBound
	}
	return 0
}

// logFollowMax is how long a log follow may run: KT_LOG_FOLLOW_MAX when set,
// else 2h on the desktop and 30m under the server profile, where the follows
// are every person's. A value that is not a duration, or lies outside
// logs.MinFollowMax to logs.MaxFollowMax, is refused rather than clamped.
func logFollowMax(inCluster bool) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv("KT_LOG_FOLLOW_MAX"))
	if raw == "" {
		if inCluster {
			return logs.DefaultFollowMax, nil
		}
		return logs.DesktopFollowMax, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("KT_LOG_FOLLOW_MAX=%q is not a duration such as 90m or 2h", raw)
	}
	if d < logs.MinFollowMax || d > logs.MaxFollowMax {
		return 0, fmt.Errorf("KT_LOG_FOLLOW_MAX=%q is out of range: it must be between %s and %s",
			raw, logs.MinFollowMax, logs.MaxFollowMax)
	}
	return d, nil
}

func sharePerPerson(inCluster bool, share int) int {
	if inCluster {
		return share
	}
	return 0
}

// passwordBehindAnUnnamedIngress is the start-up warning for password sign-in
// under --incluster with KT_TRUSTED_PROXIES empty, with its residual in numbers.
const passwordBehindAnUnnamedIngress = "[kubetower] password sign-in is on and %s is empty: behind an " +
	"ingress every attempt comes from the ingress's address, so none is counted against the person " +
	"making it and failures are logged against the ingress. A browser that signed in before keeps a " +
	"lane of its own, but a first-time browser shares five attempts a second with whoever sends them: " +
	"one host sending R requests a second refuses about (R-5)/R of first sign-ins (95%% at 100 a second). " +
	"Set it to the ingress controller's addresses"

// webUpdateReport is the update checker's answer in internal/web's own shape.
func webUpdateReport(report update.Report, checking bool) web.UpdateReport {
	return web.UpdateReport{
		State:      string(report.State),
		Latest:     report.Latest,
		ReleaseURL: report.ReleaseURL,
		CheckedAt:  report.CheckedAt,
		Note:       report.Note,
		Checking:   checking,
	}
}

// setupInstaller is SetupInfo.Installer: nil, as an interface, where nothing
// installs, so internal/web registers no route over a nil updater.
func setupInstaller(u *updater.Updater) web.UpdateInstaller {
	if u == nil {
		return nil
	}
	return u
}

// openWorkOf counts what a restart ends, from the same sources as the macOS
// window's quit question (nativewindowfacts.go): every terminal session and
// every port-forward.
func openWorkOf(w *workers) func() (int, int) {
	var terminals func() int
	if w != nil && w.shells != nil {
		terminals = w.shells.Open
	}
	var forwards func() int
	if w != nil {
		forwards = w.countForwards
	}
	return func() (int, int) { return countOf(terminals), countOf(forwards) }
}

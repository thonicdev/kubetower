package launch

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// What this binary is, for the `--version` flag and the startup line.
//
// Variables, stamped with `-ldflags -X` by cmd/ktbuild and the Dockerfile. They
// live in this package, not main, so the stamp names it in full in every build
// that links the console; -X is not subject to the internal/ rule, so a binary
// in another module can stamp it too. A stray `-X main.version` is silently
// ignored and yields a release that calls itself `dev`, which is why a test
// asserts on the Dockerfile and the build tool.
//
// An unstamped build (`go build ./cmd/kubetower`) gets the defaults below and
// says so: "dev" is true, while "0.1.0" from an unstamped build would be a lie
// that reaches a bug report.
const (
	// devVersion is what an unstamped build calls itself; developmentBuild
	// compares against it.
	devVersion = "dev"
)

// There is no edition: one product. A test refuses an edition stamp, because
// `-X` on a name that does not exist is silently ignored.

var (
	// version is the release, from the tag (release.yml, `cmd/ktbuild -version`).
	version = devVersion
	// built is the UTC date the release was cut, YYYY-MM-DD. Not a timestamp,
	// so two builds of identical source do not differ.
	built = "unknown"
)

// versionLine is the one line `--version` prints and the one the log opens with.
//
// The Go version is included because a misbehaving binary's first question is
// which compiler built it, and a vulnerability can concern the toolchain.
func versionLine() string {
	revision := ""
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			// Only present when built from a git work tree; absent rather than
			// a placeholder that looks like a hash.
			if setting.Key == "vcs.revision" && setting.Value != "" {
				revision = short(setting.Value) + ", "
			}
		}
	}
	return fmt.Sprintf("kubetower %s (%sbuilt %s, %s %s/%s)",
		version, revision, built, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}

// developmentBuild reports that nothing stamped a version into this binary:
// `go build ./cmd/kubetower`, `task build:api`, or an image built without
// KT_VERSION.
//
// For the Settings page, which must say in words that the build has no version
// rather than draw `dev` beside `built unknown` as if a field failed to render.
func developmentBuild() bool { return version == devVersion }

func short(revision string) string {
	if len(revision) > 12 {
		return revision[:12]
	}
	return revision
}

// usage is what `--help` prints.
//
// Short on purpose: everything else comes from the environment, and flags
// mirroring .env.example would be a second way to configure one binary.
func usage() string {
	return versionLine() + `

  kubetower                   serve the console on KT_LISTEN (default 127.0.0.1:5823)
  kubetower --app             serve, and open it in a window of its own
  kubetower open              a signed-in window: on the KubeTower already running here
                              if there is one, otherwise as --app
  kubetower --no-window       serve and open nothing, whatever the launch looks like
  kubetower --unattended      nobody is watching: never open the first-run box
  kubetower --install         put this binary where your applications live (macOS, Linux)
  kubetower --uninstall       take it back out, leaving your settings alone (on Windows:
                              open the uninstaller of the setup that installed it)
  kubetower --bundle=DIR      assemble a macOS KubeTower.app in DIR (the build's)
  kubetower --reset-password  draw a new password, show it once, and exit (the password
                              is asked for only in a container or beyond loopback)
  kubetower --version         print the line above and exit
  kubetower --notices         print the third-party notices this build carries and exit
  kubetower --help            print this

--install copies this file into your account and gives it a launcher (Linux)
or an .app bundle (macOS). On Windows, kubetower-<version>-setup.exe installs
it instead, with a Start-menu and a desktop shortcut. Per user, never
system-wide, and never asking for administrator or root: this process holds
administrator credentials for clusters, so it has no business being installed
where every account on the machine can read it.

No password on this machine: a desktop listening on loopback opens its window
signed in, through a one-time code it mints for that window. Opening KubeTower
again, or "kubetower open", asks the running one for another and opens a window
on it. Only names that mean this machine are answered (localhost, 127.0.0.1,
::1), which is what stops a web page that re-pointed its own name here.

The password, where there is one - in a container, with KT_LISTEN beyond
loopback, or set through KT_PASSWORD_HASH: there is no default one. The first
run in such a place draws it, delivers it once
and keeps only its bcrypt hash, in credentials.json under KT_STATE_DIR
(~/.kubetower). Delivered three ways at once, because a launcher can silently
throw the first two away: the terminal, a box on Windows, and PASSWORD-ONCE.txt
in that same directory, which is deleted the first time somebody logs in. After
that nothing can give it back — --reset-password draws another. In the container
it comes from KT_PASSWORD_HASH instead, and the environment wins over the file.

The box blocks the start-up it interrupts, which is what it is for on a desktop
and is a console that never opens its port when nothing is there to dismiss it.
--unattended is how a script says so: the box is not attempted, and the path of
PASSWORD-ONCE.txt is printed on stderr -- where a caller that does not yet know
where the state directory is can read it. It changes nothing else, and no launch
without it behaves differently.

The log: always kubetower.log in that same directory, and the terminal as well
when there is one. A shortcut, a Dock icon or a menu entry has no terminal at
all, which is why the file is not optional. It says on every start whether a
window is being opened and why, so "it launched and nothing appeared" has an
answer in the log rather than needing one guessed at.

The window: a Start-menu shortcut, a .desktop entry and a launch with no console
all open one. So does running the executable inside a macOS KubeTower.app, since
a bundle has nowhere to carry --app -- which means that if you run that same file
from a terminal to watch the log, --no-window is how you say so.

Everything else is environment variables; the full list is in .env.example.
The ones that matter on a laptop: KUBECONFIG, KT_LISTEN, KT_STATE_DIR,
KT_READONLY_CLUSTERS, KT_EXTRAS.

KUBECONFIG is a list, as it is for kubectl: several files separated by ';' on
Windows and ':' elsewhere, merged the same way, with the first file winning any
name two of them declare. The startup log names every file and warns about the
names that lost.`
}

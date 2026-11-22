// Package app is how a binary starts the console, and says which extensions it
// carries.
//
// It is public so that this is expressible from outside the module, which
// cannot import internal/. cmd/kubetower hands its extras over through the same
// door an extension built in another module would use.
//
// Run is the whole of the start-up, desktop window included, so there is one
// start-up path rather than two to keep in step. The code lives in
// internal/launch; this package is only its public door.
package app

import (
	"context"
	"io/fs"
	"os"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/internal/launch"
	"github.com/thonicdev/kubetower/internal/registry"
)

// Options is what a caller may say about a start-up beyond its extensions.
//
// Deliberately small: the console is configured by the environment and a few
// flags, and a second way to set the same things would come to disagree.
type Options struct {
	// Args is the command line without the program name, read exactly as the
	// binary reads its own: `--version`, `--no-window`, `--incluster` and the
	// rest. Nil means os.Args[1:], which is what a `main` wants.
	Args []string

	// UI is the front end served at `/`: the root of a Vite build, with
	// index.html at its top and its files under assets/. Nil is the bundle
	// embedded in the core, which carries the extras' pages too.
	//
	// A binary with pages of its own brings its own bundle. The console hashes
	// the shell's inline scripts from this same bundle into its
	// Content-Security-Policy, so the policy always matches the document.
	UI fs.FS
}

// Run registers exts, then starts the console and serves until ctx is done, a
// signal arrives, or the desktop window is closed.
//
// The extensions go through Register, so its refusals apply here too, and a
// refused one stops the start-up before anything is served. Extensions
// registered earlier are carried too.
//
// It returns nil after an ordinary stop and after a flag that answers and
// leaves (`--version`, `--help`). Some failures end the process themselves:
// `--install`, `--uninstall` and `--bundle` exit 1 after reporting, and clusters
// that cannot be opened at all, or a server that stops on its own, are fatal.
//
// With the desktop window open, closing it ends Run; ctx is looked at after.
// Without one (server profile, `--no-window`, browser fallback), ctx ends it.
//
// Once per process: it installs the signal handler, points the standard logger
// at the state directory's log file and reads process-wide flags.
//
// Call it from main's own goroutine: on Windows the window is a WebView2 message
// loop bound to the thread that created it, and only the main goroutine is on
// the thread the process started on.
//
// The version is stamped at link time into internal/launch (`version`,
// `built`); see internal/launch/version.go.
func Run(ctx context.Context, opts Options, exts ...extension.Extension) error {
	if err := Register(exts...); err != nil {
		return err
	}
	args := opts.Args
	if args == nil {
		args = os.Args[1:]
	}
	return launch.Run(ctx, args, opts.UI)
}

// Register hands the console the extensions this binary carries.
//
// Called before Run, or not at all when the extensions are handed to Run. It
// refuses a key that is not lower case, digits and inner hyphens (empty
// included), a duplicate key across calls, an old key that collides with a live
// one, and a route the router cannot mount. A refusal is an error, not a panic,
// so a main can print it; nothing of a refused call is registered.
func Register(exts ...extension.Extension) error { return registry.Register(exts...) }

// Extensions is what was registered, in registration order, which is menu
// order: a page's place in the menu is the caller's decision.
func Extensions() []extension.Extension { return registry.Extensions() }

// Keys is the registered keys, sorted, for a message that has to list them.
func Keys() []string { return registry.Keys() }

// Reset empties the registry. Tests only: a running console whose extensions
// changed underneath it would draw a menu that disagrees with its routes.
//
// Exported because a test in another module needs it: the registry is
// process-wide and refuses a key registered twice, so a module that registers an
// extension in one test and hands it to Run in another must empty it between
// the two (boundary/run_test.go). An export_test.go or internal/ would not
// reach it.
func Reset() { registry.Reset() }

// Command devwatch rebuilds and restarts a package whenever a .go file under
// the module changes.
//
// # Why this is in the repository rather than a dependency
//
// `task dev` runs two halves. A CSS or TSX change is hot-reloaded by Vite; this
// does the same for a Go change, without restarting the whole task.
//
// `air` or `wgo` would do it, but this repository has **no tool dependencies at all**:
// eighty lines of standard library beat a third-party module in the dependency
// graph of a public AGPL repository whose provenance is part of its legal position.
//
// # What it does, and what it deliberately does not
//
//   - It **builds, then runs the binary**, rather than running `go run`. On
//     Windows `go run` spawns the compiled binary as a child, and killing the
//     parent leaves the child holding the port.
//   - A build failure **leaves the last good binary running** and prints the
//     error. A watcher that kills a working console because somebody saved a
//     file mid-edit is worse than no watcher.
//   - It polls. `fsnotify` is a dependency, this is not, and a walk of a
//     module's .go files takes a few milliseconds. Half a second of latency on a save is below what a
//     compile costs anyway.
//   - It watches `.go` files only. Templates and static files are read from disk
//     at request time in development; restarting the process for them would be a
//     second, slower hot reload of something that already reloads.
//
// # Two flags for `task dev` and `task password`
//
//   - `-dotenv <file>` gives the child the variables `internal/devenv` allows
//     from that file — KT_PASSWORD_HASH, KT_SESSION_SECRET, KT_STATE_DIR — and
//     nothing else, never over a variable the environment already has. It is
//     here rather than in Task's `dotenv`, which loads the whole file: a `.env`
//     copied from `.env.example` carries the container's KT_FORWARD_PORTS, and
//     a desktop process given that binds every port-forward on 0.0.0.0. See
//     the package comment of `internal/devenv`.
//   - `-once` builds, runs the child to completion and exits with its status,
//     without watching. It is how `task password` runs `--reset-password` with
//     the same `.env` as `task dev`, and without a `go run` in between.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/thonicdev/kubetower/internal/devenv"
)

func main() {
	pkg := flag.String("pkg", ".", "the package to build and run")
	root := flag.String("root", ".", "the directory to watch for .go changes")
	every := flag.Duration("every", 500*time.Millisecond, "how often to look")
	dotenv := flag.String("dotenv", "", "a .env to take internal/devenv's allowed variables from")
	once := flag.Bool("once", false, "build, run the child once, and exit with its status")
	flag.Parse()

	environ := os.Environ()
	if *dotenv != "" {
		fromFile, err := devenv.Load(*dotenv)
		if err != nil {
			fmt.Fprintf(os.Stderr, "devwatch: %v\n", err)
			os.Exit(1)
		}
		next, added, shadowed, err := devenv.Apply(environ, fromFile)
		if err != nil {
			// Refused before anything is built or started: a state directory
			// created in the wrong place holds credentials.json from then on.
			fmt.Fprintf(os.Stderr, "devwatch: %v\n", err)
			os.Exit(1)
		}
		environ = next
		// Names, never values: two of the three are a password hash and a
		// session key, and these lines go to the terminal.
		for _, line := range fromFile.Report(added, shadowed) {
			fmt.Fprintf(os.Stderr, "devwatch: %s\n", line)
		}
	}

	binary, err := buildPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "devwatch: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(filepath.Dir(binary))

	// Ctrl-C reaches this process; the child is stopped in stop() below. Without
	// this the child outlives the watcher and holds the port, which is the whole
	// reason this builds rather than shelling out to `go run`.
	interrupted := make(chan os.Signal, 1)
	signal.Notify(interrupted, os.Interrupt, syscall.SIGTERM)

	args := flag.Args()
	var child *exec.Cmd
	stamps := snapshot(*root)

	// **Build, then stop, then run - in that order, and the order is the whole
	// behavior.** Stopping first would kill a working console on a save that does
	// not compile, and "leaving the previous build running" would then be printed
	// over nothing at all.
	build := func() bool {
		compile := exec.Command("go", "build", "-o", binary, *pkg)
		compile.Stdout, compile.Stderr = os.Stdout, os.Stderr
		return compile.Run() == nil
	}
	run := func() {
		child = exec.Command(binary, args...)
		child.Env = environ
		child.Stdout, child.Stderr, child.Stdin = os.Stdout, os.Stderr, os.Stdin
		if err := child.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "devwatch: %v\n", err)
			child = nil
		}
	}
	stop := func() {
		if child == nil || child.Process == nil {
			return
		}
		_ = child.Process.Kill()
		_, _ = child.Process.Wait()
		child = nil
	}

	if *once {
		if !build() {
			_ = os.RemoveAll(filepath.Dir(binary))
			os.Exit(1)
		}
		os.Exit(runOnce(binary, args, environ))
	}

	if !build() {
		fmt.Fprintln(os.Stderr, "devwatch: nothing compiled yet - fix the error above and save")
	} else {
		run()
	}

	ticker := time.NewTicker(*every)
	defer ticker.Stop()
	for {
		select {
		case <-interrupted:
			stop()
			return
		case <-ticker.C:
			next := snapshot(*root)
			if same(stamps, next) {
				continue
			}
			stamps = next
			fmt.Fprintln(os.Stderr, "devwatch: a .go file changed, rebuilding")
			if !build() {
				// Whatever is running is still the last thing that compiled,
				// and it stays running. A watcher that takes the console down
				// because somebody saved mid-edit is worse than no watcher.
				fmt.Fprintln(os.Stderr, "devwatch: build failed - the previous build is still running")
				continue
			}
			stop()
			run()
		}
	}
}

// snapshot is every .go file under root and when it was last written.
//
// Vendored trees, the front end and anything hidden are skipped: `web/` holds
// tens of thousands of files under `node_modules` and none of them is Go.
func snapshot(root string) map[string]time.Time {
	out := map[string]time.Time{}
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			name := entry.Name()
			if name == "node_modules" || name == "vendor" || (strings.HasPrefix(name, ".") && name != "." && path != root) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".go") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		out[path] = info.ModTime()
		return nil
	})
	return out
}

// same compares two snapshots. A file added, removed or written is a change;
// comparing only a count would miss a save that did not change the number of
// files, which is every save.
func same(before, after map[string]time.Time) bool {
	if len(before) != len(after) {
		return false
	}
	for path, at := range before {
		if next, ok := after[path]; !ok || !next.Equal(at) {
			return false
		}
	}
	return true
}

// runOnce runs the built binary to completion and returns its exit status. The
// deferred removal of the binary in main does not run past os.Exit, so it is
// removed here.
func runOnce(binary string, args, environ []string) int {
	defer os.RemoveAll(filepath.Dir(binary))
	child := exec.Command(binary, args...)
	child.Env = environ
	child.Stdout, child.Stderr, child.Stdin = os.Stdout, os.Stderr, os.Stdin
	if err := child.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "devwatch: %v\n", err)
		return 1
	}
	return 0
}

// buildPath is where the console is built to: inside a directory of its own,
// made fresh with a name nobody can predict and readable by this user only. A
// fixed name in the shared temporary directory could be taken first by another
// user of the machine, whose file this would then run.
func buildPath() (string, error) {
	dir, err := os.MkdirTemp("", "kubetower-devwatch-")
	if err != nil {
		return "", fmt.Errorf("no directory to build into: %w", err)
	}
	return filepath.Join(dir, "kubetower"+exeSuffix()), nil
}

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

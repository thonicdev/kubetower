// Package binpath appends the directories command-line tools are usually
// installed in to this process's own PATH.
//
// A kubeconfig context can authenticate through an exec credential plugin
// (`aws eks get-token`, `gke-gcloud-auth-plugin`, `kubelogin`). internal/ekstoken
// handles the EKS shape it recognizes in process; everything else still starts
// that binary.
//
// On macOS a process started from Finder, the Dock or an .app bundle gets
// launchd's PATH, roughly /usr/bin:/bin:/usr/sbin:/sbin, which has neither
// Homebrew's directories nor /usr/local/bin. The plugin is then not found, while
// a dock terminal on the same cluster works because a login shell rebuilds its
// own PATH.
//
// It must be the process's PATH, not an exec block's Env: client-go builds the
// plugin with exec.Command, which resolves a bare name through LookPath against
// the calling process's PATH; cmd.Env is only handed to the child once found.
// The same gap affects the terminal's read-only shims (LookPath("kubectl")), its
// version-skew check, and the PATH the dock's shell starts with. So this sets
// PATH once, before anything builds a client.
//
// Append, never prepend: the operator's own PATH wins, since a pinned `aws`
// changing silently would change what authenticates them to production.
//
// Nothing is added on Windows, where a GUI process inherits the full PATH from
// the registry.
//
// A missing plugin fails before any request is signed ("executable aws not
// found"), never as "unauthorized"; a 401 is a different fault.
package binpath

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Augment appends the directories on this platform's list that exist and are not
// already on PATH, and returns the ones it added.
//
// Nothing is changed on Windows, in the container, or wherever PATH already
// covers these places (the ordinary case in a terminal), so the caller logs
// only when this returns something.
//
// Call it once, early, before any goroutine exists: it writes a process-wide
// environment variable.
func Augment() []string {
	// An unreadable home only costs the home-relative candidate.
	home, _ := os.UserHomeDir()

	current := os.Getenv("PATH")
	add := toAdd(candidates(runtime.GOOS, home), current, isDir)
	if len(add) == 0 {
		return nil
	}

	if err := os.Setenv("PATH", appended(current, add)); err != nil {
		return nil
	}
	return add
}

// appended puts add after current.
//
// appended puts add after current. Its own function so it can be tested on a
// Windows host, where Augment returns before reaching it.
func appended(current string, add []string) string {
	sep := string(os.PathListSeparator)
	// current first: the operator's own PATH wins.
	next := strings.Join(add, sep)
	// An empty PATH element means the working directory on POSIX, so an empty
	// PATH is not joined onto.
	if current != "" {
		next = current + sep + next
	}
	return next
}

// candidates is the table, as a function of the platform rather than behind a
// build tag, so `go test` checks every platform's list everywhere.
//
// Order matters only when PATH names none of these, since Augment appends: most
// likely and most current first.
func candidates(goos, home string) []string {
	var dirs []string
	switch goos {
	case "darwin":
		dirs = []string{
			// Homebrew on Apple silicon.
			"/opt/homebrew/bin",
			// Homebrew on Intel, the AWS CLI installer's symlinks, and
			// kubectl's official instructions.
			"/usr/local/bin",
			// MacPorts.
			"/opt/local/bin",
		}
	case "linux":
		dirs = []string{
			// The AWS CLI v2 installer's symlinks, and kubectl's official
			// instructions.
			"/usr/local/bin",
			// Homebrew on Linux.
			"/home/linuxbrew/.linuxbrew/bin",
		}
		// Not /snap/bin: whether a confined `aws` can read ~/.aws is unknown,
		// and a fallback that changes what authenticates to production must be
		// understood first.
	default:
		// Windows and anything else: no known gap to close.
		return nil
	}

	if home != "" {
		// pipx, `pip install --user` and the XDG user bin. Last on purpose:
		// what most likely sits here is an end-of-life awscli v1, which ahead
		// of /usr/local/bin would silently downgrade the operator's v2.
		dirs = append(dirs, filepath.Join(home, ".local", "bin"))
	}
	return dirs
}

// toAdd keeps the candidates that exist and are not already on PATH.
//
// Both filters keep the caller's log line true: "appended /opt/homebrew/bin" on
// an Intel Mac would be false, and a duplicate is noise.
func toAdd(candidates []string, current string, exists func(string) bool) []string {
	have := make(map[string]bool, 8)
	for _, dir := range filepath.SplitList(current) {
		if dir != "" {
			have[filepath.Clean(dir)] = true
		}
	}

	var add []string
	for _, dir := range candidates {
		clean := filepath.Clean(dir)
		if have[clean] || !exists(clean) {
			continue
		}
		// Seen, so a list naming one place twice cannot add it twice.
		have[clean] = true
		add = append(add, clean)
	}
	return add
}

// isDir is the real existence check, separate so toAdd can be tested without
// those directories.
func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

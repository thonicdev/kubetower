// Package shellenv gives a console opened from the Finder or the Dock the
// environment the operator's terminal has.
//
// launchd starts an application with almost nothing: no AWS_PROFILE, no
// AWS_CONFIG_FILE, no KUBECONFIG, a four-directory PATH. A kubeconfig that
// names no AWS profile is then signed by internal/ekstoken with the default
// resolution, not with the profile the terminal uses, so the cluster reads as
// an expired SSO session while `kubectl` in a terminal works, and
// `aws sso login --profile p` renews a session this process never reads. Exec
// plugins and the dock's shells inherit the same thin environment.
//
// So, once at start-up and before any kubeconfig is read, the login shell is
// asked for its environment and this process takes what it lacks. A variable
// this process already has keeps its value: whoever started it meant it.
// PATH is the exception: the shell's order comes first, as in a terminal, and
// launchd's directories follow.
package shellenv

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// timeout bounds the shell. An rc file that waits for input, or execs tmux,
// would otherwise hold the console's start-up forever.
const timeout = 5 * time.Second

// waitDelay is how long to wait for the pipes once the shell is gone: a
// background job it started can hold them open.
const waitDelay = time.Second

// maxOutput bounds what is read from the shell.
const maxOutput = 1 << 20

// Result is what Import did, for one log line: names only, never values.
type Result struct {
	// Shell is the shell that was asked, or empty when none was.
	Shell string
	// Imported is the variables that were set, sorted.
	Imported []string
	// Path is whether PATH was rebuilt from the shell's.
	Path bool
	// Err is why nothing was imported, or nil.
	Err error
}

// Import reads the login shell's environment and sets in this process what it
// lacks. It does nothing outside macOS, or when this process was started from
// a terminal, which already handed it the shell's environment.
//
// Call it once, early, before any goroutine reads the environment.
func Import() Result {
	if runtime.GOOS != "darwin" || os.Getenv("TERM") != "" {
		return Result{}
	}
	shell := loginShell(os.Getenv("SHELL"))
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	env, err := read(ctx, shell)
	if err != nil {
		return Result{Shell: shell, Err: err}
	}
	imported, path := apply(env, os.LookupEnv, os.Setenv)
	return Result{Shell: shell, Imported: imported, Path: path}
}

// loginShell is $SHELL when it names an absolute path, else zsh, macOS's
// default since 10.15.
func loginShell(shell string) string {
	if filepath.IsAbs(shell) {
		return shell
	}
	return "/bin/zsh"
}

// read runs the shell as a terminal would start it, login and interactive so
// that ~/.zprofile and ~/.zshrc are both read, and returns its environment.
//
// The environment is printed NUL-separated between two random markers: an rc
// file that prints a banner, or a value with a newline in it, cannot be
// mistaken for a variable.
func read(ctx context.Context, shell string) (map[string]string, error) {
	marker, err := newMarker()
	if err != nil {
		return nil, err
	}
	// The same words in sh, bash, zsh and fish.
	script := fmt.Sprintf("printf '%%s' '%s'; /usr/bin/env -0; printf '%%s' '%s'", marker, marker)

	cmd := exec.CommandContext(ctx, shell, "-l", "-i", "-c", script)
	cmd.Stdin = nil
	var out bytes.Buffer
	cmd.Stdout = &capped{buf: &out}
	cmd.Stderr = nil
	cmd.WaitDelay = waitDelay
	ownGroup(cmd)
	runErr := cmd.Run()
	killGroup(cmd)

	if ctx.Err() != nil {
		return nil, fmt.Errorf("%s did not answer in time", shell)
	}
	env, ok := parse(out.Bytes(), marker)
	if !ok {
		if runErr != nil {
			return nil, fmt.Errorf("%s: %w", shell, runErr)
		}
		return nil, fmt.Errorf("%s printed no environment", shell)
	}
	// A non-zero exit after the environment was printed is an rc file's last
	// command failing: the environment is still the one a terminal would have.
	return env, nil
}

func newMarker() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "__KUBETOWER_ENV_" + hex.EncodeToString(b) + "__", nil
}

// parse reads `KEY=VALUE\x00…` between the first two markers.
func parse(out []byte, marker string) (map[string]string, bool) {
	m := []byte(marker)
	start := bytes.Index(out, m)
	if start < 0 {
		return nil, false
	}
	rest := out[start+len(m):]
	end := bytes.Index(rest, m)
	if end < 0 {
		return nil, false
	}
	body := rest[:end]
	if len(body) > 0 && !bytes.Contains(body, []byte{0}) {
		// An env without -0 printed lines: not something to guess from.
		return nil, false
	}
	env := map[string]string{}
	for _, entry := range bytes.Split(body, []byte{0}) {
		name, value, ok := strings.Cut(string(entry), "=")
		if !ok || name == "" {
			continue
		}
		env[name] = value
	}
	return env, len(env) > 0
}

// apply sets what this process lacks and rebuilds PATH. It returns the names
// it set, sorted, and whether PATH changed.
func apply(env map[string]string, lookup func(string) (string, bool), set func(string, string) error) ([]string, bool) {
	var imported []string
	for name, value := range env {
		if name == "PATH" || skipped(name) {
			continue
		}
		if _, ok := lookup(name); ok {
			continue
		}
		if set(name, value) == nil {
			imported = append(imported, name)
		}
	}
	sort.Strings(imported)

	current, _ := lookup("PATH")
	path := mergePath(env["PATH"], current)
	changed := false
	if path != current && set("PATH", path) == nil {
		changed = true
	}
	return imported, changed
}

// skipped is a variable that describes the shell session or the terminal, not
// the operator's setup, or one this console reads for its own configuration:
// KT_* was read before this runs, so importing it would apply half of it.
func skipped(name string) bool {
	if strings.HasPrefix(name, "_") || strings.HasPrefix(name, "KT_") {
		return true
	}
	switch name {
	case "PWD", "OLDPWD", "SHLVL", "TERM", "TERM_PROGRAM", "TERM_PROGRAM_VERSION",
		"TERM_SESSION_ID", "COLORTERM", "LINES", "COLUMNS", "PS1", "PS2", "PS3", "PS4",
		"TMUX", "TMUX_PANE", "STY", "SSH_TTY", "ITERM_SESSION_ID", "ITERM_PROFILE",
		"LC_TERMINAL", "LC_TERMINAL_VERSION", "SHELL_SESSION_ID":
		return true
	}
	return false
}

// mergePath is the shell's PATH, then whatever of current it does not name.
func mergePath(shell, current string) string {
	if shell == "" {
		return current
	}
	sep := string(os.PathListSeparator)
	seen := map[string]bool{}
	var dirs []string
	for _, list := range []string{shell, current} {
		for _, dir := range strings.Split(list, sep) {
			if dir == "" || seen[filepath.Clean(dir)] {
				continue
			}
			seen[filepath.Clean(dir)] = true
			dirs = append(dirs, dir)
		}
	}
	return strings.Join(dirs, sep)
}

// capped keeps the first maxOutput bytes.
type capped struct{ buf *bytes.Buffer }

func (c *capped) Write(p []byte) (int, error) {
	if room := maxOutput - c.buf.Len(); room > 0 {
		c.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

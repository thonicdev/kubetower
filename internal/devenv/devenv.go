// Package devenv is what the desktop development loop may take from `.env`, and
// nothing else.
//
// `.env` has two readers. Docker Compose reads all of it for `task container`,
// and `.env.example` sets KT_FORWARD_PORTS for it. The desktop process must
// never see that value: with a range set, internal/forward binds every
// port-forward on 0.0.0.0, putting a password-less tunnel into the cluster on
// the laptop's network. Task's dotenv loads every line, and a deny-list would
// miss the next container-only variable, so the desktop loop takes exactly the
// variables `task dev:password` writes.
//
// An allow-list fails closed for exposure and open for restriction: a
// KT_READONLY_CLUSTERS written into .env does not reach the loop either. So
// File.Skipped names every other non-empty KT_* key (names, never values) and
// devwatch prints them.
//
// Values are literal: no `${VAR}`, no `~`, no inline ` # comment` stripped. For
// KT_STATE_DIR that would turn `${USERPROFILE}/.kubetower-dev` into a relative
// directory inside the clone holding credentials.json, one `git add -A` from a
// commit, so StateDirProblem refuses that shape.
//
// A variable already in the environment, even empty, is not replaced, as with
// Task's own dotenv: `KT_STATE_DIR=... task dev` moves the state for one run.
package devenv

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Keys is everything the desktop loop takes from `.env`: what `task
// dev:password` writes, and nothing else.
var Keys = []string{"KT_PASSWORD_HASH", "KT_SESSION_SECRET", "KT_STATE_DIR"}

// stateDirKey is the one of Keys whose value is a path, and so the one checked.
const stateDirKey = "KT_STATE_DIR"

// ContainerOnly names the variables `.env.example` sets for `task container`
// that change what a desktop process does if they reach it. `task dev` never
// takes them (see Keys); `task dev:password` names any it finds, so a developer
// who also sources `.env` into a shell is told what is in it.
var ContainerOnly = []string{"KT_FORWARD_PORTS"}

// File is what the desktop loop reads out of one `.env`.
type File struct {
	// Path is the file asked for.
	Path string
	// Missing says the file is not there, which is a different answer from a
	// file that holds none of Keys: a typo in a path is not an empty file.
	Missing bool
	// Values is Keys with a non-empty value, and nothing else.
	Values map[string]string
	// Skipped is every other non-empty `KT_*` key the file sets, sorted. Names
	// only: the values include secrets, and these go to a terminal.
	Skipped []string
}

// Lines splits a .env into lines, CRLF or LF, without a leading byte-order mark
// (which would otherwise hide the first assignment).
func Lines(content []byte) []string {
	text := strings.TrimPrefix(string(content), "\ufeff")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

// Assignment parses `KEY=value` and `export KEY=value`, and refuses a comment or
// a blank line. A value wrapped in one pair of single or double quotes is
// unwrapped and otherwise taken literally: nothing is interpolated, so a bcrypt
// hash written single-quoted arrives whole.
func Assignment(line string) (key, value string, ok bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", "", false
	}
	if rest, found := strings.CutPrefix(trimmed, "export"); found && rest != "" &&
		(rest[0] == ' ' || rest[0] == '\t') {
		trimmed = strings.TrimSpace(rest)
	}
	key, value, found := strings.Cut(trimmed, "=")
	if !found {
		return "", "", false
	}
	return strings.TrimSpace(key), unquote(strings.TrimSpace(value)), true
}

func unquote(value string) string {
	if len(value) >= 2 {
		first, last := value[0], value[len(value)-1]
		if (first == '\'' || first == '"') && first == last {
			return value[1 : len(value)-1]
		}
	}
	return value
}

// Effective is the value the last assignment of key gives it, which is how both
// Task's loader and Compose resolve a duplicate, and whether there was one.
func Effective(lines []string, key string) (string, bool) {
	value, found := "", false
	for _, line := range lines {
		if k, v, ok := Assignment(line); ok && k == key {
			value, found = v, true
		}
	}
	return strings.TrimSpace(value), found
}

// Load reads the .env at path. A missing file is an answer, not an error — a
// clone with no `.env` runs on a drawn password, as a downloaded binary does —
// and File.Missing says which answer it was.
func Load(path string) (File, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return File{Path: path, Missing: true, Values: map[string]string{}}, nil
	}
	if err != nil {
		return File{}, fmt.Errorf("reading %s: %w", path, err)
	}
	f := Parse(raw)
	f.Path = path
	return f, nil
}

// Parse is Load without the file.
func Parse(content []byte) File {
	lines := Lines(content)
	f := File{Values: Allowed(content)}
	seen := map[string]bool{}
	for _, line := range lines {
		key, _, ok := Assignment(line)
		if !ok || !strings.HasPrefix(key, "KT_") || seen[key] || isKey(key) {
			continue
		}
		if value, _ := Effective(lines, key); value != "" {
			seen[key] = true
			f.Skipped = append(f.Skipped, key)
		}
	}
	sort.Strings(f.Skipped)
	return f
}

func isKey(name string) bool {
	for _, key := range Keys {
		if key == name {
			return true
		}
	}
	return false
}

// Allowed is the part of a .env the desktop loop takes: Keys, with a non-empty
// value, and nothing else whatever the file holds.
func Allowed(content []byte) map[string]string {
	lines := Lines(content)
	out := map[string]string{}
	for _, key := range Keys {
		if value, found := Effective(lines, key); found && value != "" {
			out[key] = value
		}
	}
	return out
}

// StateDirProblem says why a KT_STATE_DIR read literally from `.env` cannot be
// used, or "" when it can. See the package comment: the value is not expanded,
// so anything that only makes sense expanded is refused rather than created as a
// directory relative to the clone.
func StateDirProblem(value string) string {
	switch {
	case strings.Contains(value, "$"):
		return "it contains `$`, and .env is read literally here: no variable is expanded"
	case strings.Contains(value, "~"):
		return "it contains `~`, and .env is read literally here: no home directory is expanded"
	case strings.Contains(value, " #"):
		return "it contains ` #`, and .env is read literally here: an inline comment is part of the value"
	case !filepath.IsAbs(value):
		return "it is not an absolute path, so the state directory would be created relative to " +
			"wherever the process starts - inside the clone, under `task dev`"
	}
	return ""
}

// Apply adds what the file allows to an environment, leaving every variable the
// environment already has as it is. It reports the names it added and the names
// the environment already set (which win), and refuses a KT_STATE_DIR it would
// add that StateDirProblem rejects.
func Apply(environ []string, f File) (out, added, shadowed []string, err error) {
	present := map[string]bool{}
	for _, entry := range environ {
		name, _, _ := strings.Cut(entry, "=")
		present[envName(name)] = true
	}
	out = append(out, environ...)
	for _, key := range Keys {
		value, ok := f.Values[key]
		if !ok {
			continue
		}
		if present[envName(key)] {
			shadowed = append(shadowed, key)
			continue
		}
		if key == stateDirKey {
			if problem := StateDirProblem(value); problem != "" {
				where := f.Path
				if where == "" {
					where = ".env"
				}
				return nil, nil, nil, fmt.Errorf("%s in %s is %q, and %s. Write an absolute path, "+
					"as `task dev:password` does, or set %s in the shell instead", key, where, value,
					problem, key)
			}
		}
		out = append(out, key+"="+value)
		added = append(added, key)
	}
	return out, added, shadowed, nil
}

// Present reports which of ContainerOnly a .env sets to a non-empty value.
func Present(content []byte) []string {
	lines := Lines(content)
	var found []string
	for _, key := range ContainerOnly {
		if value, ok := Effective(lines, key); ok && value != "" {
			found = append(found, key)
		}
	}
	return found
}

// envName is how the platform compares variable names: case-insensitively on
// Windows, where `Kt_State_Dir` and `KT_STATE_DIR` are one variable.
func envName(name string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(name)
	}
	return name
}

// Report is what devwatch says about one Apply, a line each, names only. A
// missing file, a file with none of Keys, and keys the shell overrides each get
// their own line, so a typo in a path does not read like an empty file.
func (f File) Report(added, shadowed []string) []string {
	var out []string
	switch {
	case f.Missing:
		out = append(out, fmt.Sprintf("no %s, so nothing taken from it: the console falls back "+
			"to credentials.json in its state directory", f.Path))
	case len(f.Values) == 0:
		out = append(out, fmt.Sprintf("%s sets none of %s, so nothing taken from it",
			f.Path, strings.Join(Keys, ", ")))
	case len(added) > 0:
		out = append(out, fmt.Sprintf("from %s: %s (nothing else in that file reaches the process)",
			f.Path, strings.Join(added, ", ")))
	}
	if len(shadowed) > 0 {
		out = append(out, fmt.Sprintf("the shell already sets %s, which wins over %s",
			strings.Join(shadowed, ", "), f.Path))
	}
	if len(f.Skipped) > 0 {
		out = append(out, fmt.Sprintf("%s also sets %s - not taken: `task dev` takes only %s. "+
			"Set any you need in the shell",
			f.Path, strings.Join(f.Skipped, ", "), strings.Join(Keys, ", ")))
	}
	return out
}

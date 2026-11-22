// Package statefile is the plumbing under the small JSON files KubeTower keeps
// about the operator's own choices: the hotbars, the column layouts, the
// nameplates and the node-shell image.
//
// Only the plumbing: each store keeps one file, its own package and its own
// validation, since a bad value differs per domain. Here are the four things
// every store needs, done once: where the state directory is, how a file is
// replaced atomically and durably, the modes it is created with, and how a
// standing failure is reported once rather than once per click. A shared write
// keeps the fsync that copies would drift away from. internal/archive keeps its
// own copy, entangled with its locking.
//
// Every store built on this changes memory first, then writes. Callers depend
// on it: a failed write leaves the change in effect (ErrNotSaved), and
// internal/web may discard the layout returned beside an error and redraw from
// the store's Arrangement, which is then the truth. Write first and commit on
// success instead, and that redraw would silently undo the operator's drag; any
// write-then-commit path needs every such caller found first.
package statefile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EnvDir is the environment variable naming the state directory. The container
// sets it to /var/lib/kubetower and mounts a volume there; without the volume
// the state goes with the container, which reads as "my hotbars reset".
const EnvDir = "KT_STATE_DIR"

// The modes the state directory and its files are created with, explicit so
// that ~/.kubetower is not world-readable depending on which preference was
// changed first: it names the operator's production clusters.
//
// MkdirAll does not change an existing directory's mode, and that is kept:
// silently tightening a directory someone arranged access to is a surprise.
const (
	DirPerm  os.FileMode = 0o700
	FilePerm os.FileMode = 0o600
)

// ErrNotSaved is what every failed write wraps.
//
// The wording is the contract: the change has been applied in memory and will
// be gone at the next start. That is not a refusal, where nothing happened.
// errors.Is(err, ErrNotSaved) is how a handler tells the operator their
// arrangement will be lost without saying their drag failed.
var ErrNotSaved = errors.New("in effect now, but it will not survive a restart")

// Dir is where KubeTower keeps its state: KT_STATE_DIR, or a per-user directory
// when it is not set.
//
// The default is ~/.kubetower. With no home directory it is a relative
// `kubetower-state`, so a preference is still remembered somewhere; every store
// logs its Path() at startup, so such a file is easy to find.
func Dir() string {
	if dir := strings.TrimSpace(os.Getenv(EnvDir)); dir != "" {
		return dir
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".kubetower")
	}
	return "kubetower-state"
}

// Write replaces path with data, atomically and durably.
//
// Atomic: the bytes go to a temporary file beside the target, renamed over it,
// so the next start sees the whole old file or the whole new one.
//
// Durable: the temporary file is fsynced before the rename. Without it the
// rename can reach the disk before the bytes, and a crash leaves a zero-length
// file that every loader replaces with the defaults.
//
// The parent directory is not fsynced: that is not portable (Windows cannot
// open a directory for Sync), and losing the rename only leaves the previous
// arrangement.
//
// The error wraps ErrNotSaved and names the path and the cause; every caller
// has already applied the change in memory.
func Write(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, DirPerm); err != nil {
		return fmt.Errorf("%w: %s cannot be created (%v)", ErrNotSaved, dir, err)
	}
	base := filepath.Base(path)
	tmp, err := os.CreateTemp(dir, base+".*")
	if err != nil {
		return fmt.Errorf("%w: nothing can be written in %s (%v)", ErrNotSaved, dir, err)
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return fmt.Errorf("%w: writing %s (%v)", ErrNotSaved, name, err)
	}
	// Rename-atomic is not durable without this.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)
		return fmt.Errorf("%w: writing %s (%v)", ErrNotSaved, name, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return fmt.Errorf("%w: writing %s (%v)", ErrNotSaved, name, err)
	}
	// CreateTemp already makes the file 0600; stating the mode keeps it from
	// drifting. Not fatal: some filesystems a state volume may sit on cannot
	// chmod, and every save would fail over a mode already correct.
	_ = os.Chmod(name, FilePerm)
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return fmt.Errorf("%w: %s could not be replaced (%v)", ErrNotSaved, path, err)
	}
	sweep(dir, base)
	return nil
}

// sweep removes the temporary files a crash between CreateTemp and Rename
// leaves behind (`hotbars.json.1234567`); nothing else would, and they would
// pile up for the life of the installation.
//
// A failure is silent: it is housekeeping after a write that succeeded.
func sweep(dir, base string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	// The prefix includes the dot, so the file just renamed into place cannot
	// match.
	prefix := base + "."
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, entry.Name()))
	}
}

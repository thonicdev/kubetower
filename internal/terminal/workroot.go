package terminal

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// The terminal's work root holds the read-only overrides (the `kubectl` and
// `helm` shims a read-only session's PATH points at, which the fence probe runs
// at start-up), so whoever can write into it chooses what a read-only session
// runs. It also holds every live session's directory, which sweepStale deletes
// from.
//
// `os.TempDir()/kubetower` made with `os.MkdirAll(…, 0o700)` would be wrong
// twice on a shared Unix host: /tmp is writable by every account, and MkdirAll
// does not change the mode of an existing directory, so another user could
// create it first and plant `ro-bin/kubectl` or have the sweep delete theirs.
// So the root goes under $XDG_RUNTIME_DIR where there is a usable one (per
// user, 0700, made by the login manager), the /tmp default carries the user id
// (workRoot), and whatever the path, its owner, mode and kind are read back
// (secureWorkRoot).

// workRoot is the default directory. On Unix it is $XDG_RUNTIME_DIR/kubetower
// when that variable names an existing directory, not a symbolic link, owned by
// this account and with no group or other permission bits; otherwise it is in os.TempDir and carries the
// user id. The user id is not a security control by itself (a uid is public);
// it keeps two legitimate accounts on one host from contending for one path, so
// the strict check cannot turn into one user denying the other a start-up.
//
// Windows keeps the bare name in its temp directory: that is already
// per-account, and os.Getuid returns -1 there.
func workRoot() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(os.TempDir(), "kubetower")
	}
	uid := os.Getuid()
	if dir := runtimeDir(uid); dir != "" {
		return filepath.Join(dir, "kubetower")
	}
	name := "kubetower"
	if uid >= 0 {
		name = fmt.Sprintf("kubetower-%d", uid)
	}
	return filepath.Join(os.TempDir(), name)
}

// runtimeDir returns $XDG_RUNTIME_DIR when it can hold the work root: an
// absolute path to a directory (not a symbolic link) owned by uid, with no group
// or other permission bits. Anything else is "" and the work root falls back to os.TempDir.
func runtimeDir(uid int) string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" || !filepath.IsAbs(dir) {
		return ""
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() {
		return ""
	}
	if owner, ok := fileOwner(info); !ok || owner != uid {
		return ""
	}
	if info.Mode().Perm()&0o077 != 0 {
		return ""
	}
	return dir
}

// secureWorkRoot creates the work root and then reads it back rather than
// trusting the creation, checking, in order:
//
//   - that it is not a symbolic link (os.Lstat, so one planted in its place is
//     seen rather than followed);
//   - that it is a directory;
//   - on Unix, that it is owned by this account (os.Getuid). The owner of a
//     directory can change its mode, or rename what is in it, at any time after
//     the mode was read, so a directory another account made is refused however
//     its mode reads now;
//   - on Unix, that no other account may write to it (no group or other bits).
//
// The owner and mode checks are Unix-only: Go on Windows reports a writable
// directory as 0777 whatever its ACL, and its stat carries no user id, so the
// checks would refuse every start-up or prove nothing; the shared temp
// directory is a Unix exposure.
func secureWorkRoot(path string) error {
	return secureWorkRootAs(path, os.Getuid())
}

// secureWorkRootAs is secureWorkRoot for the account uid, so a test can ask
// about an owner it cannot create.
func secureWorkRootAs(path string, uid int) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("terminal work directory: %w", err)
	}

	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("terminal work directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("terminal work directory %s is a symbolic link, so whatever it points at would decide what a read-only session runs. Remove it", path)
	}
	if !info.IsDir() {
		return fmt.Errorf("terminal work directory %s exists and is not a directory", path)
	}
	if runtime.GOOS != "windows" {
		owner, ok := fileOwner(info)
		if !ok {
			return fmt.Errorf("terminal work directory %s: its owner could not be read, so it may belong to another account — and it holds the kubectl and helm overrides a read-only session executes", path)
		}
		if owner != uid {
			return fmt.Errorf("terminal work directory %s is owned by user id %d, not by this account (%d), so its owner can change its mode or replace what is in it at any time — and it holds the kubectl and helm overrides a read-only session executes. It was not created by this process. Remove it and start again", path, owner, uid)
		}
		if perm := info.Mode().Perm(); perm&0o077 != 0 {
			return fmt.Errorf("terminal work directory %s is mode %#o, so another account on this host can write to it — and it holds the kubectl and helm overrides a read-only session executes. It was not created by this process. Remove it and start again", path, perm)
		}
	}
	return nil
}

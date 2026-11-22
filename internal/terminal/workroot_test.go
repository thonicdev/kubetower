package terminal

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The work root on a shared host, in three parts.
//
// The work root holds the `kubectl` and `helm` overrides a read-only session's
// PATH resolves to, and that the fence probe executes at start-up.
// `os.MkdirAll` returns nil for a directory that already exists **without
// touching its mode**, so a directory another local account had created 0777
// would be accepted silently and everything below it would be theirs to write.

func TestSecureWorkRootRefusesADirectoryOtherAccountsCanWrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Go reports every writable directory as 0777 on Windows whatever its ACL says, so the mode check does not run there — see secureWorkRoot")
	}

	path := filepath.Join(t.TempDir(), "planted")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("setting up: %v", err)
	}
	// Chmod rather than a mode on Mkdir: the umask would have cleared the bits
	// the attack depends on, and the test would have proved nothing.
	if err := os.Chmod(path, 0o777); err != nil {
		t.Fatalf("setting up: %v", err)
	}

	err := secureWorkRoot(path)
	if err == nil {
		t.Fatalf("secureWorkRoot accepted a 0777 work root; anyone on this host could replace the kubectl a read-only session runs")
	}
	// The message has to name what is wrong and what to do, or the operator gets
	// a refusal to start with nothing to act on.
	for _, want := range []string{path, "read-only session", "Remove it"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal does not mention %q: %v", want, err)
		}
	}
}

func TestSecureWorkRootRefusesASymlinkInItsPlace(t *testing.T) {
	elsewhere := t.TempDir()
	path := filepath.Join(t.TempDir(), "planted")
	if err := os.Symlink(elsewhere, path); err != nil {
		t.Skipf("this host cannot create a symbolic link (%v) — on Windows that is a privilege Developer Mode grants", err)
	}

	// os.Stat would have followed it and reported a perfectly good directory.
	// The whole point of Lstat here is that it does not.
	if err := secureWorkRoot(path); err == nil {
		t.Fatalf("secureWorkRoot followed a symbolic link to %s; what it points at would decide what a read-only session runs", elsewhere)
	}
}

func TestSecureWorkRootAcceptsOneItMadeItself(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "work")
	if err := secureWorkRoot(path); err != nil {
		t.Fatalf("secureWorkRoot refused a directory it created: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("it reported success and made nothing: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("%s is not a directory", path)
	}
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Fatalf("the work root is mode %#o, want 0700", perm)
		}
	}

	// Idempotent: New() runs this on every start-up, against the directory the
	// previous run left behind.
	if err := secureWorkRoot(path); err != nil {
		t.Fatalf("a second call refused the directory the first one made: %v", err)
	}
}

// The path itself is not a security control — a uid is public. It is what stops
// two legitimate accounts on one host contending for a single directory, which
// under the check above would be one of them unable to start at all.
func TestWorkRootIsPerAccountWhereTheTempDirectoryIsShared(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")
	root := workRoot()

	if runtime.GOOS == "windows" {
		if filepath.Base(root) != "kubetower" {
			t.Fatalf("the Windows work root is %q; the per-account temp directory is already per-account, so the bare name is deliberate", root)
		}
		return
	}

	want := fmt.Sprintf("kubetower-%d", os.Getuid())
	if got := filepath.Base(root); got != want {
		t.Fatalf("the work root is %q, want %q: /tmp is shared by every account on the host", got, want)
	}
}

// A directory another account made is refused whatever its mode reads: its
// owner can flip the mode, or rename ro-bin away, after the check. A test cannot
// chown without privilege, so it asks as an account that does not own the
// directory it made.
func TestSecureWorkRootRefusesADirectoryAnotherAccountOwns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the owner check is Unix-only — see secureWorkRoot")
	}
	path := filepath.Join(t.TempDir(), "planted")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("setting up: %v", err)
	}
	other := os.Getuid() + 1

	err := secureWorkRootAs(path, other)
	if err == nil {
		t.Fatalf("secureWorkRoot accepted a 0700 work root owned by user id %d while running as %d; its owner could make it writable after the check", os.Getuid(), other)
	}
	for _, want := range []string{path, fmt.Sprintf("owned by user id %d", os.Getuid()), "Remove it"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal does not mention %q: %v", want, err)
		}
	}

	if err := secureWorkRootAs(path, os.Getuid()); err != nil {
		t.Fatalf("the owner's own directory was refused: %v", err)
	}
}

// $XDG_RUNTIME_DIR is per user, 0700 and made by the login manager, so the work
// root goes there when it is usable, and nowhere near /tmp.
func TestWorkRootPrefersAUsableRuntimeDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows keeps its per-account temp directory — see TestWorkRootIgnoresTheRuntimeDirectoryOnWindows")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("setting up: %v", err)
	}
	t.Setenv("XDG_RUNTIME_DIR", dir)

	if got, want := workRoot(), filepath.Join(dir, "kubetower"); got != want {
		t.Fatalf("the work root is %q, want %q under $XDG_RUNTIME_DIR", got, want)
	}
}

func TestWorkRootFallsBackFromAnUnusableRuntimeDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows keeps its per-account temp directory")
	}
	fallback := filepath.Join(os.TempDir(), fmt.Sprintf("kubetower-%d", os.Getuid()))

	open := t.TempDir()
	if err := os.Chmod(open, 0o755); err != nil {
		t.Fatalf("setting up: %v", err)
	}
	link := filepath.Join(t.TempDir(), "link")
	private := t.TempDir()
	if err := os.Chmod(private, 0o700); err != nil {
		t.Fatalf("setting up: %v", err)
	}
	if err := os.Symlink(private, link); err != nil {
		t.Fatalf("setting up: %v", err)
	}

	for name, value := range map[string]string{
		"group or other bits": open,
		"a symbolic link":     link,
		"a missing directory": filepath.Join(t.TempDir(), "gone"),
		"a relative path":     "run/user",
	} {
		t.Setenv("XDG_RUNTIME_DIR", value)
		if got := workRoot(); got != fallback {
			t.Errorf("with %s in $XDG_RUNTIME_DIR the work root is %q, want the fallback %q", name, got, fallback)
		}
	}
}

func TestWorkRootIgnoresTheRuntimeDirectoryOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows only")
	}
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	if got, want := workRoot(), filepath.Join(os.TempDir(), "kubetower"); got != want {
		t.Fatalf("the Windows work root is %q, want %q", got, want)
	}
}

package updater

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"
)

// The start-up sweep removes what an install cut short left, and never a copy
// young enough to be another instance's install in progress (#154).
func TestTheSweepLeavesAYoungCopyAlone(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "kubetower")
	old := filepath.Join(dir, ".kubetower-update-111")
	young := filepath.Join(dir, ".kubetower-update-222")
	other := filepath.Join(dir, "notes.txt")
	for _, p := range []string{exe, old, young, other} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-2 * sweepAge)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	sweepBeside(exe, ".kubetower-update-*")
	for path, want := range map[string]bool{exe: true, old: false, young: true, other: true} {
		if _, err := os.Stat(path); (err == nil) != want {
			t.Errorf("%s: present %v, want %v", filepath.Base(path), err == nil, want)
		}
	}
}

// Linux AppImage and tar installs sweep what a replace left, as macOS and the
// Windows zip do.
func TestEveryReplacingInstallHasASweeper(t *testing.T) {
	for _, kind := range sweptKinds() {
		if sweepers[kind] == nil {
			t.Errorf("%s has no sweeper", kind)
		}
	}
}

// The package database is asked with `--` before the path, so a path starting
// with a dash is never an option.
func TestThePackageQuestionEndsItsOptions(t *testing.T) {
	got := ownerArgs("-S", "-rf")
	if !slices.Equal(got, []string{"-S", "--", "-rf"}) {
		t.Fatalf("%q", got)
	}
}

// A helper that could not be stopped keeps its files: no download removes
// them from under it, by a person's Retry as by the automatic update.
func TestNoDownloadBesideAHelperStillAlive(t *testing.T) {
	f := newFake(t)
	u := f.updater(t.TempDir(), &recorder{}, nil)
	u.mu.Lock()
	u.status.Phase, u.status.Reason = Failed, errHelperAlive.Error()
	u.mu.Unlock()
	keep := filepath.Join(u.dir(), workDir, "install-1")
	if err := os.MkdirAll(keep, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Download(); !errors.Is(err, ErrHelperAlive) {
		t.Fatalf("Download beside a live helper: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("the helper's folder was removed: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.hits) != 0 {
		t.Errorf("asked GitHub: %v", f.hits)
	}
}

// A read-only filesystem is a folder that cannot be written: the manual
// phase, not a failure.
func TestAReadOnlyFilesystemIsNotWritable(t *testing.T) {
	for name, err := range map[string]error{
		"permission": &fs.PathError{Op: "open", Path: "/opt/kt", Err: fs.ErrPermission},
		"read-only":  &fs.PathError{Op: "open", Path: "/opt/kt", Err: syscall.EROFS},
	} {
		if !notWritable(err) {
			t.Errorf("%s: not read as not writable", name)
		}
	}
	if notWritable(errors.New("disk full")) {
		t.Error("any error read as not writable")
	}
}

// The way to stop is read atomically, and nil is no way to stop.
func TestSetStopIsReadAtomically(t *testing.T) {
	was := stopper.Load()
	t.Cleanup(func() { stopper.Store(was) })
	SetStop(nil)
	if stopFunc() != nil {
		t.Fatal("a nil stop reads as one")
	}
	called := 0
	SetStop(func() { called++ })
	stopFunc()()
	if called != 1 {
		t.Fatalf("called %d", called)
	}
}

// sweptKinds is every installation whose install writes beside it.
func sweptKinds() []Kind {
	return []Kind{LinuxAppImage, LinuxTar}
}

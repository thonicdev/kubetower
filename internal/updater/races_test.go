package updater

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
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

// Every install that writes beside the installation sweeps what a replace
// left, on the platform that registers it: the test runs on each.
func TestEveryReplacingInstallHasASweeper(t *testing.T) {
	kinds, ok := sweptKinds[runtime.GOOS]
	if !ok {
		t.Skipf("no replacing install on %s", runtime.GOOS)
	}
	for _, kind := range kinds {
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

// sweptKinds is, per platform, every installation whose install writes
// beside it.
var sweptKinds = map[string][]Kind{
	"linux":   {LinuxAppImage, LinuxTar},
	"darwin":  {MacApp, MacTar},
	"windows": {WindowsZip},
}

// A package database that does not answer within ownerWait (a locked rpm
// database, a cold HDD) leaves the kind Unknown, which the console never
// updates: never LinuxTar, which would replace a package's file as a
// tarball's (#154).
func TestAnUnansweredPackageQuestionIsNeverATarball(t *testing.T) {
	got := Detect("linux", "/usr/bin/kubetower", Probe{Owner: func(string) string { return ownerUnanswered }})
	if got.Kind != Unknown {
		t.Fatalf("kind %q, want Unknown", got.Kind)
	}
	if _, ok := AssetName("0.3.0", "linux", "amd64", got.Kind); ok {
		t.Error("an Unknown installation has an asset to install")
	}

	saved := ownedBy
	defer func() { ownedBy = saved }()
	for _, c := range []struct {
		name     string
		deb, rpm [2]bool // owned, answered
		want     string
	}{
		{"dpkg owns it", [2]bool{true, true}, [2]bool{false, false}, "deb"},
		{"rpm owns it, dpkg timed out", [2]bool{false, false}, [2]bool{true, true}, "rpm"},
		{"dpkg timed out", [2]bool{false, false}, [2]bool{false, true}, ownerUnanswered},
		{"rpm timed out", [2]bool{false, true}, [2]bool{false, false}, ownerUnanswered},
		{"both said no", [2]bool{false, true}, [2]bool{false, true}, ""},
	} {
		ownedBy = func(tool, _, _ string) (bool, bool) {
			if tool == "dpkg-query" {
				return c.deb[0], c.deb[1]
			}
			return c.rpm[0], c.rpm[1]
		}
		if got := owner("/usr/bin/kubetower"); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// The wait itself: a question that runs past it is unanswered, one that
// fails in time is a no.
func TestAPackageQuestionPastItsWaitIsUnanswered(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep here")
	}
	if owned, answered := runOwner(sleep, []string{"5"}, 50*time.Millisecond); owned || answered {
		t.Errorf("past the wait: owned %v answered %v", owned, answered)
	}
	falseTool, err := exec.LookPath("false")
	if err != nil {
		t.Skip("no false here")
	}
	if owned, answered := runOwner(falseTool, nil, 5*time.Second); owned || !answered {
		t.Errorf("a refusal in time: owned %v answered %v", owned, answered)
	}
}

// A leftover's age is the time stampedTemp wrote in its name, not its mtime:
// on macOS a mounted image's folder shows the volume's root, as old as the
// release, and ditto keeps the source's times, so a mount or a copy still in
// use would look an hour old (#154).
func TestALeftoversAgeIsTheOneInItsName(t *testing.T) {
	dir := t.TempDir()
	long := time.Now().Add(-48 * time.Hour)

	// Made now, its folder reporting a release two days old: in use, kept.
	young, err := stampedTemp(dir, "kubetower-update-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(young, long, long); err != nil {
		t.Fatal(err)
	}
	if old, ok := oldEnough(young, "kubetower-update-"); !ok || old {
		t.Errorf("%s: old %v ok %v; made just now, whatever its mtime", filepath.Base(young), old, ok)
	}

	// Named two days ago, touched just now: old.
	stale := filepath.Join(dir, ".KubeTower-update-"+strconv.FormatInt(long.Unix(), 10)+"-7.app")
	if err := os.Mkdir(stale, 0o700); err != nil {
		t.Fatal(err)
	}
	if old, ok := oldEnough(stale, ".KubeTower-update-"); !ok || !old {
		t.Errorf("%s: old %v ok %v", filepath.Base(stale), old, ok)
	}

	// A name with no stamp, from an older release or a file: its mtime.
	plain := filepath.Join(dir, ".kubetower-update-123456")
	if err := os.WriteFile(plain, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if old, _ := oldEnough(plain, ".kubetower-update-"); old {
		t.Error("a fresh unstamped file read as old")
	}
	if err := os.Chtimes(plain, long, long); err != nil {
		t.Fatal(err)
	}
	if old, _ := oldEnough(plain, ".kubetower-update-"); !old {
		t.Error("an unstamped file two days old read as young")
	}

	// And the sweep keeps the young stamped folder.
	sweepBeside(filepath.Join(dir, "KubeTower.app"), "kubetower-update-"+"*", ".KubeTower-update-*")
	if _, err := os.Stat(young); err != nil {
		t.Errorf("the young folder was swept: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("the stale folder is still there: %v", err)
	}
}

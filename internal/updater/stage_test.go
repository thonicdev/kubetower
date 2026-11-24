package updater

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// installedPlan downloads, installs, and returns the plan the installer got.
func installedPlan(t *testing.T, u *Updater, rec *recorder) Plan {
	t.Helper()
	if _, err := u.Download(); err != nil {
		t.Fatal(err)
	}
	if s := settle(t, u); s.Phase != Ready {
		t.Fatalf("%+v", s)
	}
	if _, err := u.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		rec.mu.Lock()
		n := len(rec.plans)
		rec.mu.Unlock()
		if n == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.plans) != 1 {
		t.Fatalf("%d installs", len(rec.plans))
	}
	return rec.plans[0]
}

func TestTheInstallerGetsAPrivateCopyOfTheVerifiedFile(t *testing.T) {
	f := newFake(t)
	rec := &recorder{}
	u := f.updater(t.TempDir(), rec, nil)
	plan := installedPlan(t, u, rec)
	checked := filepath.Join(u.dir(), workDir, f.asset)
	if plan.File == checked || filepath.Dir(plan.File) == filepath.Dir(checked) {
		t.Fatalf("the installer reads %s, the checked path itself", plan.File)
	}
	// A file put at the checked path now does not reach the installer's copy.
	if err := os.WriteFile(checked, []byte("swapped"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(plan.File); string(got) != string(f.body) {
		t.Fatalf("the copy holds %q", got)
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(filepath.Dir(plan.File)); info.Mode().Perm() != 0o700 {
			t.Errorf("its folder is %v, want 0700", info.Mode().Perm())
		}
	}
}

func TestARelativeStateFolderGivesTheInstallerAnAbsolutePath(t *testing.T) {
	t.Chdir(t.TempDir())
	f := newFake(t)
	rec := &recorder{}
	plan := installedPlan(t, f.updater("state", rec, nil), rec)
	if !filepath.IsAbs(plan.File) {
		t.Fatalf("%s is relative", plan.File)
	}
}

func TestAnInstallThatDidNotStartAgainIsNotAFailure(t *testing.T) {
	f := newFake(t)
	rec := &recorder{err: fmt.Errorf("%w: no /bin/sh", ErrNotRestarted)}
	u := f.updater(t.TempDir(), rec, nil)
	installedPlan(t, u, rec)
	deadline := time.Now().Add(2 * time.Second)
	for u.Status().Phase == Installing && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if s := u.Status(); s.Phase != Idle || s.Reason != "" {
		t.Fatalf("%+v, want idle", s)
	}
}

func TestAFolderThatIsNotWritableLeavesTheFileToOpen(t *testing.T) {
	var opened []string
	was := openFolder
	openFolder = func(dir string) error { opened = append(opened, dir); return nil }
	t.Cleanup(func() { openFolder = was })

	f := newFake(t)
	rec := &recorder{err: fmt.Errorf("%w: /opt/kubetower", ErrNotWritable)}
	u := f.updater(t.TempDir(), rec, nil)
	if _, err := u.OpenFolder(); !errors.Is(err, ErrNoFolder) {
		t.Fatalf("%v before any install, want ErrNoFolder", err)
	}
	plan := installedPlan(t, u, rec)
	deadline := time.Now().Add(2 * time.Second)
	for u.Status().Phase == Installing && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	s := u.Status()
	if s.Phase != Manual || s.Folder != filepath.Dir(plan.File) || s.Reason != "" {
		t.Fatalf("%+v, want manual on %s with no reason", s, filepath.Dir(plan.File))
	}
	if got, _ := os.ReadFile(plan.File); string(got) != string(f.body) {
		t.Fatalf("the verified file is gone: %q", got)
	}
	if _, err := u.OpenFolder(); err != nil || len(opened) != 1 || opened[0] != s.Folder {
		t.Fatalf("%v, opened %q", err, opened)
	}
	// A new download starts over: the folder is forgotten.
	if _, err := u.Download(); err != nil {
		t.Fatal(err)
	}
	if s := settle(t, u); s.Folder != "" {
		t.Fatalf("%+v still names a folder", s)
	}
}

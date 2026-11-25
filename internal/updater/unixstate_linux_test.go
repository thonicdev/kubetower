package updater

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Through a real installer path on Linux - the package's, pkexec faked - the
// update's states for #101: a refusal goes back to ready, the file kept; a
// prompt left open is bounded; and a console that does not stop after the
// package is in place says the update is installed, never "nothing was".

// packageUpdater is an Updater whose installer is installPackage, with
// pkexec and its package manager faked by privileged, and a restart that
// starts nothing and stops nothing.
func packageUpdater(t *testing.T, f *fake, privileged func(ctx context.Context) error) (*Updater, string) {
	t.Helper()
	dir := t.TempDir()
	// The release's .deb, which the package path downloads.
	name, ok := AssetName("0.1.3", "linux", "amd64", LinuxDeb)
	if !ok {
		t.Fatal("no .deb asset name")
	}
	f.asset = name
	wasLook, wasRun, wasRestart, wasAbandon, wasWait := lookPath, runPrivileged, restart, abandonHelper, PromptWait
	t.Cleanup(func() {
		lookPath, runPrivileged, restart, abandonHelper, PromptWait = wasLook, wasRun, wasRestart, wasAbandon, wasWait
	})
	lookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	runPrivileged = func(ctx context.Context, _ []string) ([]byte, error) { return nil, privileged(ctx) }
	restart = func(string, []string) error { return nil }
	abandonHelper = func() error { return nil }
	PromptWait = 50 * time.Millisecond
	u := f.updater(dir, &recorder{}, func(o *Options) {
		o.Installation = Installation{Kind: LinuxDeb, Path: "/usr/bin/kubetower"}
		o.Install = installPackage
		o.ExitWait = 50 * time.Millisecond
	})
	return u, dir
}

func until(t *testing.T, u *Updater, gone Phase) Status {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for u.Status().Phase == gone && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	return u.Status()
}

func readyFor(t *testing.T, u *Updater) {
	t.Helper()
	if _, err := u.Download(); err != nil {
		t.Fatal(err)
	}
	if s := settle(t, u); s.Phase != Ready {
		t.Fatalf("after the download: %+v", s)
	}
}

// exitCode is the error a command exiting with code gives, as pkexec's does.
func exitCode(code int) error {
	return exec.Command("/bin/sh", "-c", "exit "+strconv.Itoa(code)).Run()
}

// downloads counts the asset's fetches.
func (f *fake) downloads() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, hit := range f.hits {
		if hit == "/dl/"+f.asset {
			n++
		}
	}
	return n
}

func TestARefusedPasswordGoesBackToReadyAndInstallsOnTheNextTry(t *testing.T) {
	f := newFake(t)
	asked := 0
	u, dir := packageUpdater(t, f, func(context.Context) error {
		asked++
		if asked == 1 {
			return exitCode(126) // the password was not given
		}
		return nil
	})
	readyFor(t, u)
	if _, err := u.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := until(t, u, Installing)
	if s.Phase != Ready || !strings.Contains(s.Reason, "the password was not given") || s.Version != "0.1.3" {
		t.Fatalf("after a refused password: %+v, want ready again with the reason", s)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "update", "install-*")); len(left) != 0 {
		t.Errorf("the refused install's staged copy stayed: %v", left)
	}
	// Install again, no download: the package is installed and the console
	// asked to stop. It does not stop here, so the update says it is installed.
	if _, err := u.Install(context.Background()); err != nil {
		t.Fatalf("install again: %v", err)
	}
	s = until(t, u, Installing)
	if s.Phase != Failed || s.Reason != errInstalledNotRestarted.Error() {
		t.Fatalf("installed, not stopped: %+v, want the update said installed, restart KubeTower", s)
	}
	if asked != 2 || f.downloads() != 1 {
		t.Errorf("pkexec asked %d times, %d downloads; want 2 and 1", asked, f.downloads())
	}
	raw, _ := os.ReadFile(filepath.Join(dir, installLog))
	for _, want := range []string{"installer handed over", "installed in place; KubeTower did not stop to run it"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("the log lacks %q:\n%s", want, raw)
		}
	}
}

func TestAPasswordPromptLeftOpenIsBounded(t *testing.T) {
	f := newFake(t)
	u, _ := packageUpdater(t, f, func(ctx context.Context) error {
		<-ctx.Done() // pkexec killed at PromptWait, as exec.CommandContext does
		return ctx.Err()
	})
	readyFor(t, u)
	if _, err := u.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := until(t, u, Installing)
	if s.Phase != Ready || !strings.Contains(s.Reason, "no answer to the password prompt") {
		t.Fatalf("a prompt left open: %+v, want ready again with the reason", s)
	}
}

// The relaunch shell is started for real and stopped: its whole group goes,
// and the program it waited to start never runs.
func TestTheRelaunchShellIsStoppedWithItsGroup(t *testing.T) {
	wasStop, wasLog := stopSelf, currentLog()
	stopSelf = func() error { return nil } // this test process stays: the shell waits
	t.Cleanup(func() { stopSelf = wasStop; useLog(wasLog) })
	ran := filepath.Join(t.TempDir(), "ran")
	logPath := filepath.Join(t.TempDir(), "update-install.log")
	useLog(logPath)
	if err := relaunch("/usr/bin/touch", []string{ran}); err != nil {
		t.Fatal(err)
	}
	relaunchHelper.Lock()
	pid := relaunchHelper.pid
	relaunchHelper.Unlock()
	if pid == 0 {
		t.Fatal("the relaunch shell was not kept")
	}
	if err := stopRelaunch(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	// Reaped by nobody here: a zombie still answers kill 0, so ask its state.
	if raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat")); err == nil && !strings.Contains(string(raw), ") Z ") {
		t.Fatalf("the relaunch shell %d is still running: %s", pid, raw)
	}
	if _, err := os.Stat(ran); err == nil {
		t.Fatal("the stopped shell started its program")
	}
	if err := stopRelaunch(); err != nil {
		t.Errorf("stopping again: %v, want nothing to do", err)
	}
}

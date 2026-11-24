//go:build !windows

package updater

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeMac stands in for hdiutil, ditto and open, and records every call.
type fakeMac struct {
	calls []string
	image string // the version the mounted image holds; empty holds no app
}

func (f *fakeMac) run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, filepath.Base(name)+" "+args[0])
	switch {
	case name == "/usr/bin/hdiutil" && args[0] == "attach":
		mount := args[len(args)-2]
		if f.image != "" {
			if err := os.MkdirAll(filepath.Join(mount, "KubeTower.app", "Contents", "MacOS"), 0o755); err != nil {
				return nil, err
			}
			return nil, os.WriteFile(filepath.Join(mount, "KubeTower.app", "Contents", "MacOS", "kubetower"), []byte(f.image), 0o755)
		}
	case name == "/usr/bin/hdiutil" && args[0] == "detach":
		return nil, os.RemoveAll(filepath.Join(args[1], "KubeTower.app"))
	case name == "/usr/bin/ditto":
		body, err := os.ReadFile(filepath.Join(args[0], "Contents", "MacOS", "kubetower"))
		if err != nil {
			return []byte("ditto: no such file"), err
		}
		if err := os.MkdirAll(filepath.Join(args[1], "Contents", "MacOS"), 0o755); err != nil {
			return nil, err
		}
		return nil, os.WriteFile(filepath.Join(args[1], "Contents", "MacOS", "kubetower"), body, 0o755)
	}
	return nil, nil
}

func stubMac(t *testing.T, image string) (*fakeMac, *[]string) {
	t.Helper()
	f := &fakeMac{image: image}
	run, rs := runTool, restart
	runTool = f.run
	var relaunched []string
	restart = func(path string, args []string) error {
		f.calls = append(f.calls, "relaunch")
		relaunched = append([]string{path}, args...)
		return nil
	}
	t.Setenv("TMPDIR", t.TempDir())
	t.Cleanup(func() { runTool, restart = run, rs })
	return f, &relaunched
}

func TestTheAppIsCopiedFromTheImageDetachedThenSwappedAndOpened(t *testing.T) {
	f, relaunched := stubMac(t, "0.1.3")
	dir := t.TempDir()
	app := filepath.Join(dir, "KubeTower.app")
	bundleAt(t, app, "0.1.2")
	plan := Plan{File: "/state/update/kubetower-0.1.3-arm64.dmg", Installation: Installation{Kind: MacApp, Path: app}}
	if err := installApp(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if got := version(t, app); got != "0.1.3" {
		t.Fatalf("%q", got)
	}
	if !strings.HasPrefix(strings.Join(f.calls, ","), "hdiutil attach,ditto ") {
		t.Fatalf("calls %q: attach, then ditto", f.calls)
	}
	// The image is detached before the relaunch, not after.
	if got := strings.Join(f.calls[2:], ","); !strings.HasPrefix(got, "hdiutil detach") || !strings.HasSuffix(got, "relaunch") {
		t.Fatalf("calls %q: detach, then relaunch", f.calls)
	}
	if len(*relaunched) < 3 || (*relaunched)[0] != "/usr/bin/open" || (*relaunched)[1] != "-n" || (*relaunched)[2] != app {
		t.Fatalf("relaunched %q", *relaunched)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("left beside it: %v", entries)
	}
	if mounts, _ := filepath.Glob(filepath.Join(os.Getenv("TMPDIR"), mountPrefix+"*")); len(mounts) != 0 {
		t.Errorf("mount points left: %v", mounts)
	}
}

func TestAnImageWithoutTheAppIsDetachedAndChangesNothing(t *testing.T) {
	f, relaunched := stubMac(t, "")
	dir := t.TempDir()
	app := filepath.Join(dir, "KubeTower.app")
	bundleAt(t, app, "0.1.2")
	err := installApp(context.Background(), Plan{File: "/s/x.dmg", Installation: Installation{Kind: MacApp, Path: app}})
	if err == nil || version(t, app) != "0.1.2" || len(*relaunched) != 0 {
		t.Fatalf("%v, %q, relaunched %q", err, version(t, app), *relaunched)
	}
	if f.calls[len(f.calls)-1] != "hdiutil detach" {
		t.Fatalf("calls %q: the image stays mounted", f.calls)
	}
}

func TestATranslocatedAppOpensTheImageAndIsInstalledByHand(t *testing.T) {
	f, relaunched := stubMac(t, "0.1.3")
	plan := Plan{File: "/state/update/kubetower-0.1.3-arm64.dmg",
		Installation: Installation{Kind: MacApp, Path: "/private/var/folders/x/T/AppTranslocation/ABC/d/KubeTower.app"}}
	err := installApp(context.Background(), plan)
	if !errors.Is(err, ErrNotWritable) || !errors.Is(err, ErrNotInPlace) {
		t.Fatalf("%v, want the manual phase", err)
	}
	if strings.Join(f.calls, ",") != "open "+plan.File || len(*relaunched) != 0 {
		t.Fatalf("calls %q, relaunched %q: only the image opened", f.calls, *relaunched)
	}
}

func TestARestartThatFailsAfterTheSwapIsAnInstalledApp(t *testing.T) {
	stubMac(t, "0.1.3")
	restart = func(string, []string) error { return errors.New("no open") }
	dir := t.TempDir()
	app := filepath.Join(dir, "KubeTower.app")
	bundleAt(t, app, "0.1.2")
	err := installApp(context.Background(), Plan{File: "/s/x.dmg", Installation: Installation{Kind: MacApp, Path: app}})
	if !errors.Is(err, ErrNotRestarted) || version(t, app) != "0.1.3" {
		t.Fatalf("%v, %q", err, version(t, app))
	}
}

func TestTheNextStartSweepsWhatAnInstallLeft(t *testing.T) {
	f, _ := stubMac(t, "")
	dir := t.TempDir()
	app := filepath.Join(dir, "KubeTower.app")
	bundleAt(t, app, "0.1.3")
	bundleAt(t, filepath.Join(dir, ".KubeTower-update-1.app"), "half")
	bundleAt(t, filepath.Join(dir, ".KubeTower-update-2.app.old"), "0.1.2")
	_ = os.Mkdir(filepath.Join(dir, ".KubeTower-probe-3"), 0o700)
	_ = os.WriteFile(filepath.Join(dir, "Other.txt"), nil, 0o600)
	mount := filepath.Join(os.Getenv("TMPDIR"), mountPrefix+"4")
	_ = os.Mkdir(mount, 0o700)
	sweepMacApp(app)
	entries, _ := os.ReadDir(dir)
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	if strings.Join(left, ",") != "KubeTower.app,Other.txt" {
		t.Fatalf("left %v", left)
	}
	if _, err := os.Stat(mount); err == nil || strings.Join(f.calls, ",") != "hdiutil detach" {
		t.Fatalf("calls %q; the mount point is still there", f.calls)
	}
}

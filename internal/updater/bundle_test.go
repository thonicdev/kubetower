package updater

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func bundleAt(t *testing.T, path, version string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(path, "Contents", "MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "Contents", "MacOS", "kubetower"), []byte(version), 0o755); err != nil {
		t.Fatal(err)
	}
}

func version(t *testing.T, bundle string) string {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(bundle, "Contents", "MacOS", "kubetower"))
	if err != nil {
		t.Fatal(err)
	}
	return string(got)
}

func TestSwapBundlePutsTheNewAppInPlaceAndRemovesTheOld(t *testing.T) {
	dir := t.TempDir()
	app := filepath.Join(dir, "KubeTower.app")
	next := filepath.Join(dir, ".KubeTower-update-1.app")
	bundleAt(t, app, "0.1.2")
	bundleAt(t, next, "0.1.3")
	if err := swapBundle(app, next); err != nil {
		t.Fatal(err)
	}
	if got := version(t, app); got != "0.1.3" {
		t.Fatalf("%q", got)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("left beside it: %v", entries)
	}
}

func TestAFailedSwapPutsTheOldAppBack(t *testing.T) {
	dir := t.TempDir()
	app := filepath.Join(dir, "KubeTower.app")
	bundleAt(t, app, "0.1.2")
	// A new bundle that is not there: the second rename fails.
	if err := swapBundle(app, filepath.Join(dir, ".KubeTower-update-gone.app")); err == nil {
		t.Fatal("swapped in nothing")
	}
	if got := version(t, app); got != "0.1.2" {
		t.Fatalf("the old app is not back: %q", got)
	}
	// A real bundle in another folder: refused for being elsewhere, before any rename.
	elsewhere := filepath.Join(t.TempDir(), "elsewhere.app")
	bundleAt(t, elsewhere, "0.1.3")
	if err := swapBundle(app, elsewhere); err == nil || !strings.Contains(err.Error(), "is not beside") {
		t.Fatalf("%v, want refused as not beside", err)
	}
	if version(t, app) != "0.1.2" || version(t, elsewhere) != "0.1.3" {
		t.Fatal("a bundle moved")
	}
}

func TestATranslocatedOrReadOnlyAppIsNotReplacedInPlace(t *testing.T) {
	if !translocated("/private/var/folders/x/T/AppTranslocation/ABC/d/KubeTower.app") {
		t.Error("a translocated path is taken as in place")
	}
	if translocated("/Applications/KubeTower.app") {
		t.Error("/Applications is taken as translocated")
	}
	dir := t.TempDir()
	if !writableBeside(filepath.Join(dir, "KubeTower.app")) {
		t.Error("a writable folder reads as read-only")
	}
	if runtime.GOOS != "windows" && os.Getuid() != 0 {
		_ = os.Chmod(dir, 0o555)
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
		if writableBeside(filepath.Join(dir, "KubeTower.app")) {
			t.Error("a read-only folder reads as writable")
		}
	}
}

func TestAnOldAppThatCannotBeRemovedDoesNotFailTheSwap(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("a read-only directory is not read-only here")
	}
	dir := t.TempDir()
	app := filepath.Join(dir, "KubeTower.app")
	next := filepath.Join(dir, ".KubeTower-update-1.app")
	bundleAt(t, app, "0.1.2")
	bundleAt(t, next, "0.1.3")
	// Another admin's files: the old bundle's MacOS folder cannot be emptied.
	locked := filepath.Join(app, "Contents", "MacOS")
	if err := os.Chmod(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(next+".old", "Contents", "MacOS"), 0o755) })
	if err := swapBundle(app, next); err != nil {
		t.Fatalf("%v: the update is in place", err)
	}
	if got := version(t, app); got != "0.1.3" {
		t.Fatalf("%q", got)
	}
}

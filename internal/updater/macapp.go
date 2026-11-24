//go:build !windows

package updater

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/thonicdev/kubetower/textutil"
)

// The macOS app bundle's install, without a build tag beyond !windows so its
// steps run in the tests on Linux too; install_darwin.go registers it.

// runTool runs a macOS tool by its absolute path and gives its output. A
// variable, so a test replaces hdiutil, ditto and open.
var runTool = func(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// mountPrefix names the folders the disk image is mounted on, in the temp folder.
const mountPrefix = "kubetower-update-"

// installApp mounts the verified disk image read-only, copies KubeTower.app
// beside the running bundle and detaches the image, swaps the two bundles and
// opens the new app once this process has exited. Where the bundle is
// translocated or its folder not writable, the image is opened in the Finder
// and the update ends in the manual phase (Open folder).
func installApp(ctx context.Context, plan Plan) error {
	bundle := plan.Installation.Path
	if translocated(bundle) || !writableBeside(bundle) {
		_, _ = runTool(ctx, "/usr/bin/open", plan.File)
		return fmt.Errorf("%w: %w", ErrNotWritable, ErrNotInPlace)
	}
	mount, err := os.MkdirTemp("", mountPrefix)
	if err != nil {
		return err
	}
	if out, err := runTool(ctx, "/usr/bin/hdiutil", "attach", "-nobrowse", "-readonly", "-noautoopen", "-mountpoint", mount, plan.File); err != nil {
		_ = os.Remove(mount)
		return fmt.Errorf("the disk image could not be mounted: %s", textutil.FirstLine(string(out)))
	}
	next, err := copyApp(ctx, filepath.Join(mount, "KubeTower.app"), bundle)
	// Detached now, not after the relaunch: shutdown may exit before a defer runs.
	detach(mount)
	if err != nil {
		return err
	}
	if err := swapBundle(bundle, next); err != nil {
		_ = os.RemoveAll(next)
		return err
	}
	if err := restart("/usr/bin/open", append([]string{"-n", bundle, "--args"}, os.Args[1:]...)); err != nil {
		return fmt.Errorf("%w: %v", ErrNotRestarted, err)
	}
	return nil
}

// copyApp copies the mounted KubeTower.app beside bundle, and gives its path.
func copyApp(ctx context.Context, source, bundle string) (string, error) {
	if _, err := os.Stat(source); err != nil {
		return "", fmt.Errorf("the disk image holds no KubeTower.app")
	}
	next, err := os.MkdirTemp(filepath.Dir(bundle), ".KubeTower-update-")
	if err != nil {
		return "", err
	}
	_ = os.Remove(next)
	next += ".app"
	if out, err := runTool(ctx, "/usr/bin/ditto", source, next); err != nil {
		_ = os.RemoveAll(next)
		return "", fmt.Errorf("the new app could not be copied: %s", textutil.FirstLine(string(out)))
	}
	return next, nil
}

// detach unmounts the disk image and removes its mount point.
func detach(mount string) {
	_, _ = runTool(context.Background(), "/usr/bin/hdiutil", "detach", mount, "-quiet", "-force")
	_ = os.Remove(mount)
}

// sweepMacApp removes what an install cut short left behind: a half-copied
// `.KubeTower-update-*.app`, an `.app.old` that could not be removed, a write
// probe, and a disk image still mounted in the temp folder.
func sweepMacApp(bundle string) {
	sweepBeside(bundle, ".KubeTower-update-*", ".KubeTower-probe-*")
	mounts, _ := filepath.Glob(filepath.Join(os.TempDir(), mountPrefix+"*"))
	for _, mount := range mounts {
		if info, err := os.Stat(mount); err == nil && info.IsDir() {
			detach(mount)
		}
	}
}

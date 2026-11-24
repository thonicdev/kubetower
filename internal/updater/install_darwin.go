package updater

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/thonicdev/kubetower/textutil"
)

func init() {
	installers[MacApp] = installApp
	installers[MacTar] = installMacTar
}

// installMacTar replaces the binary from the tar.gz's root `kubetower`.
func installMacTar(_ context.Context, plan Plan) error {
	if err := replaceFromTarGz(plan.Installation.Path, plan.File, "kubetower"); err != nil {
		return err
	}
	return restart(plan.Installation.Path, os.Args[1:])
}

// installApp mounts the verified disk image read-only, copies KubeTower.app
// beside the running bundle, swaps the two, detaches the image and opens the
// new app once this process has exited. Where the bundle is translocated or
// its folder not writable, the image is opened in the Finder instead.
func installApp(ctx context.Context, plan Plan) error {
	bundle := plan.Installation.Path
	if translocated(bundle) || !writableBeside(bundle) {
		if err := exec.CommandContext(ctx, "/usr/bin/open", plan.File).Run(); err != nil {
			return fmt.Errorf("%w, and the disk image could not be opened: %v", ErrNotInPlace, err)
		}
		return fmt.Errorf("%w: the disk image is open in the Finder", ErrNotInPlace)
	}
	mount, err := os.MkdirTemp("", "kubetower-update-")
	if err != nil {
		return err
	}
	defer os.Remove(mount)
	attach := exec.CommandContext(ctx, "/usr/bin/hdiutil", "attach", "-nobrowse", "-readonly", "-noautoopen", "-mountpoint", mount, plan.File)
	if out, err := attach.CombinedOutput(); err != nil {
		return fmt.Errorf("the disk image could not be mounted: %s", textutil.FirstLine(string(out)))
	}
	defer func() { _ = exec.Command("/usr/bin/hdiutil", "detach", mount, "-quiet", "-force").Run() }()

	source := filepath.Join(mount, "KubeTower.app")
	if _, err := os.Stat(source); err != nil {
		return fmt.Errorf("the disk image holds no KubeTower.app")
	}
	next, err := os.MkdirTemp(filepath.Dir(bundle), ".KubeTower-update-")
	if err != nil {
		return err
	}
	_ = os.Remove(next)
	next += ".app"
	if out, err := exec.CommandContext(ctx, "/usr/bin/ditto", source, next).CombinedOutput(); err != nil {
		_ = os.RemoveAll(next)
		return fmt.Errorf("the new app could not be copied: %s", textutil.FirstLine(string(out)))
	}
	if err := swapBundle(bundle, next); err != nil {
		_ = os.RemoveAll(next)
		return err
	}
	return restart("/usr/bin/open", append([]string{"-n", bundle, "--args"}, os.Args[1:]...))
}

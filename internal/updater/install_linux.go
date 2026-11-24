package updater

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/thonicdev/kubetower/textutil"
)

func init() {
	installers[LinuxAppImage] = installAppImage
	installers[LinuxTar] = installTar
	installers[LinuxDeb] = installPackage
	installers[LinuxRPM] = installPackage
}

// installAppImage replaces the AppImage file itself, then starts it again.
func installAppImage(_ context.Context, plan Plan) error {
	if err := replaceFile(plan.Installation.Path, plan.File); err != nil {
		return err
	}
	return restart(plan.Installation.Path, os.Args[1:])
}

// installTar replaces the binary from the tar.gz's root `kubetower`.
func installTar(_ context.Context, plan Plan) error {
	if err := replaceFromTarGz(plan.Installation.Path, plan.File, "kubetower"); err != nil {
		return err
	}
	return restart(plan.Installation.Path, os.Args[1:])
}

// installPackage installs the .deb or the .rpm through pkexec, which asks for
// the password through polkit, then starts the packaged binary again.
func installPackage(ctx context.Context, plan Plan) error {
	argv, err := packageCommand(plan.Installation.Kind, plan.File, exec.LookPath)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return pkexecFailure(err, out)
	}
	return restart(plan.Installation.Path, os.Args[1:])
}

// pkexecFailure says why pkexec or the package manager refused, in one line.
func pkexecFailure(err error, out []byte) error {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		switch exit.ExitCode() {
		case 126:
			return errors.New("the password was not given: nothing was installed")
		case 127:
			return errors.New("not authorized to install packages: nothing was installed")
		}
	}
	return fmt.Errorf("the package manager refused: %s", textutil.FirstLine(string(out)))
}

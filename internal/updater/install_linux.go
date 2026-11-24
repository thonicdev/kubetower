package updater

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

func init() {
	installers[LinuxAppImage] = installAppImage
	installers[LinuxTar] = installTar
	installers[LinuxDeb] = installPackage
	installers[LinuxRPM] = installPackage
	for _, kind := range []Kind{LinuxDeb, LinuxRPM} {
		installable[kind] = func() bool {
			_, err := packageCommand(kind, "", lookPath)
			return err == nil
		}
	}
}

// lookPath and runPrivileged are exec's, held in variables for the tests.
var (
	lookPath      = exec.LookPath
	runPrivileged = func(ctx context.Context, argv []string) ([]byte, error) {
		return exec.CommandContext(ctx, argv[0], argv[1:]...).CombinedOutput()
	}
)

// installAppImage replaces the AppImage file itself, then starts it again.
func installAppImage(_ context.Context, plan Plan) error {
	if err := replaceFile(plan.Installation.Path, plan.File); err != nil {
		return err
	}
	return startAgain(plan.Installation.Path)
}

// installTar replaces the binary from the tar.gz's root `kubetower`.
func installTar(_ context.Context, plan Plan) error {
	if err := replaceFromTarGz(plan.Installation.Path, plan.File, "kubetower"); err != nil {
		return err
	}
	return startAgain(plan.Installation.Path)
}

// installPackage installs the .deb or the .rpm through pkexec, which asks for
// the password through polkit, then starts the packaged binary again.
func installPackage(ctx context.Context, plan Plan) error {
	argv, err := packageCommand(plan.Installation.Kind, plan.File, lookPath)
	if err != nil {
		return err
	}
	if out, err := runPrivileged(ctx, argv); err != nil {
		return pkexecFailure(err, out)
	}
	return startAgain(plan.Installation.Path)
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
	return fmt.Errorf("the package manager refused: %s", refusal(string(out)))
}

// refusal is the line of a package manager's output that says why: the first
// that reads as an error ("E: ...", "error: ..."), else the last one. The first
// lines are progress ("Reading package lists...").
func refusal(out string) string {
	last := ""
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "E:") || strings.HasPrefix(strings.ToLower(line), "error") {
			return line
		}
		last = line
	}
	return last
}

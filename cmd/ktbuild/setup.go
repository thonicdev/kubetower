package main

// The Windows setup wizard.
//
// `kubetower-<version>-setup.exe` is an NSIS wizard (packaging/windows/
// kubetower.nsi) around the same kubetower.exe the Windows zip carries; this
// file runs makensis and reads the result back. installsetup.go in
// internal/launch says why NSIS.
//
//   - With `-stage DIR` it wraps the kubetower.exe already in DIR, beside
//     LICENSE, LICENSING.md and THIRD_PARTY_NOTICES.txt, so a release's setup
//     installs the zip's own bytes rather than a second build.
//   - Without it, it builds the windows/amd64 binary into a temporary folder
//     with the licenses, and the notices when `task build:notices` wrote them.
//
// makensis comes from `apt-get install nsis` or the NSIS installer on Windows;
// without it this refuses before building anything.

import (
	"debug/pe"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/thonicdev/kubetower/internal/peversion"
)

// The staging folder's contents. setupExe is the name the script installs;
// the rest are what every archive carries beside it.
const (
	setupExe      = "kubetower.exe"
	setupNotices  = "THIRD_PARTY_NOTICES.txt"
	setupScript   = "packaging/windows/kubetower.nsi"
	setupIcon     = "internal/launch/winres/icon.ico"
	noMakensisFix = "makensis is not on PATH, and the Windows setup is an NSIS script (" + setupScript + "). " +
		"Install NSIS: `sudo apt-get install nsis` on Debian or Ubuntu, or the installer from " +
		"https://nsis.sourceforge.io on Windows, with its folder on PATH"
)

// setupLicences are copied beside the binary when this builds it, and must be
// beside it when it is handed a stage.
var setupLicences = []string{"LICENSE", "LICENSING.md"}

func buildSetup(t target, out string) error {
	makensis, err := exec.LookPath("makensis")
	if err != nil {
		return errors.New(noMakensisFix)
	}
	out, err = filepath.Abs(out)
	if err != nil {
		return err
	}
	root, err := moduleRoot()
	if err != nil {
		return err
	}

	stage := t.stage
	if stage == "" {
		tmp, err := os.MkdirTemp("", "ktbuild-setup-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(tmp)
		binary := t
		binary.setup = false
		if err := build(binary, filepath.Join(tmp, setupExe)); err != nil {
			return err
		}
		for _, name := range setupLicences {
			if err := copyInto(filepath.Join(root, name), tmp); err != nil {
				return err
			}
		}
		notices := filepath.Join(root, "web", "dist", "ui", setupNotices)
		if _, err := os.Stat(notices); err == nil {
			if err := copyInto(notices, tmp); err != nil {
				return err
			}
		} else {
			// Said, not skipped quietly. A release never gets here: it passes
			// -stage, and a stage without the file is refused.
			fmt.Printf("ktbuild: no %s in web/dist/ui (task build:notices writes it), so this setup "+
				"installs LICENSE and LICENSING.md only; the binary still embeds its notices\n", setupNotices)
		}
		stage = tmp
	} else {
		if stage, err = filepath.Abs(stage); err != nil {
			return err
		}
		for _, name := range append([]string{setupNotices}, setupLicences...) {
			if _, err := os.Stat(filepath.Join(stage, name)); err != nil {
				return fmt.Errorf("-stage %s has no %s, which the zip carries and so must the setup: %w", stage, name, err)
			}
		}
		// Read back as if this had built it: GUI subsystem, and the version in
		// its resource when one was asked for.
		want := ""
		if t.stampsResource() {
			want = t.version
		}
		if err := checkWindowsBinary(filepath.Join(stage, setupExe), want); err != nil {
			return fmt.Errorf("%s: %w", filepath.Join(stage, setupExe), err)
		}
	}
	// Read back for the console before wrapping it, so a setup never installs
	// "The front end is not in this binary".
	if err := checkEmbedsConsole(filepath.Join(stage, setupExe)); err != nil {
		return fmt.Errorf("%s: %w", filepath.Join(stage, setupExe), err)
	}

	if err := makeSetup(makensis, root, stage, out, t.version); err != nil {
		return err
	}
	fmt.Printf("built setup windows/amd64 %s -> %s\n", versionOrDev(t.version), out)
	return nil
}

// makeSetup runs makensis over the script with stage as its current
// directory, and reads the result back.
func makeSetup(makensis, root, stage, out, version string) error {
	_, notices := os.Stat(filepath.Join(stage, setupNotices))
	args, err := makensisArgs(root, out, version, notices == nil)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	cmd := exec.Command(makensis, args...)
	cmd.Dir = stage
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("makensis %s: %w", setupScript, err)
	}
	if err := checkSetup(out, versionOrDev(version)); err != nil {
		return fmt.Errorf("%s: %w", out, err)
	}
	return nil
}

// makensisArgs is the whole of what the script is told.
//
// `-WX` makes a warning fail the build: NSIS warns about defects (an unused
// page, a shortcut to a file never installed) that nobody reads in a log.
// `-NOCD` keeps the stage as the current directory, so every `File` is relative
// to it. Dashes, which makensis requires off Windows.
func makensisArgs(root, out, version string, notices bool) ([]string, error) {
	vi, err := viVersion(version)
	if err != nil {
		return nil, err
	}
	args := []string{
		"-V2", "-WX", "-NOCD",
		"-DVERSION=" + versionOrDev(version),
		"-DVIVERSION=" + vi,
		"-DOUTFILE=" + out,
		"-DICON=" + filepath.Join(root, filepath.FromSlash(setupIcon)),
	}
	if notices {
		args = append(args, "-DNOTICES")
	}
	return append(args, filepath.Join(root, filepath.FromSlash(setupScript))), nil
}

// viVersion is version as the four numbers a version resource's fixed part
// requires: 0.1.0-rc.1 is 0.1.0.0, and a build with no version is 0.0.0.0.
// The string parts of the resource carry the version as given.
func viVersion(version string) (string, error) {
	if version == "" {
		return "0.0.0.0", nil
	}
	core, _, _ := strings.Cut(version, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("-version %q is not MAJOR.MINOR.PATCH[-pre]", version)
	}
	for _, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 || n > 0xffff {
			return "", fmt.Errorf("-version %q: %q does not fit a Windows version resource, "+
				"whose numbers are 0 to 65535", version, part)
		}
	}
	return core + ".0", nil
}

func versionOrDev(version string) string {
	if version == "" {
		return "dev"
	}
	return version
}

// checkSetup reads the setup back: a PE file, a GUI program, and a version
// resource whose ProductName is KubeTower and whose ProductVersion and
// FileVersion equal version exactly. A substring search could pass: 0.1.0-rc.1
// contains 0.1.0, and the payload carries the same version string.
func checkSetup(path, version string) error {
	f, err := pe.Open(path)
	if err != nil {
		return fmt.Errorf("not a PE file: %w", err)
	}
	var subsystem uint16
	switch oh := f.OptionalHeader.(type) {
	case *pe.OptionalHeader64:
		subsystem = oh.Subsystem
	case *pe.OptionalHeader32:
		subsystem = oh.Subsystem
	default:
		f.Close()
		return errors.New("no optional header, so no subsystem to check")
	}
	f.Close()
	if subsystem != pe.IMAGE_SUBSYSTEM_WINDOWS_GUI {
		return fmt.Errorf("linked for PE subsystem %d, not the Windows GUI subsystem", subsystem)
	}
	for key, want := range map[string]string{
		"ProductName":    "KubeTower",
		"ProductVersion": version,
		"FileVersion":    version,
	} {
		got, err := peversion.Value(path, key)
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("the version resource's %s is %q, not %q", key, got, want)
		}
	}
	return nil
}

// copyInto copies the file at path into dir under the same name.
func copyInto(path, dir string) error {
	in, err := os.Open(path)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(filepath.Join(dir, filepath.Base(path)))
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

package launch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/thonicdev/kubetower/internal/peversion"
)

// The Windows setup, and why it is an NSIS wizard rather than this binary.
//
// Windows users expect an installer. The setup is a classic NSIS wizard
// (packaging/windows/kubetower.nsi) around the same kubetower.exe the zip
// carries: a license page, the install folder, Start-menu and desktop
// shortcuts, a finish page that launches it, one uninstaller and one Apps &
// features entry. No message box on install or uninstall.
//
//   - Unsigned either way: SmartScreen depends on Mark-of-the-Web and the
//     missing certificate, not the format; only signing removes it.
//   - One uninstall mechanism: NSIS keeps no installer database; the script
//     writes the Uninstall key (installKey) so an upgrade replaces the row, and
//     UninstallString names the wizard's uninstall.exe, which this binary's
//     `--uninstall` only launches.
//   - Buildable on Linux: makensis is packaged for Debian and Ubuntu, unlike
//     WiX. `cmd/ktbuild -setup` runs it.
//   - Per user only: `RequestExecutionLevel user` and
//     $LOCALAPPDATA\Programs\KubeTower, no elevation, nothing under HKLM.
//
// The plain kubetower.exe in the zip still runs when double-clicked, without
// installing anything. On Windows `--install` refuses with a sentence naming
// the setup (`setup.exe /S` covers scripts), and `--uninstall` starts the
// registered uninstall.exe after reading its version resource, without
// waiting. On macOS and Linux both flags do the work themselves
// (installself.go).

// installKey is where Windows looks for the Apps & features list, under
// CURRENT_USER. The setup writes it and this binary only reads it, to find the
// uninstaller. kubetower.nsi defines the same string, and a test holds them
// together: a different spelling would add a second row after an upgrade.
// Untagged so that test runs on every host.
const installKey = `Software\Microsoft\Windows\CurrentVersion\Uninstall\KubeTower`

// uninstallerName is the uninstaller the setup writes into the install folder.
const uninstallerName = "uninstall.exe"

// installRefusal is what `--install` says on Windows.
const installRefusal = "--install is gone on Windows: KubeTower is installed by kubetower-<version>-setup.exe, " +
	"from the same release page as this file. Run it, or run it with /S from a script"

// registeredUninstaller is the one uninstaller `--uninstall` may start: the
// setup's `uninstall.exe` in the folder Apps & features records for KubeTower
// (`InstallLocation` under installKey), and nowhere else.
//
// Not beside the running executable or in a default folder: the portable zip
// may be unpacked next to somebody else's uninstall.exe, and CreateProcess shows
// no SmartScreen prompt. checkUninstaller then reads the file's version
// resource before anything runs it.
//
// Pure, taking the registry value as an argument, so it is tested without a
// registry.
func registeredUninstaller(registered string) (string, error) {
	dir := strings.TrimSpace(strings.Trim(strings.TrimSpace(registered), `"`))
	if dir == "" {
		return "", errors.New("KubeTower is not registered in Apps & features for this account, so there is " +
			"no uninstaller to start. The setup registers it; a copy unpacked from the zip needs no " +
			"uninstalling - delete it")
	}
	return filepath.Join(dir, uninstallerName), nil
}

// checkUninstaller refuses a file that is not the setup's uninstaller: it must
// exist and its version resource must name KubeTower. Read, never run.
func checkUninstaller(path string) error {
	if _, err := os.Stat(path); err != nil {
		// An old self-install has a registered folder and no uninstaller.
		return fmt.Errorf("there is no uninstaller at \"%s\". If KubeTower was installed before its setup "+
			"existed, run kubetower-<version>-setup.exe over it to repair the install, then uninstall it "+
			"from Settings > Apps", path)
	}
	name, err := peversion.Value(path, "ProductName")
	if err != nil {
		return fmt.Errorf("\"%s\" was not started: its version resource could not be read (%v)", path, err)
	}
	if name != "KubeTower" {
		return fmt.Errorf("\"%s\" was not started: it names itself %q, not KubeTower", path, name)
	}
	return nil
}

//go:build windows

package launch

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"golang.org/x/sys/windows/registry"
)

// What installing means on Windows: the setup wizard's business, and this
// binary's only to point at it.
//
// kubetower-<version>-setup.exe (installsetup.go) writes the program, the
// shortcuts and the Apps & features entry, and its uninstall.exe removes them.
// So `--install` refuses with a sentence naming the setup, and `--uninstall`
// launches that uninstaller: one uninstall mechanism.
//
// Per user, never for the machine: everything under HKEY_CURRENT_USER and the
// profile, no elevation. A process holding credentials for production clusters
// does not belong in Program Files or under HKLM, and a colleague can install it
// without admin rights.

// runInstall refuses: the setup is the installer on Windows.
func runInstall() error {
	return errors.New(installRefusal)
}

// runUninstall starts the setup's uninstaller and leaves: a hand-off, not a
// result.
//
// It cannot usefully wait: NSIS copies its uninstaller to the temporary folder,
// starts the copy and exits, and the uninstaller can only delete this
// executable once this process is gone. A script that needs the exit code runs
// `"<folder>\uninstall.exe" /S _?=<folder>` itself.
//
// Only the registered install folder's uninstaller is started, and only once
// its version resource names KubeTower. Success prints one console line; the
// uninstaller is the window, so there is no message box.
func runUninstall() error {
	path, err := registeredUninstaller(registeredInstallLocation())
	if err != nil {
		return err
	}
	if err := checkUninstaller(path); err != nil {
		return err
	}
	cmd := exec.Command(path)
	cmd.Dir = filepath.Dir(path)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting %s: %w", path, err)
	}
	fmt.Fprintf(os.Stderr, "Started \"%s\". The uninstaller carries on by itself; this command does not "+
		"wait for it or report its result.\n", path)
	return cmd.Process.Release()
}

// registeredInstallLocation is the folder Apps & features records for
// KubeTower, or "" when there is none or it cannot be read.
func registeredInstallLocation() string {
	key, err := registry.OpenKey(registry.CURRENT_USER, installKey, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer key.Close()
	dir, _, err := key.GetStringValue("InstallLocation")
	if err != nil {
		return ""
	}
	return dir
}

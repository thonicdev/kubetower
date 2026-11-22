//go:build darwin

package launch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// What installing means on macOS: building a bundle.
//
//	~/Applications/KubeTower.app/
//	  Contents/
//	    Info.plist          — what makes the directory an application
//	    MacOS/kubetower      — the executable itself
//	    Resources/kubetower.icns
//
// ~/Applications rather than /Applications: a process holding credentials for
// production clusters is not installed for every account. macOS creates
// ~/Applications on demand.
//
// The downloaded binary is quarantined by Gatekeeper on first open (it is
// unsigned; the README says how to get past it). The bundle written here is
// created locally and inherits no extended attributes, so it is expected not to
// carry the quarantine flag.

// bundleRoot is `~/Applications/KubeTower.app`.
func bundleRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("finding your home directory: %w", err)
	}
	return filepath.Join(home, "Applications", bundleDirName), nil
}

// binaryDestination is inside the bundle, hence a full path.
func binaryDestination() (string, error) {
	root, err := bundleRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "Contents", "MacOS", bundleExecutableName), nil
}

// desktopInstall writes the two files that turn a directory into an application.
//
// The executable is already in Contents/MacOS (runInstall's copy); the plist
// and icon come from writeAppBundle, the same writer the downloadable bundle
// uses, so the two are one application.
func desktopInstall(exe string) (entries, notes []string, err error) {
	root, err := bundleRoot()
	if err != nil {
		return nil, nil, err
	}

	entries, notes, err = writeAppBundle(root, version)
	if err != nil {
		return entries, notes, err
	}

	// No `touch` or lsregister: lsregister is undocumented and has moved
	// between releases, and the Finder registers a bundle in a watched
	// directory by itself.

	if strings.Contains(exe, " ") {
		// Harmless here, but a launchd plist or alias written by hand will
		// need it quoted.
		notes = append(notes, "the install path contains a space; quote it in any script you write")
	}
	return entries, notes, nil
}

// desktopUninstall removes the whole bundle, which is also the executable.
//
// On macOS the application is the directory, so nothing else is left pointing
// at nothing.
func desktopUninstall(exe string) (removed, notes []string, err error) {
	root, err := bundleRoot()
	if err != nil {
		return nil, nil, err
	}
	if removeIfPresent(root) {
		removed = append(removed, root)
	}
	return removed, notes, nil
}

// removeInstalledBinary is a no-op after the bundle has gone.
//
// It still checks, because desktopUninstall can fail and leave the executable
// behind. macOS unlinks a running image, so deleting the bundle from inside it
// is legal.
func removeInstalledBinary(exe string) (gone bool, note string) {
	if _, err := os.Stat(exe); err != nil {
		return false, ""
	}
	if err := os.Remove(exe); err != nil {
		return false, fmt.Sprintf("%s could not be deleted: %v", exe, err)
	}
	return true, ""
}

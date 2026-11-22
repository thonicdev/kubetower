//go:build linux || darwin

package launch

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// `--install` and `--uninstall` on the two platforms with no setup wizard.
// install.go says what each means; install_linux.go and install_darwin.go hold
// the platform halves. install_windows.go hands both to the setup.

// runInstall places this executable and registers it with the desktop.
//
// The binary is copied before the desktop entries are made, so every entry
// points at a path that exists: a launcher that opens nothing gets reported as
// the application being broken.
func runInstall() error {
	source, err := selfPath()
	if err != nil {
		return fmt.Errorf("finding this executable: %w", err)
	}

	target, err := binaryDestination()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("making %s: %w", filepath.Dir(target), err)
	}

	inst := installation{Binary: target}

	// Installing over the copy already there is the ordinary case: the next
	// release.
	if sameFile(source, target) {
		// Re-registering is still worth doing: a launcher may have been deleted
		// by hand, and this is how it is repaired.
		inst.Notes = append(inst.Notes,
			"the executable was already in place, so only the desktop entries were rewritten")
	} else {
		displaced, err := replaceExecutable(source, target)
		if err != nil {
			return err
		}
		if displaced {
			// A displaced file means the old executable was running, and the
			// running process keeps the old code. Saying so stops somebody
			// testing a fix in the window that is still the old build.
			inst.Notes = append(inst.Notes,
				"KubeTower was running, so the old executable was moved aside rather than "+
					"overwritten. The window still open is still the old version — close it "+
					"and open it again to run this one")
		}
	}

	entries, notes, err := desktopInstall(target)
	inst.Entries = append(inst.Entries, entries...)
	inst.Notes = append(inst.Notes, notes...)
	if err != nil {
		// Not fatal: the executable is in place and runnable, and an error
		// would invite a reinstall.
		inst.Notes = append(inst.Notes, "desktop registration: "+err.Error())
	}

	// Why the installed application is a browser window here: said in the
	// installer's report as well as at launch, because an .app opened from
	// the Finder has no terminal to say it in.
	if note := nativeWindowAbsentNote(runtime.GOOS); note != "" {
		inst.Notes = append(inst.Notes, note)
	}

	report(installedHelp(inst))
	return nil
}

// runUninstall removes what runInstall made.
//
// The desktop entries go before the executable, the reverse of the install, so
// that if removing the executable fails nothing points at it.
func runUninstall() error {
	target, err := binaryDestination()
	if err != nil {
		return err
	}

	removed, notes, err := desktopUninstall(target)
	if err != nil {
		notes = append(notes, "desktop entries: "+err.Error())
	}

	// Whether a program can delete itself differs per platform.
	gone, note := removeInstalledBinary(target)
	if gone {
		removed = append(removed, target)
	}
	if note != "" {
		notes = append(notes, note)
	}

	report(uninstalledHelp(removed, notes))
	return nil
}

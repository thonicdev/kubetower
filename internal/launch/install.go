package launch

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// `--install`: the binary puts itself where this machine keeps applications,
// and `--uninstall` takes it back out, on macOS and Linux. On Windows both
// belong to the setup wizard (installsetup.go): `--install` refuses with a
// sentence naming it, and `--uninstall` launches its uninstaller.
//
// A flag rather than a separate installer program: a second binary would be
// built, versioned and blocked by the gatekeeper just like the application, and
// the file downloaded is then the file that installs, under one checksum. On
// Windows users expect a setup, so it is an NSIS wizard around the same binary.
//
// What "installed" means differs, so each platform file holds its own answer:
//
//   - Windows (the setup): the executable under %LOCALAPPDATA%\Programs\KubeTower
//     by default, Start-menu and desktop shortcuts, an Apps & features entry.
//   - Linux: the executable in ~/.local/bin, a .desktop entry and a bitmap in
//     the hicolor theme.
//   - macOS: an .app bundle in ~/Applications.
//
// All per user, never administrator or root: this process holds credentials for
// production clusters, so it should not be installed into a directory other
// accounts can read.

// installation is what an install did, in order, so the final report is a
// record rather than a description.
type installation struct {
	// Binary is where the executable now lives; on macOS inside the bundle,
	// hence a full path.
	Binary string
	// Entries is everything created besides the executable (a shortcut, a
	// desktop file), only ever shown to a person.
	Entries []string
	// Notes are what went wrong without failing the install, such as an icon
	// not written or a directory not on PATH.
	Notes []string
}

// selfPath is this executable, symlinks resolved and absolute.
//
// On Linux the file run is often a link, and copying the link would install a
// pointer into ~/Downloads that breaks when the download is tidied away.
func selfPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Abs(exe)
}

// sameFile answers whether two paths are one file on disk, following the links
// and the case-insensitivity that make string comparison wrong.
func sameFile(a, b string) bool {
	fa, err := os.Stat(a)
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(fa, fb)
}

// replaceExecutable copies source over target, including when target is running.
//
// An executable that is running cannot be overwritten (a sharing violation on
// Windows, ETXTBSY on Linux), which is exactly an update while the old version
// is open. Both allow a rename, which touches only the directory entry, so the
// running file is moved aside and the new one written in its place. The running
// process keeps the old code, so the report says to close and reopen.
//
// The displaced file usually cannot be deleted yet; it is swept by the next
// install, and failing to tidy never fails an install. displaced reports
// whether the fallback was needed.
func replaceExecutable(source, target string) (displaced bool, err error) {
	sweepAside(filepath.Dir(target))

	err = copyFile(source, target)
	if err == nil {
		return false, nil
	}

	aside := target + asideSuffix
	_ = os.Remove(aside)
	if renameErr := os.Rename(target, aside); renameErr != nil {
		// Report the original failure, which names the cause.
		return false, fmt.Errorf("writing %s: %w", target, err)
	}
	if err := copyFile(source, target); err != nil {
		// Put it back: a failed install must leave a working application.
		_ = os.Rename(aside, target)
		return false, fmt.Errorf("writing %s: %w", target, err)
	}
	// Expected to fail while the old one runs; sweepAside clears it later.
	_ = os.Remove(aside)
	return true, nil
}

// asideSuffix marks a displaced executable, chosen to read as obvious junk.
const asideSuffix = ".old"

// sweepAside deletes executables displaced by an earlier install, now that
// whatever held them has probably exited.
func sweepAside(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), asideSuffix) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// copyFile writes source to target, executable.
// Through a temporary name in the same directory and a rename, so an
// interrupted copy never leaves half an executable.
func copyFile(source, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()

	tmp, err := os.CreateTemp(filepath.Dir(target), ".kubetower-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpName)
	}()

	if _, err := io.Copy(tmp, in); err != nil {
		return err
	}
	// Before the rename, which publishes the file: otherwise a power cut can
	// leave a truncated binary in place.
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return err
	}
	return os.Rename(tmpName, target)
}

// writeFile writes a desktop entry, a plist or an icon, making its directory
// first.
func writeFile(path string, content []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, content, mode)
}

// removeIfPresent deletes a path and reports whether anything was there.
//
// A missing file is not an error during an uninstall: it is the ordinary result
// of uninstalling twice or of a shortcut removed by hand.
func removeIfPresent(path string) bool {
	if _, err := os.Lstat(path); err != nil {
		return false
	}
	return os.RemoveAll(path) == nil
}

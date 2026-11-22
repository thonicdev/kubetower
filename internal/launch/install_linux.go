//go:build linux

package launch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/thonicdev/kubetower/internal/web"
)

// What installing means on Linux: a binary on PATH, a launcher entry, an icon.
//
// All per user under ~/.local, which every desktop environment reads; never
// root, and never /usr/local/bin, on everybody's PATH.

// binaryDestination is `~/.local/bin/kubetower`.
//
// Not ~/bin, which only some distributions put on PATH, and not /usr/local/bin,
// which needs root. ~/.local/bin is the XDG user directory most distributions'
// default profiles put on PATH; where it is not, the report says so.
func binaryDestination() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("finding your home directory: %w", err)
	}
	return filepath.Join(home, ".local", "bin", "kubetower"), nil
}

// dataHome is `$XDG_DATA_HOME`, or the `~/.local/share` the specification says
// to assume when it is unset.
func dataHome() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("finding your home directory: %w", err)
	}
	return filepath.Join(home, ".local", "share"), nil
}

// desktopFile and iconFile are where the two entries go.
//
// The desktop file is named in reverse-DNS form, as the specification asks, so
// two unrelated projects called kubetower cannot overwrite each other. The icon
// is not: an Icon= key names a theme entry, looked up by the plain name.
func desktopFile() (string, error) {
	data, err := dataHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(data, "applications", "dev.thonicdev.kubetower.desktop"), nil
}

func iconFile() (string, error) {
	data, err := dataHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(data, "icons", "hicolor", "512x512", "apps", "kubetower.png"), nil
}

// desktopInstall writes the icon first and the launcher second.
//
// A launcher naming an icon not there yet is one some desktops cache as
// icon-less, past the fix.
func desktopInstall(exe string) (entries, notes []string, err error) {
	iconName := "kubetower"

	icon, iconErr := iconFile()
	if iconErr == nil {
		if png, readErr := web.AppIconPNG(); readErr != nil {
			notes = append(notes, "no icon was installed: "+readErr.Error())
			iconName = ""
		} else if writeErr := writeFile(icon, png, 0o644); writeErr != nil {
			notes = append(notes, "no icon was installed: "+writeErr.Error())
			iconName = ""
		} else {
			entries = append(entries, "icon          "+icon)
		}
	} else {
		notes = append(notes, "no icon was installed: "+iconErr.Error())
		iconName = ""
	}

	launcher, err := desktopFile()
	if err != nil {
		return entries, notes, err
	}
	if err := writeFile(launcher, desktopEntry(exe, iconName), 0o644); err != nil {
		return entries, notes, fmt.Errorf("writing %s: %w", launcher, err)
	}
	entries = append(entries, "launcher      "+launcher)

	// No update-desktop-database: it refreshes the MIME cache, and this entry
	// registers no file association. Desktops pick a new .desktop file up by
	// themselves (some after a re-login).

	if note := pathNote(exe); note != "" {
		notes = append(notes, note)
	}
	return entries, notes, nil
}

// pathNote says so when the install directory is not on PATH.
//
// A note rather than a fix: editing somebody's shell profile from an installer
// would also need the uninstaller to edit it back.
func pathNote(exe string) string {
	dir := filepath.Dir(exe)
	for _, entry := range filepath.SplitList(os.Getenv("PATH")) {
		if entry == dir {
			return ""
		}
	}
	return fmt.Sprintf(
		"%s is not on your PATH, so the launcher works but typing `kubetower` in a terminal "+
			"will not. Add it with: export PATH=\"%s:$PATH\"", dir, dir)
}

// desktopUninstall removes the launcher and the icon.
func desktopUninstall(exe string) (removed, notes []string, err error) {
	for _, locate := range []func() (string, error){desktopFile, iconFile} {
		path, locErr := locate()
		if locErr != nil {
			notes = append(notes, locErr.Error())
			continue
		}
		if removeIfPresent(path) {
			removed = append(removed, path)
		}
	}
	return removed, notes, nil
}

// removeInstalledBinary deletes the executable.
//
// Linux refuses to write over a running image (ETXTBSY) but unlinks one, so
// this works even when the running process is the one being removed.
func removeInstalledBinary(exe string) (gone bool, note string) {
	if _, err := os.Stat(exe); err != nil {
		return false, ""
	}
	if err := os.Remove(exe); err != nil {
		return false, fmt.Sprintf("%s could not be deleted: %v", exe, err)
	}
	return true, ""
}

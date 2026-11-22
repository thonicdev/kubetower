package launch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Where a confined browser can read the launch page.
//
// Ubuntu's default Firefox and Chromium are snaps, and a snap may not read a
// hidden directory in the home folder, so a page in ~/.kubetower would open on
// a file error, and with no password on the loopback desktop there is then no
// way in. A snap does read its own ~/snap/<name>/common, and a Flatpak its own
// ~/.var/app/<id>.
//
// So for a snap or Flatpak browser the page goes in that browser's own data
// directory, still 0600 and off the command line; any other browser keeps the
// state directory.

var (
	// snapBin is where snapd puts the commands of installed snaps.
	snapBin = "/snap/bin"

	// snapDesktopDirs and flatpakDesktopDirs hold the desktop entries snapd
	// and Flatpak export; an entry found there names a confined application.
	snapDesktopDirs    = []string{"/var/lib/snapd/desktop/applications"}
	flatpakDesktopDirs = func(home string) []string {
		return []string{
			filepath.Join(home, ".local", "share", "flatpak", "exports", "share", "applications"),
			"/var/lib/flatpak/exports/share/applications",
		}
	}

	// htmlHandler answers the desktop entry that opens a local HTML page, which
	// is what `xdg-open` starts for a file:// address. A variable for tests.
	htmlHandler = func() string {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "xdg-mime", "query", "default", "text/html").Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
)

// chromiumPageDir is where the page goes for the Chromium-based browser at
// path: its snap's own directory when it is a snap, otherwise "" (the state
// directory).
func chromiumPageDir(path string) string {
	if runtime.GOOS != "linux" || filepath.Dir(path) != snapBin {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	// A snap's command is `<snap>` or `<snap>.<app>`.
	name, _, _ := strings.Cut(filepath.Base(path), ".")
	return snapUserCommon(home, name)
}

// defaultBrowserPageDir is where the page goes for the default browser: the
// confined application's own directory when the handler of a local HTML page
// is a snap or a Flatpak, otherwise "" (the state directory).
func defaultBrowserPageDir() string {
	if runtime.GOOS != "linux" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return confinedDesktopDir(home, htmlHandler())
}

// confinedDesktopDir maps a desktop entry id to the data directory of the
// confined application it starts, or "" when it is not a snap's or a
// Flatpak's.
func confinedDesktopDir(home, id string) string {
	if id == "" || !strings.HasSuffix(id, ".desktop") || strings.ContainsAny(id, `/\`) {
		return ""
	}
	if exported(snapDesktopDirs, id) {
		// A snap's entry is `<snap>_<app>.desktop`.
		if name, _, ok := strings.Cut(id, "_"); ok && name != "" {
			return snapUserCommon(home, name)
		}
		return ""
	}
	if exported(flatpakDesktopDirs(home), id) {
		return filepath.Join(home, ".var", "app", strings.TrimSuffix(id, ".desktop"))
	}
	return ""
}

// snapUserCommon is a snap's per-user directory kept across its revisions,
// $SNAP_USER_COMMON inside the snap.
func snapUserCommon(home, name string) string {
	return filepath.Join(home, "snap", name, "common")
}

func exported(dirs []string, id string) bool {
	for _, dir := range dirs {
		if info, err := os.Stat(filepath.Join(dir, id)); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

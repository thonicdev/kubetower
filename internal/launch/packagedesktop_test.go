package launch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestThePackagedDesktopEntryIsTheInstallersOwn. The Linux packages (the .deb,
// the .rpm and the AppImage) ship packaging/linux/kubetower.desktop, a committed
// file, because a package is built from files and not by running the binary.
// `--install` writes desktopEntry instead. Two launchers for one program that
// say different things - another name, no --app, another category - would be
// two programs to whoever opens the applications menu, so the file has to be
// desktopEntry's bytes for an installed `kubetower` on PATH with the themed
// icon `kubetower`.
//
// Exactly one difference is allowed, and it is spelt out: `--install` quotes
// the absolute path it found (Exec="/home/x/.local/bin/kubetower" --app), and
// the packages install to /usr/bin, which is on every PATH, so their Exec names
// the command bare - what every distribution's own .desktop files do.
func TestThePackagedDesktopEntryIsTheInstallersOwn(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "packaging", "linux", "kubetower.desktop"))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(string(desktopEntry("kubetower", "kubetower")),
		"Exec=\"kubetower\" --app\n", "Exec=kubetower --app\n", 1)
	if !strings.Contains(want, "Exec=kubetower --app\n") {
		t.Fatalf("desktopEntry no longer writes Exec as `Exec=\"<path>\" --app`, so this test's one "+
			"allowed difference does not apply; read both again:\n%s", desktopEntry("kubetower", "kubetower"))
	}
	if got := string(raw); got != want {
		t.Errorf("packaging/linux/kubetower.desktop is not desktopEntry's launcher for /usr/bin/kubetower.\n"+
			"got:\n%s\nwant:\n%s", got, want)
	}
}

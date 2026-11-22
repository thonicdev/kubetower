package launch

import (
	"fmt"
	"os"
	"strings"

	"github.com/thonicdev/kubetower/internal/statefile"
)

// What `--install` and `--uninstall` tell the person who ran them, and where.
//
// On Windows the setup wizard installs and uninstalls, so only report is used,
// for a failure (`--install` refusing, `--uninstall` finding no uninstaller).
//
// Not a log line: the log is a file inside the state directory, and an
// installer whose output landed there would appear to do nothing. It goes to
// stderr, plus a box when there is no console.
//
// The box is conditional because MessageBoxW blocks until clicked, which would
// hang any script or CI. A console means a shell is driving; no console means a
// double-click, where a person is present and the box is the only channel.
// showMessage is a no-op everywhere but Windows.

// report puts a finished installer message, or a failure, in front of the
// operator.
func report(body string) {
	fmt.Fprintln(os.Stderr, body)
	if !consoleAttached {
		showMessage("KubeTower", body)
	}
}

// installedHelp is what `--install` prints on macOS and Linux.
//
// Where things went, what half-worked, and the facts nobody guesses: no
// password on this machine, listening on it only, a password only in a
// container or beyond loopback. A test holds the banner to what the console
// offers. The environment variables are left to `--help` and .env.example.
func installedHelp(inst installation) string {
	var b strings.Builder

	fmt.Fprintf(&b, "KubeTower %s is installed.\n\n", version)
	fmt.Fprintf(&b, "    application   %s\n", inst.Binary)
	for _, entry := range inst.Entries {
		fmt.Fprintf(&b, "    %s\n", entry)
	}

	if len(inst.Notes) > 0 {
		b.WriteString("\n")
		for _, note := range inst.Notes {
			fmt.Fprintf(&b, "    note: %s\n", note)
		}
	}

	b.WriteString(`
Opening it brings up a window that is already signed in. It listens on
this machine only, and there is no password here: one exists only when the
console runs in a container or on the network.

It reads the same kubeconfig your kubectl does, and contacts nothing at
all until you pick a cluster.
`)

	// The full path, not the bare command: the install directory is often not
	// on PATH. Explicit quotes rather than %q, which doubles a Windows path's
	// separators.
	fmt.Fprintf(&b, "\nA window:        \"%s\" open\n", inst.Binary)
	fmt.Fprintf(&b, "A password:      \"%s\" --reset-password  (container or network use only)\n", inst.Binary)
	fmt.Fprintf(&b, "To remove it:    \"%s\" --uninstall\n", inst.Binary)

	return strings.TrimRight(b.String(), "\n") + "\n"
}

// uninstalledHelp says what was taken away and, more importantly, what was not.
//
// The state directory is never removed by `--uninstall`, and this says so: it
// holds the credentials, the arrangement and the event archive, which cannot be
// regenerated, and a reinstall picks it all up. The Windows uninstaller keeps
// it by default too.
func uninstalledHelp(removed, notes []string) string {
	var b strings.Builder

	if len(removed) == 0 {
		b.WriteString("Nothing to remove: KubeTower was not installed here.\n")
	} else {
		b.WriteString("KubeTower is uninstalled.\n\n")
		for _, item := range removed {
			fmt.Fprintf(&b, "    removed   %s\n", item)
		}
	}

	if len(notes) > 0 {
		b.WriteString("\n")
		for _, note := range notes {
			fmt.Fprintf(&b, "    note: %s\n", note)
		}
	}

	fmt.Fprintf(&b, `
Your settings were left alone, in
%s

That directory holds the password hash, the cluster names and colors you
gave, your rail arrangement, the table layouts and the event archive.
Reinstalling picks all of it up again. Delete it by hand if you want
KubeTower to forget you.`, statefile.Dir())

	return strings.TrimRight(b.String(), "\n") + "\n"
}

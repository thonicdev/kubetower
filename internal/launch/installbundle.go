package launch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/thonicdev/kubetower/internal/web"
)

// Assembling a macOS application bundle, from any host.
//
// No build tag, so `go test` reaches it on every host, and so that a host that
// is not a Mac can produce the macOS .app (the disk-image workflow does, on
// Linux): on macOS the unit of installation is a bundle, not a bare binary.
//
// A bundle is a directory, a property list and an icon container, writable on
// any system. `--install` on a Mac and `--bundle=DIR` elsewhere both go through
// writeAppBundle, so the downloaded application and the self-installed one are
// the same bytes. That Finder opens the result is not tested here.

// bundleDirName is what the bundle directory is called.
//
// A constant because whatever packs the bundle for download (the disk-image
// workflow) spells it too, and a test holds the two together: the Finder shows
// this name, so a drift would install one application under two names.
const bundleDirName = "KubeTower.app"

// bundleExecutableName is `CFBundleExecutable` in the plist and the file that
// must exist at Contents/MacOS/ for the bundle to launch; install_darwin.go and
// the disk-image workflow place the binary at it.
const bundleExecutableName = "kubetower"

// writeAppBundle writes everything in a `KubeTower.app` except the executable.
//
// root is the bundle directory itself (…/KubeTower.app), not its parent.
//
// The executable is not this function's business, which lets it serve both
// callers: `--install` has already copied the binary in, and a build host
// places a cross-compiled one afterwards. Contents/MacOS is still created,
// since on a build host this runs before the binary is placed.
func writeAppBundle(root, release string) (entries, notes []string, err error) {
	contents := filepath.Join(root, "Contents")

	if err := os.MkdirAll(filepath.Join(contents, "MacOS"), 0o755); err != nil {
		return nil, nil, fmt.Errorf("making %s: %w", filepath.Join(contents, "MacOS"), err)
	}

	plist := filepath.Join(contents, "Info.plist")
	if err := writeFile(plist, infoPlist(release), 0o644); err != nil {
		// Fatal, unlike the icon: without Info.plist the directory is not an
		// application at all.
		return entries, notes, fmt.Errorf("writing %s: %w", plist, err)
	}
	entries = append(entries, "application   "+root)

	icon := filepath.Join(contents, "Resources", bundleExecutableName+".icns")
	png, readErr := web.AppIconPNG()
	if readErr != nil {
		return entries, append(notes, "no icon was installed: "+readErr.Error()), nil
	}
	icns, convErr := icnsFromPNG(png)
	if convErr != nil {
		return entries, append(notes, "no icon was installed: "+convErr.Error()), nil
	}
	if writeErr := writeFile(icon, icns, 0o644); writeErr != nil {
		return entries, append(notes, "no icon was installed: "+writeErr.Error()), nil
	}
	return append(entries, "icon          "+icon), notes, nil
}

// bundleFlag is how the build script asks for a bundle.
//
// `--bundle=DIR` with the value attached, because the argument loop is a switch
// over whole arguments and nothing else takes a value on the command line:
// configuration is the environment.
const bundleFlag = "--bundle="

// bundleTarget reads the directory out of `--bundle=DIR`, or "" when it is
// absent.
//
// The last one wins.
func bundleTarget(args []string) string {
	target := ""
	for _, arg := range args {
		if dir := strings.TrimPrefix(arg, bundleFlag); dir != arg {
			target = dir
		}
	}
	return target
}

// runWriteBundle assembles a bundle inside dir and says what it wrote.
//
// On stdout, not through report: the caller is a build script, and report
// shows a message box when no console is attached, which would hang a build.
func runWriteBundle(dir string) error {
	root := filepath.Join(dir, bundleDirName)

	entries, notes, err := writeAppBundle(root, version)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		fmt.Println("    " + entry)
	}
	for _, note := range notes {
		// A bundle without an icon still runs, but a release without one is
		// a defect, so any note fails this command; the caller decides.
		fmt.Println("    note: " + note)
	}
	if len(notes) > 0 {
		return fmt.Errorf("the bundle is incomplete: %s", strings.Join(notes, "; "))
	}

	fmt.Printf("\nPut the darwin executable at %s\n",
		filepath.Join(root, "Contents", "MacOS", bundleExecutableName))
	return nil
}

package updater

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Probe is what Detect may ask of the machine; every field may be nil.
type Probe struct {
	// Getenv reads one variable.
	Getenv func(string) string
	// Exists says whether a path exists.
	Exists func(string) bool
	// Owner names the package manager that owns a file: "deb", "rpm" or "".
	Owner func(path string) string
}

// Detect says how the executable at exe was installed, on goos.
//
//   - Windows: a folder holding uninstall.exe is the per-user setup's
//     (packaging/windows/kubetower.nsi writes exactly one); anything else, the zip.
//   - macOS: an executable at `<X>.app/Contents/MacOS/kubetower` is the
//     application bundle `<X>.app`; another name in a bundle is unknown;
//     anything else, the tar.gz.
//   - Linux: `$APPIMAGE` is the AppImage the runtime mounted us from, but only
//     when exe lies under `$APPDIR`, its mount: a tar.gz or a package started
//     from a terminal or an IDE that is itself an AppImage inherits both
//     variables, and replacing `$APPIMAGE` would overwrite that other
//     application. A binary dpkg or rpm owns is that package's; anything else,
//     the tar.gz.
func Detect(goos, exe string, probe Probe) Installation {
	if exe == "" {
		return Installation{}
	}
	switch goos {
	case "windows":
		if probe.Exists != nil && probe.Exists(filepath.Join(filepath.Dir(exe), "uninstall.exe")) {
			return Installation{Kind: WindowsSetup, Path: exe}
		}
		return Installation{Kind: WindowsZip, Path: exe}
	case "darwin":
		macos := filepath.Dir(exe)
		contents := filepath.Dir(macos)
		bundle := filepath.Dir(contents)
		if filepath.Base(macos) == "MacOS" && filepath.Base(contents) == "Contents" && strings.HasSuffix(bundle, ".app") {
			// Only KubeTower's own executable: a kubetower renamed inside
			// another bundle must not make the updater replace that bundle.
			if filepath.Base(exe) != "kubetower" {
				return Installation{}
			}
			return Installation{Kind: MacApp, Path: bundle}
		}
		return Installation{Kind: MacTar, Path: exe}
	case "linux":
		if probe.Getenv != nil {
			if image := probe.Getenv("APPIMAGE"); filepath.IsAbs(image) && inside(exe, probe.Getenv("APPDIR")) {
				return Installation{Kind: LinuxAppImage, Path: image}
			}
		}
		if probe.Owner != nil {
			switch probe.Owner(exe) {
			case "deb":
				return Installation{Kind: LinuxDeb, Path: exe}
			case "rpm":
				return Installation{Kind: LinuxRPM, Path: exe}
			case ownerUnanswered:
				// Maybe a package: never replaced as a tar.gz would be (#154).
				return Installation{Kind: Unknown, Path: exe}
			}
		}
		return Installation{Kind: LinuxTar, Path: exe}
	}
	return Installation{}
}

// inside says whether path lies strictly under dir, an absolute directory.
func inside(path, dir string) bool {
	if !filepath.IsAbs(dir) || !filepath.IsAbs(path) {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(path))
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// DetectRunning is Detect for this process.
func DetectRunning() Installation {
	exe, err := os.Executable()
	if err != nil {
		return Installation{}
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return Detect(runtime.GOOS, exe, Probe{
		// exe is resolved, so $APPDIR is too, or a symlinked mount would never hold it.
		Getenv: func(key string) string {
			value := os.Getenv(key)
			if key == "APPDIR" && value != "" {
				if resolved, err := filepath.EvalSymlinks(value); err == nil {
					return resolved
				}
			}
			return value
		},
		Exists: func(p string) bool { _, err := os.Stat(p); return err == nil },
		Owner:  owner,
	})
}

// ownerWait bounds each package-database question: a locked rpm database
// must not hold KubeTower's start (#154).
const ownerWait = 5 * time.Second

// ownerUnanswered is owner's answer when a package database did not answer
// within ownerWait: the binary may be a package's, so it is not a tar.gz.
const ownerUnanswered = "unanswered"

// owner asks dpkg, then rpm, whether they own path. A missing tool or a
// refusal is "nobody": the tar.gz path then applies. No answer within
// ownerWait, from either, and no yes from the other, is ownerUnanswered: a
// slow or locked database must neither hold the start nor let a package's
// file be replaced as a tarball's (#154).
func owner(path string) string {
	deb, debAnswered := ownedBy("dpkg-query", "-S", path)
	if deb {
		return "deb"
	}
	rpm, rpmAnswered := ownedBy("rpm", "-qf", path)
	if rpm {
		return "rpm"
	}
	if !debAnswered || !rpmAnswered {
		return ownerUnanswered
	}
	return ""
}

// ownedBy runs tool with flag, then `--` and path, so a path starting with a
// dash is never read as an option. answered is false only when the tool ran
// past ownerWait; a missing tool answers no.
var ownedBy = func(tool, flag, path string) (owned, answered bool) {
	found, err := systemTool(tool)
	if err != nil {
		return false, true
	}
	return runOwner(found, ownerArgs(flag, path), ownerWait)
}

// runOwner runs a package-database question within wait.
func runOwner(tool string, args []string, wait time.Duration) (owned, answered bool) {
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	err := exec.CommandContext(ctx, tool, args...).Run()
	if ctx.Err() != nil {
		return false, false
	}
	return err == nil, true
}

// ownerArgs is the question's arguments: the flag, `--`, the path.
func ownerArgs(flag, path string) []string { return []string{flag, "--", path} }

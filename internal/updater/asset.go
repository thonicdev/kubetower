// Package updater downloads, verifies and installs a newer KubeTower release
// on the desktop (#45). It acts only on the operator's two clicks, Download and
// Restart to update, and only where internal/update found a newer release.
//
// Nothing is trusted that the release's signed checksums.txt does not vouch
// for (internal/releasesig): the signature is checked first, then the asset's
// sha256 against its line, and a file that fails either is deleted before
// anything could run it.
//
// The console in a cluster and in a container never builds one: they are
// upgraded by their image tag.
package updater

import "fmt"

// Kind is how this KubeTower was installed, which decides the asset that
// replaces it and how.
type Kind string

const (
	// Unknown: no installation this package can update.
	Unknown Kind = ""
	// WindowsSetup: the per-user NSIS setup, in its own folder with uninstall.exe.
	WindowsSetup Kind = "windows-setup"
	// WindowsZip: kubetower.exe from the zip, wherever it was put.
	WindowsZip Kind = "windows-zip"
	// MacApp: KubeTower.app, from a disk image.
	MacApp Kind = "macos-app"
	// MacTar: the binary from a darwin tar.gz.
	MacTar Kind = "macos-tar"
	// LinuxAppImage: the AppImage, `$APPIMAGE`.
	LinuxAppImage Kind = "linux-appimage"
	// LinuxDeb: the .deb, its binary owned by dpkg.
	LinuxDeb Kind = "linux-deb"
	// LinuxRPM: the .rpm, its binary owned by rpm.
	LinuxRPM Kind = "linux-rpm"
	// LinuxTar: the binary from a linux tar.gz.
	LinuxTar Kind = "linux-tar"
)

// Installation is what Detect found: how, and the path an installer replaces
// (the executable, the AppImage file or the .app bundle).
type Installation struct {
	Kind Kind   `json:"kind"`
	Path string `json:"path,omitempty"`
}

// AssetName is the release asset that updates an installation, as release.yml
// names them. False where the release ships none for this platform: an
// AppImage, a .deb or an .rpm exists for x86_64 only, a Windows build for
// amd64 only.
func AssetName(version, goos, goarch string, kind Kind) (string, bool) {
	switch kind {
	case WindowsSetup:
		if goos == "windows" && goarch == "amd64" {
			return fmt.Sprintf("kubetower-%s-setup.exe", version), true
		}
	case WindowsZip:
		if goos == "windows" && goarch == "amd64" {
			return fmt.Sprintf("kubetower-%s-win-x64.zip", version), true
		}
	case MacApp:
		if goos == "darwin" {
			switch goarch {
			case "arm64":
				return fmt.Sprintf("kubetower-%s-arm64.dmg", version), true
			case "amd64":
				return fmt.Sprintf("kubetower-%s-x64.dmg", version), true
			}
		}
	case MacTar:
		if goos == "darwin" && (goarch == "arm64" || goarch == "amd64") {
			return fmt.Sprintf("kubetower_%s_darwin_%s.tar.gz", version, goarch), true
		}
	case LinuxAppImage:
		if goos == "linux" && goarch == "amd64" {
			return fmt.Sprintf("kubetower-%s-x86_64.AppImage", version), true
		}
	case LinuxDeb:
		if goos == "linux" && goarch == "amd64" {
			return fmt.Sprintf("kubetower-%s-amd64.deb", version), true
		}
	case LinuxRPM:
		if goos == "linux" && goarch == "amd64" {
			return fmt.Sprintf("kubetower-%s-x86_64.rpm", version), true
		}
	case LinuxTar:
		if goos == "linux" && (goarch == "arm64" || goarch == "amd64") {
			return fmt.Sprintf("kubetower_%s_linux_%s.tar.gz", version, goarch), true
		}
	}
	return "", false
}

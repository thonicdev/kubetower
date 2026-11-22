package web

import "io/fs"

// The application bitmap, for installers outside a browser. `internal/launch`
// cannot embed it (an embed pattern may not leave its package directory), and a
// second copy of the PNG would drift, so it is read from the FS that holds it.
// Windows takes its icon from the executable's resources instead.

// AppIconPNG returns the 512×512 application bitmap the PWA manifest offers. It
// fails only if the file was renamed; the installer then carries on with no icon
// rather than writing an empty file.
func AppIconPNG() ([]byte, error) {
	return fs.ReadFile(assets, "static/pwa/icon-512.png")
}

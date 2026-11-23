package updater

import (
	"archive/zip"
	"io"
	"os"
	"path"
	"path/filepath"
)

// writeFromZip writes the archive's root `name` to a new file beside dst and
// returns its path: the Windows zip holds `kubetower.exe` at its root.
func writeFromZip(dst, archive, name string) (string, error) {
	r, err := zip.OpenReader(archive)
	if err != nil {
		return "", err
	}
	defer r.Close()
	for _, f := range r.File {
		if path.Clean(f.Name) != name || !f.Mode().IsRegular() {
			continue
		}
		in, err := f.Open()
		if err != nil {
			return "", err
		}
		defer in.Close()
		out, err := os.CreateTemp(filepath.Dir(dst), ".kubetower-update-*.exe")
		if err != nil {
			return "", err
		}
		if _, err := io.Copy(out, io.LimitReader(in, MaxAsset)); err != nil {
			_ = out.Close()
			_ = os.Remove(out.Name())
			return "", err
		}
		if err := out.Close(); err != nil {
			_ = os.Remove(out.Name())
			return "", err
		}
		return out.Name(), nil
	}
	return "", ErrNoBinary
}

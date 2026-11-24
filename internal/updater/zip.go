package updater

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"io/fs"
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
		if f.UncompressedSize64 > MaxAsset {
			return "", fmt.Errorf("%s in the archive declares %d bytes", name, f.UncompressedSize64)
		}
		in, err := f.Open()
		if err != nil {
			return "", err
		}
		defer in.Close()
		out, err := os.CreateTemp(filepath.Dir(dst), ".kubetower-update-*.exe")
		if errors.Is(err, fs.ErrPermission) {
			// C:\Program Files: the update is installed by hand (Open folder).
			return "", fmt.Errorf("%w: %s", ErrNotWritable, filepath.Dir(dst))
		}
		if err != nil {
			return "", err
		}
		if _, err := io.Copy(out, in); err != nil {
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

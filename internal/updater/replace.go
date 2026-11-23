package updater

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
)

// replaceWith puts the bytes read from src at dst, executable, in one rename:
// a reader of dst sees the old file or the new one, never half of either. The
// new file is written beside dst, so the rename stays on one filesystem; any
// failure removes it and leaves dst as it was.
func replaceWith(dst string, src io.Reader) error {
	dir := filepath.Dir(dst)
	tmp, err := os.CreateTemp(dir, ".kubetower-update-*")
	if err != nil {
		return fmt.Errorf("%s is not writable: %w", dir, err)
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err := io.Copy(tmp, src); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return err
	}
	keep = true
	return nil
}

// replaceFile is replaceWith from a file.
func replaceFile(dst, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	return replaceWith(dst, in)
}

// ErrNoBinary: the archive holds no `kubetower` at its root.
var ErrNoBinary = errors.New("the archive holds no kubetower binary")

// replaceFromTarGz puts the archive's root `name` at dst. Only a regular file
// at the root is taken: release.yml archives the binary as `./kubetower`.
func replaceFromTarGz(dst, archive, name string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return ErrNoBinary
		}
		if err != nil {
			return err
		}
		if h.Typeflag != tar.TypeReg || path.Clean(h.Name) != name {
			continue
		}
		return replaceWith(dst, io.LimitReader(tr, MaxAsset))
	}
}

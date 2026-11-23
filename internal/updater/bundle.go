package updater

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrNotInPlace: the app cannot be replaced where it runs: it is translocated
// (run from a disk image or Downloads, read-only), or its folder is not
// writable. The disk image is opened in the Finder instead.
var ErrNotInPlace = errors.New("the app cannot be replaced where it runs")

// translocated says whether macOS runs the bundle from a read-only random
// path (App Translocation), which no update can reach.
func translocated(bundle string) bool {
	return strings.Contains(bundle, "/AppTranslocation/")
}

// swapBundle puts the bundle next at dst, where next sits beside dst on one
// volume: dst is renamed aside, next is renamed into its place, and the old
// one is removed. A failure in between puts the old bundle back.
func swapBundle(dst, next string) error {
	if filepath.Dir(dst) != filepath.Dir(next) {
		return fmt.Errorf("%s is not beside %s", next, dst)
	}
	aside := next + ".old"
	if err := os.Rename(dst, aside); err != nil {
		return err
	}
	if err := os.Rename(next, dst); err != nil {
		if back := os.Rename(aside, dst); back != nil {
			return fmt.Errorf("%w; and the old app could not be put back: %v", err, back)
		}
		return err
	}
	return os.RemoveAll(aside)
}

// writableBeside says whether a new bundle can be written beside bundle.
func writableBeside(bundle string) bool {
	probe, err := os.MkdirTemp(filepath.Dir(bundle), ".KubeTower-probe-")
	if err != nil {
		return false
	}
	_ = os.Remove(probe)
	return true
}

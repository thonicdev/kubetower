package launch

import (
	"testing"

	"github.com/thonicdev/kubetower/internal/updater"
)

// No updater is a nil interface, so internal/web registers no route over it.
func TestNoUpdaterIsANilInstaller(t *testing.T) {
	if got := setupInstaller(nil); got != nil {
		t.Fatalf("setupInstaller(nil) = %T, want a nil interface", got)
	}
	if got := setupInstaller(updater.New(updater.Options{Dir: t.TempDir()})); got == nil {
		t.Fatal("an updater was dropped")
	}
}

// What a restart ends is counted from the window's own sources; none is zero.
func TestTheRestartCountsTerminalsAndForwards(t *testing.T) {
	if terms, fwds := openWorkOf(nil)(); terms != 0 || fwds != 0 {
		t.Errorf("no workers: %d, %d", terms, fwds)
	}
	w := &workers{countForwards: func() int { return 3 }}
	if terms, fwds := openWorkOf(w)(); terms != 0 || fwds != 3 {
		t.Errorf("forwards only: %d, %d", terms, fwds)
	}
}

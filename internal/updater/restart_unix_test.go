//go:build !windows

package updater

import (
	"strings"
	"testing"
)

func TestTheUnixRestartStopsThroughTheConsolesOwnShutdown(t *testing.T) {
	was := stopper
	t.Cleanup(func() { stopper = was })
	stopped := 0
	SetStop(func() { stopped++ })
	// With a stop set, no SIGTERM: this test process would end on one.
	if err := stopSelf(); err != nil || stopped != 1 {
		t.Fatalf("%v, stopped %d times", err, stopped)
	}
}

// On quit (#122) nothing is started again: the next start runs the new build.
func TestOnQuitNothingStartsAgain(t *testing.T) {
	var started []string
	was := restart
	restart = func(path string, _ []string) error { started = append(started, path); return nil }
	t.Cleanup(func() { restart = was })
	if err := startAgainUnlessQuit(Plan{Quit: true, Installation: Installation{Path: "/opt/kt/kubetower"}}); err != nil || len(started) != 0 {
		t.Fatalf("on quit: %v %v", err, started)
	}
	if err := openAgainUnlessQuit(Plan{Quit: true}, "/Applications/KubeTower.app"); err != nil || len(started) != 0 {
		t.Fatalf("on quit, the bundle: %v %v", err, started)
	}
	if err := startAgainUnlessQuit(Plan{Installation: Installation{Path: "/opt/kt/kubetower"}}); err != nil {
		t.Fatal(err)
	}
	if err := openAgainUnlessQuit(Plan{}, "/Applications/KubeTower.app"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"/opt/kt/kubetower", "/usr/bin/open"}; strings.Join(started, ",") != strings.Join(want, ",") {
		t.Fatalf("started %v, want %v", started, want)
	}
}

//go:build !windows

package updater

import "testing"

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

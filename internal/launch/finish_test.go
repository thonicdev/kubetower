package launch

import (
	"context"
	"testing"

	"github.com/thonicdev/kubetower/internal/updater"
)

// Every quit stops the server and the shells first, then installs a verified
// update where the automatic setting says on quit (#122).
func TestEveryQuitStopsThenInstallsOnQuit(t *testing.T) {
	var steps []string
	p := &profile{onQuit: func(context.Context) { steps = append(steps, "install") }}
	p.finish(context.Background(), func() { steps = append(steps, "stop") })
	if len(steps) != 2 || steps[0] != "stop" || steps[1] != "install" {
		t.Fatalf("steps %v", steps)
	}
	// Where nothing installs, quitting only stops.
	steps = nil
	(&profile{}).finish(context.Background(), func() { steps = append(steps, "stop") })
	if len(steps) != 1 {
		t.Fatalf("steps %v", steps)
	}
}

// Nothing installs on quit where there is no installer, nor under
// --unattended, where a service's stop or restart is nobody quitting.
func TestNoInstallOnQuitWithoutAnInstallerOrUnattended(t *testing.T) {
	if installOnQuitOf(nil) != nil {
		t.Error("an install on quit with no installer")
	}
	u := updater.New(updater.Options{Version: "0.1.2", GOOS: "linux", GOARCH: "amd64", Dir: t.TempDir()})
	if installOnQuitOf(u) == nil {
		t.Error("no install on quit on the desktop")
	}
	was := unattended
	unattended = true
	t.Cleanup(func() { unattended = was })
	if installOnQuitOf(u) != nil {
		t.Error("an install on quit under --unattended")
	}
}

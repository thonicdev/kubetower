package launch

import (
	"context"
	"testing"

	"github.com/thonicdev/kubetower/internal/updater"
)

// Every quit stops the server and the shells first, then installs a verified
// update where the automatic setting says on quit (#122); nothing installs
// where there is no installer, nor under --unattended.
func TestEveryQuitStopsThenInstallsOnQuit(t *testing.T) {
	var steps []string
	was := installOnQuit
	installOnQuit = func(context.Context, *updater.Updater) { steps = append(steps, "install") }
	t.Cleanup(func() { installOnQuit = was })
	stop := func() { steps = append(steps, "stop") }
	u := updater.New(updater.Options{Version: "0.1.2", GOOS: "linux", GOARCH: "amd64", Dir: t.TempDir()})

	(&profile{installer: u}).finish(context.Background(), stop)
	if len(steps) != 2 || steps[0] != "stop" || steps[1] != "install" {
		t.Fatalf("desktop: %v", steps)
	}
	steps = nil
	(&profile{}).finish(context.Background(), stop)
	if len(steps) != 1 {
		t.Fatalf("no installer: %v", steps)
	}
	steps = nil
	wasUnattended := unattended
	unattended = true
	t.Cleanup(func() { unattended = wasUnattended })
	(&profile{installer: u}).finish(context.Background(), stop)
	if len(steps) != 1 {
		t.Fatalf("--unattended: %v", steps)
	}
}

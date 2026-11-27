package launch

import (
	"os"
	"strings"
	"testing"
)

// The update's stop is wired before the server serves (#178, C16): a request
// that installs at once would otherwise find no stop and refuse the helper.
// run is one long function, so its source is read for the order.
func TestTheUpdatesStopIsWiredBeforeTheServerServes(t *testing.T) {
	raw, err := os.ReadFile("run.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	set := strings.Index(src, "updater.SetStop(")
	serve := strings.Index(src, ":= serve(ln,")
	if set < 0 || serve < 0 {
		t.Fatalf("SetStop at %d, serve at %d: the wiring moved, check its order by hand", set, serve)
	}
	if set > serve {
		t.Error("updater.SetStop is called after the server starts serving")
	}
}

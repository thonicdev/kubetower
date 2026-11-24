package web

import (
	"testing"

	"github.com/thonicdev/kubetower/internal/api"
)

// toolWindowsFixture is the desktop's tool windows, drawn or not.
type toolWindowsFixture struct{ drawn bool }

func (f toolWindowsFixture) Available() bool { return f.drawn }

func (toolWindowsFixture) OpenToolWindow(string, string, string, bool) error { return nil }

// capabilities.toolWindow is true only where the console is in the desktop's
// native window, with forwards to show, and the API is handed the windows
// only where they can be: never under the server profile or with no forwards.
func TestTheToolWindowCapabilityFollowsTheNativeWindow(t *testing.T) {
	cases := []struct {
		name       string
		inCluster  bool
		replicated bool
		forwards   Forwards
		tools      api.ToolWindows
		offered    bool
		capability bool
	}{
		{"the desktop window drawn", false, false, forwardsFixture{}, toolWindowsFixture{drawn: true}, true, true},
		{"the browser fallback", false, false, forwardsFixture{}, toolWindowsFixture{}, true, false},
		{"no native window on this platform", false, false, forwardsFixture{}, nil, false, false},
		{"no forwarder", false, false, nil, toolWindowsFixture{drawn: true}, false, false},
		{"the server profile", true, false, forwardsFixture{}, toolWindowsFixture{drawn: true}, false, false},
		{"several replicas", false, true, forwardsFixture{}, toolWindowsFixture{drawn: true}, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			server := serverFor(t, c.inCluster)
			server.forwards = c.forwards
			server.replicated = c.replicated
			server.setup.ToolWindows = c.tools

			if got := server.toolWindows() != nil; got != c.offered {
				t.Errorf("the API is handed the tool windows: %v, want %v", got, c.offered)
			}
			if got := (apiProfile{server}).Capabilities(nil).ToolWindow; got != c.capability {
				t.Errorf("capabilities.toolWindow = %v, want %v", got, c.capability)
			}
		})
	}
}

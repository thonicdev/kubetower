package web

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/cluster"
	"sigs.k8s.io/yaml"
)

// The server profile's half that this package decides.
//
// `internal/api` holds the router and asks `Channels.Offers` which socket
// addresses to register; this file is the implementation that answers, and it is
// the only place that knows about the profile. Asserting the four addresses over
// in `internal/api` would prove that a fake said yes — so the addresses are
// checked there for what that package decides, and the *answer* is checked here.
// One decision, one place, one test.

func TestTheServerProfileOffersNoLocalShell(t *testing.T) {
	channels := apiChannels{serverFor(t, true)}

	for _, id := range []struct {
		id  api.ChannelID
		why string
	}{
		{api.ChannelTerminal, "a pseudo-terminal on the machine running the console, which in a pod is the pod"},
		{api.ChannelNodeShell, "a shell on a node, opened with the pod's credentials"},
		{api.ChannelAssistant, "the assistant is absent, not disabled"},
	} {
		if channels.Offers(id.id) {
			t.Errorf("the server profile offers %v, and it must not: %s.\n"+
				"Offers is asked once when the router is built, so a true here is an "+
				"address that exists rather than a control that is merely drawn.", id.id, id.why)
		}
	}

	if !channels.Offers(api.ChannelExec) {
		t.Error("the server profile does not offer exec, and it should.\n" +
			"Exec reaches the kubelet through client-go's remotecommand and touches no " +
			"pseudo-terminal on this machine; what bounds it is the ServiceAccount's " +
			"pods/exec, which a deployment grants or withholds on purpose.")
	}
}

// TestTheDesktopStillOffersEverything is the half that protects the person who
// did not ask for any of this: the profile removes, it never adds, so a build
// without the flag answers exactly as it did before.
func TestTheDesktopStillOffersEverything(t *testing.T) {
	channels := apiChannels{serverFor(t, false)}

	for _, id := range []api.ChannelID{
		api.ChannelTerminal, api.ChannelExec, api.ChannelNodeShell, api.ChannelAssistant,
	} {
		if !channels.Offers(id) {
			t.Errorf("the desktop stopped offering %v", id)
		}
	}
}

// TestTheProfileSaysWhatItRegistered checks the cosmetic half agrees with the
// structural one.
//
// The capability fields are not the enforcement — `Capabilities`' own header says
// *"hiding a control is cosmetic"* — but a front end told it may open a shell,
// against a router with no shell, draws a control that answers 404. They must say
// the same thing, and this is what asserts they do.
func TestTheProfileSaysWhatItRegistered(t *testing.T) {
	caps := apiProfile{serverFor(t, true)}.Capabilities(nil)

	if caps.LocalShell {
		t.Error("capabilities say localShell under the server profile, and no such address is registered")
	}
	if caps.Assistant {
		t.Error("capabilities say assistant under the server profile, which must be absent")
	}
	if caps.NodeShell {
		t.Error("capabilities say nodeShell under the server profile, and no such address is registered")
	}
	if caps.Mode != "shared" {
		t.Errorf("mode = %q under the server profile, want %q", caps.Mode, "shared")
	}
	// And whatever it is, it is a value the contract declares: the contract
	// says desktop|container|shared, and the conformance test's fake profile
	// only ever reports the desktop, so nothing else compares the two.
	declared := contractModes(t)
	for _, c := range []struct{ who, mode string }{{"server profile", caps.Mode}} {
		if !declared[c.mode] {
			t.Errorf("%s reports mode %q, which api/openapi.yaml does not declare (%v)", c.who, c.mode, declared)
		}
	}

	// And the desktop's own answer is untouched: the literal `"container"` on a
	// laptop is wrong, and deliberately left alone - see
	// the comment in apimount.go.
	desktop := apiProfile{serverFor(t, false)}.Capabilities(nil)
	if !declared[desktop.Mode] {
		t.Errorf("the desktop reports mode %q, which api/openapi.yaml does not declare (%v)", desktop.Mode, declared)
	}
	if desktop.Mode == "shared" {
		t.Error("the desktop reports the server profile's mode, so this change did not stay on its side of the flag")
	}
	// The twelve preference writes are not registered under the
	// profile, on one replica as on several, so the page must not be told it
	// may arrange the rail. One replica is not `replicated`: the settings
	// writes stay, and the settings page keys on that member, not this one.
	if caps.Preferences {
		t.Error("capabilities say preferences under the server profile, and no preference write is registered")
	}
	if caps.Replicated || desktop.Replicated {
		t.Error("capabilities say replicated on a console nobody configured as several replicas")
	}
	if !desktop.Preferences {
		t.Error("the desktop lost preferences, and the profile is only supposed to remove them under the flag")
	}
	if !desktop.LocalShell {
		t.Error("the desktop lost localShell, and the profile is only supposed to remove it under the flag")
	}
}

// shellsPresent is a terminal layer that offers everything, so that what these
// tests measure is the profile rather than a build assembled without one.
type shellsPresent struct{ Terminals }

func (shellsPresent) HasClaude() bool                { return true }
func (shellsPresent) PTYSupported() bool             { return true }
func (shellsPresent) DesktopTerminalSupported() bool { return false }
func (shellsPresent) NodeShellImage() string         { return "alpine:3.24" }
func (shellsPresent) ReadOnlyFence() (string, string) {
	return "intact", "measured at start-up: the overrides refused a mutating verb."
}

func serverFor(t *testing.T, inCluster bool) *Server {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, []byte(`apiVersion: v1
kind: Config
current-context: prod
clusters:
- name: remote
  cluster: {server: https://127.0.0.1:1}
contexts:
- name: prod
  context: {cluster: remote, user: none}
users:
- name: none
  user: {token: none}
`), 0o600); err != nil {
		t.Fatalf("writing the kubeconfig: %v", err)
	}
	manager, err := cluster.NewManager(path)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	server, err := New(Options{Clusters: manager, Terminal: shellsPresent{}, InCluster: inCluster})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return server
}

// TestTheNodeShellSectionIsAbsentUnderTheProfile is the other half of the
// same property: `available` is what tells the Settings page to leave the
// node-shell section off, so it must read "is the channel offered" rather than
// "is there a terminal layer".
func TestTheNodeShellSectionIsAbsentUnderTheProfile(t *testing.T) {
	if got := settingsOf(serverFor(t, true)).NodeShell; got.Available || got.Enabled {
		t.Errorf("under the server profile the node-shell settings say available=%v enabled=%v; the channel is not registered, so the section must not be drawn",
			got.Available, got.Enabled)
	}
	if got := settingsOf(serverFor(t, false)).NodeShell; !got.Available {
		t.Error("the desktop lost its node-shell settings section; the profile is only supposed to remove it under the flag")
	}
}

// contractModes reads the values Capabilities.mode may take from the contract
// itself, so this test cannot agree with the code by being copied from it.
func contractModes(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "api", "openapi.yaml"))
	if err != nil {
		t.Fatalf("reading the contract: %v", err)
	}
	var spec struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Enum []string `json:"enum"`
				} `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("parsing the contract: %v", err)
	}
	enum := spec.Components.Schemas["Capabilities"].Properties["mode"].Enum
	if len(enum) == 0 {
		t.Fatal("the contract declares no enum for Capabilities.mode, so there is nothing to check against")
	}
	out := map[string]bool{}
	for _, v := range enum {
		out[v] = true
	}
	return out
}

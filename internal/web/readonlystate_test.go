package web

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/cluster"
)

// fenceAs is a terminal layer whose read-only fence was measured as `state`.
// Only ReadOnlyFence is real; Capabilities is not called through it here.
type fenceAs struct {
	Terminals
	state, detail string
}

func (f fenceAs) ReadOnlyFence() (string, string) { return f.state, f.detail }

// prodManager is a cluster manager over one context, `prod`.
func prodManager(t *testing.T) *cluster.Manager {
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
	return manager
}

// readOnlyServer is a console over `prod`, marked read-only, with the terminal
// layer given and the markings that matched nothing given.
func readOnlyServer(t *testing.T, terminal Terminals, unmatched []string) *Server {
	t.Helper()
	opts := Options{Clusters: prodManager(t), ReadOnlyUnmatched: unmatched,
		ReadOnly: func(name string) bool { return name == "prod" }}
	if terminal != nil {
		opts.Terminal = terminal
	}
	server, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return server
}

// Three states reach the client, each its own word and its own sentence.
// `Capabilities.readOnly` must carry the measurement and not just two booleans:
// otherwise a fence that could not be probed reads exactly
// as one that is not on.
func TestTheFenceReachesTheClientInThreeStates(t *testing.T) {
	for _, c := range []struct {
		name     string
		terminal Terminals
		word     string
		measured bool
		says     string
	}{
		{"intact", fenceAs{state: "intact", detail: "each refused a mutating verb when asked."},
			api.FenceOn, true, "refused a mutating verb"},
		{"absent", fenceAs{state: "absent", detail: "kubectl is NOT behind the read-only overrides."},
			api.FenceOff, true, "NOT behind"},
		{"unmeasured", fenceAs{state: "unmeasured", detail: "the probe resolved neither kubectl nor helm."},
			api.FenceUnknown, false, "neither kubectl nor helm"},
		// No terminal layer: nothing was probed, which is not an override found
		// missing.
		{"no terminal layer", nil, api.FenceUnknown, false, "no read-only override was probed"},
		// A word this console does not know is not read as either side.
		{"an unknown word", fenceAs{state: "sideways", detail: "?"},
			api.FenceUnknown, false, `"sideways"`},
	} {
		t.Run(c.name, func(t *testing.T) {
			state := apiProfile{readOnlyServer(t, c.terminal, nil)}.readOnly()
			if state.Fence != c.word {
				t.Errorf("fence is %q, want %q: %+v", state.Fence, c.word, state)
			}
			if state.Measured != c.measured {
				t.Errorf("measured is %t, want %t: %+v", state.Measured, c.measured, state)
			}
			if !strings.Contains(state.Detail, c.says) {
				t.Errorf("the sentence does not say %q: %q", c.says, state.Detail)
			}
			// The profile default is a separate fact, and the measurement must
			// not move it: `prod` is the only context and it is marked.
			if !state.WritesRefused {
				t.Errorf("writesRefused moved with the fence: %+v", state)
			}
		})
	}
}

// The KT_READONLY_CLUSTERS entries that named no context reach the client:
// `Unmatched()` must have a caller, or a typo is said once in the boot log
// and the person goes on believing that cluster protected.
func TestTheMarkingsThatMatchedNothingReachTheClient(t *testing.T) {
	fence := fenceAs{state: "intact", detail: "ok"}

	state := apiProfile{readOnlyServer(t, fence, []string{"prdo", "staging"})}.readOnly()
	if !slices.Equal(state.Unmatched, []string{"prdo", "staging"}) {
		t.Fatalf("unmatched is %v, want [prdo staging]", state.Unmatched)
	}

	if state := (apiProfile{readOnlyServer(t, fence, nil)}).readOnly(); state.Unmatched != nil {
		t.Errorf("every marking matched and unmatched is %v rather than absent", state.Unmatched)
	}
}

// And through the real reading: cluster.ReadOnlyMarkings is what run.go hands
// over, so a typo in the variable is what Unmatched carries.
func TestAMarkingTypoIsWhatUnmatchedCarries(t *testing.T) {
	markings := prodManager(t).ReadOnlyMarkings("prod,prdo")
	if got := markings.Unmatched(); !slices.Equal(got, []string{"prdo"}) {
		t.Fatalf("Unmatched() = %v, want [prdo]", got)
	}
}

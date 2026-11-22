package rightsizing

import (
	"errors"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/extension"
)

// The window the core reads is keyed extension.ContainerKey, and the engine's
// history by HistoryKey: the two must name the same container, or every series
// is read and none matches, which draws as a page with no window.
func TestAWindowFromTheCoreLandsUnderTheEngineKey(t *testing.T) {
	key := extension.ContainerKey("prod", "web-0", "app")
	h := HistoryFromWindow(extension.Window{State: extension.WindowRead, Detail: "Prometheus, 6h at 2m", Complete: true,
		Containers: map[string]extension.ContainerWindow{
			key:         {CPU: []extension.Sample{{T: 1, V: 100}, {T: 2, V: 120}}},
			"not-a-key": {CPU: []extension.Sample{{T: 1, V: 1}}},
		}})
	if !h.State.OK || h.State.Detail != "Prometheus, 6h at 2m" {
		t.Errorf("state = %+v", h.State)
	}
	if len(h.Containers) != 1 {
		t.Fatalf("%d containers, want the one well-formed key", len(h.Containers))
	}
	if got := HistoryKey(h.Containers[0].Namespace, h.Containers[0].Pod, h.Containers[0].Container); got != key {
		t.Errorf("the engine keys it %q, the core %q", got, key)
	}
	if len(h.Containers[0].CPU) != 2 || h.Containers[0].CPU[1].V != 120 {
		t.Errorf("series = %+v", h.Containers[0].CPU)
	}
}

// Every state without a window says what it costs.
func TestEveryStateWithoutAWindowSaysWhatItCosts(t *testing.T) {
	for _, state := range []extension.WindowState{extension.WindowNoPrometheus, extension.WindowUnreadable,
		extension.WindowEmpty} {
		h := HistoryFromWindow(extension.Window{State: state, Detail: "because"})
		if h.State.OK || !strings.HasPrefix(h.State.Detail, "because") || !strings.Contains(h.State.Detail, "no P95") {
			t.Errorf("%s: %+v", state, h.State)
		}
	}
	// Read, and nothing in it: not a window.
	if h := HistoryFromWindow(extension.Window{State: extension.WindowRead}); h.State.OK {
		t.Errorf("an empty read window counts as a window: %+v", h.State)
	}
}

// With no usage, Assemble stops there, and still names the window's state.
func TestAssembleStopsAtNoUsageAndKeepsTheHistory(t *testing.T) {
	r := Assemble(Inputs{UsageErr: errors.New("no metrics-server"),
		History: History{State: Availability{Detail: "no Prometheus"}}})
	if r.Usage.OK || r.Usage.Detail != "no metrics-server" || r.History.Detail != "no Prometheus" || r.Examined != 0 {
		t.Errorf("report = %+v", r)
	}
	if Measurable(map[string]Usage{}, nil) || Measurable(map[string]Usage{"a/b/c": {}}, errors.New("x")) ||
		!Measurable(map[string]Usage{"a/b/c": {}}, nil) {
		t.Error("Measurable disagrees with Assemble's own early return")
	}
}

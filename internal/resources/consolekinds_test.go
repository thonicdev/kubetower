package resources

import (
	"testing"

	"github.com/thonicdev/kubetower/internal/consolekinds"
)

// The console's address for a kind (internal/consolekinds) spells its own
// table of the thirty-six, because it is a leaf the extension contract
// delegates to. This holds it to the kinds this package actually lists, in the
// group and resource each list is asked with: a kind added to kinds.go and not
// there would be linked by an extra as `plural.group`, an address the menu does
// not use.
func TestTheTypedKindsAreTheConsolesOwn(t *testing.T) {
	want := map[consolekinds.Kind]bool{}
	for _, d := range Kinds() {
		q, ok := d.ListQuestion()
		if !ok {
			t.Fatalf("%s has no list question", d.Kind)
		}
		want[consolekinds.Kind{Group: q.Attrs.Group, Resource: q.Attrs.Resource}] = true
		if q.Attrs.Resource != d.Kind {
			t.Errorf("%s is listed as %s: its address would not be its plural", d.Kind, q.Attrs.Resource)
		}
	}
	got := map[consolekinds.Kind]bool{}
	for _, k := range consolekinds.Typed() {
		if got[k] {
			t.Errorf("%+v is in consolekinds twice", k)
		}
		got[k] = true
		if !want[k] {
			t.Errorf("consolekinds types %+v, which kinds.go does not list", k)
		}
	}
	for k := range want {
		if !got[k] {
			t.Errorf("kinds.go lists %+v, which consolekinds does not type", k)
		}
	}
}

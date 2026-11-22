package resources

import (
	"slices"
	"testing"
)

// The menu's jump box matches kubectl's names for a kind,
// and they come from the table Lookup resolves, so the two cannot disagree.
func TestAliasesOfIsEveryNameLookupAnswersTo(t *testing.T) {
	got := AliasesOf("services")
	if want := []string{"service", "services", "svc"}; !slices.Equal(got, want) {
		t.Errorf("AliasesOf(services) = %v, want %v", got, want)
	}
	for _, d := range Kinds() {
		for _, name := range AliasesOf(d.Kind) {
			if found, ok := Lookup(name); !ok || found.Kind != d.Kind {
				t.Errorf("%s carries %q, which Lookup resolves to %q", d.Kind, name, found.Kind)
			}
		}
	}
	if AliasesOf("widgets") != nil {
		t.Error("a kind not served here has aliases")
	}
	// A copy: changing it does not reach the next caller.
	got[0] = "changed"
	if AliasesOf("services")[0] != "service" {
		t.Error("AliasesOf hands out its index")
	}
}

package extras

import (
	"slices"
	"strings"
	"testing"
)

// The catalog is alphabetical by label, case-insensitively, whatever order
// it was registered in and whatever the keys say. The labels here sort
// differently by key (key "a" has the last label), and one starts lower case.
func TestTheCatalogueIsSortedByLabelNotByKeyOrRegistration(t *testing.T) {
	saved := Catalogue()
	t.Cleanup(func() {
		Reset()
		Register(saved...)
	})
	Reset()
	Register(
		Extra{Key: "a", Label: "Zeta"},
		Extra{Key: "b", Label: "beta"},
		Extra{Key: "c", Label: "Alpha"},
		Extra{Key: "d", Label: "KubeCron"},
		Extra{Key: "e", Label: "KubeAdjust"},
	)

	var labels, keys []string
	for _, e := range Catalogue() {
		labels = append(labels, e.Label)
		keys = append(keys, e.Key)
	}
	if want := []string{"Alpha", "beta", "KubeAdjust", "KubeCron", "Zeta"}; !slices.Equal(labels, want) {
		t.Errorf("labels = %v, want %v", labels, want)
	}
	if want := []string{"c", "b", "e", "d", "a"}; !slices.Equal(keys, want) {
		t.Errorf("keys = %v, want %v", keys, want)
	}
	state := New(t.TempDir(), testClusters, nil).State("alpha")
	for i, s := range state {
		if !strings.EqualFold(s.Extra.Label, Catalogue()[i].Label) {
			t.Errorf("Settings row %d is %q, the menu has %q", i, s.Extra.Label, Catalogue()[i].Label)
		}
	}
}

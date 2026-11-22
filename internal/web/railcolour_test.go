package web

import (
	"context"
	"testing"

	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/nameplates"
)

// TestEveryClusterIsSentAPaletteColour checks on
// the server: a cluster with no stored color receives a palette key, and one
// with a stored color receives that one.
//
// Through the real `apiClusters.List` over the rail fixture's kubeconfig, which
// is what `GET /api/clusters` answers with. `internal/api`'s members test proves
// the contract type can carry `colour`; only this package can prove the stores
// fill it, and an always-present member that is filled with "" would conform to
// nothing the client could draw.
func TestEveryClusterIsSentAPaletteColour(t *testing.T) {
	prefs, server := prefsUnder(t)

	list := func() map[string]api.Cluster {
		t.Helper()
		clusters, fail := apiClusters{server}.List(context.Background())
		if fail != nil {
			t.Fatalf("the cluster list failed: %+v", fail)
		}
		byName := map[string]api.Cluster{}
		for _, cluster := range clusters {
			byName[cluster.Name] = cluster
		}
		return byName
	}

	before := list()
	if len(before) < 2 {
		t.Fatalf("the fixture lists %d clusters, too few to say anything", len(before))
	}
	for name, cluster := range before {
		if _, ok := nameplates.ColourOf(cluster.Colour); !ok {
			t.Errorf("%s was sent color %q, which is not a palette key", name, cluster.Colour)
		}
		if cluster.Colour != nameplates.AutoColour(name) {
			t.Errorf("%s has no stored color and was sent %q, not its automatic %q",
				name, cluster.Colour, nameplates.AutoColour(name))
		}
		if cluster.Initials == "" {
			t.Errorf("%s was sent no initials, and the tile draws nothing else", name)
		}
	}

	// A chosen color wins over the automatic one. Chosen to differ from it, so
	// the assertion cannot pass by coincidence.
	chosen := "teal"
	if nameplates.AutoColour("mi-k3s") == chosen {
		chosen = "pink-pale"
	}
	if err := prefs.SetRailNameplate(api.RailNameplateWrite{Cluster: "mi-k3s", Colour: &chosen}); err != nil {
		t.Fatalf("storing a color: %v", err)
	}
	if got := list()["mi-k3s"].Colour; got != chosen {
		t.Errorf("mi-k3s was given %q and is sent %q", chosen, got)
	}

	// And clearing it is the automatic color again, not an absent member.
	empty := ""
	if err := prefs.SetRailNameplate(api.RailNameplateWrite{Cluster: "mi-k3s", Colour: &empty}); err != nil {
		t.Fatalf("clearing the color: %v", err)
	}
	if got := list()["mi-k3s"].Colour; got != nameplates.AutoColour("mi-k3s") {
		t.Errorf("after clearing, mi-k3s is sent %q, not its automatic color", got)
	}
}

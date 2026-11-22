package nameplates

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// The palette reaches the client, so the automatic color has to be one of its
// keys and the contract has to list them.

// TestAutoColourIsAKeyOnThePalette pins the half the server owns: a context
// nobody colored is drawn in a palette key, never in a hue of its own, which
// the contract could only send as "absent".
func TestAutoColourIsAKeyOnThePalette(t *testing.T) {
	names := []string{
		"", "docker-desktop", "kubernetes-admin@kubernetes", "mi-k3s", "SHOP-EKS-1",
		"SHOP-EKS-1_OWNER",
		"arn:aws:eks:eu-west-3:123456789012:cluster/a-very-long-production-cluster-name-for-layout",
		"контекст", "集群-1",
	}
	for i := 0; i < 200; i++ {
		names = append(names, "ctx-"+strings.Repeat("x", i%7)+string(rune('a'+i%26))+string(rune('0'+i%10)))
	}

	seen := map[string]bool{}
	for _, name := range names {
		key := AutoColour(name)
		if _, ok := ColourOf(key); !ok {
			t.Fatalf("AutoColour(%q) = %q, which is not a key on the palette", name, key)
		}
		// Stable: the context name is the only input, so a reload, a restart
		// and another machine all draw the same tile.
		if again := AutoColour(name); again != key {
			t.Fatalf("AutoColour(%q) answered %q and then %q", name, key, again)
		}
		seen[key] = true
	}
	// Spread, not one swatch for everybody: two hundred names reach most of the
	// sixteen. A derivation that collapsed to one key would pass the two checks
	// above and draw a rail of identical squares.
	if len(seen) < len(palette)/2 {
		t.Errorf("two hundred context names reached only %d of %d swatches", len(seen), len(palette))
	}
}

// TestThePaletteIsTheContractsEnum reads `ClusterColour` out of
// `api/openapi.yaml` at run time and compares it with the palette, both ways.
//
// The contract is hand-written and this table is Go, so they are two copies of
// one list. A key added here and not there is a value the client's type does
// not admit and its color map has no token for; a key added there and not
// here is a swatch the client offers and the server refuses.
func TestThePaletteIsTheContractsEnum(t *testing.T) {
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatalf("the contract could not be read: %v", err)
	}
	converted, err := yaml.YAMLToJSON(raw)
	if err != nil {
		t.Fatalf("the contract is not YAML: %v", err)
	}
	var spec struct {
		Components struct {
			Schemas map[string]struct {
				Enum []string `json:"enum"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(converted, &spec); err != nil {
		t.Fatalf("the contract is not an object: %v", err)
	}
	declared := spec.Components.Schemas["ClusterColour"].Enum
	if len(declared) == 0 {
		t.Fatal("api/openapi.yaml declares no ClusterColour enum")
	}

	var keys []string
	for _, colour := range Palette() {
		keys = append(keys, colour.Key)
	}
	for _, key := range keys {
		if !slices.Contains(declared, key) {
			t.Errorf("the palette has %q and the contract's ClusterColour does not", key)
		}
	}
	for _, key := range declared {
		if !slices.Contains(keys, key) {
			t.Errorf("the contract's ClusterColour has %q and the palette does not", key)
		}
	}
}

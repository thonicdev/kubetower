package customresources

import (
	"slices"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// A CRD whose `names.kind` is DeployHook must not be labeled "Deployhooks" -
// its plural, capitalized - in the menu and on its page. The label is the
// kind, pluralized, from what the API server says.
func TestACRDIsLabelledFromItsKind(t *testing.T) {
	object := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apiextensions.k8s.io/v1",
		"kind":       "CustomResourceDefinition",
		"metadata":   map[string]any{"name": "deployhooks.example.io"},
		"spec": map[string]any{
			"group": "example.io",
			"scope": "Namespaced",
			"names": map[string]any{
				"kind":       "DeployHook",
				"listKind":   "DeployHookList",
				"plural":     "deployhooks",
				"singular":   "deployhook",
				"shortNames": []any{"dh"},
			},
			"versions": []any{
				map[string]any{"name": "v1", "served": true, "storage": true},
			},
		},
	}}

	d, ok := definitionFrom(object)
	if !ok {
		t.Fatal("the definition was refused")
	}
	if got := d.Title(); got != "DeployHooks" {
		t.Errorf("Title() = %q, want %q", got, "DeployHooks")
	}
	// The names the jump box matches: kubectl's, short name included.
	for _, name := range []string{"deployhooks", "deployhook", "dh"} {
		if !slices.Contains(d.Aliases(), name) {
			t.Errorf("Aliases() = %v, missing %q", d.Aliases(), name)
		}
	}
}

func TestAKindIsPluralisedByItsPluralsOwnEnding(t *testing.T) {
	for _, c := range []struct{ kind, plural, want string }{
		{"DeployHook", "deployhooks", "DeployHooks"},
		{"Certificate", "certificates", "Certificates"},
		{"NetworkPolicy", "networkpolicies", "NetworkPolicies"},
		{"IngressRoute", "ingressroutes", "IngressRoutes"},
		{"Ingress", "ingresses", "Ingresses"},
		{"Prometheus", "prometheuses", "Prometheuses"},
		// The plural is not the kind's: the author's plural, capitalized.
		{"Foo", "bars", "Bars"},
		{"Person", "people", "People"},
		{"", "widgets", "Widgets"},
		// A kind written all lower case is still a heading.
		{"widget", "widgets", "Widgets"},
		{"policy", "policies", "Policies"},
	} {
		if got := kindPlural(c.kind, c.plural); got != c.want {
			t.Errorf("kindPlural(%q, %q) = %q, want %q", c.kind, c.plural, got, c.want)
		}
	}
}

// Titles keep their kind's capitals, so the menu orders them as a reader does,
// not by byte: IPAddressPools after Ingresses, in both orders the package keeps.
func TestTitlesAreOrderedIgnoringCase(t *testing.T) {
	pools := Definition{Group: "example.io", Kind: "IPAddressPool", Plural: "ipaddresspools"}
	ingresses := Definition{Group: "example.io", Kind: "Ingress", Plural: "ingresses"}

	if !projectOrder(ingresses, pools) || projectOrder(pools, ingresses) {
		t.Errorf("projectOrder put %q before %q", pools.Title(), ingresses.Title())
	}
	if !foldedLess("Ingress", "IPAddressPool") || foldedLess("IPAddressPool", "Ingress") {
		t.Error("foldedLess orders by byte, not as a reader does")
	}
	// A tie on the folded name is still an order.
	if foldedLess("abc", "ABC") == foldedLess("ABC", "abc") {
		t.Error("foldedLess is not total on names that differ only in case")
	}
}

// The catalog's order is the menu's, and it follows the labels as they read:
// "IngressClasses" before "Ingresses" before "IPAddressPools", whatever the
// kinds' byte order says.
func TestTheCatalogueIsOrderedByItsLabels(t *testing.T) {
	crd := func(kind, plural string) any {
		object := crdFor(plural).(map[string]any)
		object["spec"].(map[string]any)["names"].(map[string]any)["kind"] = kind
		return object
	}
	server := &crdServer{items: []any{
		crd("IPAddressPool", "ipaddresspools"),
		crd("Ingress", "ingresses"),
		crd("IngressClass", "ingressclasses"),
	}}
	registry, client := server.registry(t)

	definitions, err := registry.Definitions(t.Context(), client)
	if err != nil {
		t.Fatalf("fetching the catalog: %v", err)
	}
	var titles []string
	for _, d := range definitions {
		titles = append(titles, d.Title())
	}
	if want := []string{"IngressClasses", "Ingresses", "IPAddressPools"}; !slices.Equal(titles, want) {
		t.Errorf("titles in order %v, want %v", titles, want)
	}
}

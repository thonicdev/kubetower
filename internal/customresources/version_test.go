package customresources

// The list and the drawer read one CRD at one version.
//
// The list resolves a CRD's version here, from the definition itself. The
// drawer, the YAML view and edit resolve it through `internal/kindref`, whose
// mapper is built from discovery, and discovery's answer is the highest served
// version in Kube-aware order. Preferring the storage version here would, on a
// CRD part-way through a migration — `v1beta1` still the storage version, `v1`
// already served — list the row at `v1beta1`, with v1beta1's printer columns,
// and open it at `v1`, with nothing on screen saying so.
//
// These tests hold the two halves to one answer by asking both of them, rather
// than by asserting the list's choice alone: a list that agrees with a fixed
// string is a list that will drift the day the drawer changes.

import (
	"context"
	"sort"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kubeversion "k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/kindref"
)

// gizmoCRD is the ordinary shape of a CRD mid-migration: the operator has
// started serving `v1`, but the objects in etcd are still written as `v1beta1`
// until a storage migration has run. Each version declares a column the other
// does not, so a table can be read for which version it came from.
const gizmoCRD = `{
  "apiVersion": "apiextensions.k8s.io/v1",
  "kind": "CustomResourceDefinition",
  "metadata": {"name": "gizmos.example.io"},
  "spec": {
    "group": "example.io",
    "scope": "Namespaced",
    "names": {"kind": "Gizmo", "listKind": "GizmoList", "plural": "gizmos", "singular": "gizmo"},
    "versions": [
      {
        "name": "v1beta1",
        "served": true,
        "storage": true,
        "additionalPrinterColumns": [
          {"name": "Legacy", "type": "string", "jsonPath": ".spec.legacy"}
        ]
      },
      {
        "name": "v1",
        "served": true,
        "storage": false,
        "additionalPrinterColumns": [
          {"name": "Ready", "type": "string", "jsonPath": ".status.ready"}
        ]
      }
    ]
  }
}`

// gizmoCRDWith is gizmoCRD with its versions replaced, for the cases below that
// differ from it only there.
func gizmoCRDWith(versions string) string {
	start := strings.Index(gizmoCRD, `"versions": [`)
	return gizmoCRD[:start] + `"versions": ` + versions + "\n  }\n}"
}

// publishedAsTheAPIServerDoes is the discovery document the API server serves
// for these CRDs, built the way kube-apiserver builds it rather than by hand.
//
// Read in k8s.io/apiextensions-apiserver v0.37.1 (the release matching this
// module's k8s.io/apimachinery v0.37.1),
// pkg/apiserver/customresource_discovery_controller.go: every served version of
// every CRD in a group, and no unserved one, enters that group's list (line
// 116); the list is sorted by `sortGroupDiscoveryByKubeAwareVersion` (lines 242,
// 283-287), which is `version.CompareKubeAwareVersionStrings(a, b) > 0`,
// highest first; and the group's PreferredVersion is the first entry after that
// sort (line 249). Deprecation plays no part in it. Aggregated discovery orders
// a CRD's versions the same way: k8s.io/apiserver v0.37.1,
// pkg/endpoints/discovery/aggregated/handler.go line 455, after a priority every
// CRD version shares.
//
// The fake discovery client takes the first list it is given as the preferred
// version and keeps the rest in order, so handing it one list per version, in
// that order, reproduces the API server's answer. All the fixtures here are in
// one group, which is what makes the group-wide preferred version reachable.
func publishedAsTheAPIServerDoes(t *testing.T, bodies ...string) []*metav1.APIResourceList {
	t.Helper()
	group := ""
	byVersion := map[string][]metav1.APIResource{}
	for _, body := range bodies {
		crd := object(t, body)
		spec, _, _ := unstructured.NestedMap(crd.Object, "spec")
		group = nestedString(spec, "group")
		resource := metav1.APIResource{
			Name:         nestedString(spec, "names", "plural"),
			SingularName: nestedString(spec, "names", "singular"),
			Kind:         nestedString(spec, "names", "kind"),
			Namespaced:   nestedString(spec, "scope") == "Namespaced",
			Verbs:        metav1.Verbs{"get", "list"},
		}
		for _, v := range versionsOf(spec) {
			if v.served {
				byVersion[v.name] = append(byVersion[v.name], resource)
			}
		}
	}

	served := make([]string, 0, len(byVersion))
	for name := range byVersion {
		served = append(served, name)
	}
	sort.Slice(served, func(i, j int) bool {
		return kubeversion.CompareKubeAwareVersionStrings(served[i], served[j]) > 0
	})

	out := make([]*metav1.APIResourceList, 0, len(served))
	for _, name := range served {
		out = append(out, &metav1.APIResourceList{
			GroupVersion: group + "/" + name,
			APIResources: byVersion[name],
		})
	}
	return out
}

// drawerVersion is the version the drawer reads a kind at: the same cache and the
// same mapper `internal/detail`'s Locate and `internal/changes` go through, built
// over the discovery document above instead of a cluster's. Every CRD after
// `body` is installed beside it, in the same group, and is only there to move
// the group's preferred version.
func drawerVersion(t *testing.T, body, kind string, beside ...string) string {
	t.Helper()
	resources := publishedAsTheAPIServerDoes(t, append([]string{body}, beside...)...)
	disco := &fakediscovery.FakeDiscovery{Fake: &k8stesting.Fake{Resources: resources}}
	_, mapping, err := kindref.NewCache().Mapping(t.Context(), "test", kind,
		func(ctx context.Context) (meta.RESTMapper, error) { return kindref.DiscoveryMapper(ctx, disco) })
	if err != nil {
		t.Fatalf("the drawer could not resolve %s: %v", kind, err)
	}
	return mapping.GroupVersionKind.Version
}

// listVersion is the version the list reads a kind at, taken from the request
// the list actually sent rather than from the Definition: what the table claims
// and what it asked the API server for are separate facts, and the second is
// the one that draws deprecation warnings.
func listVersion(t *testing.T, body, plural string) (string, []string) {
	t.Helper()

	// Every version the CRD names is registered with the fake, so a list at the
	// wrong version answers with the wrong version rather than panicking: the
	// test is meant to fail on the version, not on the fixture.
	crd := object(t, body)
	spec, _, _ := unstructured.NestedMap(crd.Object, "spec")
	listKinds := map[schema.GroupVersionResource]string{crdGVR: "CustomResourceDefinitionList"}
	for _, v := range versionsOf(spec) {
		listKinds[schema.GroupVersionResource{Group: "example.io", Version: v.name, Resource: plural}] =
			nestedString(spec, "names", "listKind")
	}
	fake := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, crd)

	registry := NewRegistry()
	client := &cluster.Client{Context: "test", Config: &rest.Config{}, Dynamic: fake}

	table, err := registry.List(t.Context(), accesstest.Allow(), client, plural+".example.io", "", nil, "", "", nil)
	if err != nil {
		t.Fatalf("listing %s: %v", plural, err)
	}
	titles := make([]string, 0, len(table.Columns))
	for _, c := range table.Columns {
		titles = append(titles, c.Title)
	}

	var asked []string
	for _, action := range fake.Actions() {
		if action.GetVerb() == "list" && action.GetResource().Resource == plural {
			asked = append(asked, action.GetResource().Version)
		}
	}
	if len(asked) != 1 {
		t.Fatalf("the list asked for %s %d times (%v), want once", plural, len(asked), asked)
	}
	return asked[0], titles
}

// The mid-migration case, end to end on both sides.
func TestAMidMigrationCRDIsListedAtTheVersionTheDrawerOpens(t *testing.T) {
	listed, titles := listVersion(t, gizmoCRD, "gizmos")
	opened := drawerVersion(t, gizmoCRD, "gizmos.example.io")

	if opened != "v1" {
		t.Fatalf("the drawer resolved gizmos at %s; this fixture expects discovery's v1, so the fixture is wrong, "+
			"not the list", opened)
	}
	if listed != opened {
		t.Fatalf("the list read gizmos at %s and the drawer at %s: the same row is listed with one version's "+
			"columns and opened, shown as YAML and edited at another", listed, opened)
	}

	// The columns are the chosen version's, and only its: "Legacy" is v1beta1's
	// and its JSONPath was written against v1beta1's shape.
	want := []string{"Name", "Namespace", "Ready", "Age"}
	if strings.Join(titles, ",") != strings.Join(want, ",") {
		t.Errorf("the table's columns are %v, want v1's %v", titles, want)
	}
}

// Discovery's preferred version belongs to the group, not to the kind. Another
// CRD in example.io serving `v2` makes `v2` the group's preferred version, and
// gizmos has no `v2`. The mapper then walks the group's other versions in the
// same Kube-aware order and lands on gizmos' own highest, `v1` — which is why
// the list can compute the answer from the one CRD without asking discovery.
func TestAGroupPreferringAVersionThisKindLacksStillAgrees(t *testing.T) {
	sprocket := `{
  "apiVersion": "apiextensions.k8s.io/v1",
  "kind": "CustomResourceDefinition",
  "metadata": {"name": "sprockets.example.io"},
  "spec": {
    "group": "example.io",
    "scope": "Namespaced",
    "names": {"kind": "Sprocket", "listKind": "SprocketList", "plural": "sprockets", "singular": "sprocket"},
    "versions": [{"name": "v2", "served": true, "storage": true}]
  }
}`
	listed, _ := listVersion(t, gizmoCRD, "gizmos")
	opened := drawerVersion(t, gizmoCRD, "gizmos.example.io", sprocket)
	if listed != "v1" || opened != "v1" {
		t.Fatalf("listed at %s, opened at %s, want both at v1: the group prefers v2, which gizmos does not serve",
			listed, opened)
	}
}

// A CRD with one version has the same answer however the version is chosen. The
// widget fixtures elsewhere in this package cover the columns; this pins the
// version on both sides.
func TestASingleVersionCRDIsUnchanged(t *testing.T) {
	single := gizmoCRDWith(`[
      {"name": "v1alpha1", "served": true, "storage": true,
       "additionalPrinterColumns": [{"name": "Legacy", "type": "string", "jsonPath": ".spec.legacy"}]}
    ]`)

	listed, titles := listVersion(t, single, "gizmos")
	if listed != "v1alpha1" {
		t.Errorf("a CRD serving only v1alpha1 was listed at %s", listed)
	}
	if opened := drawerVersion(t, single, "gizmos.example.io"); opened != listed {
		t.Errorf("the list read %s and the drawer %s", listed, opened)
	}
	want := []string{"Name", "Namespace", "Legacy", "Age"}
	if strings.Join(titles, ",") != strings.Join(want, ",") {
		t.Errorf("the table's columns are %v, want %v", titles, want)
	}
}

// The order is Kube-aware, not lexical and not the order the CRD declares: GA
// above beta above alpha whatever the major number, then the major, then the
// minor. Every row is asked of both sides, so a rule that only matched the
// mid-migration example would fail here.
func TestTheListAndTheDrawerAgreeOnEveryShape(t *testing.T) {
	cases := []struct {
		name     string
		versions string
		want     string
	}{
		{
			name:     "GA beats a beta with a higher major",
			versions: `[{"name": "v2beta1", "served": true, "storage": false}, {"name": "v1", "served": true, "storage": true}]`,
			want:     "v1",
		},
		{
			name:     "beta beats alpha, declared out of order",
			versions: `[{"name": "v1beta1", "served": true, "storage": false}, {"name": "v1alpha1", "served": true, "storage": true}]`,
			want:     "v1beta1",
		},
		{
			name:     "a higher version that is not served is not read",
			versions: `[{"name": "v1", "served": true, "storage": true}, {"name": "v2", "served": false, "storage": false}]`,
			want:     "v1",
		},
		{
			name:     "v10 is above v2, which a string comparison gets wrong",
			versions: `[{"name": "v2", "served": true, "storage": true}, {"name": "v10", "served": true, "storage": false}]`,
			want:     "v10",
		},
		{
			name:     "a Kube-shaped version beats a free-form one",
			versions: `[{"name": "stable", "served": true, "storage": true}, {"name": "v1alpha1", "served": true, "storage": false}]`,
			want:     "v1alpha1",
		},
		{
			name:     "two free-form versions: the lexically first",
			versions: `[{"name": "foo", "served": true, "storage": true}, {"name": "bar", "served": true, "storage": false}]`,
			want:     "bar",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := gizmoCRDWith(tc.versions)
			listed, _ := listVersion(t, body, "gizmos")
			opened := drawerVersion(t, body, "gizmos.example.io")
			if listed != tc.want || opened != tc.want {
				t.Errorf("listed at %s, opened at %s, want both at %s", listed, opened, tc.want)
			}
		})
	}
}

// A served version marked `deprecated` is still chosen when it is the highest.
//
// The conservative reading: discovery, kubectl and the drawer ignore
// deprecation when they choose, so the list does too. Skipping a deprecated
// version here would silence the warnings, and split the list from the drawer:
// the list at one version, the drawer at another. Choosing differently is a
// decision for both halves at once.
//
// The common case is the other way round: `v1beta1` deprecated, `v1` served
// beside it. That one lists at `v1` and draws no warning at all.
func TestADeprecatedVersionIsChosenExactlyAsDiscoveryChoosesIt(t *testing.T) {
	cases := []struct {
		name     string
		versions string
		want     string
	}{
		{
			name: "the deprecated beta beside GA: GA, and no warning",
			versions: `[{"name": "v1beta1", "served": true, "storage": true, "deprecated": true},
			            {"name": "v1", "served": true, "storage": false}]`,
			want: "v1",
		},
		{
			name: "the highest is the deprecated one: still the highest",
			versions: `[{"name": "v1", "served": true, "storage": false, "deprecated": true},
			            {"name": "v1beta1", "served": true, "storage": true}]`,
			want: "v1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := gizmoCRDWith(tc.versions)
			listed, _ := listVersion(t, body, "gizmos")
			opened := drawerVersion(t, body, "gizmos.example.io")
			if listed != tc.want || opened != tc.want {
				t.Errorf("listed at %s, opened at %s, want both at %s", listed, opened, tc.want)
			}
		})
	}
}

// The chosen version declares no printer columns, and another version does.
//
// The table shows Name, Namespace and Age, which is what `kubectl get` prints
// for the same version: the API server builds its Table from the columns of the
// version asked for. Borrowing another version's columns is the conservative
// choice's opposite — their JSONPaths were written against that version's
// shape, and a column that silently resolves to nothing on every row reads as a
// fact about the objects.
func TestAChosenVersionWithoutColumnsDoesNotBorrowAnotherVersions(t *testing.T) {
	body := gizmoCRDWith(`[
      {"name": "v1beta1", "served": true, "storage": true,
       "additionalPrinterColumns": [{"name": "Legacy", "type": "string", "jsonPath": ".spec.legacy"}]},
      {"name": "v1", "served": true, "storage": false}
    ]`)

	listed, titles := listVersion(t, body, "gizmos")
	if opened := drawerVersion(t, body, "gizmos.example.io"); listed != "v1" || opened != "v1" {
		t.Fatalf("listed at %s, opened at %s, want both at v1", listed, opened)
	}
	want := []string{"Name", "Namespace", "Age"}
	if strings.Join(titles, ",") != strings.Join(want, ",") {
		t.Errorf("the table's columns are %v, want %v: v1 declares none, and v1beta1's are not v1's", titles, want)
	}
}

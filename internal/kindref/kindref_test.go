package kindref

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// mapperWith builds a mapper holding the kinds a real cluster of this shape
// serves: the built-ins whose URLs are bare, and two custom resources from
// different operators whose URLs are qualified.
func mapperWith(t *testing.T) meta.RESTMapper {
	t.Helper()

	core := schema.GroupVersion{Version: "v1"}
	apps := schema.GroupVersion{Group: "apps", Version: "v1"}
	argo := schema.GroupVersion{Group: "argoproj.io", Version: "v1alpha1"}
	certs := schema.GroupVersion{Group: "cert-manager.io", Version: "v1"}

	m := meta.NewDefaultRESTMapper([]schema.GroupVersion{core, apps, argo, certs})
	m.Add(core.WithKind("Pod"), meta.RESTScopeNamespace)
	m.Add(core.WithKind("Node"), meta.RESTScopeRoot)
	m.Add(apps.WithKind("Deployment"), meta.RESTScopeNamespace)
	m.Add(argo.WithKind("ApplicationSet"), meta.RESTScopeNamespace)
	m.Add(certs.WithKind("Certificate"), meta.RESTScopeNamespace)
	return m
}

// A custom resource's URL carries `plural.group`
// because two operators may each define a "Policy"; passing that whole string as
// the resource matches nothing, and the drawer refuses an object the list has
// just drawn.
func TestAQualifiedCustomResourceResolves(t *testing.T) {
	m := mapperWith(t)

	gvk, err := Resolve(m, "applicationsets.argoproj.io")
	if err != nil {
		t.Fatalf("an Argo CD ApplicationSet did not resolve: %v\n"+
			"This is the resource being listed on the page the operator clicked from, so the "+
			"console can see it and cannot open it.", err)
	}
	if gvk.Kind != "ApplicationSet" || gvk.Group != "argoproj.io" {
		t.Errorf("resolved to %s, want argoproj.io/ApplicationSet", gvk)
	}
}

// Built-in kinds carry a bare name, and that has to keep resolving: the
// qualified forms are a fall-through, not a replacement.
func TestABareKindStillResolves(t *testing.T) {
	m := mapperWith(t)

	for _, arg := range []string{"pods", "pod", "nodes", "deployments"} {
		if _, err := Resolve(m, arg); err != nil {
			t.Errorf("%q no longer resolves: %v", arg, err)
		}
	}
}

// A group given without the qualified form, and the qualified form itself.
func TestAGroupedBuiltInResolvesEitherWayItIsWritten(t *testing.T) {
	m := mapperWith(t)

	for _, arg := range []string{"deployments.apps", "deployments.v1.apps"} {
		gvk, err := Resolve(m, arg)
		if err != nil {
			t.Errorf("%q did not resolve: %v", arg, err)
			continue
		}
		if gvk.Kind != "Deployment" || gvk.Group != "apps" {
			t.Errorf("%q resolved to %s, want apps/Deployment", arg, gvk)
		}
	}
}

// Two dots do not mean fully qualified. `applicationsets.argoproj.io` parses as
// resource `applicationsets`, version `argoproj`, group `io` — nonsense that the
// mapper rejects — and the resolution must then carry on to the reading that is
// right, rather than stopping at the first attempt.
func TestTwoDotsAreNotAssumedToBeAVersion(t *testing.T) {
	got := candidates("applicationsets.argoproj.io")
	if len(got) < 2 {
		t.Fatalf("only %d candidate(s) for a qualified custom resource: %v", len(got), got)
	}
	if got[0].Version != "argoproj" {
		t.Errorf("the first candidate is %v; the point of this test is that the "+
			"resource.version.group reading is tried and is wrong here", got[0])
	}
	if got[1].Resource != "applicationsets" || got[1].Group != "argoproj.io" {
		t.Errorf("the second candidate is %v, want applicationsets in argoproj.io — "+
			"the reading that is actually correct", got[1])
	}
}

// Two operators each defining the same plural is the reason URLs are qualified
// at all. Both must resolve, to different kinds.
func TestTwoOperatorsPluralsDoNotCollide(t *testing.T) {
	m := mapperWith(t)

	argo, err := Resolve(m, "applicationsets.argoproj.io")
	if err != nil {
		t.Fatalf("argo: %v", err)
	}
	cert, err := Resolve(m, "certificates.cert-manager.io")
	if err != nil {
		t.Fatalf("cert-manager: %v", err)
	}
	if argo.Group == cert.Group {
		t.Errorf("both resolved into %q", argo.Group)
	}
}

// A kind nothing serves says so, in the console's own words rather than in the
// mapper's — which names a GroupVersionResource the operator never typed.
func TestAKindTheClusterDoesNotServeIsRefusedInPlainWords(t *testing.T) {
	m := mapperWith(t)

	_, err := Resolve(m, "widgets.example.com")
	if err == nil {
		t.Fatal("a kind nothing serves resolved")
	}
	if !strings.Contains(err.Error(), "widgets.example.com") {
		t.Errorf("the refusal does not name what was asked for: %q", err)
	}
	if strings.Contains(err.Error(), "no matches for") {
		t.Errorf("the mapper's own error reached the operator: %q", err)
	}
}

// The scope comes from the mapper and is never guessed: a guessed scope is how
// a node's name ends up read as a namespace.
func TestMappingReportsTheScopeTheClusterGivesIt(t *testing.T) {
	m := mapperWith(t)

	node, err := Mapping(m, "nodes")
	if err != nil {
		t.Fatalf("nodes: %v", err)
	}
	if node.Scope.Name() != meta.RESTScopeNameRoot {
		t.Errorf("a Node is reported as %q. A cluster-scoped object whose scope is guessed "+
			"wrong is how a node's name became a namespace: it passes every validator and "+
			"is then stated as fact", node.Scope.Name())
	}

	appset, err := Mapping(m, "applicationsets.argoproj.io")
	if err != nil {
		t.Fatalf("applicationsets: %v", err)
	}
	if appset.Scope.Name() != meta.RESTScopeNameNamespace {
		t.Errorf("an ApplicationSet is reported as %q, want namespaced", appset.Scope.Name())
	}
	if appset.Resource.Resource != "applicationsets" {
		t.Errorf("the mapping addresses %q, want the plural alone — the group belongs in its "+
			"own field", appset.Resource.Resource)
	}
}

func TestAnEmptyKindIsRefusedRatherThanResolved(t *testing.T) {
	if _, err := Resolve(mapperWith(t), "   "); err == nil {
		t.Error("an empty kind resolved to something")
	}
}

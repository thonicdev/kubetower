package edit

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
)

// The guarded create, and the four things it refuses.
//
// Against the fake, always, for the reason at the top of edit_test.go: a test of
// a write path is a write.

var applicationGVR = schema.GroupVersionResource{
	Group: "argoproj.io", Version: "v1alpha1", Resource: "applications",
}

// createHarness is a fake cluster that records every create that reached it.
//
// The recording is the point. "It refused" and "it refused and sent nothing" are
// different claims, and only the second one is a guardrail — the same assertion
// `harness.noWrites` makes for patches.
type createHarness struct {
	t    *testing.T
	dyn  *dynamicfake.FakeDynamicClient
	sent []recorded
}

func newCreateHarness(t *testing.T) *createHarness {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{applicationGVR: "ApplicationList"})
	h := &createHarness{t: t, dyn: dyn}
	dyn.PrependReactor("create", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
		create, ok := action.(k8stesting.CreateActionImpl)
		if !ok {
			return false, nil, nil
		}
		object, _ := create.GetObject().(*unstructured.Unstructured)
		name := ""
		if object != nil {
			name = object.GetName()
		}
		h.sent = append(h.sent, recorded{
			Verb: action.GetVerb(), Resource: action.GetResource().Resource,
			Namespace: action.GetNamespace(), Name: name,
		})
		// The fake's tracker refuses a dry-run create it has already accepted
		// once, so the dry run answers with the object rather than storing it —
		// which is what a real API server does.
		if hasDryRunAll(create.CreateOptions.DryRun) {
			return true, create.GetObject(), nil
		}
		return false, nil, nil
	})
	return h
}

func hasDryRunAll(values []string) bool {
	for _, value := range values {
		if value == metav1.DryRunAll {
			return true
		}
	}
	return false
}

func (h *createHarness) nothingSent() {
	h.t.Helper()
	if len(h.sent) != 0 {
		h.t.Fatalf("a refusal still reached the cluster: %+v", h.sent)
	}
}

// guardedApplications is a Location as locate() would stamp one.
func (h *createHarness) guardedApplications(readOnly func(string) bool) Location {
	h.t.Helper()
	writer := New(Options{
		Resolve: applicationResolver{dyn: h.dyn, cluster: "prod"},
		Access:  accesstest.Allow(),
		Logf:    func(string, ...any) {},
	})
	if readOnly != nil {
		writer.readOnly = readOnly
	}
	where, err := writer.Locate(h.t.Context(), Target{Cluster: "prod", Kind: "applications.argoproj.io", Name: "trivy-operator"})
	if err != nil {
		h.t.Fatalf("Locate: %v", err)
	}
	return where
}

type applicationResolver struct {
	dyn     *dynamicfake.FakeDynamicClient
	cluster string
	// clusterScoped mislocates the kind on purpose, for the scope test.
	clusterScoped bool
}

func (a applicationResolver) Locate(_ context.Context, _, _ string) (Location, error) {
	return Location{
		Cluster: a.cluster, Resource: applicationGVR, Kind: "Application",
		Namespaced: !a.clusterScoped, Dynamic: a.dyn,
	}, nil
}

func application(name, namespace string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata":   map[string]any{"name": name, "namespace": namespace},
	}}
}

// A Location nobody resolved never reaches a cluster.
//
// This is the whole reason create.go exists rather than a caller building its own
// `dynamic.ResourceInterface` out of `Location.Dynamic`. Every field of Location is
// exported, so one can be assembled by hand — and a hand-assembled one has not
// been through `KT_READONLY_CLUSTERS`. The stamp is the difference between "the
// guard is called by every path today" and "the guard cannot be skipped".
func TestACreateOnAHandAssembledLocationIsRefusedAndNothingIsSent(t *testing.T) {
	h := newCreateHarness(t)

	fabricated := Location{
		Cluster: "prod", Resource: applicationGVR, Kind: "Application",
		Namespaced: true, Dynamic: h.dyn,
	}
	_, _, err := Create(context.Background(), fabricated,
		Target{Cluster: "prod", Kind: "applications.argoproj.io", Namespace: "argocd", Name: "trivy-operator"},
		application("trivy-operator", "argocd"), false)

	refusal, ok := AsRefusal(err)
	if !ok {
		t.Fatalf("a fabricated Location was not refused in this package's vocabulary: %v", err)
	}
	if !strings.Contains(refusal.Reason, "guardrail") {
		t.Errorf("the refusal does not say what is wrong: %q", refusal.Reason)
	}
	h.nothingSent()
}

// And a resolved Location on a cluster in KT_READONLY_CLUSTERS never becomes one
// at all, which is the guard this stamp exists to make unskippable.
func TestACreateOnAReadOnlyClusterIsRefusedBeforeAnythingIsBuilt(t *testing.T) {
	h := newCreateHarness(t)
	writer := New(Options{
		Resolve:  applicationResolver{dyn: h.dyn, cluster: "prod"},
		Access:   accesstest.Allow(),
		ReadOnly: func(name string) bool { return name == "prod" },
		Logf:     func(string, ...any) {},
	})

	_, err := writer.Locate(t.Context(), Target{Cluster: "prod", Kind: "applications.argoproj.io", Name: "trivy-operator"})
	refusal, ok := AsRefusal(err)
	if !ok {
		t.Fatalf("a marked cluster was not refused: %v", err)
	}
	if refusal.Kind != RefusedReadOnly {
		t.Errorf("the refusal is kind %d, want RefusedReadOnly — the header a monitor reads "+
			"comes off this", refusal.Kind)
	}
	if !strings.Contains(refusal.Reason, "KT_READONLY_CLUSTERS") {
		t.Errorf("the refusal does not name the setting that caused it: %q", refusal.Reason)
	}
	h.nothingSent()
}

// The body and the address have to be the same object.
//
// A create sends the *body's* own name, so a disagreement here creates something
// under a name no guard, no confirmation and no log line ever saw.
func TestACreateWhoseBodyAndAddressDisagreeIsRefused(t *testing.T) {
	h := newCreateHarness(t)
	where := h.guardedApplications(nil)

	for _, c := range []struct {
		what   string
		target Target
		object *unstructured.Unstructured
		says   string
	}{
		{
			what:   "a different name",
			target: Target{Cluster: "prod", Namespace: "argocd", Name: "trivy-operator"},
			object: application("something-else", "argocd"),
			says:   "body's own name",
		},
		{
			what:   "a different namespace",
			target: Target{Cluster: "prod", Namespace: "argocd", Name: "trivy-operator"},
			object: application("trivy-operator", "kube-system"),
			says:   "have to agree",
		},
		{
			what:   "no apiVersion",
			target: Target{Cluster: "prod", Namespace: "argocd", Name: "trivy-operator"},
			object: &unstructured.Unstructured{Object: map[string]any{
				"metadata": map[string]any{"name": "trivy-operator", "namespace": "argocd"},
			}},
			says: "no apiVersion",
		},
		{
			what:   "no object at all",
			target: Target{Cluster: "prod", Namespace: "argocd", Name: "trivy-operator"},
			object: nil,
			says:   "no object to create",
		},
	} {
		t.Run(c.what, func(t *testing.T) {
			h.sent = nil
			_, _, err := Create(context.Background(), where, c.target, c.object, false)
			refusal, ok := AsRefusal(err)
			if !ok {
				t.Fatalf("%s was not refused: %v", c.what, err)
			}
			if !strings.Contains(refusal.Reason, c.says) {
				t.Errorf("the refusal for %s reads %q, which does not say %q", c.what, refusal.Reason, c.says)
			}
			h.nothingSent()
		})
	}
}

// The scope is refused in both directions, which is one more than a patch needs.
//
// A whole family of bugs starts with a name being read as a namespace. A create is the one write whose namespace is *chosen*
// rather than read off the object it is patching, so both halves are refused
// here: a namespaced kind with no namespace, and a cluster-scoped kind with one.
func TestACreateRefusesANamespaceOnEitherSideOfTheScope(t *testing.T) {
	t.Run("namespaced, none given", func(t *testing.T) {
		h := newCreateHarness(t)
		where := h.guardedApplications(nil)
		_, _, err := Create(context.Background(), where,
			Target{Cluster: "prod", Name: "trivy-operator"}, application("trivy-operator", ""), false)
		refusal, ok := AsRefusal(err)
		if !ok || !strings.Contains(refusal.Reason, "no namespace was given") {
			t.Fatalf("a namespaced create with no namespace was not refused: %v", err)
		}
		h.nothingSent()
	})

	t.Run("cluster-scoped, one given", func(t *testing.T) {
		h := newCreateHarness(t)
		writer := New(Options{
			Resolve: applicationResolver{dyn: h.dyn, cluster: "prod", clusterScoped: true},
			Access:  accesstest.Allow(),
			Logf:    func(string, ...any) {},
		})
		where, err := writer.Locate(t.Context(), Target{Cluster: "prod", Kind: "applications.argoproj.io", Name: "trivy-operator"})
		if err != nil {
			t.Fatalf("Locate: %v", err)
		}
		_, _, err = Create(context.Background(), where,
			Target{Cluster: "prod", Namespace: "argocd", Name: "trivy-operator"},
			application("trivy-operator", "argocd"), false)
		refusal, ok := AsRefusal(err)
		if !ok || !strings.Contains(refusal.Reason, "cluster-scoped") {
			t.Fatalf("a namespace on a cluster-scoped kind was not refused: %v", err)
		}
		h.nothingSent()
	})
}

// The happy path, and what the dry run is for.
//
// Two requests, the first carrying DryRun=All: the API server validates, runs
// admission and quota and stores nothing, so a create an admission webhook will
// refuse is refused before anything exists. Asserted here rather than only at the
// call sites, because "there was a dry run" is the property, and a caller that
// stops doing it is the regression.
func TestACreateIsSentToTheAddressItWasGiven(t *testing.T) {
	h := newCreateHarness(t)
	where := h.guardedApplications(nil)
	target := Target{Cluster: "prod", Namespace: "argocd", Name: "trivy-operator"}

	if _, _, err := Create(context.Background(), where, target, application("trivy-operator", "argocd"), true); err != nil {
		t.Fatalf("the dry run was refused: %v", err)
	}
	created, _, err := Create(context.Background(), where, target, application("trivy-operator", "argocd"), false)
	if err != nil {
		t.Fatalf("the create was refused: %v", err)
	}
	if created.GetName() != "trivy-operator" {
		t.Errorf("the created object came back as %q", created.GetName())
	}
	if len(h.sent) != 2 {
		t.Fatalf("want a dry run and a commit, got %+v", h.sent)
	}
	for _, got := range h.sent {
		if got.Resource != "applications" || got.Namespace != "argocd" || got.Name != "trivy-operator" {
			t.Errorf("a create went somewhere else: %+v", got)
		}
	}
}

// APIVersionOf reads the version the *cluster* serves rather than a constant.
//
// `argoproj.io/v1alpha1` is what every Argo CD serves today and is still not
// something to write down: `internal/kindref` exists because two packages
// independently hardcoded half of a qualified kind, and the failure is a 404 for
// an object the console had just listed.
func TestAPIVersionOfUsesTheVersionTheClusterServes(t *testing.T) {
	for _, c := range []struct {
		resource schema.GroupVersionResource
		want     string
	}{
		{applicationGVR, "argoproj.io/v1alpha1"},
		{schema.GroupVersionResource{Group: "argoproj.io", Version: "v1beta1", Resource: "applications"}, "argoproj.io/v1beta1"},
		// A core kind has no group, and `/v1` would be wrong.
		{schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}, "v1"},
	} {
		if got := APIVersionOf(Location{Resource: c.resource}); got != c.want {
			t.Errorf("APIVersionOf(%v) = %q, want %q", c.resource, got, c.want)
		}
	}
}

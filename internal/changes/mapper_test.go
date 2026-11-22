package changes

// The Changes panel must not refuse a kind the drawer beside it has just
// rendered.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
)

// readerFor builds a Reader whose mapper and dynamic client are both fakes. The
// kubeconfig names one context and points it at a port nothing listens on, so
// anything that tried to reach a cluster would fail loudly rather than quietly
// succeed.
func readerFor(t *testing.T, mapper func() (meta.RESTMapper, error), objects ...runtime.Object) *Reader {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config")
	body := `apiVersion: v1
kind: Config
clusters:
- name: nowhere
  cluster:
    server: https://127.0.0.1:1
contexts:
- name: prod
  context: {cluster: nowhere, user: nobody}
users:
- name: nobody
  user: {token: none}
current-context: prod
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing a kubeconfig: %v", err)
	}
	clusters, err := cluster.NewManager(path)
	if err != nil {
		t.Fatalf("loading the kubeconfig: %v", err)
	}

	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			{Version: "v1", Resource: "pods"}:                                        "PodList",
			{Group: "argoproj.io", Version: "v1alpha1", Resource: "applicationsets"}: "ApplicationSetList",
		},
		objects...,
	)

	reader := NewReader(clusters, accesstest.Allow())
	reader.newMapper = func(context.Context, *cluster.Client) (meta.RESTMapper, error) { return mapper() }
	reader.dynamicOf = func(*cluster.Client) (dynamic.Interface, error) { return dyn, nil }
	return reader
}

func mapperFor(kinds ...schema.GroupVersionKind) meta.RESTMapper {
	groups := make([]schema.GroupVersion, 0, len(kinds))
	for _, k := range kinds {
		groups = append(groups, k.GroupVersion())
	}
	m := meta.NewDefaultRESTMapper(groups)
	for _, k := range kinds {
		m.Add(k, meta.RESTScopeNamespace)
	}
	return m
}

func appSet(namespace, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "ApplicationSet",
		"metadata": map[string]any{"name": name, "namespace": namespace, "resourceVersion": "1"},
	}}
}

// A mapper held with no way to invalidate it would refuse a CRD installed a
// moment ago — while `internal/detail`, which retries, renders the same object
// in the drawer this panel sits in.
func TestACRDInstalledSinceTheMapperWasBuiltIsNotRefused(t *testing.T) {
	pod := schema.GroupVersionKind{Version: "v1", Kind: "Pod"}
	appset := schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "ApplicationSet"}

	var builds int
	reader := readerFor(t, func() (meta.RESTMapper, error) {
		builds++
		if builds == 1 {
			return mapperFor(pod), nil // before the operator installed Argo CD
		}
		return mapperFor(pod, appset), nil
	}, appSet("argocd", "platform"))

	// Warm the cache with the pre-CRD mapper, the way any other panel would.
	if _, err := reader.Read(context.Background(), "prod", Request{Kind: "pods", Namespace: "default", Name: "api-1"}); err == nil {
		t.Fatal("a pod that does not exist was reported as read")
	}

	report, err := reader.Read(context.Background(), "prod", Request{
		Kind: "applicationsets.argoproj.io", Namespace: "argocd", Name: "platform",
	})
	if err != nil {
		t.Fatalf("the Changes panel refused a kind the drawer around it renders: %v\n"+
			"This package held a mapper with no retry while internal/detail "+
			"rebuilt one, so the two halves of a single drawer disagree for up to ten minutes", err)
	}
	// The report names the resource, which is the plural alone: the group lives
	// in its own field, and passing the whole qualified string as the resource
	// is what kindref exists to prevent.
	if report.Kind != "applicationsets" {
		t.Errorf("report is about %q, want applicationsets", report.Kind)
	}
	if builds != 2 {
		t.Errorf("built the mapper %d times, want 2 — one rebuild for the unknown kind", builds)
	}

	// And the rebuilt mapper is the one now held. Resolving through
	// `Mapper(key, true, …)` — rebuild on every call — passes the paragraph
	// above by coincidence while paying a full discovery walk on every open.
	if _, err := reader.Read(context.Background(), "prod", Request{
		Kind: "applicationsets.argoproj.io", Namespace: "argocd", Name: "platform",
	}); err != nil {
		t.Fatalf("the second open of the same object failed: %v", err)
	}
	if builds != 2 {
		t.Errorf("built the mapper %d times over two opens, want 2 — the rebuild is a recovery, "+
			"not the mechanism, and discovery is dozens of round trips per click", builds)
	}
}

// A cluster that could not be asked still says so, and does not say the kind does
// not exist. The two are different answers.
func TestDiscoveryFailureIsReportedAsOne(t *testing.T) {
	reader := readerFor(t, func() (meta.RESTMapper, error) {
		return nil, errNoCredentials
	})

	_, err := reader.Read(context.Background(), "prod", Request{Kind: "pods", Namespace: "default", Name: "api-1"})
	if err == nil {
		t.Fatal("a cluster that could not be asked answered")
	}
	if !strings.Contains(err.Error(), "discovering") {
		t.Errorf("error = %q, want it phrased as a failed discovery rather than as a missing kind", err)
	}
}

var errNoCredentials = errNoCreds("the server has asked for the client to provide credentials")

type errNoCreds string

func (e errNoCreds) Error() string { return string(e) }

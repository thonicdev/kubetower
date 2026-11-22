package customresources

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"k8s.io/client-go/rest"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
)

// A cluster's custom resources are read through the dynamic client the
// cluster manager already holds for it, never through one built here from its
// config: that would be a second transport and connection pool per cluster.
// The config points at a server that counts what reaches it, so a client
// built from it would be seen there; the manager's client is a fake that
// records what it was asked.
func TestCustomResourcesAreReadThroughTheClustersOwnDynamicClient(t *testing.T) {
	var reached atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Add(1)
		http.Error(w, "not this client", http.StatusTeapot)
	}))
	t.Cleanup(server.Close)

	registry, _, fake := newRegistry(t,
		object(t, widgetCRD),
		widget(t, "one", "apps", `{"spec": {"size": 3}}`),
	)
	client := &cluster.Client{Context: "test", Config: &rest.Config{Host: server.URL}, Dynamic: fake}

	if _, err := registry.Definitions(t.Context(), client); err != nil {
		t.Fatalf("definitions: %v", err)
	}
	if _, err := registry.List(t.Context(), accesstest.Allow(), client, "widgets.example.io", "", nil, "", "", nil); err != nil {
		t.Fatalf("listing widgets: %v", err)
	}
	if _, err := registry.Get(t.Context(), accesstest.Allow(), client, "widgets", "apps", "one"); err != nil {
		t.Fatalf("reading a widget: %v", err)
	}

	if n := reached.Load(); n != 0 {
		t.Errorf("%d requests went through a client built from the config, want every read through Client.Dynamic", n)
	}
	seen := map[string]bool{}
	for _, action := range fake.Actions() {
		seen[action.GetVerb()+" "+action.GetResource().Resource] = true
	}
	for _, want := range []string{"list customresourcedefinitions", "list widgets", "get widgets"} {
		if !seen[want] {
			t.Errorf("the cluster's own dynamic client was not asked to %s; it saw %v", want, seen)
		}
	}
}

// A client with a config and no dynamic client is not one to read through:
// an error, not a client built on the side, and not an empty catalog.
func TestAClientWithoutItsDynamicClientIsAnError(t *testing.T) {
	registry := NewRegistry()
	client := &cluster.Client{Context: "test", Config: &rest.Config{Host: "https://cluster.invalid"}}
	if _, err := registry.Definitions(t.Context(), client); err == nil {
		t.Error("a client with no dynamic client produced a catalog")
	}
	if _, err := registry.List(t.Context(), accesstest.Allow(), client, "widgets.example.io", "", nil, "", "", nil); err == nil {
		t.Error("a client with no dynamic client produced a list")
	}
}

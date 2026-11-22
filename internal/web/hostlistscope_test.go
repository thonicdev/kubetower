package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
)

// Host.List took a kind's scope from the rows it read, so a namespaced kind with
// no objects answered a namespace-scoped person a refusal instead of an empty,
// narrowed page. The scope now comes from discovery.

// scopeHost is a cluster serving example.io/v1 with two kinds and no objects of
// either: widgets (namespaced) and gadgets (cluster-scoped). discoveryFails
// makes the group's discovery document an error.
func scopeHost(t *testing.T, discoveryFails bool) extension.Host {
	t.Helper()
	return scopeHostIn(t, discoveryFails, nil)
}

// scopeHostIn is scopeHost on a cluster that also lists namespaces.
func scopeHostIn(t *testing.T, discoveryFails bool, namespaces []string) extension.Host {
	t.Helper()
	write := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch path := r.URL.Path; {
		case path == "/api/v1/namespaces" && namespaces != nil:
			items := []any{}
			for _, n := range namespaces {
				items = append(items, map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": n}})
			}
			write(w, map[string]any{"apiVersion": "v1", "kind": "NamespaceList",
				"metadata": map[string]any{"resourceVersion": "9"}, "items": items})
		case path == "/apis/example.io/v1":
			if discoveryFails {
				http.Error(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"InternalError","code":500}`,
					http.StatusInternalServerError)
				return
			}
			write(w, map[string]any{"kind": "APIResourceList", "groupVersion": "example.io/v1", "resources": []any{
				map[string]any{"name": "widgets", "singularName": "widget", "namespaced": true, "kind": "Widget",
					"verbs": []string{"get", "list"}},
				map[string]any{"name": "gadgets", "singularName": "gadget", "namespaced": false, "kind": "Gadget",
					"verbs": []string{"get", "list"}},
			}})
		case strings.HasSuffix(path, "/widgets"):
			write(w, map[string]any{"apiVersion": "example.io/v1", "kind": "WidgetList",
				"metadata": map[string]any{"resourceVersion": "9"}, "items": []any{}})
		case strings.HasSuffix(path, "/gadgets"):
			write(w, map[string]any{"apiVersion": "example.io/v1", "kind": "GadgetList",
				"metadata": map[string]any{"resourceVersion": "9"}, "items": []any{}})
		default:
			http.Error(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"NotFound","code":404}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(kubeconfig, []byte(`apiVersion: v1
kind: Config
current-context: prod
clusters:
- name: prod
  cluster: {server: `+server.URL+`}
contexts:
- name: prod
  context: {cluster: prod, user: sa}
users:
- name: sa
  user: {token: sa-token}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := cluster.NewManager(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	reviews := &accesstest.Reviews{Answer: roleBindings}
	s, err := New(Options{Clusters: manager, InCluster: true,
		Access: access.New(access.Options{Reviews: reviews.For, InCluster: true})})
	if err != nil {
		t.Fatal(err)
	}
	return s.Host()
}

func scopeQuery(resource string) extension.ListQuery {
	return extension.ListQuery{GVR: schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: resource}}
}

func TestHostListOfAnEmptyNamespacedKindIsEmptyForANarrowedPerson(t *testing.T) {
	h := scopeHost(t, false)
	page, err := h.List(asPerson(shopViewer()), "prod", scopeQuery("widgets"))
	if err != nil {
		t.Fatalf("List: %v; an empty namespaced kind was refused", err)
	}
	if len(page.Items) != 0 {
		t.Errorf("Items %v; want none", page.Items)
	}
	if page.Complete {
		t.Error("Complete is true on a list narrowed to the person's namespaces")
	}
	if !strings.Contains(page.Note, "namespaces where you may") {
		t.Errorf("Note %q does not say the list is narrowed", page.Note)
	}
}

func TestHostListOfAClusterScopedKindStaysRefusedWhenNarrowed(t *testing.T) {
	h := scopeHost(t, false)
	_, err := h.List(asPerson(shopViewer()), "prod", scopeQuery("gadgets"))
	var refused *access.Refused
	if !errors.As(err, &refused) {
		t.Fatalf("err %v; want an *access.Refused", err)
	}
}

func TestHostListSaysWhenDiscoveryFailsRatherThanRefusingOrAnsweringEmpty(t *testing.T) {
	h := scopeHost(t, true)
	page, err := h.List(asPerson(shopViewer()), "prod", scopeQuery("widgets"))
	if err == nil {
		t.Fatalf("no error, page %+v; a failed discovery was drawn as an answer", page)
	}
	var refused *access.Refused
	if errors.As(err, &refused) {
		t.Errorf("err %v is a refusal; a failed discovery is not a denial", err)
	}
}

// A person who may list a kind in none of the cluster's namespaces is refused,
// not shown the empty page the cluster would give for a kind it holds none of
// (R5 §5); a person who may list it somewhere reads the narrowed page.
func TestHostListRefusesAPersonWhoMayListTheKindNowhere(t *testing.T) {
	h := scopeHostIn(t, false, []string{"shop", "other"})
	_, err := h.List(asPerson(nobody()), "prod", scopeQuery("widgets"))
	var refused *access.Refused
	if !errors.As(err, &refused) || !errors.Is(err, extension.ErrDenied) {
		t.Fatalf("err %v; want a denial for a person who may list widgets nowhere", err)
	}
	page, err := h.List(asPerson(person("rita@example.com", "oidc:readers")), "prod", scopeQuery("widgets"))
	if err != nil {
		t.Fatalf("a person who may list everywhere: %v", err)
	}
	if len(page.Items) != 0 || !page.Complete {
		t.Errorf("page %+v; want the cluster's empty, complete answer", page)
	}
}

func TestHostListKeepsTheNarrowedPageWhereTheNamespacesAreUnknown(t *testing.T) {
	h := scopeHost(t, false)
	page, err := h.List(asPerson(nobody()), "prod", scopeQuery("widgets"))
	if err != nil {
		t.Fatalf("List: %v; a refusal drawn without the namespaces to review", err)
	}
	if page.Complete {
		t.Error("Complete is true on a narrowed list")
	}
}

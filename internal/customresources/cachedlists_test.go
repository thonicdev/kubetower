package customresources

// What this package puts on the wire for its two lists — the CRD catalog and
// one custom kind's instances — and what happens when the API server hands back
// a continue token.
//
// A real dynamic client against an httptest server, not the `dynamicfake` the
// rest of this suite uses: the fake ignores `resourceVersion` and `limit` alike,
// so neither property below is visible through it.
//
// The catalog is the sharper of the two cases. Each CRD carries its whole
// OpenAPI schema, so it is the most expensive list on a cluster to serve out of
// etcd — and a cluster can easily have more than five hundred of them, at which
// point a list truncated at the chunk size reports a kind as unknown while the
// cluster is serving it. That is the "never an empty table" failure in this
// package's own doc comment, wearing the shape of a complete answer.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/resources"
)

// crdServer answers list calls the way an API server that honors `limit` does:
// a chunk at a time with a continue token, and the whole list in one response
// when no limit was asked for — which is what today's bare `ListOptions{}` gets,
// and why it is expensive rather than wrong.
type crdServer struct {
	mu    sync.Mutex
	asked map[string][]url.Values

	// items is the catalog; instances is what one custom kind's list answers.
	items     []any
	instances []any
}

func (s *crdServer) queries(what string) []url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]url.Values(nil), s.asked[what]...)
}

func (s *crdServer) start(t *testing.T) dynamic.Interface {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		what, items, listKind := "instances", s.instances, "WidgetList"
		if strings.Contains(r.URL.Path, "customresourcedefinitions") {
			what, items, listKind = "catalogue", s.items, "CustomResourceDefinitionList"
		}

		s.mu.Lock()
		if s.asked == nil {
			s.asked = map[string][]url.Values{}
		}
		s.asked[what] = append(s.asked[what], r.URL.Query())
		s.mu.Unlock()

		from, _ := strconv.Atoi(r.URL.Query().Get("continue"))
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		to := len(items)
		if limit > 0 && from+limit < to {
			to = from + limit
		}
		token := ""
		if to < len(items) {
			token = strconv.Itoa(to)
		}

		body, _ := json.Marshal(map[string]any{
			"apiVersion": "apiextensions.k8s.io/v1",
			"kind":       listKind,
			"metadata":   map[string]any{"resourceVersion": "42", "continue": token},
			"items":      items[from:to],
		})
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	t.Cleanup(server.Close)

	// QPS below zero disables client-go's own rate limiter. These tests make two
	// requests, but the catalog one builds five hundred objects and a throttled
	// client would make that an unexplained pause.
	dyn, err := dynamic.NewForConfig(&rest.Config{Host: server.URL, QPS: -1})
	if err != nil {
		t.Fatalf("building a dynamic client: %v", err)
	}
	return dyn
}

func (s *crdServer) registry(t *testing.T) (*Registry, *cluster.Client) {
	dyn := s.start(t)
	r := &Registry{}
	return r, &cluster.Client{Context: "test", Config: &rest.Config{}, Dynamic: dyn}
}

// crdFor is one definition, complete enough for buildCatalogue to keep it.
func crdFor(plural string) any {
	return map[string]any{
		"apiVersion": "apiextensions.k8s.io/v1",
		"kind":       "CustomResourceDefinition",
		"metadata":   map[string]any{"name": plural + ".example.io"},
		"spec": map[string]any{
			"group": "example.io",
			"scope": "Namespaced",
			"names": map[string]any{
				"kind":     plural,
				"listKind": plural + "List",
				"plural":   plural,
				"singular": plural,
			},
			"versions": []any{
				map[string]any{"name": "v1", "served": true, "storage": true},
			},
		},
	}
}

// --- the options -------------------------------------------------------------

func TestTheCatalogueIsListedFromTheWatchCache(t *testing.T) {
	server := &crdServer{items: []any{crdFor("widgets")}}
	registry, client := server.registry(t)

	if _, err := registry.Definitions(context.Background(), client); err != nil {
		t.Fatalf("fetching the catalog: %v", err)
	}

	asked := server.queries("catalogue")
	if len(asked) != 1 {
		t.Fatalf("%d requests for a one-page catalog", len(asked))
	}
	if got := asked[0].Get("resourceVersion"); got != "0" {
		t.Errorf("resourceVersion is %q, so the CRD list is a quorum read out of "+
			"etcd — and a CRD carries its whole schema", got)
	}
	if got := asked[0].Get("limit"); got != fmt.Sprint(resources.PageSize) {
		t.Errorf("limit is %q, want %d", got, resources.PageSize)
	}
}

// --- the truncation ----------------------------------------------------------

// A cluster with more than five hundred CRDs is not exotic — an OpenShift
// cluster serves well over a thousand — and the kind that falls off the end
// comes back from Definitions as absent, which the console renders as "unknown kind"
// for something the cluster is serving right now.
func TestTheCatalogueIsNotTruncatedAtTheChunkSize(t *testing.T) {
	const total = resources.PageSize + 1
	items := make([]any, 0, total)
	for i := 0; i < total; i++ {
		items = append(items, crdFor(fmt.Sprintf("widgets%04d", i)))
	}
	server := &crdServer{items: items}
	registry, client := server.registry(t)

	kinds, err := registry.Definitions(context.Background(), client)
	if err != nil {
		t.Fatalf("fetching the catalog: %v", err)
	}
	if len(kinds) != total {
		t.Fatalf("%d kinds in the catalog, want %d — the list stopped at the "+
			"chunk size, so a kind the cluster serves reads as unknown", len(kinds), total)
	}
	if pages := len(server.queries("catalogue")); pages != 2 {
		t.Errorf("%d list requests, want 2 — one per page", pages)
	}
}

// The instances of one custom kind are the other list, and it is the one behind
// a table that redraws on a timer. A Kyverno PolicyReport or a cert-manager
// Certificate list on a real cluster runs to thousands of objects, so the chunk
// size is reached here long before it is reached in the catalog.
func TestACustomKindsInstancesAreCachedAndNotTruncated(t *testing.T) {
	const total = resources.PageSize + 1
	instances := make([]any, 0, total)
	for i := 0; i < total; i++ {
		instances = append(instances, map[string]any{
			"apiVersion": "example.io/v1",
			"kind":       "widgets",
			"metadata":   map[string]any{"name": fmt.Sprintf("widget-%04d", i), "namespace": "apps"},
		})
	}
	server := &crdServer{items: []any{crdFor("widgets")}, instances: instances}
	registry, client := server.registry(t)

	table, err := registry.List(context.Background(), accesstest.Allow(), client, "widgets", "apps", nil, "", "", nil)
	if err != nil {
		t.Fatalf("listing widgets: %v", err)
	}
	if len(table.Rows) != total {
		t.Fatalf("%d rows, want %d — the list stopped at the chunk size, and the "+
			"filter box above it would answer \"no results\" for a name that is "+
			"in the cluster", len(table.Rows), total)
	}

	asked := server.queries("instances")
	if len(asked) != 2 {
		t.Fatalf("%d list requests, want 2 — one per page", len(asked))
	}
	if got := asked[0].Get("resourceVersion"); got != "0" {
		t.Errorf("resourceVersion is %q, so this list is a quorum read out of etcd", got)
	}
	if got := asked[0].Get("limit"); got != fmt.Sprint(resources.PageSize) {
		t.Errorf("limit is %q, want %d", got, resources.PageSize)
	}
	// The API server refuses continue and resourceVersion together; Paged drops
	// it after the first page, and a call site that rebuilt the options itself
	// would not.
	if got := asked[1].Get("resourceVersion"); got != "" {
		t.Errorf("page 2 carried resourceVersion=%q beside a continue token, which "+
			"the API server rejects", got)
	}
}

package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
)

// eachServer plays an API server holding perNamespace pods in each of shop and
// billing, and nodes, every object padded to pad bytes. It honors `limit`
// with a continue token (the offset), answers the pods as a table when asked,
// and records each request.
type eachServer struct {
	perNamespace, nodes, pad int
	mu                       sync.Mutex
	requests                 []*http.Request
}

func (s *eachServer) seen() []*http.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*http.Request(nil), s.requests...)
}

func (s *eachServer) host(t *testing.T) extension.Host {
	t.Helper()
	write := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	padding := strings.Repeat("x", s.pad)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests = append(s.requests, r.Clone(r.Context()))
		s.mu.Unlock()
		query := r.URL.Query()
		var names, namespaces []string
		kind := "Pod"
		switch r.URL.Path {
		case "/api/v1":
			write(w, map[string]any{"kind": "APIResourceList", "groupVersion": "v1", "resources": []any{
				map[string]any{"name": "pods", "namespaced": true, "kind": "Pod", "verbs": []any{"list"}},
				map[string]any{"name": "nodes", "namespaced": false, "kind": "Node", "verbs": []any{"list"}}}})
			return
		case "/api/v1/pods", "/api/v1/namespaces/shop/pods", "/api/v1/namespaces/billing/pods":
			for _, ns := range []string{"billing", "shop"} {
				if r.URL.Path != "/api/v1/pods" && !strings.Contains(r.URL.Path, "/"+ns+"/") {
					continue
				}
				for i := 0; i < s.perNamespace; i++ {
					names, namespaces = append(names, ns+"-"+strconv.Itoa(i)), append(namespaces, ns)
				}
			}
		case "/api/v1/nodes":
			kind = "Node"
			for i := 0; i < s.nodes; i++ {
				names, namespaces = append(names, "node-"+strconv.Itoa(i)), append(namespaces, "")
			}
		default:
			http.Error(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"NotFound","code":404}`, http.StatusNotFound)
			return
		}
		from, _ := strconv.Atoi(query.Get("continue"))
		to, next := len(names), ""
		if n, err := strconv.Atoi(query.Get("limit")); err == nil && n > 0 && from+n < len(names) {
			to, next = from+n, strconv.Itoa(from+n)
		}
		if strings.Contains(r.Header.Get("Accept"), "as=Table") {
			rows := []any{}
			for i := from; i < to; i++ {
				rows = append(rows, map[string]any{"cells": []any{names[i]},
					"object": map[string]any{"kind": "PartialObjectMetadata", "apiVersion": "meta.k8s.io/v1",
						"metadata": map[string]any{"name": names[i], "namespace": namespaces[i]}}})
			}
			write(w, map[string]any{"kind": "Table", "apiVersion": "meta.k8s.io/v1",
				"metadata": map[string]any{"continue": next}, "columnDefinitions": []any{map[string]any{"name": "Name"}}, "rows": rows})
			return
		}
		items := []any{}
		for i := from; i < to; i++ {
			items = append(items, map[string]any{"apiVersion": "v1", "kind": kind,
				"metadata": map[string]any{"name": names[i], "namespace": namespaces[i]},
				"spec":     map[string]any{"padding": padding}})
		}
		write(w, map[string]any{"apiVersion": "v1", "kind": kind + "List", "metadata": map[string]any{"continue": next}, "items": items})
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
	srv, err := New(Options{Clusters: manager, InCluster: true,
		Access: access.New(access.Options{Reviews: reviews.For, InCluster: true})})
	if err != nil {
		t.Fatal(err)
	}
	return srv.Host()
}

var (
	podsGVR  = schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	nodesGVR = schema.GroupVersionResource{Version: "v1", Resource: "nodes"}
)

func reader() *http.Request { return asPerson(person("rita", "oidc:readers")) }

// collect is an Each that keeps each object's namespace/name.
func collect(into *[]string) func(unstructured.Unstructured) error {
	return func(u unstructured.Unstructured) error {
		*into = append(*into, u.GetNamespace()+"/"+u.GetName())
		return nil
	}
}

// Every object is handed over once, across every page, each request asking for
// one page and not from the watch cache; Items stays empty.
func TestHostListEachWalksEveryPage(t *testing.T) {
	s := &eachServer{perNamespace: 300}
	var got []string
	page, err := s.host(t).List(reader(), "prod", extension.ListQuery{GVR: podsGVR, Each: collect(&got)})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 600 || !page.Complete || len(page.Items) != 0 {
		t.Fatalf("handed %d, complete %v, items %d", len(got), page.Complete, len(page.Items))
	}
	lists := 0
	for _, r := range s.seen() {
		if r.URL.Path != "/api/v1/pods" {
			continue
		}
		lists++
		q := r.URL.Query()
		if q.Get("limit") != strconv.Itoa(extension.EachPageSize) || q.Get("resourceVersion") != "" {
			t.Errorf("request %s: want limit %d and no resourceVersion", r.URL.RawQuery, extension.EachPageSize)
		}
	}
	if lists != 3 {
		t.Errorf("%d page requests, want 3 for 600 objects", lists)
	}
}

// A person who may list pods in shop only has shop's walked, and only shop's
// read: billing's objects never reach the console, let alone Each.
func TestHostListEachIsNarrowedBeforeAnyObjectIsRead(t *testing.T) {
	s := &eachServer{perNamespace: 5}
	var got []string
	page, err := s.host(t).List(asPerson(shopViewer()), "prod", extension.ListQuery{GVR: podsGVR, Each: collect(&got)})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 5 || !strings.HasPrefix(got[0], "shop/") || page.Complete || !strings.Contains(page.Note, "namespaces where you may") {
		t.Fatalf("handed %v, page %+v", got, page)
	}
	for _, r := range s.seen() {
		if r.URL.Path == "/api/v1/pods" && !strings.Contains(r.Header.Get("Accept"), "as=Table") {
			t.Errorf("the whole list was read as objects: %s", r.URL)
		}
		if strings.Contains(r.URL.Path, "/billing/") {
			t.Errorf("billing was read: %s", r.URL)
		}
	}
}

// A refusal is ErrDenied and reaches no object; a cluster-scoped kind denied
// is refused, not narrowed.
func TestHostListEachRefusal(t *testing.T) {
	s := &eachServer{perNamespace: 5, nodes: 3}
	h := s.host(t)
	called := false
	each := func(unstructured.Unstructured) error { called = true; return nil }
	q := extension.ListQuery{GVR: podsGVR, Namespace: "shop", Each: each}
	if _, err := h.List(asPerson(nobody()), "prod", q); !errors.Is(err, extension.ErrDenied) {
		t.Errorf("pods in shop for nobody: %v, want ErrDenied", err)
	}
	if _, err := h.List(asPerson(shopViewer()), "prod", extension.ListQuery{GVR: nodesGVR, Each: each}); !errors.Is(err, extension.ErrDenied) {
		t.Errorf("nodes for a shop viewer: %v, want ErrDenied", err)
	}
	if called {
		t.Error("Each was called for a refused list")
	}
	var got []string
	page, err := h.List(reader(), "prod", extension.ListQuery{GVR: nodesGVR, Each: collect(&got)})
	if err != nil || len(got) != 3 || !page.Complete {
		t.Errorf("nodes for a reader: %v, handed %v, complete %v", err, got, page.Complete)
	}
}

// Limit stops the walk, says so, and reads no further page or namespace.
func TestHostListEachLimit(t *testing.T) {
	s := &eachServer{perNamespace: 600}
	var got []string
	page, err := s.host(t).List(reader(), "prod", extension.ListQuery{GVR: podsGVR, Limit: 260, Each: collect(&got)})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 260 || page.Complete || !strings.Contains(page.Note, "first 260") {
		t.Fatalf("handed %d, page %+v", len(got), page)
	}
	if n := len(s.seen()); n > 3 {
		t.Errorf("%d requests for 260 objects, want at most two pages and discovery", n)
	}
}

// An error from Each stops the read at once and comes back as it is.
func TestHostListEachErrorComesBackAsItIs(t *testing.T) {
	s := &eachServer{perNamespace: 600}
	stop := errors.New("enough")
	n := 0
	_, err := s.host(t).List(reader(), "prod", extension.ListQuery{GVR: podsGVR, Each: func(unstructured.Unstructured) error {
		n++
		if n == 10 {
			return stop
		}
		return nil
	}})
	if err != stop {
		t.Fatalf("err %v, want Each's own error, unwrapped", err)
	}
	if lists := len(s.seen()); lists > 2 {
		t.Errorf("%d requests after Each stopped on the first page", lists)
	}
}

func TestHostListEachAndColumnsIsRefused(t *testing.T) {
	s := &eachServer{perNamespace: 1}
	q := extension.ListQuery{GVR: podsGVR, Columns: true, Each: func(unstructured.Unstructured) error { return nil }}
	if _, err := s.host(t).List(reader(), "prod", q); err == nil || len(s.seen()) != 0 {
		t.Fatalf("err %v, %d requests: want a refusal before any request", err, len(s.seen()))
	}
}

// What a walk keeps alive is a page, not the list: 2 000 objects of 16 KiB
// (31 MiB as JSON), and the heap the walk leaves alive stays under a page's
// worth several times over, far below the list's.
func TestHostListEachHoldsOnePageAtATime(t *testing.T) {
	if testing.Short() {
		t.Skip("a memory test")
	}
	const pad = 16 << 10
	s := &eachServer{perNamespace: 1000, pad: pad}
	h := s.host(t)
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	var peak uint64
	n := 0
	_, err := h.List(reader(), "prod", extension.ListQuery{GVR: podsGVR, Each: func(unstructured.Unstructured) error {
		n++
		if n%100 == 0 {
			runtime.GC()
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			if m.HeapAlloc > before.HeapAlloc && m.HeapAlloc-before.HeapAlloc > peak {
				peak = m.HeapAlloc - before.HeapAlloc
			}
		}
		return nil
	}})
	if err != nil || n != 2000 {
		t.Fatalf("err %v, handed %d", err, n)
	}
	whole := uint64(2000 * pad)
	t.Logf("peak retained %d MiB, the list %d MiB", peak>>20, whole>>20)
	if peak > whole/2 {
		t.Errorf("peak retained %d MiB while walking a %d MiB list: more than a page is kept", peak>>20, whole>>20)
	}
}

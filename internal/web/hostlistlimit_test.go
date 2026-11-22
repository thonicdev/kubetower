package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
)

// limitServer serves three pods, billing's first, and honors a list's limit
// with a continue token, as an API server does. It records every pod list's
// limit.
type limitServer struct {
	mu     sync.Mutex
	limits []string
}

func (l *limitServer) host(t *testing.T) extension.Host {
	t.Helper()
	pods := []any{
		map[string]any{"metadata": map[string]any{"name": "bill-0", "namespace": "billing"}},
		map[string]any{"metadata": map[string]any{"name": "api-0", "namespace": "shop"}},
		map[string]any{"metadata": map[string]any{"name": "api-1", "namespace": "shop"}},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/pods" {
			http.Error(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"NotFound","code":404}`, http.StatusNotFound)
			return
		}
		limit := r.URL.Query().Get("limit")
		l.mu.Lock()
		l.limits = append(l.limits, limit)
		l.mu.Unlock()
		items, next := pods, ""
		if n, err := strconv.Atoi(limit); err == nil && n > 0 && n < len(pods) && r.URL.Query().Get("continue") == "" {
			items, next = pods[:n], "more"
		} else if r.URL.Query().Get("continue") != "" {
			items = pods[1:]
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "v1", "kind": "PodList",
			"metadata": map[string]any{"resourceVersion": "9", "continue": next}, "items": items})
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

var podsQuery = extension.ListQuery{GVR: schema.GroupVersionResource{Version: "v1", Resource: "pods"}, Limit: 1}

// A person who may list the kind everywhere is answered with one request for
// the rows asked for, and the page says it was cut short.
func TestHostListWithALimitAsksTheClusterForThatManyRows(t *testing.T) {
	l := &limitServer{}
	page, err := l.host(t).List(asPerson(person("erin@example.com", "oidc:readers")), "prod", podsQuery)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page.Items) != 1 || page.Complete || page.Note == "" {
		t.Errorf("page %d items, complete %v, note %q: want one row, said to be cut short", len(page.Items), page.Complete, page.Note)
	}
	if len(l.limits) != 1 || l.limits[0] != "1" {
		t.Errorf("pod lists asked with limits %q, want one request with limit 1", l.limits)
	}
}

// A person who may list the kind only in some namespaces: a limit sent to the
// cluster would return billing's pod, which narrowing drops, and the page would
// read empty. The rows are cut after narrowing instead.
func TestHostListWithALimitKeepsTheRowsANarrowedPersonMaySee(t *testing.T) {
	l := &limitServer{}
	page, err := l.host(t).List(asPerson(shopViewer()), "prod", podsQuery)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].GetNamespace() != "shop" || page.Complete {
		t.Errorf("page %+v, want one of shop's pods, not complete", page)
	}
}

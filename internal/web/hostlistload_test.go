package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
)

// What reviewing an extension's list for the person costs: an extra that lists
// every ConfigMap of a cluster of 5 000, across 100 and then 200 namespaces,
// for one person. The reviews are per namespace and resource, held a minute,
// never per object; the time and the memory stay bounded; and past the
// namespaces a list is narrowed across, it is refused instead of reviewed.

const loadObjects = 5000

// loadCluster is an API server holding loadObjects ConfigMaps spread evenly
// over namespaces namespaces, answered in one page. lists counts the list
// requests it served.
func loadCluster(t *testing.T, namespaces int) (*cluster.Manager, *atomic.Int64) {
	t.Helper()
	items := make([]any, 0, loadObjects)
	for i := 0; i < loadObjects; i++ {
		items = append(items, map[string]any{"apiVersion": "v1", "kind": "ConfigMap",
			"metadata": map[string]any{"name": fmt.Sprintf("cm-%05d", i), "namespace": fmt.Sprintf("ns-%03d", i%namespaces),
				"uid": fmt.Sprintf("uid-%05d", i), "resourceVersion": "1"},
			"data": map[string]any{"key": strings.Repeat("v", 64)}})
	}
	body, err := json.Marshal(map[string]any{"apiVersion": "v1", "kind": "ConfigMapList",
		"metadata": map[string]any{"resourceVersion": "9"}, "items": items})
	if err != nil {
		t.Fatal(err)
	}
	lists := &atomic.Int64{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/configmaps":
			lists.Add(1)
			_, _ = w.Write(body)
		case "/api/v1":
			_ = json.NewEncoder(w).Encode(map[string]any{"kind": "APIResourceList", "groupVersion": "v1",
				"resources": []any{map[string]any{"name": "configmaps", "namespaced": true, "kind": "ConfigMap",
					"verbs": []string{"get", "list"}}}})
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
	return manager, lists
}

// evenNamespaces lets grace list ConfigMaps in the even-numbered namespaces
// only, and not across the cluster.
func evenNamespaces(q accesstest.Question) (bool, error) {
	if q.Self || q.Attributes.Resource != "configmaps" || q.Attributes.Namespace == "" {
		return q.Self, nil
	}
	var n int
	if _, err := fmt.Sscanf(q.Attributes.Namespace, "ns-%03d", &n); err != nil {
		return false, nil
	}
	return n%2 == 0, nil
}

// measuredList is one List with what it took: the time and the bytes allocated.
func measuredList(t *testing.T, h extension.Host) (extension.Page, time.Duration, uint64, error) {
	t.Helper()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	page, err := h.List(asPerson(grace()), "prod", extension.ListQuery{GVR: schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}})
	took := time.Since(start)
	runtime.ReadMemStats(&after)
	return page, took, after.TotalAlloc - before.TotalAlloc, err
}

// The ceilings are generous for a loaded CI runner under -race: a one-minute
// review cache that stopped holding, or a review per object, is two orders of
// magnitude past them.
const (
	loadTimeCeiling  = 20 * time.Second
	loadAllocCeiling = 512 << 20
)

func TestAnExtensionsListIsReviewedPerNamespaceNotPerObject(t *testing.T) {
	if testing.Short() {
		t.Skip("a load test")
	}
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	t.Run("allowed across the cluster", func(t *testing.T) {
		manager, lists := loadCluster(t, 200)
		reviews := &accesstest.Reviews{}
		s, err := New(Options{Clusters: manager, InCluster: true,
			Access: access.New(access.Options{Reviews: reviews.For, InCluster: true})})
		if err != nil {
			t.Fatal(err)
		}
		page, took, alloc, err := measuredList(t, s.hostFor(fixtureKey))
		if err != nil || len(page.Items) != loadObjects || !page.Complete {
			t.Fatalf("List answered %d items, complete %t, %v", len(page.Items), page.Complete, err)
		}
		if got := len(reviews.Asked()); got != 1 {
			t.Errorf("%d reviews for one list allowed across the cluster, want 1", got)
		}
		if took > loadTimeCeiling || alloc > loadAllocCeiling {
			t.Errorf("one list of %d took %s and %d MiB, past %s and %d MiB", loadObjects, took, alloc>>20, loadTimeCeiling, loadAllocCeiling>>20)
		}
		t.Logf("%d objects, 200 namespaces, allowed: %s, %d MiB allocated, %d review, %d list", loadObjects, took, alloc>>20, len(reviews.Asked()), lists.Load())
	})

	t.Run("narrowed to half of 100 namespaces", func(t *testing.T) {
		manager, lists := loadCluster(t, 100)
		reviews := &accesstest.Reviews{Answer: evenNamespaces}
		s, err := New(Options{Clusters: manager, InCluster: true,
			Access: access.New(access.Options{Reviews: reviews.For, InCluster: true})})
		if err != nil {
			t.Fatal(err)
		}
		h := s.hostFor(fixtureKey)
		page, took, alloc, err := measuredList(t, h)
		if err != nil || len(page.Items) != loadObjects/2 || page.Complete {
			t.Fatalf("List answered %d items, complete %t, %v; want %d, narrowed", len(page.Items), page.Complete, err, loadObjects/2)
		}
		for _, item := range page.Items {
			var n int
			if _, err := fmt.Sscanf(item.GetNamespace(), "ns-%03d", &n); err != nil || n%2 != 0 {
				t.Fatalf("the narrowed list holds %s/%s, a namespace grace may not list", item.GetNamespace(), item.GetName())
			}
		}
		// One review across the cluster, then one per namespace: 100
		// namespaces x 1 resource, whatever the 5 000 objects.
		first := len(reviews.Asked())
		if first != 1+100 {
			t.Errorf("%d reviews for one narrowed list, want 1 + 100 namespaces", first)
		}
		if took > loadTimeCeiling || alloc > loadAllocCeiling {
			t.Errorf("one narrowed list of %d took %s and %d MiB, past %s and %d MiB", loadObjects, took, alloc>>20, loadTimeCeiling, loadAllocCeiling>>20)
		}
		// Asked again within the minute: read again, reviewed from the held
		// answers.
		if _, err := h.List(asPerson(grace()), "prod", extension.ListQuery{GVR: schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}}); err != nil {
			t.Fatal(err)
		}
		if again := len(reviews.Asked()) - first; again != 0 {
			t.Errorf("the same list asked again within the minute made %d reviews, want 0", again)
		}
		t.Logf("%d objects, 100 namespaces, narrowed: %s, %d MiB allocated, %d reviews, %d lists", loadObjects, took, alloc>>20, first, lists.Load())
	})

	t.Run("narrowed across 200 namespaces", func(t *testing.T) {
		manager, _ := loadCluster(t, 200)
		reviews := &accesstest.Reviews{Answer: evenNamespaces}
		s, err := New(Options{Clusters: manager, InCluster: true,
			Access: access.New(access.Options{Reviews: reviews.For, InCluster: true})})
		if err != nil {
			t.Fatal(err)
		}
		_, took, alloc, err := measuredList(t, s.hostFor(fixtureKey))
		var refused *access.NotNarrowed
		if !errors.As(err, &refused) {
			t.Fatalf("a list narrowed across 200 namespaces answered %v, want a refusal asking for a namespace", err)
		}
		// Past the namespaces a list is narrowed across, nothing is reviewed
		// one namespace at a time: the cluster-wide review alone.
		if got := len(reviews.Asked()); got != 1 {
			t.Errorf("%d reviews before the refusal, want the 1 across the cluster", got)
		}
		if took > loadTimeCeiling || alloc > loadAllocCeiling {
			t.Errorf("the refused list took %s and %d MiB, past %s and %d MiB", took, alloc>>20, loadTimeCeiling, loadAllocCeiling>>20)
		}
		t.Logf("%d objects, 200 namespaces, narrowed: refused in %s, %d MiB allocated: %v", loadObjects, took, alloc>>20, err)
	})
}

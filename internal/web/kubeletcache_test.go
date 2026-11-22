package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/identity"
	"github.com/thonicdev/kubetower/internal/kubeletstats"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// The kubelet cache: two views
// inside the TTL cost one kubelet round, and what it holds is never the
// answer's filter - each person still gets only the pods of the namespaces
// they may list.

// kubeletServer is an API server whose kubelets, reached through the node
// proxy, name pods in "shop" and "billing", and which counts every summary it
// is asked for, by node.
type kubeletServer struct {
	mu    sync.Mutex
	reads map[string]int
}

func (k *kubeletServer) count(node string) int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.reads[node]
}

func (k *kubeletServer) total() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	n := 0
	for _, c := range k.reads {
		n += c
	}
	return n
}

func kubeletManager(t *testing.T) (*cluster.Manager, *kubeletServer) {
	t.Helper()
	fake := &kubeletServer{reads: map[string]int{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/nodes" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "v1", "kind": "NodeList",
				"metadata": map[string]any{"resourceVersion": "1"},
				"items": []any{map[string]any{"metadata": map[string]any{"name": "node-1"},
					"status": map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}}}})
			return
		}
		// /api/v1/nodes/<node>/proxy/stats/summary
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) != 7 || parts[2] != "nodes" || parts[4] != "proxy" || parts[6] != "summary" {
			http.Error(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"NotFound","code":404}`, http.StatusNotFound)
			return
		}
		node := parts[3]
		if node == "node-refused" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","code":403,` +
				`"message":"nodes \"node-refused\" is forbidden: User \"sa\" cannot get resource \"nodes/proxy\""}`))
			return
		}
		fake.mu.Lock()
		fake.reads[node]++
		fake.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"node": map[string]any{"nodeName": node, "fs": map[string]any{"usedBytes": 10, "capacityBytes": 100}},
			"pods": []any{
				map[string]any{"podRef": map[string]any{"name": "web-" + node, "namespace": "shop"}},
				map[string]any{"podRef": map[string]any{"name": "ledger-" + node, "namespace": "billing"}},
			},
		})
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
	return manager, fake
}

// kubeletHost is the console's Host over kubeletServer under the server
// profile: everybody may list nodes; shop's people may list pods in shop
// only, billing's in billing only.
func kubeletHost(t *testing.T) (*Server, *kubeletServer) {
	t.Helper()
	manager, fake := kubeletManager(t)
	reviews := &accesstest.Reviews{Answer: func(q accesstest.Question) (bool, error) {
		if q.Attributes.Resource == "nodes" {
			return true, nil
		}
		for _, group := range q.Groups {
			if group == "oidc:"+q.Attributes.Namespace {
				return true, nil
			}
		}
		return false, nil
	}}
	s, err := New(Options{Clusters: manager, InCluster: true,
		Access: access.New(access.Options{Reviews: reviews.For, InCluster: true})})
	if err != nil {
		t.Fatal(err)
	}
	return s, fake
}

func usageRequest(name, group string) *http.Request {
	ctx := identity.With(context.Background(), identity.Who{Name: name, Method: identity.SSO, Groups: []string{"oidc:" + group}})
	return httptest.NewRequest(http.MethodGet, "/x/prod/kubeadjust/", nil).WithContext(ctx)
}

func usagePods(t *testing.T, s *Server, r *http.Request, nodes ...string) []string {
	t.Helper()
	got, err := s.hostFor("kubeadjust").Usage(r, "prod", nodes)
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if got.Asked != len(nodes) || got.Answered != len(nodes) {
		t.Errorf("asked %d answered %d, want %d of %d", got.Asked, got.Answered, len(nodes), len(nodes))
	}
	var names []string
	for _, node := range got.Nodes {
		for _, p := range node.Pods {
			names = append(names, p.Namespace+"/"+p.Name)
		}
	}
	sort.Strings(names)
	return names
}

func TestTwoUsageReadsInsideTheTTLAskEachKubeletOnce(t *testing.T) {
	s, fake := kubeletHost(t)
	alice := usageRequest("alice@example.com", "shop")
	usagePods(t, s, alice, "node-1", "node-2")
	usagePods(t, s, alice, "node-1", "node-2")
	if fake.count("node-1") != 1 || fake.count("node-2") != 1 {
		t.Errorf("two views inside the TTL read node-1 %d times and node-2 %d times, want once each",
			fake.count("node-1"), fake.count("node-2"))
	}

	// Past the TTL the kubelet is asked again: the cache states the
	// resolution the figures have, it does not freeze them.
	later := time.Now().Add(usageTTL + time.Second)
	s.kubelets.now = func() time.Time { return later }
	usagePods(t, s, alice, "node-1")
	if fake.count("node-1") != 2 {
		t.Errorf("past the TTL node-1 was read %d times in all, want 2", fake.count("node-1"))
	}
}

// The cache holds the kubelet's whole answer, and each person still
// gets only the pods of the namespaces they may list - the second person is
// answered from what the first one's read put in the cache, and sees none of
// the first one's pods.
func TestTheKubeletCacheNeverServesOnePersonAnothersPods(t *testing.T) {
	s, fake := kubeletHost(t)
	alice := usagePods(t, s, usageRequest("alice@example.com", "shop"), "node-1")
	bob := usagePods(t, s, usageRequest("bob@example.com", "billing"), "node-1")
	if fake.total() != 1 {
		t.Fatalf("the kubelet was read %d times; the second person was meant to be answered from the cache", fake.total())
	}
	if strings.Join(alice, ",") != "shop/web-node-1" {
		t.Errorf("alice, who may list pods in shop only, got %v", alice)
	}
	if strings.Join(bob, ",") != "billing/ledger-node-1" {
		t.Errorf("bob, who may list pods in billing only, got %v", bob)
	}
}

// The Nodes list's Disk column and Host.Usage read the same kubelets: through
// one cache, so a list refresh and an extension's view inside the TTL cost one
// read of each.
func TestTheDiskColumnAndHostUsageShareOneKubeletRead(t *testing.T) {
	s, fake := kubeletHost(t)
	usagePods(t, s, usageRequest("alice@example.com", "shop"), "node-1")
	disk := s.nodeDisk(context.Background(), "prod")
	if d, ok := disk["node-1"]; !ok || d.UsedBytes != 10 || d.CapacityBytes != 100 {
		t.Errorf("the Disk column read %+v for node-1", disk)
	}
	if fake.count("node-1") != 1 {
		t.Errorf("node-1 was read %d times by a view and a Disk refresh inside the TTL, want once", fake.count("node-1"))
	}
}

// Readers arriving together while a read is in flight wait for it rather than
// each asking: a screen of tiles is many requests in the same instant.
func TestConcurrentUsageReadsShareTheReadInFlight(t *testing.T) {
	s, fake := kubeletHost(t)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			usagePods(t, s, usageRequest("alice@example.com", "shop"), "node-1")
		}()
	}
	wg.Wait()
	if fake.count("node-1") != 1 {
		t.Errorf("eight readers at once read node-1 %d times, want once", fake.count("node-1"))
	}
}

// A kubelet's refusal reaches an extra through Host.Usage as the API server's
// 403, so it can be told apart with apierrors.IsForbidden, under the same
// short sentence the console draws.
func TestAKubeletRefusalReachesAnExtraAsAForbidden(t *testing.T) {
	s, _ := kubeletHost(t)
	got, err := s.hostFor("kubeadjust").Usage(usageRequest("alice@example.com", "shop"), "prod", []string{"node-1", "node-refused"})
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if got.Asked != 2 || got.Answered != 1 || got.FirstErr == nil {
		t.Fatalf("asked %d answered %d, FirstErr %v; want 1 of 2 and the refusal kept", got.Asked, got.Answered, got.FirstErr)
	}
	if !apierrors.IsForbidden(got.FirstErr) {
		t.Errorf("apierrors.IsForbidden(%q) = false; want the kubelet's 403 classifiable", got.FirstErr)
	}
	var status *apierrors.StatusError
	if !errors.As(got.FirstErr, &status) {
		t.Fatalf("errors.As(%q, *StatusError) = false", got.FirstErr)
	}
	if want := "node-refused: " + kubeletstats.ShortError(status); got.FirstErr.Error() != want {
		t.Errorf("FirstErr reads %q; want %q", got.FirstErr, want)
	}
}

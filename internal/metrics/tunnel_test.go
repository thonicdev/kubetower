package metrics

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
)

// TestAPodTunnelDigsToThePodItNames drives forwarderTunnels.OpenPod through the
// real forwarder and a cluster manager's real client, against an API server
// that records every request and refuses every upgrade. What is asserted is
// where the tunnel went: the pod's portforward subresource, reviewed on that
// pod, and nothing else - no Service read, no pod list. A tunneller that asked
// for a Service by that name instead would read services/api-0 first.
func TestAPodTunnelDigsToThePodItNames(t *testing.T) {
	// The loopback rewrite must not move this address.
	t.Setenv("KT_IN_CONTAINER", "0")

	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path)
		mu.Unlock()
		http.Error(w, "no upgrade here", http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)

	kubeconfig := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(kubeconfig, []byte(fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: tunnel
  cluster: {server: %q}
users:
- name: tunnel
  user: {token: not-a-real-token}
contexts:
- name: tunnel
  context: {cluster: tunnel, user: tunnel}
current-context: tunnel
`, srv.URL)), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := cluster.NewManager(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	reviews := &accesstest.Reviews{}
	tunnels, ok := managerTunnels(manager, reviews.Gate(), func(string, ...any) {}).(PodTunneller)
	if !ok {
		t.Fatal("the forwarder's tunneller opens no tunnel to a pod")
	}

	if tunnel, err := tunnels.OpenPod(context.Background(), "tunnel", "shop", "api-0", 6060); err == nil {
		tunnel.Close()
		t.Fatal("a server that refuses every upgrade gave a tunnel")
	}

	mu.Lock()
	got := append([]string(nil), seen...)
	mu.Unlock()
	const path = "/api/v1/namespaces/shop/pods/api-0/portforward"
	if len(got) == 0 {
		t.Fatal("the tunnel never reached the cluster")
	}
	for _, request := range got {
		if request != http.MethodGet+" "+path && request != http.MethodPost+" "+path {
			t.Errorf("the cluster received %q, want only the upgrade to %s", request, path)
		}
	}
	asked := reviews.Asked()
	if len(asked) == 0 {
		t.Fatal("the tunnel was dug without a review")
	}
	for _, q := range asked {
		a := q.Attributes
		if a.Verb != "create" || a.Resource != "pods" || a.Subresource != "portforward" || a.Namespace != "shop" || a.Name != "api-0" {
			t.Errorf("reviewed as %+v, want create pods/portforward on shop/api-0", a)
		}
	}
}

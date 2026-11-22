package cluster

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
)

// withInClusterConfig replaces client-go's reading of the ServiceAccount mount
// for one test. The real one reads a fixed path under /var/run/secrets and two
// environment variables the kubelet sets, and neither exists on a developer's
// machine or on the runner.
func withInClusterConfig(t *testing.T, source func() (*rest.Config, error)) {
	t.Helper()
	was := inClusterConfig
	inClusterConfig = source
	t.Cleanup(func() { inClusterConfig = was })
}

// The server profile's manager is one cluster, `in-cluster`, and it answers
// through the pod's own identity rather than through a kubeconfig.
//
// **The token is asserted as a file, not as a value.** The projected token
// lives an hour and the kubelet rotates it; a client that copied the token out
// of the file once would stop authenticating an hour after the pod started, and
// every test that runs in less than an hour would pass. What makes rotation work
// is that `BearerTokenFile` survives into the config the clients are built
// from, so client-go re-reads it - that is what is checked here, and the
// request's header is checked to prove the file is what is presented.
func TestTheServerProfileReachesItsOwnClusterWithoutAKubeconfig(t *testing.T) {
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("first-token"), 0o600); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var seen []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"kind":"NamespaceList","apiVersion":"v1","metadata":{},"items":[{"metadata":{"name":"default"}}]}`))
	}))
	t.Cleanup(api.Close)

	withInClusterConfig(t, func() (*rest.Config, error) {
		return &rest.Config{Host: api.URL, BearerTokenFile: token}, nil
	})
	// And no kubeconfig anywhere this process could find one, so that a pass
	// cannot come from the merge by accident.
	t.Setenv(EnvKubeconfig, filepath.Join(t.TempDir(), "absent"))

	m, err := NewInClusterManager()
	if err != nil {
		t.Fatalf("NewInClusterManager: %v", err)
	}
	if !m.InCluster() {
		t.Error("the manager does not say it is in-cluster")
	}
	if got := m.Contexts(); len(got) != 1 || got[0] != InClusterContext {
		t.Fatalf("contexts = %v, want exactly [%s] - the cluster it runs in and no other", got, InClusterContext)
	}
	if !m.Known(InClusterContext) {
		t.Errorf("%s is not Known, so every request naming it would be a 404", InClusterContext)
	}
	if m.Known("prod") {
		t.Error("a context that is not the pod's own is Known")
	}

	for _, streaming := range []bool{false, true} {
		client, err := m.client(InClusterContext, streaming)
		if err != nil {
			t.Fatalf("client(streaming=%v): %v", streaming, err)
		}
		if client.Config.BearerTokenFile != token {
			t.Errorf("streaming=%v: BearerTokenFile = %q, want %q - a token copied out once "+
				"stops working when the kubelet rotates it", streaming, client.Config.BearerTokenFile, token)
		}
		if streaming && client.Config.Timeout != 0 {
			t.Errorf("the streaming client carries a timeout of %s", client.Config.Timeout)
		}
		if !streaming && client.Config.Timeout == 0 {
			t.Error("the ordinary client carries no timeout, so a cluster that stops answering holds the page")
		}
		list, err := client.Clients.CoreV1().Namespaces().List(context.Background(), metav1.ListOptions{})
		if err != nil {
			t.Fatalf("streaming=%v: listing namespaces: %v", streaming, err)
		}
		if len(list.Items) != 1 {
			t.Errorf("streaming=%v: %d namespaces, want the one the server sent", streaming, len(list.Items))
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 || seen[0] != "Bearer first-token" {
		t.Errorf("Authorization = %v, want the token from the file", seen)
	}
}

// Outside a pod the profile refuses to start, with client-go's own reason in the
// sentence, rather than drawing one cluster that will never answer.
func TestTheServerProfileRefusesOutsideAPod(t *testing.T) {
	withInClusterConfig(t, func() (*rest.Config, error) {
		return nil, rest.ErrNotInCluster
	})
	_, err := NewInClusterManager()
	if !errors.Is(err, rest.ErrNotInCluster) {
		t.Fatalf("err = %v, want it to carry rest.ErrNotInCluster", err)
	}
	if !strings.Contains(err.Error(), "--incluster") {
		t.Errorf("the refusal does not name the flag that asked for it: %v", err)
	}
}

// Reload is how a kubeconfig change is picked up. Under the profile there is no
// file, and a Reload that went looking for one would fail the whole manager.
func TestReloadUnderTheServerProfileKeepsItsOneCluster(t *testing.T) {
	withInClusterConfig(t, func() (*rest.Config, error) {
		return &rest.Config{Host: "https://10.96.0.1:443"}, nil
	})
	m, err := NewInClusterManager()
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if got := m.Contexts(); len(got) != 1 || got[0] != InClusterContext {
		t.Errorf("after Reload, contexts = %v", got)
	}
	if m.Loopback(InClusterContext) {
		t.Error("the cluster service address reads as the loopback")
	}
}

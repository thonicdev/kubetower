package changes

// Read must use the cluster's held dynamic client. One built from client.Config
// on every call - one per drawer - never hits client-go's transport cache,
// because cluster.Manager sets Dial on that config: a new http.Transport, a new
// TLS handshake and a connection held idle for 90 s, every time.
//
// Counted on the wire, through the real manager, against a TLS server.

import (
	"context"
	"runtime"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/cluster/clustertest"
)

func TestReadGoesThroughTheClustersOneConnection(t *testing.T) {
	// 127.0.0.1 is this test's server; the loopback rewrite must not fire.
	t.Setenv("KT_IN_CONTAINER", "0")

	server := clustertest.Start(t, "test")
	clusters, err := cluster.NewManager(server.Kubeconfig)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	reader := NewReader(clusters, accesstest.Allow())
	// The mapper is supplied so that every connection counted is one a read
	// opened; discovery is a separate path with its own cache (kindref).
	reader.newMapper = func(context.Context, *cluster.Client) (meta.RESTMapper, error) {
		return mapperFor(schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}), nil
	}

	ctx := context.Background()
	read := func() {
		t.Helper()
		report, err := reader.Read(ctx, "test", Request{Kind: "configmaps", Namespace: "default", Name: "settings"})
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if report.Name != "settings" {
			t.Fatalf("Read answered about %q, want the object asked for", report.Name)
		}
	}

	read()
	if n := server.Opened(); n != 1 {
		t.Fatalf("one read opened %d connections; the fixture is not what this test assumes", n)
	}
	baseline := runtime.NumGoroutine()

	const calls = 30
	for range calls {
		read()
	}

	if n := server.Opened(); n != 1 {
		t.Errorf("%d reads opened %d connections to the API server, want 1. Each extra one is "+
			"a client built from client.Config instead of the cluster's own", calls+1, n)
	}
	// Three goroutines per leaked connection, so a per-call client shows as ~90.
	if grown := runtime.NumGoroutine() - baseline; grown > 5 {
		t.Errorf("goroutines grew by %d over %d reads, want them back at the baseline of %d", grown, calls, baseline)
	}
}

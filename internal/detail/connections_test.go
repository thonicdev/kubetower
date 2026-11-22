package detail

// One connection per cluster, from the caller's side.
//
// internal/cluster proves the manager holds one dynamic client per cluster
// (TestAClusterHoldsOneDynamicClientAndOneTransport). That is not the same as
// Locate using it: a client built from client.Config on every call never hits
// client-go's transport cache, because the manager sets Dial on that config -
// a new http.Transport, a new TLS handshake and a connection held idle for 90 s
// per drawer, per envFrom reference, per target of a bulk action: 500 calls,
// 500 connections, 18 to 1518 goroutines.
//
// So this counts on the wire, through the real manager, against a TLS server.

import (
	"context"
	"runtime"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/cluster/clustertest"
)

var configMapKind = schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}

func TestLocateReadsThroughTheClustersOneConnection(t *testing.T) {
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
		return mapperKnowing(configMapKind), nil
	}

	ctx := context.Background()
	read := func() Location {
		t.Helper()
		where, err := reader.Locate(ctx, "test", "configmaps")
		if err != nil {
			t.Fatalf("Locate: %v", err)
		}
		iface, err := where.Interface("default")
		if err != nil {
			t.Fatalf("Interface: %v", err)
		}
		if _, err := iface.Get(ctx, "settings", metav1.GetOptions{}); err != nil {
			t.Fatalf("reading through the located client: %v", err)
		}
		return where
	}

	// The first read opens the cluster's connection, and that one is expected.
	where := read()
	held, err := clusters.Client("test")
	if err != nil {
		t.Fatalf("Client: %v", err)
	}
	if where.Dynamic != held.Dynamic {
		t.Error("Locate handed out a dynamic client that is not the one the cluster holds")
	}
	if n := server.Opened(); n != 1 {
		t.Fatalf("one read opened %d connections; the fixture is not what this test assumes", n)
	}
	baseline := runtime.NumGoroutine()

	const calls = 30
	for range calls {
		read()
	}

	if n := server.Opened(); n != 1 {
		t.Errorf("%d Locate+Get opened %d connections to the API server, want 1. Each extra one "+
			"is a client built from client.Config instead of the cluster's own: a new "+
			"transport, a new TLS handshake and a connection held idle for 90 s", calls+1, n)
	}
	// Three goroutines per leaked connection (the transport's read and write
	// loops and the server's side), so a per-call client shows as ~90 here. The
	// slack is for anything unrelated that the runtime starts or stops.
	if grown := runtime.NumGoroutine() - baseline; grown > 5 {
		t.Errorf("goroutines grew by %d over %d reads, want them back at the baseline of %d", grown, calls, baseline)
	}
}

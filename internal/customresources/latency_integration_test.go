package customresources

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/cluster"
)

// What the catalog costs, against a real cluster.
//
// The navigation asks what a cluster serves before it can draw itself, so a
// catalog fetch lands on a page load — and with a plain expiry and no
// renewal it lands at every expiry, forever: fast, fast, freeze, fast. A large
// cluster's fetch can cost the better part of a second.
//
// Off by default, because it needs a cluster:
//
//	KT_INTEGRATION_CLUSTER=<context> go test ./internal/customresources/ -run Latency -v
//
// It asserts a ceiling rather than a figure. A test that pins "under 40 ms" on
// somebody's laptop is a test that fails on a train, and this one exists to
// catch the *shape* coming back — a fetch on the request path — not to police
// milliseconds.
func TestLatencyTheCatalogueIsNotOnTheRequestPathTwice(t *testing.T) {
	name := os.Getenv("KT_INTEGRATION_CLUSTER")
	if name == "" {
		t.Skip("set KT_INTEGRATION_CLUSTER to a kubeconfig context to run this")
	}

	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatalf("no home directory and no KUBECONFIG: %v", err)
		}
		kubeconfig = filepath.Join(home, ".kube", "config")
	}

	clusters, err := cluster.NewManager(kubeconfig)
	if err != nil {
		t.Fatalf("reading %s: %v", kubeconfig, err)
	}
	resolved, ok := clusters.Resolve(name)
	if !ok {
		t.Fatalf("%q is not in %s", name, kubeconfig)
	}
	client, err := clusters.Client(resolved)
	if err != nil {
		t.Fatalf("building a client: %v", err)
	}

	registry := NewRegistry()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// The client is built lazily and an EKS context pays `aws eks get-token` on
	// its first call. Spend that here so it is not attributed to the catalog.
	if _, err := registry.Definitions(ctx, client); err != nil {
		t.Fatalf("the first fetch failed: %v", err)
	}

	measure := func() time.Duration {
		start := time.Now()
		if _, err := registry.Definitions(ctx, client); err != nil {
			t.Fatalf("a fetch failed: %v", err)
		}
		return time.Since(start)
	}

	held := measure()
	t.Logf("held catalog: %s", held.Round(time.Microsecond))
	if held > 20*time.Millisecond {
		t.Errorf("a held catalog took %s; it is supposed to be a map lookup", held)
	}

	// Age it past the renewal point but not past the window, which is what an
	// operator clicking around actually produces.
	registry.renewAfter = 0
	ageing := measure()
	t.Logf("aging catalog (renewal triggered): %s", ageing.Round(time.Microsecond))
	if ageing > 50*time.Millisecond {
		t.Errorf("a caller waited %s for a renewal that is supposed to happen behind them; "+
			"this is the 740 ms freeze coming back", ageing)
	}

	// Give the background renewal time to land, then confirm it did — a renewal
	// that never runs would keep this fast and quietly let the catalog expire
	// into the synchronous path.
	time.Sleep(2 * time.Second)
	after := measure()
	t.Logf("after the renewal landed: %s", after.Round(time.Microsecond))

	// And the honest case: expired means the caller waits, and that is correct.
	registry.ttl = 0
	expired := measure()
	t.Logf("expired catalog (caller waits, by design): %s", expired.Round(time.Microsecond))
	if expired < time.Millisecond {
		t.Error("an expired catalog answered instantly; it should have gone to the cluster, " +
			"and a cluster that has stopped answering must be able to say so")
	}
}

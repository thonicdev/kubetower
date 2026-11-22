package metrics

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/cluster"
)

// What a Prometheus query costs, against a real cluster.
//
// Without the pool, Query and QueryRange each open a session and close it again
// around the one question they are asked, and on an EKS context a session is
// often a port-forward: a SPDY upgrade and
// roughly 1.8 s to come up. One overview render is up to four of those — two
// range queries for the history charts, two instant ones for the hottest pods —
// and the panel on a resource is two more, every fifteen seconds, per open tab.
//
// Off by default, because it needs a cluster with a Prometheus in it:
//
//	KT_INTEGRATION_CLUSTER=example-tools go test ./internal/metrics/ -run Latency -v
//
// It asserts a ratio rather than a figure. A test that pins "under 40 ms" on
// somebody's laptop is a test that fails on a train; this one exists to catch the
// *shape* coming back — a tunnel per query — and it measures that shape in the
// same run so the comparison is against this cluster on this day and not against
// a number somebody wrote down once.
func TestLatencyAQueryDoesNotPayForItsOwnTunnel(t *testing.T) {
	name := os.Getenv("KT_INTEGRATION_CLUSTER")
	if name == "" {
		t.Skip("set KT_INTEGRATION_CLUSTER to a kubeconfig context with a Prometheus to run this")
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

	m := New(Options{Clusters: clusters, Logf: t.Logf})
	t.Cleanup(m.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Spend the discovery and the exec credential plugin here so neither is
	// attributed to a query. An EKS context pays `aws eks get-token` on its first
	// call.
	found := m.Discover(ctx, resolved, true)
	if found.Kind != KindPrometheus {
		t.Skipf("%s reports %q (%s); this measurement needs a Prometheus", resolved, found.Kind, found.Detail)
	}
	t.Logf("%s: %s reached by %s", resolved, found.Prometheus.Address(), found.Transport)

	measure := func(what string, run func() error) time.Duration {
		start := time.Now()
		if err := run(); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		took := time.Since(start)
		t.Logf("%-34s %s", what+":", took.Round(time.Millisecond))
		return took
	}

	// The baseline for comparison: a session opened and closed around one
	// question.
	throwaway := measure("a query owning its own session", func() error {
		session, err := m.OpenSession(ctx, resolved)
		if err != nil {
			return err
		}
		defer session.Close()
		_, err = session.Query(ctx, "up")
		return err
	})

	// The new one. The first pays for the tunnel; the rest must not.
	first := measure("the first pooled query", func() error {
		_, err := m.Query(ctx, resolved, "up")
		return err
	})

	var repeats time.Duration
	const rounds = 5
	for i := range rounds {
		repeats += measure("a repeat on the live session", func() error {
			_, err := m.Query(ctx, resolved, "up")
			return err
		})
		_ = i
	}
	repeat := repeats / rounds

	m.pool.mu.Lock()
	held := len(m.pool.live)
	m.pool.mu.Unlock()
	if held != 1 {
		t.Errorf("%d sessions are held after %d queries against one cluster, want 1", held, rounds+1)
	}

	if found.Transport != TransportForward {
		t.Logf("this cluster answers through the API service proxy, where a session costs nothing; " +
			"the ratio below is only meaningful on the port-forward transport")
		return
	}

	// The whole point, stated as a ratio: a query that reuses the session must be
	// a fraction of one that stands a port-forward up for itself.
	if repeat*2 > throwaway {
		t.Errorf("a repeat query took %s against %s for one that opens its own tunnel; "+
			"the session is not being reused",
			repeat.Round(time.Millisecond), throwaway.Round(time.Millisecond))
	}
	t.Logf("saved per query: %s (first query %s, repeat %s, throwaway %s)",
		(throwaway - repeat).Round(time.Millisecond),
		first.Round(time.Millisecond), repeat.Round(time.Millisecond), throwaway.Round(time.Millisecond))

	// And the honest case: once the session is let go, the next query pays again.
	// A pool that never released would be a listener and a SPDY stream held into
	// every cluster the operator ever looked at.
	m.Close()
	m.pool.mu.Lock()
	held = len(m.pool.live)
	m.pool.mu.Unlock()
	if held != 0 {
		t.Fatalf("%d sessions survived Close; each holds a listener and a stream into the cluster", held)
	}
	after := measure("the first query after Close", func() error {
		_, err := m.Query(ctx, resolved, "up")
		return err
	})
	if after < repeat {
		t.Error("a query after Close was as cheap as one on a live session, so nothing was released")
	}
}

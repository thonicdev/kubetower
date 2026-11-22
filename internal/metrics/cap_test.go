package metrics

// The cap on what Prometheus may send back, on both transports. A Prometheus
// that is not the cluster's own - the decoy choice_test.go is about - can
// answer with any size it likes, and an uncapped service proxy reads all of it.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/internal/cluster"
)

// oversized serves a health check, and a query answer one byte over or exactly
// at the cap.
func oversized(over bool) http.HandlerFunc {
	size := maxPromResponse
	if over {
		size++
	}
	answer := strings.Repeat("x", size)
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, endpointHealth) {
			_, _ = w.Write([]byte("Prometheus is Healthy."))
			return
		}
		_, _ = w.Write([]byte(answer))
	}
}

// Through a port-forward. Cutting the body at the cap and handing back what was
// read as though it were the answer would be wrong: a truncated answer is not
// an answer.
func TestAnOversizedAnswerThroughThePortForwardIsRefused(t *testing.T) {
	for _, over := range []bool{false, true} {
		prometheus := httptest.NewServer(oversized(over))
		t.Cleanup(prometheus.Close)

		h := newHarness(t, promService("monitoring", "prometheus", map[string]string{"app": "prometheus"}, "web", 9090))
		h.tunnels.addr = strings.TrimPrefix(prometheus.URL, "http://")

		body, err := h.metrics.Query(context.Background(), testCluster, "up")
		switch {
		case over && (!errors.Is(err, errTooLarge) || body != nil):
			t.Errorf("an answer one byte over the cap gave %d bytes and %v; want nothing and errTooLarge", len(body), err)
		case !over && (err != nil || len(body) != maxPromResponse):
			t.Errorf("an answer exactly at the cap gave %d bytes, %v; want all of it", len(body), err)
		}
	}
}

// Through the API server's service proxy, which is the transport preferred
// whenever the API server allows it - and the one where client-go, left alone,
// reads the whole body, whatever it is.
func TestAnOversizedAnswerThroughTheServiceProxyIsRefused(t *testing.T) {
	target := decoy()[0]
	for _, over := range []bool{false, true} {
		client := rawCluster(t, oversized(over))

		body, err := restRaw(context.Background(), client, target.proxyPath(endpointQuery), nil)
		switch {
		case over && (!errors.Is(err, errTooLarge) || body != nil):
			t.Errorf("an answer one byte over the cap gave %d bytes and %v; want nothing and errTooLarge", len(body), err)
		case !over && (err != nil || len(body) != maxPromResponse):
			t.Errorf("an answer exactly at the cap gave %d bytes, %v; want all of it", len(body), err)
		}
	}
}

// And the same through the whole path a chart takes: a configured target on
// the service proxy, a session from the pool, Query - with the cluster's own
// REST client against a server standing in for the API server, so the cap is
// the one restRaw puts in front of client-go and not a test double's.
func TestAnOversizedAnswerThroughAProxiedSessionIsRefused(t *testing.T) {
	client := rawCluster(t, oversized(true))
	m := New(Options{
		Clusters:   oneCluster{client: client},
		Configured: true,
		Prometheus: decoy()[0],
		Logf:       t.Logf,
	})
	t.Cleanup(m.Close)

	found := m.Discover(context.Background(), testCluster, false)
	if found.Kind != KindPrometheus || found.Transport != TransportProxy {
		t.Fatalf("kind %q transport %q (%s); want the service proxy", found.Kind, found.Transport, found.Detail)
	}
	body, err := m.Query(context.Background(), testCluster, "up")
	if !errors.Is(err, errTooLarge) || body != nil {
		t.Fatalf("query gave %d bytes and %v; want nothing and errTooLarge", len(body), err)
	}
}

// oneCluster is a Clusters holding one client, under testCluster.
type oneCluster struct{ client *cluster.Client }

func (o oneCluster) Resolve(name string) (string, bool) { return name, name == testCluster }

func (o oneCluster) Client(string) (*cluster.Client, error) { return o.client, nil }

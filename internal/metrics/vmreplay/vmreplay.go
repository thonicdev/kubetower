// Package vmreplay checks that VictoriaMetrics answers the queries the console
// sends, against a real cluster: the queries are recorded as sent to the
// cluster's Prometheus, and replayed against each VictoriaMetrics target,
// through the API server's service proxy and through a port-forward.
//
// Test support only: imported by the opt-in integration tests
// (KT_VM_CLUSTER) of internal/metrics and internal/overview, never by a binary.
package vmreplay

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/metrics"
)

// DefaultTargets are vm-operator's vmsingle and vmselect named "main" in "vm".
const DefaultTargets = "vm/vmsingle-main:8428,vm/vmselect-main:8481/select/0/prometheus"

// Setup is what a test needs: the cluster's clients, its resolved name, the
// kubeconfig it came from, a Metrics that discovered its Prometheus, and the
// VictoriaMetrics targets to replay against.
type Setup struct {
	Kubeconfig string
	Clusters   *cluster.Manager
	Cluster    string
	Reference  *metrics.Metrics
	Targets    string
}

// Open runs against the kubeconfig context name, and replays against targets
// (comma-separated, as KT_PROMETHEUS_SERVICE writes them; empty is
// DefaultTargets). The test reads both from its environment, KT_VM_CLUSTER and
// KT_VM_TARGETS, and an empty name skips it. Open fails the test unless
// discovery finds the cluster's own Prometheus, the reference every answer is
// compared with.
func Open(ctx context.Context, t *testing.T, name, targets string) Setup {
	t.Helper()
	if name == "" {
		t.Skip("set KT_VM_CLUSTER to a kubeconfig context running Prometheus and VictoriaMetrics to run this")
	}
	if targets == "" {
		targets = DefaultTargets
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
	reference := metrics.New(metrics.Options{Clusters: clusters, Access: accesstest.Allow(), Logf: t.Logf})
	t.Cleanup(reference.Close)
	found := reference.Discover(ctx, resolved, true)
	if found.Kind != metrics.KindPrometheus || found.Engine != metrics.EnginePrometheus {
		t.Fatalf("discovery found %q engine %q (%s); this test needs the cluster's Prometheus as the reference", found.Kind, found.Engine, found.Detail)
	}
	t.Logf("reference: %s", found.Detail)
	return Setup{Kubeconfig: kubeconfig, Clusters: clusters, Cluster: resolved, Reference: reference, Targets: targets}
}

// Query is one query as the console sent it. Range is false for an instant
// query.
type Query struct {
	PromQL string
	Range  bool
	Step   time.Duration
}

// RangeQuerier is what the console's readers ask: metrics.RangeQuerier and
// overview.Metrics.
type RangeQuerier interface {
	Query(ctx context.Context, cluster, promQL string) ([]byte, error)
	QueryRange(ctx context.Context, cluster, promQL string, start, end time.Time, step time.Duration) ([]byte, error)
}

// Recorder passes queries on and keeps each distinct one.
type Recorder struct {
	Q    RangeQuerier
	mu   sync.Mutex
	seen map[Query]bool
	all  []Query
}

func (r *Recorder) keep(q Query) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seen == nil {
		r.seen = map[Query]bool{}
	}
	if !r.seen[q] {
		r.seen[q] = true
		r.all = append(r.all, q)
	}
}

// Query records an instant query and sends it on.
func (r *Recorder) Query(ctx context.Context, cluster, promQL string) ([]byte, error) {
	r.keep(Query{PromQL: promQL})
	return r.Q.Query(ctx, cluster, promQL)
}

// QueryRange records a range query and sends it on.
func (r *Recorder) QueryRange(ctx context.Context, cluster, promQL string, start, end time.Time, step time.Duration) ([]byte, error) {
	r.keep(Query{PromQL: promQL, Range: true, Step: step})
	return r.Q.QueryRange(ctx, cluster, promQL, start, end, step)
}

// Queries is every distinct query recorded, in the order first sent.
func (r *Recorder) Queries() []Query {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Query(nil), r.all...)
}

// Replay runs every query against the reference and against each target,
// through the service proxy and through a port-forward, over the last ten
// minutes: a query that finds series on Prometheus must find some on
// VictoriaMetrics, and none may be refused.
func Replay(ctx context.Context, t *testing.T, s Setup, queries []Query) {
	t.Helper()
	if len(queries) == 0 {
		t.Fatal("no query was recorded")
	}
	t.Logf("%d distinct queries recorded", len(queries))
	forwarded := refusingProxy(t, s.Kubeconfig, s.Cluster)
	end := time.Now()
	start := end.Add(-10 * time.Minute)
	for _, address := range strings.Split(s.Targets, ",") {
		target, err := metrics.ParseTarget(strings.TrimSpace(address))
		if err != nil {
			t.Fatal(err)
		}
		for _, way := range []struct {
			transport string
			clusters  *cluster.Manager
		}{{metrics.TransportProxy, s.Clusters}, {metrics.TransportForward, forwarded}} {
			t.Run(target.Address()+" by "+way.transport, func(t *testing.T) {
				vm := metrics.New(metrics.Options{Clusters: way.clusters, Access: accesstest.Allow(), Logf: t.Logf, Configured: true, Prometheus: target})
				t.Cleanup(vm.Close)
				found := vm.Discover(ctx, s.Cluster, true)
				if found.Kind != metrics.KindPrometheus || found.Engine != metrics.EngineVictoriaMetrics || found.Transport != way.transport {
					t.Fatalf("kind %q engine %q transport %q (%s)", found.Kind, found.Engine, found.Transport, found.Detail)
				}
				compare(ctx, t, s, vm, queries, start, end)
			})
		}
	}
}

func compare(ctx context.Context, t *testing.T, s Setup, vm *metrics.Metrics, queries []Query, start, end time.Time) {
	t.Helper()
	ask := func(m *metrics.Metrics, q Query) (int, error) {
		var body []byte
		var err error
		if q.Range {
			body, err = m.QueryRange(ctx, s.Cluster, q.PromQL, start, end, min(q.Step, time.Minute))
		} else {
			body, err = m.Query(ctx, s.Cluster, q.PromQL)
		}
		if err != nil {
			return 0, err
		}
		return series(body)
	}
	compared, withData := 0, 0
	for _, q := range queries {
		want, err := ask(s.Reference, q)
		if err != nil {
			t.Logf("skipped, Prometheus refused it too: %v: %s", err, q.PromQL)
			continue
		}
		got, err := ask(vm, q)
		switch {
		case err != nil:
			t.Errorf("VictoriaMetrics refused (%v): %s", err, q.PromQL)
		case want > 0 && got == 0:
			t.Errorf("Prometheus has %d series, VictoriaMetrics none: %s", want, q.PromQL)
		default:
			compared++
			if want > 0 {
				withData++
			}
		}
	}
	t.Logf("%d of %d queries answered alike, %d of them with series", compared, len(queries), withData)
	if withData == 0 {
		t.Error("no query found series on Prometheus, so nothing was compared")
	}
}

// series counts the series of an answer that carry at least one finite value.
// A NaN is a gap to the console (metrics' samples.go, overview's sampleValue),
// and where Prometheus answers a series of NaN - a quantile over buckets that
// did not move - VictoriaMetrics leaves the series out: the same gap.
func series(body []byte) (int, error) {
	var answer struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Value  []json.RawMessage   `json:"value"`
				Values [][]json.RawMessage `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		return 0, err
	}
	if answer.Status != "success" {
		return 0, fmt.Errorf("status %q", answer.Status)
	}
	count := 0
	for _, one := range answer.Data.Result {
		points := one.Values
		if one.Value != nil {
			points = append(points, one.Value)
		}
		for _, point := range points {
			if finite(point) {
				count++
				break
			}
		}
	}
	return count, nil
}

func finite(point []json.RawMessage) bool {
	if len(point) != 2 {
		return false
	}
	var text string
	if json.Unmarshal(point[1], &text) != nil {
		return false
	}
	value, err := strconv.ParseFloat(text, 64)
	return err == nil && !math.IsNaN(value) && !math.IsInf(value, 0)
}

// refusingProxy is the cluster behind a local front that refuses the API
// server's service proxy, as an EKS control plane does for pods on a secondary
// CIDR, and passes everything else, port-forward upgrades included: the
// console then reaches every Service by port-forward. It answers a Manager on
// a kubeconfig naming only that front, under the same context name.
func refusingProxy(t *testing.T, kubeconfig, context string) *cluster.Manager {
	t.Helper()
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig},
		&clientcmd.ConfigOverrides{CurrentContext: context},
	).ClientConfig()
	if err != nil {
		t.Fatal(err)
	}
	upstream, err := url.Parse(config.Host)
	if err != nil {
		t.Fatal(err)
	}
	transport, err := rest.TransportFor(config)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	proxy.Transport = transport
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/services/") && strings.Contains(r.URL.Path, "/proxy") {
			http.Error(w, "Address is not allowed", http.StatusServiceUnavailable)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(front.Close)

	written := clientcmdapi.NewConfig()
	written.Clusters["front"] = &clientcmdapi.Cluster{Server: front.URL}
	written.AuthInfos["front"] = &clientcmdapi.AuthInfo{}
	written.Contexts[context] = &clientcmdapi.Context{Cluster: "front", AuthInfo: "front"}
	written.CurrentContext = context
	path := filepath.Join(t.TempDir(), "config")
	if err := clientcmd.WriteToFile(*written, path); err != nil {
		t.Fatal(err)
	}
	clusters, err := cluster.NewManager(path)
	if err != nil {
		t.Fatal(err)
	}
	return clusters
}

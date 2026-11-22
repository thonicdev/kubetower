package overview

// The queries in object.go, against a real Prometheus.
//
// Every selector in this package is a sentence about somebody else's data
// model, and a unit test can only check that the sentence was written as
// intended — not that it is true. One such case: CPU and memory need
// `container!=""` and the network counters must not have it, because cAdvisor
// collects them from the pod's network namespace and they carry no container
// label at all. Both spellings compile, both return a well-formed chart, and
// one of them returns a well-formed chart of nothing while saying the pod is
// idle.
//
// Off by default, because it needs a cluster with a Prometheus in it and a live
// credential for it:
//
//	KT_INTEGRATION_CLUSTER=<context> go test ./internal/overview/ -run Prometheus -v
//
// Read-only throughout: five range queries and one instant query.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/metrics"
)

// promSetup resolves the context, opens a metrics backend on it, and skips
// unless that cluster actually has a Prometheus.
func promSetup(t *testing.T) (Metrics, string) {
	t.Helper()

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

	backend := metrics.New(metrics.Options{Clusters: clusters, Logf: t.Logf})
	t.Cleanup(backend.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	found := backend.Discover(ctx, resolved, true)
	if found.Kind != metrics.KindPrometheus {
		t.Skipf("%s reports %q (%s); these queries need a Prometheus", resolved, found.Kind, found.Detail)
	}
	t.Logf("%s: %s reached by %s", resolved, found.Prometheus.Address(), found.Transport)
	return backend, resolved
}

// The probe, against a real Prometheus. Whatever it answers is the fact this
// installation's panels are entitled to state, so it is logged rather than
// asserted: a cluster without cAdvisor filesystem series is a true answer and
// not a failing test.
func TestPrometheusSaysWhichCountersItHolds(t *testing.T) {
	backend, name := promSetup(t)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	// The pod's shapes, which are the demanding ones: a bare counter for network
	// and a per-container one for the filesystem.
	specs := []seriesSpec{
		{chart: ChartNetwork, metric: metricNetIn, probe: metricNetIn},
		{chart: ChartNetwork, metric: metricNetOut, probe: metricNetOut},
		{chart: ChartDisk, metric: metricDisk, probe: metricDisk + `{container!=""}`},
	}
	query := probeQuery(specs)
	t.Logf("probe: %s", query)

	known, err := probeMetrics(ctx, backend, name, query)
	if err != nil {
		t.Fatalf("the probe failed: %v", err)
	}
	for _, spec := range specs {
		t.Logf("%-45s exported: %v", spec.metric, known[spec.metric])
	}
	if len(known) == 0 {
		t.Errorf("%s exports none of the optional counters; if that is wrong, the probe's query is wrong", name)
	}
	// One sub-expression answering and the rest silent is the signature of the
	// `or on(__name__)` mistake: a plain `or` discards everything after the first.
	if len(known) == 1 && len(specs) > 1 {
		t.Errorf("only %v came back from a probe asking about %d counters; the sub-answers are being "+
			"discarded rather than the counters being absent", known, len(specs))
	}
}

// A pod's five series, fetched the way the panel fetches them.
//
// The assertion is the one that cannot be made without a cluster: if the network
// counters exist, a pod that is *doing* something has to come back with samples.
// A pod is chosen by asking Prometheus which one moved the most traffic, so the
// test cannot pass by measuring something idle.
func TestPrometheusAnswersAPodsNetworkAndSaysWhenItCannot(t *testing.T) {
	backend, name := promSetup(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// pod!="" matters here as much as it does in the node queries: cAdvisor's
	// machine-level series carry no pod label, they are larger than any single
	// pod, and topk would hand back that bucket with an empty name.
	raw, err := backend.Query(ctx, name,
		`topk(1, sum by (namespace, pod) (rate(container_network_receive_bytes_total{pod!=""}[5m])))`)
	if err != nil {
		t.Fatalf("asking which pod moves the most traffic: %v", err)
	}
	busiest, err := parseVector(raw, func(v float64) PodUsage { return PodUsage{MemoryBytes: int64(v)} })
	if err != nil {
		t.Fatalf("reading the answer: %v", err)
	}
	if len(busiest) == 0 {
		t.Skipf("%s has no pod network series at all; there is nothing to verify against", name)
	}
	target := busiest[0]
	t.Logf("busiest pod by ingress: %s/%s at %s", target.Namespace, target.Name, formatBytes(target.MemoryBytes)+"/s")

	got := PodHistory(ctx, backend, name, target.Namespace, target.Name, WindowFor("1h"))

	for _, series := range []History{got.CPU, got.Memory, got.NetIn, got.NetOut, got.Disk} {
		last := "—"
		if n := len(series.Points); n > 0 {
			last = format(int64(series.Points[n-1].Value), series.Unit)
		}
		t.Logf("%-22s %4d samples  last %-12s %s", series.Title, len(series.Points), last, series.Unavailable)
	}
	t.Logf("network support: ok=%v %s", got.Support.Network.OK, got.Support.Network.Detail)
	t.Logf("filesystem support: ok=%v %s", got.Support.Disk.OK, got.Support.Disk.Detail)

	// What a reader would actually see on the network chart, from real samples.
	// Logged because the figures under the line are the part of a chart that can
	// be checked against `kubectl top` or a Grafana panel by hand — a rendered
	// line cannot be.
	for _, key := range AllCharts {
		for _, line := range drawnText(string(got.Chart(key))) {
			t.Logf("%-8s | %s", key, line)
		}
	}

	// This is the check the unit tests cannot make. The counters are there — the
	// probe just said so — and this is the busiest pod on the cluster, so an empty
	// series here means the selector is wrong, which is precisely the failure that
	// draws a flat line under a pod moving megabytes a second.
	if got.Support.Network.OK && len(got.NetIn.Points) == 0 {
		t.Errorf("the busiest pod on %s came back with no ingress samples although the counters exist; "+
			"the network selector is matching nothing (%q)", name, got.NetIn.Unavailable)
	}
	if len(got.NetIn.Points) == 0 && got.NetIn.Unavailable == "" {
		t.Error("the network series is empty with nothing saying why, which reads as an idle pod")
	}
}

// A node's four charts, and in particular whether this cluster labels cAdvisor
// with `node` at all.
func TestPrometheusAnswersANodesSeries(t *testing.T) {
	backend, name := promSetup(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	raw, err := backend.Query(ctx, name, `count by (node) (kube_node_info)`)
	if err != nil {
		t.Skipf("cannot find a node name to ask about: %v", err)
	}
	node := nodeNameFrom(t, raw)
	if node == "" {
		t.Skip("this Prometheus carries no kube-state-metrics node series to pick a name from")
	}
	t.Logf("node: %s", node)

	got := NodeHistory(ctx, backend, name, node, WindowFor("1h"))
	for _, series := range []History{got.CPU, got.Memory, got.NetIn, got.NetOut, got.Disk} {
		last := "—"
		if n := len(series.Points); n > 0 {
			last = format(int64(series.Points[n-1].Value), series.Unit)
		}
		t.Logf("%-22s %4d samples  last %-12s %s", series.Title, len(series.Points), last, series.Unavailable)
	}
	if len(got.CPU.Points) == 0 {
		t.Errorf("a running node reported no CPU samples; the node selector matches neither label "+
			"on %s (%q)", name, got.CPU.Unavailable)
	}
}

// nodeNameFrom pulls the first `node` label out of an instant query's answer.
func nodeNameFrom(t *testing.T, raw []byte) string {
	t.Helper()
	var resp promVector
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("reading the answer: %v", err)
	}
	for _, series := range resp.Data.Result {
		if name := series.Metric["node"]; name != "" {
			return name
		}
	}
	return ""
}

package metrics_test

// Every query the console sends to a Prometheus, checked against
// VictoriaMetrics on a real cluster: the queries of internal/metrics and of
// internal/overview are recorded as sent to the cluster's own Prometheus, and
// replayed against each VictoriaMetrics target, through the API server's
// service proxy and through a port-forward.
//
// Off by default, because it needs a cluster running Prometheus and
// VictoriaMetrics side by side, scraping the same targets:
//
//	KT_VM_CLUSTER=kind-kubetower go test ./internal/metrics/ -run VictoriaMetricsIntegration -v
//
// KT_VM_TARGETS lists the targets, comma-separated, as KT_PROMETHEUS_SERVICE
// writes them (default: vm-operator's vmsingle and vmselect named "main" in
// namespace "vm"). KT_VM_REFERENCE names the cluster's Prometheus the same
// way, where discovery does not choose it: since #149 it chooses nothing when
// Prometheus and VictoriaMetrics Services are in different namespaces.

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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/metrics"
	"github.com/thonicdev/kubetower/internal/overview"
)

// defaultTargets are vm-operator's vmsingle and vmselect named "main" in "vm".
const defaultTargets = "vm/vmsingle-main:8428,vm/vmselect-main:8481/select/0/prometheus"

// setup is what a test needs: the cluster's clients, its resolved name, the
// kubeconfig it came from, a Metrics that discovered its Prometheus, and the
// VictoriaMetrics targets to replay against.
type setup struct {
	Kubeconfig string
	Clusters   *cluster.Manager
	Cluster    string
	Reference  *metrics.Metrics
	Targets    string
	// Front is the same cluster behind refusingProxy.
	Front *cluster.Manager
}

// open runs against the kubeconfig context name, and replays against targets
// (comma-separated, as KT_PROMETHEUS_SERVICE writes them; empty is
// defaultTargets). The test reads both from its environment, KT_VM_CLUSTER and
// KT_VM_TARGETS, and an empty name skips it. open fails the test unless
// discovery finds the cluster's own Prometheus, the reference every answer is
// compared with.
func open(ctx context.Context, t *testing.T, name, targets string) setup {
	t.Helper()
	if name == "" {
		t.Skip("set KT_VM_CLUSTER to a kubeconfig context running Prometheus and VictoriaMetrics to run this")
	}
	if targets == "" {
		targets = defaultTargets
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
	options := metrics.Options{Clusters: clusters, Access: accesstest.Allow(), Logf: t.Logf}
	if address := os.Getenv("KT_VM_REFERENCE"); address != "" {
		target, err := metrics.ParseTarget(address)
		if err != nil {
			t.Fatalf("KT_VM_REFERENCE: %v", err)
		}
		options.Configured, options.Prometheus = true, target
	}
	reference := metrics.New(options)
	t.Cleanup(reference.Close)
	found := reference.Discover(ctx, resolved, true)
	if found.Kind != metrics.KindPrometheus || found.Engine != metrics.EnginePrometheus {
		t.Fatalf("discovery found %q engine %q (%s); this test needs the cluster's Prometheus as the reference (KT_VM_REFERENCE names it)", found.Kind, found.Engine, found.Detail)
	}
	t.Logf("reference: %s", found.Detail)
	return setup{Kubeconfig: kubeconfig, Clusters: clusters, Cluster: resolved, Reference: reference, Targets: targets,
		Front: refusingProxy(t, kubeconfig, resolved)}
}

// query is one query as the console sent it. Range is false for an instant
// query.
type query struct {
	PromQL string
	Range  bool
	Step   time.Duration
}

// rangeQuerier is what the console's readers ask: metrics.RangeQuerier and
// overview.Metrics.
type rangeQuerier interface {
	Query(ctx context.Context, cluster, promQL string) ([]byte, error)
	QueryRange(ctx context.Context, cluster, promQL string, start, end time.Time, step time.Duration) ([]byte, error)
}

// recorder passes queries on and keeps each distinct one.
type recorder struct {
	Q    rangeQuerier
	mu   sync.Mutex
	seen map[query]bool
	all  []query
}

func (r *recorder) keep(q query) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seen == nil {
		r.seen = map[query]bool{}
	}
	if !r.seen[q] {
		r.seen[q] = true
		r.all = append(r.all, q)
	}
}

// Query records an instant query and sends it on.
func (r *recorder) Query(ctx context.Context, cluster, promQL string) ([]byte, error) {
	r.keep(query{PromQL: promQL})
	return r.Q.Query(ctx, cluster, promQL)
}

// QueryRange records a range query and sends it on.
func (r *recorder) QueryRange(ctx context.Context, cluster, promQL string, start, end time.Time, step time.Duration) ([]byte, error) {
	r.keep(query{PromQL: promQL, Range: true, Step: step})
	return r.Q.QueryRange(ctx, cluster, promQL, start, end, step)
}

// queries is every distinct query recorded, in the order first sent.
func (r *recorder) queries() []query {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]query(nil), r.all...)
}

// replay runs every query against the reference and against each target,
// through the service proxy and through a port-forward, over the last ten
// minutes: a query that finds series on Prometheus must find some on
// VictoriaMetrics, and none may be refused.
func replay(ctx context.Context, t *testing.T, s setup, queries []query) {
	t.Helper()
	if len(queries) == 0 {
		t.Fatal("no query was recorded")
	}
	t.Logf("%d distinct queries recorded", len(queries))
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
		}{{metrics.TransportProxy, s.Clusters}, {metrics.TransportForward, s.Front}} {
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

func compare(ctx context.Context, t *testing.T, s setup, vm *metrics.Metrics, queries []query, start, end time.Time) {
	t.Helper()
	ask := func(m *metrics.Metrics, q query) (int, error) {
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
// CIDR, answers that metrics-server is not installed, and passes everything
// else, port-forward upgrades included: the console then reaches every Service
// by port-forward. It answers a Manager on
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
		// And no metrics-server, so the overview's top pods ask Prometheus.
		if strings.HasPrefix(r.URL.Path, "/apis/metrics.k8s.io") {
			http.NotFound(w, r)
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

func TestVictoriaMetricsIntegrationAnswersEveryQuery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	s := open(ctx, t, os.Getenv("KT_VM_CLUSTER"), os.Getenv("KT_VM_TARGETS"))

	// Discovery keeps Prometheus where both run (Open), and finds
	// VictoriaMetrics as vm-operator labels it where Prometheus is not.
	client, err := s.Clusters.Client(s.Cluster)
	if err != nil {
		t.Fatal(err)
	}
	address, via, err := metrics.VictoriaMetricsAlone(ctx, client)
	if err != nil {
		t.Fatalf("no VictoriaMetrics Service found: %v", err)
	}
	t.Logf("VictoriaMetrics alone would be %s, via %s", address, via)

	// A typo in the path is not a VictoriaMetrics reached, though vmselect
	// answers health at its root: through the service proxy, and through the
	// front that refuses it, where only a port-forward reaches the Service to
	// be told the path is not there (#176).
	for _, typo := range []string{"vm/vmselect-main:8481/select/O/prometheus", "vm/vmsingle-main:8428/typo"} {
		target, err := metrics.ParseTarget(typo)
		if err != nil {
			t.Fatal(err)
		}
		for _, way := range []struct {
			transport string
			clusters  *cluster.Manager
		}{{metrics.TransportProxy, s.Clusters}, {metrics.TransportForward, s.Front}} {
			wrong := metrics.New(metrics.Options{Clusters: way.clusters, Access: accesstest.Allow(), Logf: t.Logf, Configured: true, Prometheus: target})
			found := wrong.Discover(ctx, s.Cluster, true)
			wrong.Close()
			if found.Kind != metrics.KindUnreachable || !strings.Contains(found.Detail, "not under "+target.Path) {
				t.Errorf("%s by %s: kind %q (%s), want unreachable under its own path", typo, way.transport, found.Kind, found.Detail)
			}
		}
	}

	replay(ctx, t, s, recordAll(ctx, t, s))
}

// recordAll is every query internal/metrics and internal/overview send,
// recorded as sent to the cluster's Prometheus.
func recordAll(ctx context.Context, t *testing.T, s setup) []query {
	t.Helper()
	client, err := s.Clusters.Client(s.Cluster)
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{Q: s.Reference}
	pods, err := client.Clients.CoreV1().Pods("kube-system").List(ctx, metav1.ListOptions{LabelSelector: "k8s-app=kube-dns"})
	if err != nil || len(pods.Items) == 0 {
		t.Fatalf("no CoreDNS pods in kube-system (%v)", err)
	}
	var refs []metrics.DNSPodRef
	for _, p := range pods.Items {
		refs = append(refs, metrics.DNSPodRef{Name: p.Name, Node: p.Spec.NodeName, IP: p.Status.PodIP})
	}
	if _, err := metrics.ReadWindow(ctx, rec, s.Cluster, "kube-system"); err != nil {
		t.Fatal(err)
	}
	for _, span := range []metrics.DNSSpan{metrics.DNSSpan15m, metrics.DNSSpan1h, metrics.DNSSpan6h} {
		if _, err := metrics.ReadDNS(ctx, rec, s.Cluster, span, "kube-system", refs); err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range metrics.UsageQueries() {
		rec.Query(ctx, s.Cluster, query)
	}

	recordOverview(ctx, t, s, rec)
	return rec.queries()
}

// recordOverview sends internal/overview's queries through rec: the cluster's
// history at every zoom, a pod's, a node's and a claim's at every window, a
// finished run's totals, and the top pods, which ask Prometheus where
// metrics-server is not installed (the front answers that).
func recordOverview(ctx context.Context, t *testing.T, s setup, rec *recorder) {
	t.Helper()
	client, err := s.Clusters.Client(s.Cluster)
	if err != nil {
		t.Fatal(err)
	}
	front, err := s.Front.Client(s.Cluster)
	if err != nil {
		t.Fatal(err)
	}
	pods, err := client.Clients.CoreV1().Pods("kube-system").List(ctx, metav1.ListOptions{LabelSelector: "k8s-app=kube-dns"})
	if err != nil || len(pods.Items) == 0 {
		t.Fatalf("no CoreDNS pods in kube-system (%v)", err)
	}
	pod := pods.Items[0]
	claims, err := client.Clients.CoreV1().PersistentVolumeClaims("").List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	gate := accesstest.Allow()
	if _, err := overview.Cluster(ctx, gate, client, rec); err != nil {
		t.Fatal(err)
	}
	if _, _, top, _ := overview.TopPods(ctx, gate, front, "", rec); !top.OK {
		t.Fatalf("the top pods were not read from Prometheus: %s", top.Detail)
	}
	for _, w := range overview.ZoomWindows {
		overview.ZoomClusterHistory(ctx, rec, s.Cluster, w)
	}
	for _, w := range overview.Windows {
		overview.PodHistory(ctx, rec, s.Cluster, pod.Namespace, pod.Name, w)
		overview.NodeHistory(ctx, rec, s.Cluster, pod.Spec.NodeName, w)
		for _, claim := range claims.Items {
			overview.ReadVolumeHistory(ctx, rec, s.Cluster, claim.Namespace, claim.Name, w)
			break
		}
	}
	run := &overview.Run{Start: time.Now().Add(-5 * time.Minute), End: time.Now().Add(-time.Minute)}
	overview.PodHistory(ctx, rec, s.Cluster, pod.Namespace, pod.Name, overview.Windows[0]).CountRun(ctx, rec, s.Cluster, pod.Namespace, pod.Name, run)
}

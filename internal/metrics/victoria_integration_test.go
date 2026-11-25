package metrics

// Every query this package sends, against VictoriaMetrics on a real cluster.
//
// Off by default, because it needs a cluster running Prometheus and
// VictoriaMetrics side by side, scraping the same targets:
//
//	KT_VM_CLUSTER=kind-kubetower go test ./internal/metrics/ -run VictoriaMetricsIntegration -v
//
// KT_VM_TARGETS lists the VictoriaMetrics targets, comma-separated, as
// KT_PROMETHEUS_SERVICE writes them (default: vm-operator's vmsingle and
// vmselect named "main" in namespace "vm"). The queries are recorded against
// the cluster's Prometheus, as the console sends them, and replayed against
// each target over the last ten minutes: a query that finds series on
// Prometheus must find some on VictoriaMetrics, and none may fail.
// overview's victoria_integration_test.go does the same for that package.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
)

const defaultVMTargets = "vm/vmsingle-main:8428,vm/vmselect-main:8481/select/0/prometheus"

func TestVictoriaMetricsIntegrationAnswersEveryQuery(t *testing.T) {
	name := os.Getenv("KT_VM_CLUSTER")
	if name == "" {
		t.Skip("set KT_VM_CLUSTER to a kubeconfig context running Prometheus and VictoriaMetrics to run this")
	}
	clusters, resolved := vmClusters(t, name)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// The desktop's own discovery keeps Prometheus where both run.
	desktop := New(Options{Clusters: clusters, Access: accesstest.Allow(), Logf: t.Logf})
	t.Cleanup(desktop.Close)
	found := desktop.Discover(ctx, resolved, true)
	if found.Kind != KindPrometheus || found.Engine != EnginePrometheus {
		t.Fatalf("discovery found %q engine %q (%s); this test needs the cluster's Prometheus as the reference", found.Kind, found.Engine, found.Detail)
	}
	t.Logf("reference: %s", found.Detail)

	// Without it, discovery finds VictoriaMetrics as vm-operator labels it.
	client, err := clusters.Client(resolved)
	if err != nil {
		t.Fatal(err)
	}
	var candidates []corev1.Service
	for _, query := range promQueries {
		list, err := client.Clients.CoreV1().Services(metav1.NamespaceAll).List(ctx, metav1.ListOptions{LabelSelector: query})
		if err != nil {
			t.Fatal(err)
		}
		candidates = append(candidates, list.Items...)
	}
	vm, ok := choose(candidates, vmChoices())
	if !ok || vm.target == nil {
		t.Fatalf("no VictoriaMetrics Service found among %d candidates (%s)", len(candidates), vm.unchosen)
	}
	t.Logf("VictoriaMetrics alone would be %s, via %s", vm.target.Address(), vm.via)

	// What this package asks, recorded against Prometheus.
	rec := &recorder{q: desktop}
	pods, err := client.Clients.CoreV1().Pods("kube-system").List(ctx, metav1.ListOptions{LabelSelector: "k8s-app=kube-dns"})
	if err != nil || len(pods.Items) == 0 {
		t.Fatalf("no CoreDNS pods in kube-system (%v)", err)
	}
	var refs []DNSPodRef
	for _, p := range pods.Items {
		refs = append(refs, DNSPodRef{Name: p.Name, Node: p.Spec.NodeName, IP: p.Status.PodIP})
	}
	if _, err := ReadWindow(ctx, rec, resolved, "kube-system"); err != nil {
		t.Fatal(err)
	}
	for _, span := range []DNSSpan{DNSSpan15m, DNSSpan1h, DNSSpan6h} {
		if _, err := ReadDNS(ctx, rec, resolved, span, "kube-system", refs); err != nil {
			t.Fatal(err)
		}
	}
	for _, kind := range []kinded{podKinded, nodeKinded} {
		for _, namespace := range []string{"", "kube-system"} {
			rec.Query(ctx, resolved, kind.cpu(namespace))
			rec.Query(ctx, resolved, kind.memory(namespace))
		}
	}

	replayOnVictoriaMetrics(ctx, t, clusters, resolved, desktop, rec.recorded())
}

func vmClusters(t *testing.T, name string) (*cluster.Manager, string) {
	t.Helper()
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
	return clusters, resolved
}

// recorded is one query as the console sent it. Range is false for an
// instant query.
type recorded struct {
	PromQL string
	Range  bool
	Step   time.Duration
}

// recorder passes queries to q and keeps each distinct one.
type recorder struct {
	q    RangeQuerier
	mu   sync.Mutex
	seen map[recorded]bool
	all  []recorded
}

func (r *recorder) keep(q recorded) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seen == nil {
		r.seen = map[recorded]bool{}
	}
	if !r.seen[q] {
		r.seen[q] = true
		r.all = append(r.all, q)
	}
}

func (r *recorder) Query(ctx context.Context, cluster, promQL string) ([]byte, error) {
	r.keep(recorded{PromQL: promQL})
	return r.q.Query(ctx, cluster, promQL)
}

func (r *recorder) QueryRange(ctx context.Context, cluster, promQL string, start, end time.Time, step time.Duration) ([]byte, error) {
	r.keep(recorded{PromQL: promQL, Range: true, Step: step})
	return r.q.QueryRange(ctx, cluster, promQL, start, end, step)
}

func (r *recorder) recorded() []recorded {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recorded(nil), r.all...)
}

// replayOnVictoriaMetrics runs every recorded query against Prometheus and
// each VictoriaMetrics target over the last ten minutes.
func replayOnVictoriaMetrics(ctx context.Context, t *testing.T, clusters *cluster.Manager, resolved string, reference *Metrics, queries []recorded) {
	t.Helper()
	if len(queries) == 0 {
		t.Fatal("no query was recorded")
	}
	t.Logf("%d distinct queries recorded", len(queries))
	raw := os.Getenv("KT_VM_TARGETS")
	if raw == "" {
		raw = defaultVMTargets
	}
	end := time.Now()
	start := end.Add(-10 * time.Minute)
	ask := func(m *Metrics, q recorded) (int, error) {
		var body []byte
		var err error
		if q.Range {
			body, err = m.QueryRange(ctx, resolved, q.PromQL, start, end, min(q.Step, time.Minute))
		} else {
			body, err = m.Query(ctx, resolved, q.PromQL)
		}
		if err != nil {
			return 0, err
		}
		return seriesIn(body)
	}
	for _, address := range strings.Split(raw, ",") {
		target, err := ParseTarget(strings.TrimSpace(address))
		if err != nil {
			t.Fatal(err)
		}
		t.Run(target.Address(), func(t *testing.T) {
			vm := New(Options{Clusters: clusters, Access: accesstest.Allow(), Logf: t.Logf, Configured: true, Prometheus: target})
			t.Cleanup(vm.Close)
			found := vm.Discover(ctx, resolved, true)
			if found.Kind != KindPrometheus || found.Engine != EngineVictoriaMetrics {
				t.Fatalf("%s: kind %q engine %q (%s)", target.Address(), found.Kind, found.Engine, found.Detail)
			}
			t.Logf("%s", found.Detail)

			// The port-forward, which a cluster whose service proxy refuses
			// uses: the console's own tunnel, then the query under the path.
			tunnel, err := vm.tunnels.Open(ctx, resolved, target.Namespace, target.Service, target.Port)
			if err != nil {
				t.Fatalf("port-forward to %s: %v", target.Address(), err)
			}
			body, err := httpGet(ctx, "http://"+tunnel.Addr()+target.Path+endpointQuery, url.Values{"query": {"up"}}, instantTimeout)
			tunnel.Close()
			if n, perr := seriesIn(body); err != nil || perr != nil || n == 0 {
				t.Fatalf("through the port-forward: %d series, %v, %v", n, err, perr)
			}
			compared, withData := 0, 0
			for _, q := range queries {
				want, err := ask(reference, q)
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
		})
	}
}

// seriesIn counts the series of a Prometheus API answer.
func seriesIn(body []byte) (int, error) {
	var answer struct {
		Status string `json:"status"`
		Data   struct {
			Result []json.RawMessage `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		return 0, err
	}
	if answer.Status != "success" {
		return 0, fmt.Errorf("status %q", answer.Status)
	}
	return len(answer.Data.Result), nil
}

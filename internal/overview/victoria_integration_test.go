package overview

// Every query this package sends, against VictoriaMetrics on a real cluster:
// internal/metrics' victoria_integration_test.go for this package's queries,
// with the same environment.
//
//	KT_VM_CLUSTER=kind-kubetower go test ./internal/overview/ -run VictoriaMetricsIntegration -v

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/cluster"
	"github.com/thonicdev/kubetower/internal/metrics"
)

const defaultVMTargets = "vm/vmsingle-main:8428,vm/vmselect-main:8481/select/0/prometheus"

func TestVictoriaMetricsIntegrationAnswersEveryOverviewQuery(t *testing.T) {
	name := os.Getenv("KT_VM_CLUSTER")
	if name == "" {
		t.Skip("set KT_VM_CLUSTER to a kubeconfig context running Prometheus and VictoriaMetrics to run this")
	}
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		home, _ := os.UserHomeDir()
		kubeconfig = filepath.Join(home, ".kube", "config")
	}
	clusters, err := cluster.NewManager(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	resolved, ok := clusters.Resolve(name)
	if !ok {
		t.Fatalf("%q is not in %s", name, kubeconfig)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	reference := metrics.New(metrics.Options{Clusters: clusters, Access: accesstest.Allow(), Logf: t.Logf})
	t.Cleanup(reference.Close)
	if found := reference.Discover(ctx, resolved, true); found.Kind != metrics.KindPrometheus || found.Engine != metrics.EnginePrometheus {
		t.Fatalf("discovery found %q engine %q (%s); this test needs the cluster's Prometheus as the reference", found.Kind, found.Engine, found.Detail)
	}

	client, err := clusters.Client(resolved)
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

	// What this package asks, recorded against Prometheus.
	rec := &recorder{q: reference}
	clusterHistory(ctx, rec, resolved)
	promPodUsage(ctx, rec, resolved, 10)
	for _, w := range ZoomWindows {
		ZoomClusterHistory(ctx, rec, resolved, w)
	}
	for _, w := range Windows {
		PodHistory(ctx, rec, resolved, pod.Namespace, pod.Name, w)
		NodeHistory(ctx, rec, resolved, pod.Spec.NodeName, w)
		for _, claim := range claims.Items {
			ReadVolumeHistory(ctx, rec, resolved, claim.Namespace, claim.Name, w)
			break
		}
	}
	started := time.Now().Add(-5 * time.Minute)
	ended := time.Now().Add(-time.Minute)
	PodHistory(ctx, rec, resolved, pod.Namespace, pod.Name, Windows[0]).CountRun(ctx, rec, resolved, pod.Namespace, pod.Name, &Run{Start: started, End: ended})

	queries := rec.recorded()
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
	ask := func(m *metrics.Metrics, q recorded) (int, error) {
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
		return countSeries(body)
	}
	for _, address := range strings.Split(raw, ",") {
		target, err := metrics.ParseTarget(strings.TrimSpace(address))
		if err != nil {
			t.Fatal(err)
		}
		t.Run(target.Address(), func(t *testing.T) {
			vm := metrics.New(metrics.Options{Clusters: clusters, Access: accesstest.Allow(), Logf: t.Logf, Configured: true, Prometheus: target})
			t.Cleanup(vm.Close)
			if found := vm.Discover(ctx, resolved, true); found.Engine != metrics.EngineVictoriaMetrics {
				t.Fatalf("kind %q engine %q (%s)", found.Kind, found.Engine, found.Detail)
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

type recorded struct {
	PromQL string
	Range  bool
	Step   time.Duration
}

// recorder passes queries to q and keeps each distinct one.
type recorder struct {
	q    Metrics
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

func countSeries(body []byte) (int, error) {
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

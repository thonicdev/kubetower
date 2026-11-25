package metrics_test

// Every query internal/metrics sends, against VictoriaMetrics on a real
// cluster, through the service proxy and through a port-forward (vmreplay).
//
// Off by default, because it needs a cluster running Prometheus and
// VictoriaMetrics side by side, scraping the same targets:
//
//	KT_VM_CLUSTER=kind-kubetower go test ./internal/metrics/ -run VictoriaMetricsIntegration -v
//
// internal/overview's victoria_integration_test.go does the same for that
// package's queries.

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thonicdev/kubetower/internal/access/accesstest"
	"github.com/thonicdev/kubetower/internal/metrics"
	"github.com/thonicdev/kubetower/internal/metrics/vmreplay"
)

func TestVictoriaMetricsIntegrationAnswersEveryQuery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	s := vmreplay.Open(ctx, t, os.Getenv("KT_VM_CLUSTER"), os.Getenv("KT_VM_TARGETS"))

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
	// answers health at its root.
	for _, typo := range []string{"vm/vmselect-main:8481/select/O/prometheus", "vm/vmsingle-main:8428/typo"} {
		target, err := metrics.ParseTarget(typo)
		if err != nil {
			t.Fatal(err)
		}
		wrong := metrics.New(metrics.Options{Clusters: s.Clusters, Access: accesstest.Allow(), Logf: t.Logf, Configured: true, Prometheus: target})
		found := wrong.Discover(ctx, s.Cluster, true)
		wrong.Close()
		if found.Kind != metrics.KindUnreachable || !strings.Contains(found.Detail, "not under "+target.Path) {
			t.Errorf("%s: kind %q (%s), want unreachable under its own path", typo, found.Kind, found.Detail)
		}
	}

	rec := &vmreplay.Recorder{Q: s.Reference}
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

	vmreplay.Replay(ctx, t, s, rec.Queries())
}

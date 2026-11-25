package overview

// Every query this package sends, against VictoriaMetrics on a real cluster,
// through the service proxy and through a port-forward: see
// internal/metrics/vmreplay, and internal/metrics' victoria_integration_test.go
// for that package's queries, with the same environment.
//
//	KT_VM_CLUSTER=kind-kubetower go test ./internal/overview/ -run VictoriaMetricsIntegration -v

import (
	"context"
	"os"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thonicdev/kubetower/internal/metrics/vmreplay"
)

func TestVictoriaMetricsIntegrationAnswersEveryOverviewQuery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	s := vmreplay.Open(ctx, t, os.Getenv("KT_VM_CLUSTER"), os.Getenv("KT_VM_TARGETS"))
	resolved := s.Cluster

	client, err := s.Clusters.Client(resolved)
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
	rec := &vmreplay.Recorder{Q: s.Reference}
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

	vmreplay.Replay(ctx, t, s, rec.Queries())
}

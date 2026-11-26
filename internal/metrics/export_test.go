package metrics

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thonicdev/kubetower/internal/cluster"
)

// For the integration test in package metrics_test (victoria_integration_test.go),
// which cannot import vmreplay from inside this package.

// UsageQueries are the Prometheus fallback's instant queries for pod and node
// usage, cluster-wide and in kube-system.
func UsageQueries() []string {
	var out []string
	for _, kind := range []kinded{podKinded, nodeKinded} {
		for _, namespace := range []string{"", "kube-system"} {
			out = append(out, kind.cpu(namespace), kind.memory(namespace))
		}
	}
	return out
}

// VictoriaMetricsAlone is the target discovery would choose on this cluster
// if no Prometheus ran there, and the selector that found it.
func VictoriaMetricsAlone(ctx context.Context, client *cluster.Client) (string, string, error) {
	var candidates []corev1.Service
	for _, query := range promQueries {
		list, err := client.Clients.CoreV1().Services(metav1.NamespaceAll).List(ctx, metav1.ListOptions{LabelSelector: query})
		if err != nil {
			return "", "", err
		}
		candidates = append(candidates, list.Items...)
	}
	chosen, ok := choose(candidates, vmChoices())
	if !ok || chosen.target == nil {
		return "", "", fmt.Errorf("none among %d candidates (%s)", len(candidates), chosen.unchosen)
	}
	return chosen.target.Address(), chosen.via, nil
}

package metrics

import (
	"context"
	"fmt"

	"github.com/thonicdev/kubetower/internal/cluster"
)

// For the integration tests in package metrics_test
// (vmreplay_integration_test.go), which reach these through the package's own
// discovery rather than a copy of it.

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
	candidates, refused := listCandidates(ctx, client)
	if refused != nil {
		return "", "", fmt.Errorf("listing Services: %s", refused.detail)
	}
	chosen, ok, _ := choose(candidates, vmChoices())
	if !ok || chosen.target == nil {
		return "", "", fmt.Errorf("none among %d candidates (%s)", len(candidates), chosen.unchosen)
	}
	return chosen.target.Address(), chosen.via, nil
}

package launch

import (
	"fmt"
	"strings"

	"github.com/thonicdev/kubetower/internal/metrics"
)

// Which Prometheus the server profile queries, and who says so.
//
// Under `--incluster`, KT_PROMETHEUS_SERVICE (`namespace/service:port[/path]`) is the
// only way a Prometheus is chosen; unset, there is none and the charts say why.
// Discovery guesses from labels anybody creating a Service writes, and a shared
// console shows what it finds to everybody: a tenant's labeled Service could
// outrank the real Prometheus and receive every query. Who chooses the metrics
// backend is a trust boundary, owned by whoever installs the console.
//
// The desktop refuses the variable rather than ignoring it: it looks for
// Prometheus on each cluster, and one Service cannot name it on all of them.

// readPrometheus reads KT_PROMETHEUS_SERVICE for the profile: the target, or
// nil for none, and whether the choice is configuration's alone (the server
// profile).
func readPrometheus(inCluster bool, getenv func(string) string) (configured bool, target *metrics.PromTarget, err error) {
	raw := strings.TrimSpace(getenv(metrics.EnvPrometheusService))
	if !inCluster {
		if raw != "" {
			return false, nil, fmt.Errorf("%s is read only by the server profile (--incluster); "+
				"the desktop looks for Prometheus on each cluster itself, and one Service cannot name it on all of them",
				metrics.EnvPrometheusService)
		}
		return false, nil, nil
	}
	if raw == "" {
		return true, nil, nil
	}
	target, err = metrics.ParseTarget(raw)
	if err != nil {
		return false, nil, fmt.Errorf("%s: %w", metrics.EnvPrometheusService, err)
	}
	return true, target, nil
}

// prometheusSummary is the start-up line for what readPrometheus returned.
// Said out loud because under the server profile "the charts have no history"
// comes down to one variable.
func prometheusSummary(configured bool, target *metrics.PromTarget) string {
	switch {
	case !configured:
		return "looked for on each cluster"
	case target == nil:
		return fmt.Sprintf("none - %s is not set, and the server profile does not look for one, so the charts have no history",
			metrics.EnvPrometheusService)
	}
	return fmt.Sprintf("%s, from %s", target.Address(), metrics.EnvPrometheusService)
}

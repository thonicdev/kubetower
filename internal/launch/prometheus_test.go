package launch

import (
	"strings"
	"testing"

	"github.com/thonicdev/kubetower/internal/metrics"
)

func prometheusEnv(value string) func(string) string {
	return func(name string) string {
		if name == metrics.EnvPrometheusService {
			return value
		}
		return ""
	}
}

// Under the server profile the variable is the whole choice: set, it is the
// target; unset, there is none - and both are configuration, so nothing is
// discovered either way.
func TestTheServerProfileTakesItsPrometheusFromTheVariableAlone(t *testing.T) {
	configured, target, err := readPrometheus(true, prometheusEnv(" monitoring/kps-prometheus:9090 "))
	if err != nil || !configured || target == nil || target.Address() != "monitoring/kps-prometheus:9090" {
		t.Fatalf("set: configured %v target %+v err %v", configured, target, err)
	}

	configured, target, err = readPrometheus(true, prometheusEnv(""))
	if err != nil || !configured || target != nil {
		t.Fatalf("unset: configured %v target %+v err %v; want configured, and no Prometheus", configured, target, err)
	}
}

// A value that does not parse refuses the start, naming the variable: a typo
// is otherwise a console with no history and nothing saying why.
func TestAMistypedPrometheusRefusesTheStart(t *testing.T) {
	_, _, err := readPrometheus(true, prometheusEnv("monitoring/kps-prometheus"))
	if err == nil || !strings.Contains(err.Error(), metrics.EnvPrometheusService) {
		t.Fatalf("got %v; want a refusal naming %s", err, metrics.EnvPrometheusService)
	}
}

// The desktop looks for itself, and refuses the variable rather than ignoring
// it: one Service cannot name a Prometheus on every cluster of a kubeconfig.
func TestTheDesktopRefusesAConfiguredPrometheus(t *testing.T) {
	configured, target, err := readPrometheus(false, prometheusEnv(""))
	if err != nil || configured || target != nil {
		t.Fatalf("unset: configured %v target %+v err %v; want discovery", configured, target, err)
	}
	if _, _, err := readPrometheus(false, prometheusEnv("monitoring/kps-prometheus:9090")); err == nil ||
		!strings.Contains(err.Error(), "--incluster") {
		t.Fatalf("set on the desktop: got %v; want a refusal naming the server profile", err)
	}
}

func TestThePrometheusLineSaysWhichOfTheThree(t *testing.T) {
	target := &metrics.PromTarget{Namespace: "monitoring", Service: "kps-prometheus", Port: 9090}
	for _, c := range []struct {
		configured bool
		target     *metrics.PromTarget
		want       string
	}{
		{false, nil, "looked for on each cluster"},
		{true, nil, "none - " + metrics.EnvPrometheusService + " is not set"},
		{true, target, "monitoring/kps-prometheus:9090, from " + metrics.EnvPrometheusService},
	} {
		if got := prometheusSummary(c.configured, c.target); !strings.HasPrefix(got, c.want) {
			t.Errorf("prometheusSummary(%v, %+v) = %q, want it to start %q", c.configured, c.target, got, c.want)
		}
	}
}

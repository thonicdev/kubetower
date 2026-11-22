package metrics

// Who chooses the console's Prometheus.
//
// A person who may create a Service in a namespace of their own can label it
// `headlamp-prometheus=true`, which outranks every label a real
// kube-prometheus-stack carries; chosen by label alone, the console would draw
// that person's series for everybody - and send them every query, naming the
// pods and nodes people were looking at. The decoy below:
// `monitoring/kps-prometheus` beside `team-a/x`.

import (
	"context"
	"errors"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// decoy is the scenario: a real kube-prometheus-stack Service in `monitoring`,
// and a tenant's Service in `team-a` carrying the label that ranks first.
func decoy() []*PromTarget {
	return []*PromTarget{
		{Namespace: "monitoring", Service: "kps-prometheus", Port: 9090},
		{Namespace: "team-a", Service: "x", Port: 9090},
	}
}

func decoyHarness(t *testing.T) *harness {
	t.Helper()
	return newHarness(t,
		promService("monitoring", "kps-prometheus", map[string]string{"app": "kube-prometheus-stack-prometheus"}, "http-web", 9090),
		promService("team-a", "x", map[string]string{"headlamp-prometheus": "true"}, "web", 9090),
	)
}

// notFound is what the API server says about an aggregated API nobody
// installed.
func notFound(path string) error {
	return apierrors.NewNotFound(schema.GroupResource{Group: "metrics.k8s.io"}, path)
}

// askedThrough reports whether any path the harness was asked for goes
// through the Service at target.
func (h *harness) askedThrough(target *PromTarget) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	prefix := target.proxyPath("")
	for _, path := range h.paths {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// --- The server profile: the deployment chooses -------------------------------

// The configured target is the one queried, the decoy is never asked, and no
// Service is listed to look for anything: under the server profile nothing in
// the cluster takes part in the choice.
func TestTheServerProfileQueriesTheConfiguredPrometheusAndNotADecoy(t *testing.T) {
	real, tenant := decoy()[0], decoy()[1]
	h := newHarnessWith(t, func(o *Options) {
		o.Configured = true
		o.Prometheus = &PromTarget{Namespace: real.Namespace, Service: real.Service, Port: real.Port}
	},
		promService("monitoring", "kps-prometheus", map[string]string{"app": "kube-prometheus-stack-prometheus"}, "http-web", 9090),
		promService("team-a", "x", map[string]string{"headlamp-prometheus": "true"}, "web", 9090),
	)
	answer := []byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`)
	h.answer(rawAnswers{
		real.proxyPath(endpointHealth):   {body: []byte("Prometheus is Healthy.")},
		real.proxyPath(endpointQuery):    {body: answer},
		tenant.proxyPath(endpointHealth): {body: []byte("Prometheus is Healthy.")},
		tenant.proxyPath(endpointQuery):  {body: []byte(`{"status":"success","data":{"resultType":"vector","result":["chosen by team-a"]}}`)},
	})

	found := h.metrics.Discover(context.Background(), testCluster, false)
	if found.Kind != KindPrometheus || found.Prometheus == nil || found.Prometheus.Address() != real.Address() {
		t.Fatalf("kind %q target %+v (%s); want %s", found.Kind, found.Prometheus, found.Detail, real.Address())
	}
	if !strings.Contains(found.Detail, EnvPrometheusService) {
		t.Errorf("the detail does not say the target was configured: %q", found.Detail)
	}
	body, err := h.metrics.Query(context.Background(), testCluster, "up")
	if err != nil || string(body) != string(answer) {
		t.Fatalf("query gave %s, %v; want the configured Prometheus's answer", body, err)
	}
	if h.askedThrough(tenant) {
		t.Errorf("the decoy was asked: %v", h.paths)
	}
	if n := h.listed(); n != 0 {
		t.Errorf("%d Service lists were sent; the server profile looks for nothing", n)
	}
}

// Nothing configured is no Prometheus - not a fall back to discovery, which
// would hand the choice straight back to whoever writes the labels. The
// charts say why there is no history.
func TestTheServerProfileWithNothingConfiguredLooksForNoPrometheus(t *testing.T) {
	_, tenant := decoy()[0], decoy()[1]
	h := newHarnessWith(t, func(o *Options) { o.Configured = true },
		promService("team-a", "x", map[string]string{"headlamp-prometheus": "true"}, "web", 9090),
	)
	h.answer(rawAnswers{
		metricsAPIPath:                   {body: []byte("{}")},
		tenant.proxyPath(endpointHealth): {body: []byte("Prometheus is Healthy.")},
	})

	found := h.metrics.Discover(context.Background(), testCluster, false)
	if found.Kind != KindMetricsServer || found.Prometheus != nil {
		t.Fatalf("kind %q target %+v (%s); want metrics-server alone", found.Kind, found.Prometheus, found.Detail)
	}
	if !strings.Contains(found.Detail, EnvPrometheusService) {
		t.Errorf("the detail does not name the variable that would configure one: %q", found.Detail)
	}
	if h.askedThrough(tenant) || h.listed() != 0 {
		t.Errorf("something was looked for: %d lists, paths %v", h.listed(), h.paths)
	}
	_, err := h.metrics.Query(context.Background(), testCluster, "up")
	var none NoHistory
	if !errors.As(err, &none) || !strings.Contains(err.Error(), EnvPrometheusService) {
		t.Errorf("a query gave %v; want NoHistory naming %s", err, EnvPrometheusService)
	}
}

// A configured target that does not answer is unreachable, under its own
// name, and nothing else is tried in its place.
func TestAConfiguredPrometheusThatDoesNotAnswerIsNotReplaced(t *testing.T) {
	real, tenant := decoy()[0], decoy()[1]
	h := newHarnessWith(t, func(o *Options) {
		o.Configured = true
		o.Prometheus = real
	}, promService("team-a", "x", map[string]string{"headlamp-prometheus": "true"}, "web", 9090))
	h.tunnels.err = errors.New("no pod behind the service")
	h.answer(rawAnswers{
		real.proxyPath(endpointHealth):   {err: apierrors.NewServiceUnavailable("no endpoints available for service")},
		tenant.proxyPath(endpointHealth): {body: []byte("Prometheus is Healthy.")},
	})

	found := h.metrics.Discover(context.Background(), testCluster, false)
	if found.Kind != KindUnreachable || found.Prometheus == nil || found.Prometheus.Address() != real.Address() {
		t.Fatalf("kind %q target %+v (%s); want %s, unreachable", found.Kind, found.Prometheus, found.Detail, real.Address())
	}
	if h.askedThrough(tenant) {
		t.Errorf("the decoy was asked in its place: %v", h.paths)
	}
}

func TestATargetIsReadAsAddressWritesIt(t *testing.T) {
	got, err := ParseTarget("monitoring/kube-prometheus-stack-prometheus:9090")
	if err != nil || got.Address() != "monitoring/kube-prometheus-stack-prometheus:9090" {
		t.Fatalf("got %+v, %v", got, err)
	}
	for _, bad := range []string{
		"",
		"monitoring/prometheus",       // no port
		"prometheus:9090",             // no namespace
		"/prometheus:9090",            // empty namespace
		"monitoring/:9090",            // empty service
		"Monitoring/prometheus:9090",  // not a namespace name
		"monitoring/9prom:9090",       // not a Service name
		"monitoring/a/b:9090",         // two slashes
		"monitoring/prometheus:web",   // a named port is the pod's business
		"monitoring/prometheus:0",     // out of range
		"monitoring/prometheus:65536", // out of range
		" monitoring/prometheus:9090", // the caller trims; this does not guess
	} {
		if target, err := ParseTarget(bad); err == nil {
			t.Errorf("%q was accepted as %+v", bad, target)
		}
	}
}

// --- The desktop: several namespaces, no silent pick ---------------------------

// Two Services in two namespaces both claim to be Prometheus. Which one is
// real is not something a label can settle - anybody who may create a Service
// may write any label on it - so the desktop does not choose, and says which
// namespaces it would have had to choose between.
func TestTheDesktopDoesNotChooseBetweenPrometheusInTwoNamespaces(t *testing.T) {
	real, tenant := decoy()[0], decoy()[1]
	for _, c := range []struct {
		name          string
		metricsServer rawAnswer
		want          string
	}{
		{"with metrics-server", rawAnswer{body: []byte("{}")}, KindMetricsServer},
		{"without metrics-server", rawAnswer{err: notFound(metricsAPIPath)}, KindNone},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := decoyHarness(t)
			h.answer(rawAnswers{
				metricsAPIPath:                   c.metricsServer,
				real.proxyPath(endpointHealth):   {body: []byte("Prometheus is Healthy.")},
				tenant.proxyPath(endpointHealth): {body: []byte("Prometheus is Healthy.")},
			})

			found := h.metrics.Discover(context.Background(), testCluster, false)
			if found.Kind != c.want || found.Prometheus != nil {
				t.Fatalf("kind %q target %+v (%s); want %s and no Prometheus chosen", found.Kind, found.Prometheus, found.Detail, c.want)
			}
			for _, namespace := range []string{"monitoring", "team-a"} {
				if !strings.Contains(found.Detail, namespace) {
					t.Errorf("the detail does not name %s, so nobody can tell why there is no history: %q", namespace, found.Detail)
				}
			}
			if h.askedThrough(tenant) || h.askedThrough(real) {
				t.Errorf("a Prometheus was probed although none was chosen: %v", h.paths)
			}
		})
	}
}

// What a namespace holds is one owner's, so the ranking still decides inside
// one. A kube-prometheus-stack release always has two matching Services in its
// namespace - `<release>-prometheus` and the operator's `prometheus-operated` -
// and refusing to choose between them would cost every such cluster its
// history.
func TestTheDesktopStillChoosesInsideOneNamespace(t *testing.T) {
	h := newHarness(t,
		promService("monitoring", "kps-prometheus", map[string]string{"app": "kube-prometheus-stack-prometheus"}, "http-web", 9090),
		promService("monitoring", "prometheus-operated", map[string]string{"operated-prometheus": "true"}, "web", 9090),
	)
	chosen := &PromTarget{Namespace: "monitoring", Service: "kps-prometheus", Port: 9090}
	h.answer(rawAnswers{chosen.proxyPath(endpointHealth): {body: []byte("Prometheus is Healthy.")}})

	found := h.metrics.Discover(context.Background(), testCluster, false)
	if found.Kind != KindPrometheus || found.Prometheus == nil || found.Prometheus.Address() != chosen.Address() {
		t.Fatalf("kind %q target %+v (%s); want %s", found.Kind, found.Prometheus, found.Detail, chosen.Address())
	}
}

// A Service that matches a selector and has no port worth querying is not a
// candidate, so it cannot make a cluster ambiguous either: the tenant's
// exporter labeled `app=prometheus` on port 8080 does not take the real
// Prometheus away.
func TestAServiceWithNoPrometheusPortDoesNotMakeAClusterAmbiguous(t *testing.T) {
	h := newHarness(t,
		promService("monitoring", "kps-prometheus", map[string]string{"app": "kube-prometheus-stack-prometheus"}, "http-web", 9090),
		promService("team-a", "exporter", map[string]string{"app": "prometheus"}, "http", 8080),
	)
	chosen := decoy()[0]
	h.answer(rawAnswers{chosen.proxyPath(endpointHealth): {body: []byte("Prometheus is Healthy.")}})

	found := h.metrics.Discover(context.Background(), testCluster, false)
	if found.Kind != KindPrometheus || found.Prometheus.Address() != chosen.Address() {
		t.Fatalf("kind %q (%s); want %s", found.Kind, found.Detail, chosen.Address())
	}
}

package metrics

// VictoriaMetrics serves the Prometheus HTTP API: vmsingle at its root on
// 8428, vmselect under /select/<tenant>/prometheus on 8481. Discovery finds
// either where no Prometheus is, and a target may carry the path.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const vmHealthy = "VictoriaMetrics is Healthy.\n"

// The Services as vm-operator and the two charts create them.
func vmsingleOperator() *corev1.Service {
	return promService("vm", "vmsingle-main", map[string]string{
		"app.kubernetes.io/name": "vmsingle", "app.kubernetes.io/instance": "main",
		"app.kubernetes.io/component": "monitoring", "managed-by": "vm-operator",
	}, "http", 8428)
}

// vmsingleOperatorNow is vm-operator's Service as current releases publish
// it: the API on 8429, and 8428 an alias some release may drop.
func vmsingleOperatorNow(alias bool) *corev1.Service {
	svc := vmsingleOperator()
	svc.Spec.Ports = []corev1.ServicePort{{Name: "http", Port: 8429, TargetPort: intstr.FromInt32(8429)}}
	if alias {
		svc.Spec.Ports = append(svc.Spec.Ports, corev1.ServicePort{Name: "http-alias", Port: 8428, TargetPort: intstr.FromInt32(8429)})
	}
	return svc
}

func vmselectOperator() *corev1.Service {
	return promService("vm", "vmselect-main", map[string]string{
		"app.kubernetes.io/name": "vmselect", "app.kubernetes.io/instance": "main",
		"app.kubernetes.io/component": "monitoring", "managed-by": "vm-operator",
	}, "http", 8481)
}

func vmsingleChart() *corev1.Service {
	svc := promService("vm", "r-victoria-metrics-single-server", map[string]string{
		"app.kubernetes.io/name": "victoria-metrics-single", "app.kubernetes.io/component": "server",
	}, "http", 8428)
	svc.Spec.Ports[0].TargetPort = intstr.FromString("http")
	return svc
}

func vmclusterChart(component string, port int32) *corev1.Service {
	return promService("vm", "r-victoria-metrics-cluster-"+component, map[string]string{
		"app.kubernetes.io/name": "victoria-metrics-cluster", "app.kubernetes.io/component": component,
	}, "http", port)
}

func TestVictoriaMetricsIsFoundAsEachInstallLabelsIt(t *testing.T) {
	for _, c := range []struct {
		name     string
		services []*corev1.Service
		want     string
		selector string
	}{
		{"vm-operator vmsingle", []*corev1.Service{vmsingleOperator()}, "vm/vmsingle-main:8428", "app.kubernetes.io/name=vmsingle"},
		{"vm-operator vmsingle on 8429 with the alias", []*corev1.Service{vmsingleOperatorNow(true)}, "vm/vmsingle-main:8429", "app.kubernetes.io/name=vmsingle"},
		{"vm-operator vmsingle on 8429 alone", []*corev1.Service{vmsingleOperatorNow(false)}, "vm/vmsingle-main:8429", "app.kubernetes.io/name=vmsingle"},
		{"vm-operator vmselect", []*corev1.Service{vmselectOperator()}, "vm/vmselect-main:8481/select/0/prometheus", "app.kubernetes.io/name=vmselect"},
		{"victoria-metrics-single chart", []*corev1.Service{vmsingleChart()}, "vm/r-victoria-metrics-single-server:8428", "victoria-metrics-single"},
		{
			// vminsert and vmstorage carry the same name label and are not the
			// query API.
			"victoria-metrics-cluster chart",
			[]*corev1.Service{vmclusterChart("vminsert", 8480), vmclusterChart("vmstorage", 8482), vmclusterChart("vmselect", 8481)},
			"vm/r-victoria-metrics-cluster-vmselect:8481/select/0/prometheus", "app.kubernetes.io/component=vmselect",
		},
		{
			"an older victoria-metrics-cluster chart",
			[]*corev1.Service{promService("vm", "old-vmselect", map[string]string{"app": "vmselect"}, "http", 8481)},
			"vm/old-vmselect:8481/select/0/prometheus", "app=vmselect",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			objects := make([]corev1.Service, len(c.services))
			for i, svc := range c.services {
				objects[i] = *svc
			}
			h := newHarness(t, toObjects(objects)...)
			target, err := ParseTarget(c.want)
			if err != nil {
				t.Fatal(err)
			}
			h.answer(rawAnswers{
				// Health at the root only, so the path is confirmed by a query.
				pathFree(c.want) + "/proxy" + endpointHealth: {body: []byte(vmHealthy)},
				target.proxyPath(endpointQuery):              {body: []byte(`{"status":"success","data":{"resultType":"scalar","result":[0,"1"]}}`)},
			})

			found := h.metrics.Discover(context.Background(), testCluster, false)
			if found.Kind != KindPrometheus || found.Prometheus == nil {
				t.Fatalf("kind %q (%s), want the Prometheus API", found.Kind, found.Detail)
			}
			if got := found.Prometheus.Address(); got != c.want {
				t.Errorf("target %q, want %q", got, c.want)
			}
			if found.Engine != EngineVictoriaMetrics {
				t.Errorf("engine %q, want %q", found.Engine, EngineVictoriaMetrics)
			}
			if !strings.Contains(found.Detail, c.selector) || !strings.Contains(found.Detail, "VictoriaMetrics") {
				t.Errorf("detail does not say what was found and how: %q", found.Detail)
			}
		})
	}
}

// pathFree is the proxy root of an address `ns/svc:port[/path]`.
func pathFree(address string) string {
	ns, rest, _ := strings.Cut(address, "/")
	svcPort, _, _ := strings.Cut(rest, "/")
	return "/api/v1/namespaces/" + ns + "/services/" + svcPort
}

func toObjects(services []corev1.Service) []runtime.Object {
	out := make([]runtime.Object, len(services))
	for i := range services {
		out[i] = &services[i]
	}
	return out
}

// A cluster running both keeps Prometheus, and VictoriaMetrics in another
// namespace does not make it ambiguous.
// Prometheus and VictoriaMetrics in one namespace: one owner put both there,
// and Prometheus is kept (#133).
func TestPrometheusIsPreferredToVictoriaMetricsInOneNamespace(t *testing.T) {
	vm := vmsingleOperator()
	vm.Namespace = "monitoring"
	h := newHarness(t,
		vm,
		promService("monitoring", "kps-prometheus", map[string]string{"app": "kube-prometheus-stack-prometheus"}, "http-web", 9090),
	)
	chosen := &PromTarget{Namespace: "monitoring", Service: "kps-prometheus", Port: 9090}
	h.answer(rawAnswers{chosen.proxyPath(endpointHealth): {body: []byte("Prometheus Server is Healthy.\n")}})

	found := h.metrics.Discover(context.Background(), testCluster, false)
	if found.Kind != KindPrometheus || found.Prometheus == nil || found.Prometheus.Address() != chosen.Address() {
		t.Fatalf("kind %q target %+v (%s); want %s", found.Kind, found.Prometheus, found.Detail, chosen.Address())
	}
	if found.Engine != EnginePrometheus {
		t.Errorf("engine %q", found.Engine)
	}
}

// The decoy across engines (#133): a real vmsingle in vm and a Service
// labelled app=prometheus on 9090 in a tenant's namespace. Neither is chosen,
// nothing is asked of either, and the detail names both namespaces.
func TestPrometheusAndVictoriaMetricsInDifferentNamespacesAreNotChosen(t *testing.T) {
	decoy := promService("team-a", "fake", map[string]string{"app": "prometheus"}, "web", 9090)
	h := newHarness(t, vmsingleOperator(), decoy)
	h.answer(rawAnswers{metricsAPIPath: {err: notFound(metricsAPIPath)}})

	found := h.metrics.Discover(context.Background(), testCluster, false)
	if found.Kind != KindNone || found.Prometheus != nil {
		t.Fatalf("kind %q target %+v (%s); want none chosen", found.Kind, found.Prometheus, found.Detail)
	}
	if !strings.Contains(found.Detail, "Prometheus (team-a)") || !strings.Contains(found.Detail, "VictoriaMetrics (vm)") {
		t.Errorf("detail %q does not name both namespaces", found.Detail)
	}
	for _, p := range h.paths {
		if strings.Contains(p, "team-a") || strings.Contains(p, "/vm/") {
			t.Errorf("a candidate was asked: %s", p)
		}
	}
}

// The same rule as for Prometheus: a label cannot tell the real one from a
// tenant's.
func TestVictoriaMetricsInTwoNamespacesIsNotChosen(t *testing.T) {
	other := vmsingleOperator()
	other.Namespace = "team-a"
	h := newHarness(t, vmsingleOperator(), other)
	h.answer(rawAnswers{metricsAPIPath: {err: notFound(metricsAPIPath)}})

	found := h.metrics.Discover(context.Background(), testCluster, false)
	if found.Kind != KindNone || found.Prometheus != nil || !strings.Contains(found.Detail, "team-a") {
		t.Fatalf("kind %q target %+v (%s); want none chosen, naming the namespaces", found.Kind, found.Prometheus, found.Detail)
	}
}

func TestAVictoriaMetricsServiceOnAnotherPortIsNotTheAPI(t *testing.T) {
	h := newHarness(t, promService("vm", "vmsingle-main", map[string]string{"app.kubernetes.io/name": "vmsingle"}, "metrics", 8080))
	h.answer(rawAnswers{metricsAPIPath: {err: notFound(metricsAPIPath)}})

	if found := h.metrics.Discover(context.Background(), testCluster, false); found.Kind != KindNone {
		t.Fatalf("kind %q (%s), want none", found.Kind, found.Detail)
	}
}

func TestEveryVictoriaMetricsSelectorIsCoveredByAQuery(t *testing.T) {
	for _, s := range vmSelectors {
		parsed := mustCompile(t, s.selector)
		requirements, _ := parsed.Requirements()
		labelled := map[string]string{}
		for _, r := range requirements {
			labelled[r.Key()] = r.Values().List()[0]
		}
		covered := false
		for _, query := range promQueries {
			if mustCompile(t, query).Matches(labels.Set(labelled)) {
				covered = true
			}
		}
		if !covered {
			t.Errorf("no query returns a Service matching %q", s.selector)
		}
	}
}

// --- A target with a path ----------------------------------------------------

func TestATargetWithAPathIsReadAsAddressWritesIt(t *testing.T) {
	for _, raw := range []string{
		"vm/vmselect-main:8481/select/0/prometheus",
		"monitoring/prometheus:9090/prom",
		"vm/vmselect-main:8481/select/team-a.b_c~1/prometheus",
	} {
		got, err := ParseTarget(raw)
		if err != nil || got.Address() != raw {
			t.Errorf("%q: got %+v, %v", raw, got, err)
		}
	}
	got, _ := ParseTarget("vm/vmselect-main:8481/select/0/prometheus")
	if got.Path != "/select/0/prometheus" || got.Port != 8481 {
		t.Errorf("parsed as %+v", got)
	}
	for _, bad := range []string{
		"vm/vmselect:8481/",                     // trailing slash, empty path
		"vm/vmselect:8481/select//prometheus",   // empty segment
		"vm/vmselect:8481/select/0/prometheus/", // trailing slash
		"vm/vmselect:8481/../../secrets",        // leaves the proxy path
		"vm/vmselect:8481/select/./prometheus",  // dot segment
		"vm/vmselect:8481/select?x=1",           // a query
		"vm/vmselect:8481/select#x",             // a fragment
		"vm/vmselect:8481/sel ect",              // a space
		"vm/vmselect:8481/%2e%2e",               // an escape
		"vm/vmselect:08481/select",              // a port written oddly
		"vm/vmselect:8481/" + strings.Repeat("a", maxTargetPath),
	} {
		if target, err := ParseTarget(bad); err == nil {
			t.Errorf("%q was accepted as %+v", bad, target)
		}
	}
}

func TestAPathIsUnderTheServiceProxy(t *testing.T) {
	target := &PromTarget{Namespace: "vm", Service: "vmselect-main", Port: 8481, Path: "/select/0/prometheus"}
	want := "/api/v1/namespaces/vm/services/vmselect-main:8481/proxy/select/0/prometheus/api/v1/query"
	if got := target.proxyPath(endpointQuery); got != want {
		t.Fatalf("proxy path %q, want %q", got, want)
	}
}

// Queries go under the path on both transports; the health check finds the
// root when the path has none.
func TestAPathIsUsedThroughThePortForward(t *testing.T) {
	var asked []string
	vmselect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		switch r.URL.Path {
		case endpointHealth:
			w.Write([]byte(vmHealthy))
		case vmselectPath + endpointQuery:
			w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
		default:
			http.Error(w, "unsupported path", http.StatusBadRequest)
		}
	}))
	defer vmselect.Close()

	h := newHarness(t, vmselectOperator())
	h.tunnels.addr = strings.TrimPrefix(vmselect.URL, "http://")

	found := h.metrics.Discover(context.Background(), testCluster, false)
	if found.Kind != KindPrometheus || found.Transport != TransportForward || found.Engine != EngineVictoriaMetrics {
		t.Fatalf("kind %q transport %q engine %q (%s)", found.Kind, found.Transport, found.Engine, found.Detail)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := h.metrics.Query(ctx, testCluster, "up"); err != nil {
		t.Fatalf("query: %v (asked %v)", err, asked)
	}
	if last := asked[len(asked)-1]; last != vmselectPath+endpointQuery {
		t.Errorf("the query went to %q", last)
	}
	h.metrics.Close()
}

// A Prometheus served under --web.route-prefix answers its health under the
// path, and is not asked at the root.
func TestAPrometheusUnderARoutePrefixIsAskedUnderIt(t *testing.T) {
	target, err := ParseTarget("monitoring/prometheus:9090/prom")
	if err != nil {
		t.Fatal(err)
	}
	h := newHarnessWith(t, func(o *Options) { o.Configured, o.Prometheus = true, target })
	h.answer(rawAnswers{target.proxyPath(endpointHealth): {body: []byte("Prometheus Server is Healthy.\n")}})

	found := h.metrics.Discover(context.Background(), testCluster, false)
	if found.Kind != KindPrometheus || found.Transport != TransportProxy || found.Engine != EnginePrometheus {
		t.Fatalf("kind %q transport %q engine %q (%s)", found.Kind, found.Transport, found.Engine, found.Detail)
	}
	if h.askedFor(target.proxyRoot(endpointHealth)) {
		t.Errorf("the root was asked although the path answered: %v", h.paths)
	}
}

func TestTheEngineIsReadFromTheHealthAnswer(t *testing.T) {
	for _, c := range []struct{ body, fallback, want string }{
		{vmHealthy, EnginePrometheus, EngineVictoriaMetrics},
		{"Prometheus Server is Healthy.\n", EngineVictoriaMetrics, EnginePrometheus},
		{"OK", EngineVictoriaMetrics, EngineVictoriaMetrics},
		{"", "", EnginePrometheus},
	} {
		if got := engineOf([]byte(c.body), c.fallback); got != c.want {
			t.Errorf("engineOf(%q, %q) = %q, want %q", c.body, c.fallback, got, c.want)
		}
	}
}

func (h *harness) askedFor(path string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, p := range h.paths {
		if p == path {
			return true
		}
	}
	return false
}

// --- A source set in Settings ------------------------------------------------

// The desktop's choice for one cluster is the only source queried there, even
// where discovery would find a Prometheus.
func TestASourceSetInSettingsIsQueriedInsteadOfDiscovery(t *testing.T) {
	set := &PromTarget{Namespace: "vm", Service: "vmselect-main", Port: 8481, Path: vmselectPath}
	h := newHarnessWith(t, func(o *Options) {
		o.Sources = func(cluster string) *PromTarget {
			if cluster == testCluster {
				copied := *set
				return &copied
			}
			return nil
		}
	}, promService("monitoring", "kps-prometheus", map[string]string{"app": "kube-prometheus-stack-prometheus"}, "http-web", 9090))
	h.answer(rawAnswers{
		set.proxyRoot(endpointHealth): {body: []byte(vmHealthy)},
		set.proxyPath(endpointQuery):  {body: []byte(`{"status":"success","data":{"resultType":"scalar","result":[0,"1"]}}`)},
	})

	found := h.metrics.Discover(context.Background(), testCluster, false)
	if found.Kind != KindPrometheus || found.Prometheus.Address() != set.Address() || found.Engine != EngineVictoriaMetrics {
		t.Fatalf("kind %q target %+v engine %q (%s)", found.Kind, found.Prometheus, found.Engine, found.Detail)
	}
	if !strings.Contains(found.Detail, SourceSetting) {
		t.Errorf("the detail does not say the source was set: %q", found.Detail)
	}
	if h.listed() != 0 {
		t.Errorf("Services were listed although the source was set: %d lists", h.listed())
	}
}

// One that does not answer is reported under its own name, not replaced.
func TestASourceSetInSettingsThatDoesNotAnswerIsNotReplaced(t *testing.T) {
	set := &PromTarget{Namespace: "vm", Service: "gone", Port: 8428}
	h := newHarnessWith(t, func(o *Options) {
		o.Sources = func(string) *PromTarget { copied := *set; return &copied }
	}, promService("monitoring", "kps-prometheus", map[string]string{"app": "kube-prometheus-stack-prometheus"}, "http-web", 9090))
	h.tunnels.err = errors.New("no endpoints")
	h.answer(rawAnswers{})

	found := h.metrics.Discover(context.Background(), testCluster, false)
	if found.Kind != KindUnreachable || found.Prometheus == nil || found.Prometheus.Address() != set.Address() {
		t.Fatalf("kind %q target %+v (%s)", found.Kind, found.Prometheus, found.Detail)
	}
}

// The server profile never reads it: the deployment chooses there.
func TestTheServerProfileIgnoresSourcesSetInSettings(t *testing.T) {
	configured := &PromTarget{Namespace: "monitoring", Service: "kps-prometheus", Port: 9090}
	h := newHarnessWith(t, func(o *Options) {
		o.Configured, o.Prometheus = true, configured
		o.Sources = func(string) *PromTarget { return &PromTarget{Namespace: "team-a", Service: "x", Port: 9090} }
	})
	h.answer(rawAnswers{configured.proxyPath(endpointHealth): {body: []byte("Prometheus Server is Healthy.")}})

	found := h.metrics.Discover(context.Background(), testCluster, false)
	if found.Prometheus == nil || found.Prometheus.Address() != configured.Address() {
		t.Fatalf("target %+v (%s)", found.Prometheus, found.Detail)
	}
}

// Every engine answers health at its root, so a typo in the path
// (`/select/O/prometheus`) would pass a health check that falls back there.
// The path is confirmed by a query, and the target is reported as not
// reached, under its own name: the server profile's rule for a configured
// target that does not answer.
func TestAPathThatOnlyTheRootAnswersIsNotReached(t *testing.T) {
	target, err := ParseTarget("vm/vmselect-main:8481/select/O/prometheus")
	if err != nil {
		t.Fatal(err)
	}
	h := newHarnessWith(t, func(o *Options) { o.Configured, o.Prometheus = true, target })
	h.answer(rawAnswers{target.proxyRoot(endpointHealth): {body: []byte(vmHealthy)}})

	found := h.metrics.Discover(context.Background(), testCluster, false)
	if found.Kind != KindUnreachable || found.Prometheus == nil || found.Prometheus.Address() != target.Address() {
		t.Fatalf("kind %q target %+v (%s); want unreachable, under its own name", found.Kind, found.Prometheus, found.Detail)
	}
	if !strings.Contains(found.Detail, "not under /select/O/prometheus") {
		t.Errorf("the detail does not name the path: %q", found.Detail)
	}
	if opened, _ := h.tunnels.counts(); opened != 0 {
		t.Errorf("%d port-forwards opened to a Service the proxy already reached", opened)
	}
}

// The same through the port-forward.
func TestAPathThatOnlyTheRootAnswersIsNotReachedThroughThePortForward(t *testing.T) {
	vmselect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == endpointHealth {
			w.Write([]byte(vmHealthy))
			return
		}
		http.Error(w, "unsupported path", http.StatusBadRequest)
	}))
	defer vmselect.Close()
	target, _ := ParseTarget("vm/vmselect-main:8481/select/O/prometheus")
	h := newHarnessWith(t, func(o *Options) { o.Configured, o.Prometheus = true, target })
	h.tunnels.addr = strings.TrimPrefix(vmselect.URL, "http://")

	found := h.metrics.Discover(context.Background(), testCluster, false)
	if found.Kind != KindUnreachable || !strings.Contains(found.Detail, "not under /select/O/prometheus") {
		t.Fatalf("kind %q (%s)", found.Kind, found.Detail)
	}
}

// One health attempt that hangs leaves the next its own time.
func TestEachHealthAttemptHasItsOwnTimeout(t *testing.T) {
	target := &PromTarget{Namespace: "monitoring", Service: "prometheus", Port: 9090, Path: "/prom"}
	asked := 0
	body, err := target.answers(context.Background(), 50*time.Millisecond, func(ctx context.Context, path string, _ url.Values) ([]byte, error) {
		asked++
		if path == "/prom"+endpointHealth {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return []byte("Prometheus Server is Healthy."), nil
	})
	if err != nil || string(body) != "Prometheus Server is Healthy." || asked != 3 {
		t.Fatalf("%q, %v after %d asks", body, err, asked)
	}
}

// Only "not here" - a 404, or VictoriaMetrics' 400 - is a wrong path (#156).
// A 401, a 5xx or a timeout on the confirming query says nothing about the
// path: it comes back as itself, so the proxy's failure falls back to the
// port-forward instead of being reported as a typo.
func TestOnlyNotFoundOrBadRequestIsAWrongPath(t *testing.T) {
	target := &PromTarget{Namespace: "vm", Service: "vmselect-main", Port: 8481, Path: "/select/0/prometheus"}
	status := func(code int) error {
		return apierrors.NewGenericServerResponse(code, http.MethodGet, schema.GroupResource{Resource: "prometheus"}, "", "x", 0, false)
	}
	for _, c := range []struct {
		name  string
		err   error
		wrong bool
	}{
		{"404", status(http.StatusNotFound), true},
		{"400, unsupported path", status(http.StatusBadRequest), true},
		{"401", status(http.StatusUnauthorized), false},
		{"503", status(http.StatusServiceUnavailable), false},
		{"timeout", context.DeadlineExceeded, false},
	} {
		_, err := target.answers(context.Background(), time.Second, func(_ context.Context, path string, _ url.Values) ([]byte, error) {
			if path == endpointHealth {
				return []byte(vmHealthy), nil
			}
			if strings.HasSuffix(path, endpointQuery) {
				return nil, c.err
			}
			return nil, status(http.StatusNotFound)
		})
		var wrongPath *pathRefused
		if got := errors.As(err, &wrongPath); got != c.wrong || err == nil {
			t.Errorf("%s: %v, wrong path %v, want %v", c.name, err, got, c.wrong)
		}
	}
}

// The proxy reaches the Service's root, but an authenticating front refuses
// the query under the path with a 401 (#156). That says nothing about the
// path: the port-forward is tried, and the target is reached through it.
func TestARefusedQueryUnderThePathFallsBackToThePortForward(t *testing.T) {
	target, _ := ParseTarget("vm/vmselect-main:8481/select/0/prometheus")
	vmselect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case endpointHealth:
			w.Write([]byte(vmHealthy))
		case target.Path + endpointQuery:
			w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
		default:
			http.Error(w, "unsupported path", http.StatusBadRequest)
		}
	}))
	defer vmselect.Close()
	h := newHarnessWith(t, func(o *Options) { o.Configured, o.Prometheus = true, target })
	unauthorised := apierrors.NewGenericServerResponse(http.StatusUnauthorized, http.MethodGet, schema.GroupResource{Resource: "services"}, "", "", 0, false)
	h.answer(rawAnswers{
		target.proxyRoot(endpointHealth):              {body: []byte(vmHealthy)},
		target.proxyRoot(target.Path + endpointQuery): {err: unauthorised},
	})
	h.tunnels.addr = strings.TrimPrefix(vmselect.URL, "http://")

	found := h.metrics.Discover(context.Background(), testCluster, false)
	if found.Kind != KindPrometheus || found.Transport != TransportForward {
		t.Fatalf("kind %q transport %q (%s); want reached by the port-forward", found.Kind, found.Transport, found.Detail)
	}
	h.metrics.Close()
}

// The refusal names at most three namespaces of each engine, then counts the
// rest, so the detail stays one line (#133).
func TestTheCrossEngineRefusalNamesAtMostThreeNamespacesEach(t *testing.T) {
	prom := map[string]bool{"a": true, "b": true, "c": true, "d": true, "e": true}
	got := notChosenAcross(prom, map[string]bool{"vm": true})
	if !strings.Contains(got, "Prometheus (a, b, c and 2 more)") || !strings.Contains(got, "VictoriaMetrics (vm)") {
		t.Errorf("%q", got)
	}
}

package metrics

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/thonicdev/kubetower/internal/cluster"
)

const testCluster = "SHOP-EKS-1"

// --- Test doubles ------------------------------------------------------------

type fakeClusters struct {
	contexts  []string
	clientset kubernetes.Interface
	failWith  error
}

func (f *fakeClusters) Resolve(name string) (string, bool) {
	for _, candidate := range f.contexts {
		if candidate == name || cluster.MakeDNSFriendly(candidate) == name {
			return candidate, true
		}
	}
	return "", false
}

func (f *fakeClusters) Client(name string) (*cluster.Client, error) {
	if f.failWith != nil {
		return nil, f.failWith
	}
	if _, ok := f.Resolve(name); !ok {
		return nil, fmt.Errorf("unknown cluster %q", name)
	}
	return &cluster.Client{Context: name, Clients: f.clientset}, nil
}

// fakeTunnels hands out an address a test already serves on, so the
// port-forward transport can be exercised without a cluster or a SPDY upgrade.
type fakeTunnels struct {
	addr   string
	err    error
	mu     sync.Mutex
	opened int
	closed int
	last   *fakeTunnel
}

func (f *fakeTunnels) Open(_ context.Context, _, _, _ string, _ int) (Tunnel, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.mu.Lock()
	f.opened++
	tunnel := &fakeTunnel{owner: f, done: make(chan struct{})}
	f.last = tunnel
	f.mu.Unlock()
	return tunnel, nil
}

// killLast stops the newest tunnel as its pod going would: Done closes, and
// nobody called Close.
func (f *fakeTunnels) killLast() {
	f.mu.Lock()
	defer f.mu.Unlock()
	close(f.last.done)
}

func (f *fakeTunnels) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opened, f.closed
}

type fakeTunnel struct {
	owner *fakeTunnels
	done  chan struct{}
}

func (t *fakeTunnel) Addr() string          { return t.owner.addr }
func (t *fakeTunnel) Done() <-chan struct{} { return t.done }
func (t *fakeTunnel) Close() {
	t.owner.mu.Lock()
	t.owner.closed++
	t.owner.mu.Unlock()
}

// rawAnswers is a stand-in for the cluster's REST client, keyed by the path the
// code asks for. A path with no answer is a 404, which is what the API server
// says about an aggregated API nobody installed.
type rawAnswers map[string]rawAnswer

type rawAnswer struct {
	body []byte
	err  error
}

func (r rawAnswers) fn(mu *sync.Mutex, seen *[]string) rawFunc {
	return func(_ context.Context, _ *cluster.Client, path string, _ url.Values) ([]byte, error) {
		if seen != nil {
			mu.Lock()
			*seen = append(*seen, path)
			mu.Unlock()
		}
		if answer, ok := r[path]; ok {
			return answer.body, answer.err
		}
		return nil, apierrors.NewNotFound(schema.GroupResource{Group: "metrics.k8s.io"}, path)
	}
}

type harness struct {
	metrics   *Metrics
	clientset *fake.Clientset
	tunnels   *fakeTunnels

	// Guarded: several tests below drive the same harness from more than one
	// goroutine, which is the only way a single-flight can be tested at all.
	mu        sync.Mutex
	selectors []string
	paths     []string
	logged    []string
}

func newHarness(t *testing.T, objects ...runtime.Object) *harness {
	t.Helper()
	return newHarnessWith(t, nil, objects...)
}

// newHarnessWith is newHarness with the Options adjusted first, for the
// server profile's configured Prometheus (Options.Configured).
func newHarnessWith(t *testing.T, adjust func(*Options), objects ...runtime.Object) *harness {
	t.Helper()

	h := &harness{clientset: fake.NewClientset(objects...), tunnels: &fakeTunnels{}}
	h.clientset.PrependReactor("list", "services", func(action k8stesting.Action) (bool, runtime.Object, error) {
		list, ok := action.(k8stesting.ListAction)
		if ok {
			h.mu.Lock()
			h.selectors = append(h.selectors, list.GetListRestrictions().Labels.String())
			h.mu.Unlock()
		}
		return false, nil, nil
	})

	opts := Options{
		Clusters: &fakeClusters{contexts: []string{testCluster, "docker-desktop"}, clientset: h.clientset},
		Tunnels:  h.tunnels,
		Logf: func(format string, v ...any) {
			h.mu.Lock()
			h.logged = append(h.logged, fmt.Sprintf(format, v...))
			h.mu.Unlock()
		},
	}
	if adjust != nil {
		adjust(&opts)
	}
	h.metrics = New(opts)
	t.Cleanup(h.metrics.Close)
	// Nothing answers unless a test says so; the default is a cluster with no
	// aggregated metrics API.
	h.answer(rawAnswers{})
	return h
}

func (h *harness) answer(answers rawAnswers) {
	h.mu.Lock()
	h.paths = nil
	h.mu.Unlock()
	h.metrics.raw = answers.fn(&h.mu, &h.paths)
}

func (h *harness) listed() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.selectors)
}

func (h *harness) lines() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.logged...)
}

// failServices makes every service list fail, which is how an unreachable or
// refused cluster presents itself to the discovery.
func (h *harness) failServices(err error) {
	h.clientset.PrependReactor("list", "services", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, err
	})
}

func promService(namespace, name string, labelSet map[string]string, portName string, port int32) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Labels: labelSet},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{{Name: portName, Port: port}},
		},
	}
}

// --- The selectors -----------------------------------------------------------

// The list decides whether a cluster running a full Prometheus reads as having
// only a metrics-server. Both the membership and the order are pinned: dropping
// the plain `app` key makes kube-prometheus-stack invisible, and
// moving `operated-prometheus=true` up would let a Thanos sidecar's Service win
// over the real one.
func TestPrometheusSelectorsCoverKubePrometheusStackAndAreTriedMostSpecificFirst(t *testing.T) {
	want := []string{
		"headlamp-prometheus=true",
		"app.kubernetes.io/name=prometheus,app.kubernetes.io/component=server",
		"app.kubernetes.io/name=prometheus",
		"app=prometheus",
		"app=kube-prometheus-stack-prometheus",
		"operated-prometheus=true",
	}
	if len(promSelectors) != len(want) {
		t.Fatalf("promSelectors has %d entries, want %d: %v", len(promSelectors), len(want), promSelectors)
	}
	for i, selector := range want {
		if promSelectors[i] != selector {
			t.Errorf("selector %d is %q, want %q", i, promSelectors[i], selector)
		}
		if _, err := labels.Parse(promSelectors[i]); err != nil {
			t.Errorf("selector %q does not parse as a label selector: %v", promSelectors[i], err)
		}
	}
}

func TestEveryQueryIsSentBeforeGivingUpOnPrometheus(t *testing.T) {
	h := newHarness(t)
	h.metrics.Discover(context.Background(), testCluster, false)

	if len(h.selectors) != len(promQueries) {
		t.Fatalf("sent %d queries (%v), want all %d", len(h.selectors), h.selectors, len(promQueries))
	}
	for i, query := range promQueries {
		// The server-side form is normalized — requirements come back sorted by
		// key — so the comparison goes through the same parser.
		want := mustParse(t, query)
		if h.selectors[i] != want {
			t.Errorf("query %d was %q, want %q", i, h.selectors[i], want)
		}
	}
}

// The six selectors are the preference order and the four queries are what is
// actually asked of the API server. A selector added above without widening a
// query would be applied to a set of Services that can never contain a match —
// silently, and the symptom is the one that cost six clusters their Prometheus:
// a confident "metrics-server" for a cluster running a full stack.
//
// So: for every selector, a Service labeled exactly as that selector demands
// must be returned by at least one query.
func TestEverySelectorIsCoveredByAQuery(t *testing.T) {
	for _, selector := range promSelectors {
		parsed := mustCompile(t, selector)
		labelled := labels.Set{}
		requirements, selectable := parsed.Requirements()
		if !selectable {
			t.Fatalf("%q selects nothing at all", selector)
		}
		for _, r := range requirements {
			values := r.Values().List()
			if len(values) == 0 {
				t.Fatalf("%q has a requirement with no value to build a Service from", selector)
			}
			labelled[r.Key()] = values[0]
		}
		if !parsed.Matches(labelled) {
			t.Fatalf("could not build a Service matching %q; the test is wrong, not the code", selector)
		}

		covered := false
		for _, query := range promQueries {
			if mustCompile(t, query).Matches(labelled) {
				covered = true
				break
			}
		}
		if !covered {
			t.Errorf("no query in promQueries returns a Service matching %q, so that selector can never match anything", selector)
		}
	}
}

func mustCompile(t *testing.T, selector string) labels.Selector {
	t.Helper()
	parsed, err := labels.Parse(selector)
	if err != nil {
		t.Fatalf("parsing %q: %v", selector, err)
	}
	return parsed
}

// The order is applied in memory rather than by asking four times, and it is
// the part that is load-bearing: `operated-prometheus=true` also matches a
// Thanos sidecar's Service on some releases, so a Service carrying both labels
// must still be reported under the more specific one.
func TestThePreferenceOrderSurvivesBeingAppliedInMemory(t *testing.T) {
	both := promService("monitoring", "prometheus-operated",
		map[string]string{"app": "prometheus", "operated-prometheus": "true"}, "web", 9090)
	h := newHarness(t, both)
	h.answer(rawAnswers{
		"/api/v1/namespaces/monitoring/services/prometheus-operated:9090/proxy/-/healthy": {body: []byte("ok")},
	})

	found := h.metrics.Discover(context.Background(), testCluster, false)
	if found.Kind != KindPrometheus {
		t.Fatalf("kind is %q (%s)", found.Kind, found.Detail)
	}
	if !strings.Contains(found.Detail, "app=prometheus") || strings.Contains(found.Detail, "operated-prometheus") {
		t.Errorf("claimed under the wrong selector: %q", found.Detail)
	}
}

func mustParse(t *testing.T, selector string) string {
	t.Helper()
	parsed, err := labels.Parse(selector)
	if err != nil {
		t.Fatalf("parsing %q: %v", selector, err)
	}
	return parsed.String()
}

func TestKubePrometheusStackIsFoundUnderThePlainAppLabel(t *testing.T) {
	h := newHarness(t, promService("monitoring", "kps-prometheus",
		map[string]string{"app": "kube-prometheus-stack-prometheus"}, "http-web", 9090))
	h.answer(rawAnswers{
		"/api/v1/namespaces/monitoring/services/kps-prometheus:9090/proxy/-/healthy": {body: []byte("Prometheus is Healthy.")},
	})

	found := h.metrics.Discover(context.Background(), testCluster, false)
	if found.Kind != KindPrometheus {
		t.Fatalf("kind is %q (%s), want prometheus", found.Kind, found.Detail)
	}
	if found.Prometheus.Address() != "monitoring/kps-prometheus:9090" {
		t.Errorf("target is %q", found.Prometheus.Address())
	}
	if !strings.Contains(found.Detail, "app=kube-prometheus-stack-prometheus") {
		t.Errorf("detail does not name the selector that found it: %q", found.Detail)
	}
}

// A port called "http" is exposed by the operator, kube-state-metrics and every
// exporter in a kube-prometheus-stack release. Choosing one of those gives a
// Prometheus that answers every query with nothing.
func TestAPortMerelyNamedHTTPIsNotTakenForPrometheus(t *testing.T) {
	h := newHarness(t, promService("monitoring", "kube-state-metrics",
		map[string]string{"app": "prometheus"}, "http", 8080))
	h.answer(rawAnswers{metricsAPIPath: {body: []byte("{}")}})

	found := h.metrics.Discover(context.Background(), testCluster, false)
	if found.Kind != KindMetricsServer {
		t.Fatalf("kind is %q (%s), want metrics-server", found.Kind, found.Detail)
	}
}

// The prometheus-community/prometheus chart publishes its server as `http` on
// port 80, forwarding to 9090. Refusing it would read a cluster running the
// most common chart outside the operator as having no Prometheus at all.
func TestAServerPortThatForwardsTo9090IsPrometheus(t *testing.T) {
	svc := promService("monitoring", "prom-prometheus-server", map[string]string{
		"app.kubernetes.io/name": "prometheus", "app.kubernetes.io/component": "server",
	}, "http", 80)
	svc.Spec.Ports[0].TargetPort = intstr.FromInt32(9090)
	h := newHarness(t, svc)
	h.answer(rawAnswers{
		"/api/v1/namespaces/monitoring/services/prom-prometheus-server:80/proxy/-/healthy": {body: []byte("Prometheus Server is Healthy.")},
	})

	found := h.metrics.Discover(context.Background(), testCluster, false)
	if found.Kind != KindPrometheus {
		t.Fatalf("kind is %q (%s), want prometheus", found.Kind, found.Detail)
	}
	// Reached on the Service's own port: the API server's proxy follows the
	// targetPort itself.
	if found.Prometheus.Address() != "monitoring/prom-prometheus-server:80" {
		t.Errorf("target is %q", found.Prometheus.Address())
	}
}

// The guard that still holds: an exporter's `http` port forwards to itself, never
// to 9090, and a named targetPort is not followed.
func TestAnHTTPPortThatForwardsElsewhereIsStillNotPrometheus(t *testing.T) {
	for _, target := range []intstr.IntOrString{intstr.FromInt32(8080), intstr.FromString("web")} {
		svc := promService("monitoring", "kube-state-metrics",
			map[string]string{"app": "prometheus"}, "http", 8080)
		svc.Spec.Ports[0].TargetPort = target
		h := newHarness(t, svc)
		h.answer(rawAnswers{metricsAPIPath: {body: []byte("{}")}})

		if found := h.metrics.Discover(context.Background(), testCluster, false); found.Kind != KindMetricsServer {
			t.Errorf("targetPort %s: kind is %q (%s), want metrics-server", target.String(), found.Kind, found.Detail)
		}
	}
}

// --- The classification: could-not-ask is never none ------------------------

// "None" — no metrics on this cluster — must never be the answer for a cluster
// that could not be authenticated against: it reads "neither Prometheus nor
// metrics-server answers" and then "prometheus" when probed again.
func TestAnExpiredSSOSessionIsReportedAsCouldNotAskAndNeverAsNone(t *testing.T) {
	h := newHarness(t)
	h.failServices(errors.New("getting credentials: exec: executable aws failed with exit code 255"))

	found := h.metrics.Discover(context.Background(), testCluster, false)
	if found.Kind == KindNone {
		t.Fatalf("an unauthenticated cluster was reported as having no metrics: %+v", found)
	}
	if !found.CouldNotAsk() {
		t.Fatalf("kind is %q, want an unreachable/unauthorised one", found.Kind)
	}
	if len(h.paths) != 0 {
		t.Errorf("metrics-server was asked anyway, over %v", h.paths)
	}
}

func TestNoneIsOnlyReportedWhenTheClusterItselfAnswered(t *testing.T) {
	h := newHarness(t)
	h.answer(rawAnswers{metricsAPIPath: {err: apierrors.NewNotFound(schema.GroupResource{Group: "metrics.k8s.io"}, "v1beta1")}})

	found := h.metrics.Discover(context.Background(), testCluster, false)
	if found.Kind != KindNone {
		t.Fatalf("kind is %q (%s), want none", found.Kind, found.Detail)
	}
}

func TestAnUnauthorisedMetricsAPIIsNotAnAbsence(t *testing.T) {
	h := newHarness(t)
	h.answer(rawAnswers{metricsAPIPath: {err: apierrors.NewUnauthorized("the bearer token is not accepted")}})

	found := h.metrics.Discover(context.Background(), testCluster, false)
	if found.Kind != KindUnauthorised {
		t.Fatalf("kind is %q (%s), want unauthorized", found.Kind, found.Detail)
	}
}

// A 403 on a cluster-wide list of services says nothing about metrics.k8s.io,
// which a read-only role is usually allowed. Stopping there would report an
// absence for a cluster with a working metrics-server.
func TestAForbiddenServiceListStillLetsMetricsServerAnswer(t *testing.T) {
	h := newHarness(t)
	h.failServices(apierrors.NewForbidden(schema.GroupResource{Resource: "services"}, "",
		errors.New("no RBAC rule allows listing services at cluster scope")))
	h.answer(rawAnswers{metricsAPIPath: {body: []byte("{}")}})

	found := h.metrics.Discover(context.Background(), testCluster, false)
	if found.Kind != KindMetricsServer {
		t.Fatalf("kind is %q (%s), want metrics-server", found.Kind, found.Detail)
	}
	if !strings.Contains(found.Detail, "Prometheus could not be looked for") {
		t.Errorf("detail hides that Prometheus was never looked for: %q", found.Detail)
	}
}

func TestAForbiddenClusterWithNoMetricsServerIsNotReportedAsEmpty(t *testing.T) {
	h := newHarness(t)
	h.failServices(apierrors.NewForbidden(schema.GroupResource{Resource: "services"}, "", errors.New("denied")))

	found := h.metrics.Discover(context.Background(), testCluster, false)
	if found.Kind != KindUnauthorised {
		t.Fatalf("kind is %q (%s), want unauthorized", found.Kind, found.Detail)
	}
}

func TestAPrometheusThatIsFoundButUnreachableIsNotAnAbsence(t *testing.T) {
	h := newHarness(t, promService("monitoring", "prometheus",
		map[string]string{"app": "prometheus"}, "web", 9090))
	h.tunnels.err = errors.New("no running pod behind service monitoring/prometheus")
	// The service proxy fails too: that is every EKS context here.
	h.answer(rawAnswers{})

	found := h.metrics.Discover(context.Background(), testCluster, false)
	if found.Kind != KindUnreachable {
		t.Fatalf("kind is %q (%s), want unreachable", found.Kind, found.Detail)
	}
	if found.Prometheus == nil {
		t.Fatal("the target that was found is not reported, so nobody can act on it")
	}
	if !strings.Contains(found.Detail, "port-forward") {
		t.Errorf("detail does not say which transports were tried: %q", found.Detail)
	}
}

func TestAnUnknownClusterIsNotCachedAsAMetricsAnswer(t *testing.T) {
	h := newHarness(t)
	found := h.metrics.Discover(context.Background(), "not-in-the-kubeconfig", false)
	if !found.CouldNotAsk() {
		t.Fatalf("kind is %q, want an admission that nothing was asked", found.Kind)
	}
	if len(h.metrics.entries) != 0 {
		t.Errorf("a caller's typo was cached as a cluster: %v", h.metrics.entries)
	}
}

func TestClassifyReadsA404AsEvidenceAboutTheClusterAndNothingElseAs(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string // "" means: not a failure to ask
	}{
		{"an absent aggregated API", apierrors.NewNotFound(schema.GroupResource{}, "x"), ""},
		{"a rejected credential", apierrors.NewUnauthorized("no"), KindUnauthorised},
		{"a refused read", apierrors.NewForbidden(schema.GroupResource{}, "x", errors.New("no")), KindUnauthorised},
		{"a server timeout", apierrors.NewTimeoutError("too slow", 1), KindUnreachable},
		{"an unavailable API", apierrors.NewServiceUnavailable("no endpoints"), KindUnreachable},
		{"an expired SSO session", errors.New(`getting credentials: exec: executable aws failed with exit code 255`), KindUnreachable},
		{"a refused connection", errors.New("dial tcp 127.0.0.1:6443: connect: connection refused"), KindUnreachable},
		{"a connect the console's dial bound ended", errors.New(`Get "https://10.0.0.1:6443/api/v1/services?labelSelector=x&resourceVersion=0": dial tcp 10.0.0.1:6443: i/o timeout`), KindUnreachable},
		{"an unparseable body", errors.New("json: cannot unmarshal object into field"), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := classify(c.err)
			switch {
			case c.want == "" && got != nil:
				t.Fatalf("classified as %q, want evidence about the cluster", got.kind)
			case c.want != "" && got == nil:
				t.Fatalf("not classified, so it would end up described as an absence")
			case c.want != "" && got.kind != c.want:
				t.Fatalf("classified as %q, want %q", got.kind, c.want)
			}
		})
	}
}

// A 403 is the one refusal that is not total: the same identity often reads
// metrics.k8s.io while being refused a cluster-wide list of services.
func TestOnlyA403LetsTheProbeCarryOn(t *testing.T) {
	forbidden := classify(apierrors.NewForbidden(schema.GroupResource{}, "x", errors.New("no")))
	if forbidden == nil || forbidden.total {
		t.Fatalf("a 403 is total, so metrics-server would never be asked: %+v", forbidden)
	}
	unauthorised := classify(apierrors.NewUnauthorized("no"))
	if unauthorised == nil || !unauthorised.total {
		t.Fatalf("a 401 is not total, so a cluster that accepts no credential is asked twice: %+v", unauthorised)
	}

	// And a connect that ran out of time is total, in the words internal/cluster's
	// dial bound produces. Without that bound, on Windows the kernel gives up on
	// the connect first, at 21 s, with a sentence of its own (`connectex: A
	// connection attempt failed because the connected party did not properly
	// respond after a period of time`) that matches nothing here — so the probe
	// reads it as "another query is free to try", runs all four Service queries
	// and then metrics.k8s.io, and an overview on that cluster costs five
	// connects in a row: 1m45s. The bound makes the
	// dialer speak first and in Go's words; this pins that those words end the
	// probe at the first one.
	timedOut := classify(errors.New(`Get "https://10.0.0.1:6443/api/v1/services?resourceVersion=0": dial tcp 10.0.0.1:6443: i/o timeout`))
	if timedOut == nil || !timedOut.total {
		t.Fatalf("a connect that timed out is not total, so an unreachable cluster is asked five times over: %+v", timedOut)
	}
}

// The kind strings have one spelling, here. If a consumer outside this package
// ever switches on them, that consumer is where a pin against drift belongs.

// --- Caching -----------------------------------------------------------------

func TestDiscoveryIsCachedAndAFailureIsRetriedSooner(t *testing.T) {
	h := newHarness(t)
	h.answer(rawAnswers{metricsAPIPath: {body: []byte("{}")}})
	ctx := context.Background()

	h.metrics.Discover(ctx, testCluster, false)
	first := h.listed()
	h.metrics.Discover(ctx, testCluster, false)
	if h.listed() != first {
		t.Fatalf("a cached answer was probed again: %d selectors then %d", first, h.listed())
	}

	// A success is held for the full TTL...
	h.metrics.entry(testCluster).result.CheckedAt = time.Now().Add(-2 * failureTTL)
	h.metrics.Discover(ctx, testCluster, false)
	if h.listed() != first {
		t.Fatalf("a good answer expired after %s", 2*failureTTL)
	}

	// ...whereas "could not ask" is not, because the cure is on the operator's
	// side and takes effect immediately.
	h.metrics.entry(testCluster).result = &Discovery{
		Kind:      KindUnreachable,
		Detail:    "an expired session",
		CheckedAt: time.Now().Add(-2 * failureTTL),
	}
	h.metrics.Discover(ctx, testCluster, false)
	if h.listed() == first {
		t.Fatal("a failure was cached as long as a success, so the operator cannot see their fix work")
	}
}

func TestForceReprobesEvenWithinTheTTL(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.metrics.Discover(ctx, testCluster, false)
	first := h.listed()
	h.metrics.Discover(ctx, testCluster, true)
	if h.listed() == first {
		t.Fatal("force did not re-probe, so the probe-again button does nothing")
	}
}

// --- One probe at a time, and a caller who can leave --------------------------

// The probe is six cluster-wide Service lists, a 15 s proxy check, a
// port-forward and a 15 s tunnel check. Thirteen contexts arriving at once must
// produce one of those per cluster, not thirteen.
func TestConcurrentDiscoveriesShareOneProbe(t *testing.T) {
	h := newHarness(t)
	release := make(chan struct{})
	h.clientset.PrependReactor("list", "services", func(k8stesting.Action) (bool, runtime.Object, error) {
		<-release
		return false, nil, nil
	})

	const callers = 8
	var wg sync.WaitGroup
	wg.Add(callers)
	for range callers {
		go func() {
			defer wg.Done()
			h.metrics.Discover(context.Background(), testCluster, false)
		}()
	}

	// Let them all pile up on the one probe before it is allowed to finish.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := h.listed(); got != len(promQueries) {
		t.Fatalf("%d callers produced %d Service lists, want the %d of a single probe",
			callers, got, len(promQueries))
	}
}

// A mutex held across the probe would leave a waiter stuck: a request whose
// browser had gone away could not leave the queue, so under a two-second
// refresh and a 30 s failure TTL the queue would never drain. A waiter must be
// able to give up.
func TestAWaitingCallerLeavesWhenItsOwnRequestIsCancelled(t *testing.T) {
	h := newHarness(t)
	release := make(chan struct{})
	defer close(release)
	h.clientset.PrependReactor("list", "services", func(k8stesting.Action) (bool, runtime.Object, error) {
		<-release
		return false, nil, nil
	})

	go h.metrics.Discover(context.Background(), testCluster, false)
	time.Sleep(50 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	left := make(chan *Discovery, 1)
	go func() { left <- h.metrics.Discover(ctx, testCluster, false) }()
	cancel()

	select {
	case found := <-left:
		if !found.CouldNotAsk() {
			t.Fatalf("a canceled request answered %q, which a UI renders as a fact about the cluster", found.Kind)
		}
		if h.metrics.entry(testCluster).result != nil {
			t.Error("the cancellation was cached as this cluster's answer")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a canceled request was still queued behind the probe; this is the wedge the pool exists to remove")
	}
}

// --- What reaches the log -----------------------------------------------------

// A cluster whose SSO session has lapsed is re-probed every failureTTL and fails
// identically every time. One line per cluster per 30 s across fourteen clusters
// is the same as no logging; internal/recorder says it once per distinct reason
// and says the recovery.
func TestTheSameDiscoveryIsSaidOnceAndTheChangeIsSaid(t *testing.T) {
	h := newHarness(t)
	h.failServices(errors.New("getting credentials: exec: executable aws failed with exit code 255"))
	ctx := context.Background()

	for range 4 {
		h.metrics.Discover(ctx, testCluster, true)
	}
	if got := len(h.lines()); got != 1 {
		t.Fatalf("the same failure was logged %d times: %v", got, h.lines())
	}

	// The operator runs `aws sso login`. That must be audible.
	h.clientset.PrependReactor("list", "services", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, &corev1.ServiceList{}, nil
	})
	h.answer(rawAnswers{metricsAPIPath: {body: []byte("{}")}})
	h.metrics.Discover(ctx, testCluster, true)

	lines := h.lines()
	if len(lines) != 2 {
		t.Fatalf("the recovery was not said, or was said more than once: %v", lines)
	}
	if !strings.Contains(lines[1], KindMetricsServer) {
		t.Errorf("the recovery line does not say what the cluster now has: %q", lines[1])
	}
}

// probe() costs up to a minute by its own comment and its callers give it 10 s,
// 20 s and 25 s, so caching a cut-short probe would make the *ordinary* path a
// page timing out its own probe, storing "unreachable", and every other caller
// being told for the next failureTTL that the cluster has no metrics. The
// cluster is fine; the deadline was ours. An empty answer and a failed one are
// different answers, through the cache too.
func TestAProbeThatRanOutOfTimeDoesNotBecomeTheCachedAnswer(t *testing.T) {
	// A cluster that really does run Prometheus, so "unreachable" can only come
	// from the failure injected below and never from the fixture.
	h := newHarness(t, promService("monitoring", "prometheus",
		map[string]string{"app": "prometheus"}, "http-web", 9090))
	h.answer(rawAnswers{
		"/api/v1/namespaces/monitoring/services/prometheus:9090/proxy/-/healthy": {body: []byte("Prometheus is Healthy.")},
	})

	// Fail the first list only: one probe aborts the way a cut-short one does,
	// every probe after it sees the cluster as it is.
	var failedOnce sync.Once
	h.clientset.PrependReactor("list", "services", func(k8stesting.Action) (bool, runtime.Object, error) {
		var err error
		failedOnce.Do(func() {
			err = errors.New("dial tcp 10.0.0.1:6443: connect: connection refused")
		})
		if err != nil {
			return true, nil, err
		}
		return false, nil, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	first := h.metrics.Discover(ctx, testCluster, false)
	// Still answered honestly to its own caller — not cached is not the same as
	// not returned.
	if !first.CouldNotAsk() {
		t.Fatalf("the cut-short probe reported %q (%s), want an unreachable one", first.Kind, first.Detail)
	}

	// The next caller has a live deadline. It must probe rather than be handed the
	// previous caller's timeout, which is what failureTTL would have done for 30 s.
	second := h.metrics.Discover(context.Background(), testCluster, false)
	if second.CouldNotAsk() {
		t.Errorf("a canceled probe was cached and served to the next caller: %q (%s)",
			second.Kind, second.Detail)
	}
	if second.Kind != KindPrometheus {
		t.Errorf("kind is %q (%s), want the Prometheus this cluster runs", second.Kind, second.Detail)
	}
}

// The other half of the same decision: a probe that *reached* a verdict is a fact
// about the cluster, and must still be cached even though its caller had gone.
// Discarding those would mean a slow page never populates the cache for the fast
// one behind it.
func TestACancelledProbeThatStillFoundSomethingIsCached(t *testing.T) {
	h := newHarness(t, promService("monitoring", "prometheus",
		map[string]string{"app": "prometheus"}, "http-web", 9090))
	h.answer(rawAnswers{
		"/api/v1/namespaces/monitoring/services/prometheus:9090/proxy/-/healthy": {body: []byte("Prometheus is Healthy.")},
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if found := h.metrics.Discover(ctx, testCluster, false); found.Kind != KindPrometheus {
		t.Fatalf("kind is %q (%s)", found.Kind, found.Detail)
	}

	before := h.listed()
	if found := h.metrics.Discover(context.Background(), testCluster, false); found.Kind != KindPrometheus {
		t.Fatalf("second call: kind is %q (%s)", found.Kind, found.Detail)
	}
	if h.listed() != before {
		t.Errorf("the second caller re-probed (%d lists, was %d): a verdict was thrown away because its caller had gone",
			h.listed(), before)
	}
}

// A forced Discover - a save of the metrics source - takes only a probe that
// began after it was asked. One that began before and ended after may have
// read the source the save replaced, and is probed again (#156).
func TestAForcedDiscoverTakesOnlyAProbeBegunAfterIt(t *testing.T) {
	h := newHarness(t)
	h.answer(rawAnswers{metricsAPIPath: {err: notFound(metricsAPIPath)}})
	h.metrics.Discover(context.Background(), testCluster, false)
	first := h.listed()

	resolved, _ := h.metrics.clusters.Resolve(testCluster)
	e := h.metrics.entry(resolved)
	e.mu.Lock()
	stale := *e.result
	stale.startedAt = time.Now().Add(-time.Minute)
	stale.CheckedAt = time.Now().Add(time.Minute)
	e.result = &stale
	e.mu.Unlock()

	h.metrics.Discover(context.Background(), testCluster, true)
	if h.listed() == first {
		t.Fatal("a probe begun before the forced Discover was taken as its answer")
	}
	// And one begun after it is taken, without a second probe.
	again := h.listed()
	e.mu.Lock()
	fresh := *e.result
	fresh.startedAt = time.Now().Add(time.Hour)
	e.result = &fresh
	e.mu.Unlock()
	h.metrics.Discover(context.Background(), testCluster, true)
	if h.listed() != again {
		t.Error("a probe begun after the forced Discover was not taken")
	}
}

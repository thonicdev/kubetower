package metrics

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/thonicdev/kubetower/internal/cluster"
)

func TestTargetAddressIsThePasteableForm(t *testing.T) {
	target := &PromTarget{Namespace: "monitoring", Service: "prometheus", Port: 9090}
	if got := target.Address(); got != "monitoring/prometheus:9090" {
		t.Fatalf("address is %q", got)
	}
	want := "/api/v1/namespaces/monitoring/services/prometheus:9090/proxy/api/v1/query"
	if got := target.proxyPath(endpointQuery); got != want {
		t.Fatalf("proxy path is %q, want %q", got, want)
	}
}

// --- Range query parameters --------------------------------------------------

func TestRangeParamsStateTheWindowInUnixSeconds(t *testing.T) {
	start := time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)

	params, err := rangeParams("up", start, end, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if params.Get("start") != strconv.FormatInt(start.Unix(), 10) || params.Get("end") != strconv.FormatInt(end.Unix(), 10) {
		t.Fatalf("window is %s → %s", params.Get("start"), params.Get("end"))
	}
	if params.Get("step") != "30" {
		t.Fatalf("step is %q, want 30", params.Get("step"))
	}
	if params.Get("query") != "up" {
		t.Fatalf("query is %q", params.Get("query"))
	}
}

// A step formatted as whole seconds turns anything finer into `step=0`, which
// Prometheus rejects outright.
func TestRangeParamsNeverAskForAZeroStep(t *testing.T) {
	start := time.Now().Add(-time.Minute)
	params, err := rangeParams("up", start, time.Now(), 200*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	step, err := strconv.ParseFloat(params.Get("step"), 64)
	if err != nil {
		t.Fatalf("step %q is not a number: %v", params.Get("step"), err)
	}
	if step < 1 {
		t.Fatalf("step is %v, which Prometheus refuses", step)
	}
}

// Prometheus refuses a range query resolving to more than 11 000 points, with an
// error naming a resolution the operator never typed. A slider producing a fine
// step over a wide window must give a coarser chart, not a dialog.
func TestRangeParamsWidenTheStepRatherThanExceedPrometheusPointLimit(t *testing.T) {
	start := time.Now().Add(-7 * 24 * time.Hour)
	end := time.Now()

	params, err := rangeParams("up", start, end, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	step, err := strconv.ParseFloat(params.Get("step"), 64)
	if err != nil {
		t.Fatal(err)
	}
	points := end.Sub(start).Seconds() / step
	if points > maxRangePoints {
		t.Fatalf("%.0f points at a step of %vs, over the %d Prometheus allows", points, step, maxRangePoints)
	}
	if step == 1 {
		t.Fatal("the step was left as asked, so Prometheus rejects the query")
	}
}

func TestRangeParamsRefuseAWindowThatEndsBeforeItStarts(t *testing.T) {
	now := time.Now()
	if _, err := rangeParams("up", now, now.Add(-time.Hour), time.Minute); err == nil {
		t.Fatal("a reversed window was accepted")
	}
	if _, err := rangeParams("up", now, now, time.Minute); err == nil {
		t.Fatal("an empty window was accepted")
	}
	if _, err := rangeParams("  ", now, now.Add(time.Hour), time.Minute); err == nil {
		t.Fatal("an empty expression was accepted")
	}
}

// --- What comes back ---------------------------------------------------------

// Prometheus answers a bad query with 400 and a body naming the label it did not
// recognize. That body is the useful part, and the interface promises it
// verbatim.
func TestAPrometheusErrorBodyIsPassedThroughVerbatim(t *testing.T) {
	body := []byte(`{"status":"error","errorType":"bad_data","error":"unknown label"}`)
	got, err := promAnswer(body, errors.New("the server rejected our request"))
	if err != nil {
		t.Fatalf("a Prometheus answer was turned into a transport error: %v", err)
	}
	if string(got) != string(body) {
		t.Fatalf("body was rewritten: %s", got)
	}
}

func TestATransportFailureStaysAFailure(t *testing.T) {
	if _, err := promAnswer([]byte("<html>503 Address is not allowed</html>"), errors.New("service proxy refused")); err == nil {
		t.Fatal("an API server error was reported as a Prometheus answer")
	}
	if _, err := promAnswer(nil, errors.New("connection refused")); err == nil {
		t.Fatal("an empty failure was reported as an answer")
	}
}

// --- The two transports ------------------------------------------------------

func TestTheServiceProxyIsUsedWhenTheAPIServerAllowsIt(t *testing.T) {
	h := newHarness(t, promService("monitoring", "prometheus", map[string]string{"app": "prometheus"}, "web", 9090))
	target := &PromTarget{Namespace: "monitoring", Service: "prometheus", Port: 9090}
	answer := []byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`)
	h.answer(rawAnswers{
		target.proxyPath(endpointHealth): {body: []byte("Prometheus is Healthy.")},
		target.proxyPath(endpointQuery):  {body: answer},
	})

	found := h.metrics.Discover(context.Background(), testCluster, false)
	if found.Transport != TransportProxy {
		t.Fatalf("transport is %q (%s), want the proxy", found.Transport, found.Detail)
	}

	body, err := h.metrics.Query(context.Background(), testCluster, "up")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != string(answer) {
		t.Fatalf("body was rewritten: %s", body)
	}
	if opened, _ := h.tunnels.counts(); opened != 0 {
		t.Fatalf("a port-forward was opened although the proxy works (%d)", opened)
	}
}

// Every EKS context in this installation answers the service proxy with
// `503 Address is not allowed`, and the same call on the K3S cluster returns
// 200. That is why the transport is probed rather than inferred.
func TestAClusterWhoseServiceProxyRefusesIsQueriedThroughThePortForward(t *testing.T) {
	var asked []string
	prometheus := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		switch r.URL.Path {
		case endpointHealth:
			w.Write([]byte("Prometheus is Healthy."))
		case endpointRange:
			w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[],"step":"` + r.URL.Query().Get("step") + `"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer prometheus.Close()

	h := newHarness(t, promService("monitoring", "prometheus", map[string]string{"app": "prometheus"}, "web", 9090))
	h.tunnels.addr = strings.TrimPrefix(prometheus.URL, "http://")

	found := h.metrics.Discover(context.Background(), testCluster, false)
	if found.Kind != KindPrometheus || found.Transport != TransportForward {
		t.Fatalf("kind %q transport %q (%s)", found.Kind, found.Transport, found.Detail)
	}

	end := time.Now()
	body, err := h.metrics.QueryRange(context.Background(), testCluster, "up", end.Add(-time.Hour), end, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"step":"60"`) {
		t.Fatalf("the step did not reach Prometheus: %s", body)
	}

	opened, _ := h.tunnels.counts()
	if opened != 2 {
		// One to prove the transport works, one for the query itself — and the
		// second is kept open afterwards, which is what the pool is for.
		t.Fatalf("%d tunnels opened, want 2", opened)
	}
	h.metrics.Close()
	if opened, closed := h.tunnels.counts(); closed != opened {
		t.Fatalf("%d tunnels opened and %d closed; a leaked forward holds a listener and a stream", opened, closed)
	}
	if len(asked) == 0 {
		t.Fatal("Prometheus was never reached")
	}
}

// A session is the reason the port-forward is affordable at all: one takes
// ~1.8 s to stand up, and the panel that needs it asks five questions.
func TestASessionOpensOneTunnelForSeveralQueries(t *testing.T) {
	prometheus := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	defer prometheus.Close()

	h := newHarness(t, promService("monitoring", "prometheus", map[string]string{"app": "prometheus"}, "web", 9090))
	h.tunnels.addr = strings.TrimPrefix(prometheus.URL, "http://")
	ctx := context.Background()

	h.metrics.Discover(ctx, testCluster, false) // the transport probe's own tunnel
	before, _ := h.tunnels.counts()

	session, err := h.metrics.OpenSession(ctx, testCluster)
	if err != nil {
		t.Fatal(err)
	}
	for range 5 {
		if _, err := session.Query(ctx, "up"); err != nil {
			t.Fatal(err)
		}
	}
	session.Close()

	opened, closed := h.tunnels.counts()
	if opened-before != 1 {
		t.Fatalf("five queries opened %d tunnels, want 1", opened-before)
	}
	if closed != opened {
		t.Fatalf("%d opened, %d closed", opened, closed)
	}
}

func TestOpeningASessionSaysWhichOfTheThreeReasonsItCannot(t *testing.T) {
	ctx := context.Background()

	metricsServerOnly := newHarness(t)
	metricsServerOnly.answer(rawAnswers{metricsAPIPath: {body: []byte("{}")}})
	if _, err := metricsServerOnly.metrics.OpenSession(ctx, testCluster); err == nil ||
		!strings.Contains(err.Error(), "no history") {
		t.Fatalf("a metrics-server cluster gave %v, want a mention of the missing history", err)
	}

	nothing := newHarness(t)
	if _, err := nothing.metrics.OpenSession(ctx, testCluster); err == nil ||
		!strings.Contains(err.Error(), "no Prometheus") {
		t.Fatalf("a bare cluster gave %v", err)
	}

	unreachable := newHarness(t)
	unreachable.failServices(errors.New("getting credentials: exec: executable aws failed with exit code 255"))
	if _, err := unreachable.metrics.OpenSession(ctx, testCluster); err == nil ||
		!strings.Contains(err.Error(), "could not be asked") {
		t.Fatalf("an unreachable cluster gave %v, and must not be described as having no Prometheus", err)
	}
}

// The error a could-not-ask discovery refuses a
// session with carries the status the discovery was classified from, beneath
// the same sentence, so internal/web's stateOf reads a 401 as a lapsed
// credential and a 403 as a refusal rather than as a cluster that did not
// answer.
func TestACouldNotAskRefusalCarriesTheStatusItWasClassifiedFrom(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name string
		err  error
		is   func(error) bool
	}{
		{"a 401", apierrors.NewUnauthorized("Unauthorized"), apierrors.IsUnauthorized},
		{"a 403", apierrors.NewForbidden(schema.GroupResource{Resource: "services"}, "",
			errors.New(`User "dev" cannot list resource "services"`)), apierrors.IsForbidden},
	} {
		h := newHarness(t)
		h.failServices(c.err)
		_, err := h.metrics.OpenSession(ctx, testCluster)
		if err == nil || !strings.Contains(err.Error(), "could not be asked") {
			t.Fatalf("%s: the session opened or said %v", c.name, err)
		}
		if !c.is(err) {
			t.Errorf("%s: %v does not carry the status it was classified from", c.name, err)
		}
	}
}

// And Prometheus answering a status itself, through a port-forward: a 403 from
// an authenticating proxy in front of it is a 403, under the same sentence.
func TestAPrometheusStatusIsCarriedAsAStatus(t *testing.T) {
	prometheus := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "forbidden by the proxy", http.StatusForbidden)
	}))
	defer prometheus.Close()

	_, err := httpGet(context.Background(), prometheus.URL+"/api/v1/query", nil, time.Second)
	if err == nil || !strings.HasPrefix(err.Error(), "prometheus answered 403: forbidden by the proxy") {
		t.Fatalf("got %v; want the sentence it always said", err)
	}
	if !apierrors.IsForbidden(err) {
		t.Errorf("%v is not readable as a 403", err)
	}
}

// The proxy transport carries the query through the API server, so the
// parameters have to survive as query parameters rather than being pasted into
// a path.
func TestProxyQueriesCarryTheirParameters(t *testing.T) {
	var seen url.Values
	h := newHarness(t, promService("monitoring", "prometheus", map[string]string{"app": "prometheus"}, "web", 9090))
	target := &PromTarget{Namespace: "monitoring", Service: "prometheus", Port: 9090}
	answers := rawAnswers{
		target.proxyPath(endpointHealth): {body: []byte("ok")},
		target.proxyPath(endpointQuery):  {body: []byte(`{"status":"success"}`)},
	}
	inner := answers.fn(&h.mu, nil)
	h.metrics.raw = func(ctx context.Context, client *cluster.Client, path string, params url.Values) ([]byte, error) {
		if path == target.proxyPath(endpointQuery) {
			seen = params
		}
		return inner(ctx, client, path, params)
	}

	if _, err := h.metrics.Query(context.Background(), testCluster, `sum(rate(x[5m]))`); err != nil {
		t.Fatal(err)
	}
	if seen.Get("query") != `sum(rate(x[5m]))` {
		t.Fatalf("the expression reached the API server as %q", seen.Get("query"))
	}
}

// --- The pooled session -------------------------------------------------------

// The reason the pool exists. One overview render is up to four queries — two
// range queries for the history charts, two instant ones for the hottest pods —
// and without the pool each of them would stand a port-forward up and tear it
// down again, at roughly 1.8 s a time, every fifteen seconds, per open tab.
func TestRepeatedQueriesShareOneTunnel(t *testing.T) {
	prometheus := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	defer prometheus.Close()

	h := newHarness(t, promService("monitoring", "prometheus", map[string]string{"app": "prometheus"}, "web", 9090))
	h.tunnels.addr = strings.TrimPrefix(prometheus.URL, "http://")
	ctx := context.Background()

	h.metrics.Discover(ctx, testCluster, false) // the transport probe's own tunnel
	before, _ := h.tunnels.counts()

	end := time.Now()
	for range 4 {
		if _, err := h.metrics.Query(ctx, testCluster, "up"); err != nil {
			t.Fatal(err)
		}
		if _, err := h.metrics.QueryRange(ctx, testCluster, "up", end.Add(-time.Hour), end, time.Minute); err != nil {
			t.Fatal(err)
		}
	}

	opened, _ := h.tunnels.counts()
	if opened-before != 1 {
		t.Fatalf("eight queries opened %d tunnels, want 1", opened-before)
	}
}

// Held is not leaked. A live session owns a listener and a SPDY stream into the
// cluster, and a tab that was closed must not keep one.
func TestASessionNobodyIsUsingIsTornDown(t *testing.T) {
	prometheus := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	defer prometheus.Close()

	h := newHarness(t, promService("monitoring", "prometheus", map[string]string{"app": "prometheus"}, "web", 9090))
	h.tunnels.addr = strings.TrimPrefix(prometheus.URL, "http://")
	// The property, not the milliseconds: whatever the window is, a session
	// outlives its last user by it and no longer.
	h.metrics.idle = 10 * time.Millisecond
	ctx := context.Background()

	if _, err := h.metrics.Query(ctx, testCluster, "up"); err != nil {
		t.Fatal(err)
	}
	opened, closed := h.tunnels.counts()
	if closed == opened {
		t.Fatal("the session was closed with the query, so the next one pays for a tunnel again")
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if opened, closed = h.tunnels.counts(); closed == opened {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%d tunnels opened and %d closed after the idle window; one is wedged open", opened, closed)
}

// A cluster's Prometheus can move — a chart renamed, a Service replaced, or the
// transport re-probed into the other one. The pooled session then reaches
// something that is no longer there, and holding it would answer queries about a
// Prometheus nobody asked about.
func TestASessionIsRetiredWhenTheClusterAnswersSomewhereElse(t *testing.T) {
	prometheus := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	defer prometheus.Close()

	h := newHarness(t, promService("monitoring", "prometheus", map[string]string{"app": "prometheus"}, "web", 9090))
	h.tunnels.addr = strings.TrimPrefix(prometheus.URL, "http://")
	ctx := context.Background()

	if _, err := h.metrics.Query(ctx, testCluster, "up"); err != nil {
		t.Fatal(err)
	}
	opened, closed := h.tunnels.counts()
	if closed == opened {
		t.Fatal("the first session was not held, so this test proves nothing")
	}

	// The same cluster, a different Service.
	moved := promService("observability", "prometheus", map[string]string{"app": "prometheus"}, "web", 9090)
	if err := h.clientset.Tracker().Delete(
		corev1.SchemeGroupVersion.WithResource("services"), "monitoring", "prometheus"); err != nil {
		t.Fatal(err)
	}
	if err := h.clientset.Tracker().Add(moved); err != nil {
		t.Fatal(err)
	}
	h.metrics.Discover(ctx, testCluster, true)

	if _, err := h.metrics.Query(ctx, testCluster, "up"); err != nil {
		t.Fatal(err)
	}
	if _, nowClosed := h.tunnels.counts(); nowClosed <= closed {
		t.Fatalf("the stale session was kept: %d closed before the move, %d after", closed, nowClosed)
	}
	h.metrics.Close()
	if opened, closed := h.tunnels.counts(); closed != opened {
		t.Fatalf("%d tunnels opened and %d closed", opened, closed)
	}
}

// Two panels drawing at once must not each stand a tunnel up.
func TestConcurrentFirstQueriesOpenOneTunnel(t *testing.T) {
	prometheus := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	defer prometheus.Close()

	h := newHarness(t, promService("monitoring", "prometheus", map[string]string{"app": "prometheus"}, "web", 9090))
	h.tunnels.addr = strings.TrimPrefix(prometheus.URL, "http://")
	ctx := context.Background()

	h.metrics.Discover(ctx, testCluster, false) // the transport probe's own tunnel
	before, _ := h.tunnels.counts()

	var wg sync.WaitGroup
	wg.Add(6)
	for range 6 {
		go func() {
			defer wg.Done()
			if _, err := h.metrics.Query(ctx, testCluster, "up"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()

	if opened, _ := h.tunnels.counts(); opened-before != 1 {
		t.Fatalf("six concurrent queries opened %d tunnels, want 1", opened-before)
	}
}

// A failure is not pooled. The cure is usually on the operator's side and takes
// effect at once; holding it would tell them their fix did not work — the same
// asymmetry as failureTTL.
func TestASessionThatCouldNotBeOpenedIsNotHeld(t *testing.T) {
	h := newHarness(t, promService("monitoring", "prometheus", map[string]string{"app": "prometheus"}, "web", 9090))
	h.tunnels.addr = "127.0.0.1:1"
	ctx := context.Background()
	h.metrics.Discover(ctx, testCluster, false)
	// Reachable at probe time, gone by query time.
	h.tunnels.err = errors.New("no running pod behind service monitoring/prometheus")

	if _, err := h.metrics.Query(ctx, testCluster, "up"); err == nil {
		t.Fatal("a query answered although no tunnel could be opened")
	}
	h.metrics.pool.mu.Lock()
	held := len(h.metrics.pool.live)
	h.metrics.pool.mu.Unlock()
	if held != 0 {
		t.Fatalf("%d failed sessions were pooled", held)
	}
}

// TestAPooledSessionWhoseTunnelStoppedIsNotHandedOutAgain: the tunnel is pinned
// to the Prometheus pod chosen when it opened; when that pod restarts the
// tunnel stops, and a pool that kept handing the session out would have every
// chart on the cluster answered "connection refused" until nobody had looked
// for 45 s. The next query after the tunnel stops opens a new one.
func TestAPooledSessionWhoseTunnelStoppedIsNotHandedOutAgain(t *testing.T) {
	prometheus := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	defer prometheus.Close()

	h := newHarness(t, promService("monitoring", "prometheus", map[string]string{"app": "prometheus"}, "web", 9090))
	h.tunnels.addr = strings.TrimPrefix(prometheus.URL, "http://")
	ctx := context.Background()

	if _, err := h.metrics.Query(ctx, testCluster, "up"); err != nil {
		t.Fatal(err)
	}
	before, _ := h.tunnels.counts()
	h.tunnels.killLast()

	if _, err := h.metrics.Query(ctx, testCluster, "up"); err != nil {
		t.Fatalf("the query after the tunnel stopped failed: %v", err)
	}
	opened, closed := h.tunnels.counts()
	if opened != before+1 {
		t.Fatalf("the pool handed out a session whose tunnel had stopped: %d tunnels opened, want %d", opened, before+1)
	}
	if closed == 0 {
		t.Error("the stopped tunnel's session was not closed when it was retired")
	}
}

// A connection into a tunnel nobody uses any more is closed after a while,
// rather than kept until the process ends.
func TestTheTunnelClientClosesIdleConnections(t *testing.T) {
	tr, ok := httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("the tunnel client's transport is %T, want *http.Transport", httpClient.Transport)
	}
	if tr.IdleConnTimeout <= 0 {
		t.Error("the tunnel client keeps an idle connection for ever: set IdleConnTimeout")
	}
	if tr.Proxy != nil {
		t.Error("the tunnel client consults a proxy for the loopback")
	}
}

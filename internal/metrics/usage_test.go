package metrics

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	podUsageJSON = `{"items":[
		{"metadata":{"name":"web-0","namespace":"default"},"containers":[
			{"name":"app","usage":{"cpu":"12m","memory":"100Mi"}},
			{"name":"sidecar","usage":{"cpu":"3m","memory":"20Mi"}}]}]}`

	// Two namespaces each running a `web-0`, which is the ordinary state of a
	// cluster and the reason an all-namespaces map cannot be keyed by name.
	collidingPodsJSON = `{"items":[
		{"metadata":{"name":"web-0","namespace":"default"},"containers":[{"name":"app","usage":{"cpu":"12m","memory":"100Mi"}}]},
		{"metadata":{"name":"web-0","namespace":"staging"},"containers":[{"name":"app","usage":{"cpu":"250m","memory":"1Gi"}}]}]}`

	nodeUsageJSON = `{"items":[
		{"metadata":{"name":"ip-10-0-1-2"},"usage":{"cpu":"1250m","memory":"3Gi"}}]}`
)

func TestPodUsageSumsAPodsContainers(t *testing.T) {
	h := newHarness(t)
	h.answer(rawAnswers{metricsAPIPath + "/namespaces/default/pods": {body: []byte(podUsageJSON)}})

	usage, err := h.metrics.PodUsage(context.Background(), testCluster, "default")
	if err != nil {
		t.Fatal(err)
	}
	got, ok := usage["web-0"]
	if !ok {
		t.Fatalf("no row for web-0: %v", usage)
	}
	if got.CPUMillicores != 15 {
		t.Errorf("CPU is %dm, want 15m — metrics-server states no pod total, only per container", got.CPUMillicores)
	}
	if got.MemoryBytes != 120<<20 {
		t.Errorf("memory is %d bytes, want %d", got.MemoryBytes, 120<<20)
	}
	if got.From != KindMetricsServer {
		t.Errorf("row does not say where it came from: %q", got.From)
	}
}

func TestPodUsageAcrossNamespacesKeepsTwoPodsOfTheSameName(t *testing.T) {
	h := newHarness(t)
	h.answer(rawAnswers{metricsAPIPath + "/pods": {body: []byte(collidingPodsJSON)}})

	usage, err := h.metrics.PodUsage(context.Background(), testCluster, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(usage) != 2 {
		t.Fatalf("%d rows, want 2 — one pod silently overwrote the other: %v", len(usage), usage)
	}
	if usage[Key("staging", "web-0")].CPUMillicores != 250 {
		t.Errorf("staging/web-0 is %v", usage[Key("staging", "web-0")])
	}
}

func TestNodeUsageReadsTheStatedTotal(t *testing.T) {
	h := newHarness(t)
	h.answer(rawAnswers{metricsAPIPath + "/nodes": {body: []byte(nodeUsageJSON)}})

	usage, err := h.metrics.NodeUsage(context.Background(), testCluster)
	if err != nil {
		t.Fatal(err)
	}
	got := usage["ip-10-0-1-2"]
	if got.CPUMillicores != 1250 || got.MemoryBytes != 3<<30 {
		t.Fatalf("node usage is %+v", got)
	}
}

// A cluster with no metrics stack is not a broken cluster. The table shows an em
// dash; it does not show an error over a perfectly good list of pods.
func TestNoBackendAtAllIsAnAbsenceAndNotAFailure(t *testing.T) {
	h := newHarness(t)

	_, err := h.metrics.PodUsage(context.Background(), testCluster, "default")
	if err == nil {
		t.Fatal("usage was reported for a cluster with no backend")
	}
	if !IsNoMetrics(err) {
		t.Fatalf("%v is not recognisable as an absence, so a table cannot show an em dash", err)
	}
	if CouldNotAsk(err) {
		t.Fatalf("an absence was reported as a refusal: %v", err)
	}
}

// Could-not-ask is not none, at the point a table reads it: an em dash where
// the truth is "your session expired" is the same lie as "none" where the truth
// is "I could not ask".
func TestAnUnreachableClusterIsNeverReportedAsAnEmDash(t *testing.T) {
	h := newHarness(t)
	h.failServices(errors.New("getting credentials: exec: executable aws failed with exit code 255"))

	_, err := h.metrics.PodUsage(context.Background(), testCluster, "default")
	if err == nil {
		t.Fatal("usage was reported for a cluster that never answered")
	}
	if IsNoMetrics(err) {
		t.Fatalf("an expired session was reported as an absence: %v", err)
	}
	if !CouldNotAsk(err) {
		t.Fatalf("%v does not tell the caller this one is theirs to fix", err)
	}
}

func TestARefusedMetricsAPIIsNotAnAbsenceEither(t *testing.T) {
	h := newHarness(t)
	h.answer(rawAnswers{
		metricsAPIPath + "/nodes": {err: apierrors.NewForbidden(
			schema.GroupResource{Group: "metrics.k8s.io", Resource: "nodes"}, "", errors.New("denied"))},
	})

	_, err := h.metrics.NodeUsage(context.Background(), testCluster)
	if !CouldNotAsk(err) || IsNoMetrics(err) {
		t.Fatalf("a refusal came back as %v", err)
	}
}

// Anything reaching a PromQL string literal from a browser is validated first; a
// quote in a namespace would otherwise rewrite the expression.
func TestPodUsageRefusesANamespaceThatIsNotAKubernetesName(t *testing.T) {
	h := newHarness(t)
	if _, err := h.metrics.PodUsage(context.Background(), testCluster, `default",container!="x`); err == nil {
		t.Fatal("a namespace carrying a quote was accepted")
	}
}

// --- The Prometheus fallback -------------------------------------------------

func promUsageServer(t *testing.T, series map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == endpointHealth {
			w.Write([]byte("Prometheus is Healthy."))
			return
		}
		query := r.URL.Query().Get("query")
		for needle, body := range series {
			if strings.Contains(query, needle) {
				w.Write([]byte(body))
				return
			}
		}
		w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
}

func TestPodUsageFallsBackToPrometheusWhenThereIsNoMetricsServer(t *testing.T) {
	prometheus := promUsageServer(t, map[string]string{
		"container_cpu_usage_seconds_total": `{"status":"success","data":{"resultType":"vector","result":[
			{"metric":{"namespace":"default","pod":"web-0"},"value":[1755400000,"0.015"]}]}}`,
		"container_memory_working_set_bytes": `{"status":"success","data":{"resultType":"vector","result":[
			{"metric":{"namespace":"default","pod":"web-0"},"value":[1755400000,"125829120"]}]}}`,
	})
	defer prometheus.Close()

	h := newHarness(t, promService("monitoring", "prometheus", map[string]string{"app": "prometheus"}, "web", 9090))
	h.tunnels.addr = strings.TrimPrefix(prometheus.URL, "http://")

	usage, err := h.metrics.PodUsage(context.Background(), testCluster, "default")
	if err != nil {
		t.Fatal(err)
	}
	got := usage["web-0"]
	if got.CPUMillicores != 15 {
		t.Errorf("CPU is %dm, want 15m — Prometheus counts cores, a request is written in thousandths", got.CPUMillicores)
	}
	if got.MemoryBytes != 120<<20 {
		t.Errorf("memory is %d bytes", got.MemoryBytes)
	}
	if got.From != KindPrometheus {
		t.Errorf("row claims to come from %q", got.From)
	}
}

// The node label is not on every installation's cadvisor series. A series
// without one cannot be put in a row, and inventing a key would put a figure
// next to the wrong node.
func TestNodeUsageFromPrometheusSkipsSeriesItCannotAttribute(t *testing.T) {
	prometheus := promUsageServer(t, map[string]string{
		"container_cpu_usage_seconds_total": `{"status":"success","data":{"resultType":"vector","result":[
			{"metric":{"node":"ip-10-0-1-2"},"value":[1755400000,"1.25"]},
			{"metric":{},"value":[1755400000,"9"]}]}}`,
		"container_memory_working_set_bytes": `{"status":"success","data":{"resultType":"vector","result":[
			{"metric":{"node":"ip-10-0-1-2"},"value":[1755400000,"3221225472"]}]}}`,
	})
	defer prometheus.Close()

	h := newHarness(t, promService("monitoring", "prometheus", map[string]string{"app": "prometheus"}, "web", 9090))
	h.tunnels.addr = strings.TrimPrefix(prometheus.URL, "http://")

	usage, err := h.metrics.NodeUsage(context.Background(), testCluster)
	if err != nil {
		t.Fatal(err)
	}
	if len(usage) != 1 {
		t.Fatalf("%d rows, want 1: %v", len(usage), usage)
	}
	if usage["ip-10-0-1-2"].CPUMillicores != 1250 || usage["ip-10-0-1-2"].MemoryBytes != 3<<30 {
		t.Fatalf("node usage is %+v", usage["ip-10-0-1-2"])
	}
}

func TestPrometheusQueriesDropCadvisorsPerPodAggregate(t *testing.T) {
	cpu := podKinded.cpu("default")
	if !strings.Contains(cpu, `container!=""`) {
		t.Fatalf("the pod CPU query would count every pod twice: %s", cpu)
	}
	if !strings.Contains(cpu, `namespace="default"`) {
		t.Fatalf("the namespace does not reach the query: %s", cpu)
	}
	if strings.Contains(podKinded.cpu(""), "namespace=") {
		t.Fatalf("an all-namespaces query pinned a namespace: %s", podKinded.cpu(""))
	}
}

// TestSampleValueRefusesNaNAndInfinity.
//
// `fmt.Sscanf("%g")` parses "NaN" and "+Inf" without returning an error, so both
// would come back as usable samples — and the caller multiplies by a thousand
// and truncates. Go does not define that conversion to saturate: on amd64
// `int64(math.NaN()*1000)` is -9223372036854775808, and a counter reset would put
// exactly that in the CPU column of the pod list.
//
// The assertion is written on the **conversion**, not only on the boolean,
// because a test that checked `ok` alone would keep passing if somebody later
// returned NaN with ok=true and left the caller to guard it. What must not be
// wrong is the column.
func TestSampleValueRefusesNaNAndInfinity(t *testing.T) {
	for _, text := range []string{"NaN", "nan", "+Inf", "-Inf", "inf"} {
		value, ok := sampleValue([]any{1.0, text})
		if ok {
			t.Fatalf("sampleValue(%q) returned a usable sample %v; int64(v*1000) is %d, which is what the CPU column would then show",
				text, value, int64(value*1000))
		}
	}

	// And the guard has not taken an ordinary reading with it. 0.125 cores is
	// 125 millicores, which is the arithmetic the caller performs.
	value, ok := sampleValue([]any{1.0, "0.125"})
	if !ok || int64(value*1000) != 125 {
		t.Fatalf("a plain sample no longer reads: ok=%v value=%v", ok, value)
	}
}

// TestNaNLeavesTheObjectWithoutAnEntryRatherThanAZero is the other half, and it
// is the half that says what the operator sees.
//
// Refusing the sample is only correct because `collectSeries` then skips the
// series: the key is never created, so the row carries no Prometheus reading at
// all and the column renders an em dash. A zero instead would swap one wrong
// number for another — "using nothing" is a claim, and an absent answer is not
// an empty one.
func TestNaNLeavesTheObjectWithoutAnEntryRatherThanAZero(t *testing.T) {
	body := []byte(`{"status":"success","data":{"resultType":"vector","result":[
		{"metric":{"pod":"api-0","namespace":"default"},"value":[1,"NaN"]},
		{"metric":{"pod":"api-1","namespace":"default"},"value":[1,"0.25"]}]}}`)

	usage := map[string]Usage{}
	if err := collectSeries(body, "default", podKinded, usage, func(entry *Usage, value float64) {
		entry.CPUMillicores = int64(value * 1000)
	}); err != nil {
		t.Fatalf("collectSeries: %v", err)
	}

	if _, present := usage["api-0"]; present {
		t.Fatalf("the pod whose sample was NaN has an entry: %+v — the column will print a number where the honest answer is that there is none", usage["api-0"])
	}
	if usage["api-1"].CPUMillicores != 250 {
		t.Fatalf("the readable pod beside it lost its figure: %+v", usage["api-1"])
	}
}

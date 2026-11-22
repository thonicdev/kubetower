package overview

// The cluster-level half of the double-scrape rule.
//
// A cluster can carry two kubelet scrape targets over the same endpoint —
// every cAdvisor series stored twice, differing only in `service`, so a bare
// `sum()` returns exactly double — which is why aggregations are wrapped in
// `avg without (job, instance, endpoint, service, metrics_path)`.
// `TestEveryObjectQueryDeduplicatesBeforeSumming` in object_test.go pins the
// five pod charts and the five node charts.
//
// This file pins the four queries in metrics.go: the cluster dashboard's two
// sparklines, the zoomed version of the same two, and the "hottest pods"
// board's Prometheus fallback. Same method, same reason: **checked per query rather
// than by counting occurrences**, because the failure being guarded against is
// one query copied without the wrapper, and a count still passes with the
// wrapper on the wrong three.
//
// Measured on such a cluster, without the wrapper:
//
//	sum(rate(container_cpu_usage_seconds_total{container!="",container!="POD"}[5m]))
//	  raw 7.206 cores   deduplicated 3.817 cores
//	sum(container_memory_working_set_bytes{container!="",container!="POD"})
//	  raw 47.64 GB      deduplicated 26.67 GB
//
// and a pod whose container declares a 7-core limit peaked, on a 24-hour
// chart, at 10.71 cores raw and 5.36 deduplicated. A limit is not the
// denominator of any of these charts, but one of those figures is impossible.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// theWrapper is what every aggregation in this package has to carry. Spelled out
// from `scrapeLabels` rather than typed again, so that changing the label list
// changes what is required and not merely what is claimed.
func theWrapper() string { return "avg without (" + scrapeLabels + ") (" }

// everyPrometheusQueryThisPackageSends drives each Prometheus-backed entry point
// in metrics.go and zoom.go and returns the PromQL that came out.
//
// Driving the callers rather than reading the four variables is the point: a
// fifth query added to any of these paths is collected here without this test
// being edited, which is the only way a guard survives the change it exists for.
func everyPrometheusQueryThisPackageSends(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}

	// The dashboard's two sparklines. Range queries.
	dash := &recordingMetrics{answer: matrix(t, [2]any{1.0, "1"})}
	clusterHistory(context.Background(), dash, "any")
	for i, q := range dash.queries {
		out[fmt.Sprintf("clusterHistory[%d]", i)] = q
	}

	// The same two over a chosen window. It references the dashboard's strings
	// rather than copying them, and this proves that it still does.
	zoom := &recordingMetrics{answer: matrix(t, [2]any{1.0, "1"})}
	ZoomClusterHistory(context.Background(), zoom, "any", WindowFor("6h"))
	for i, q := range zoom.queries {
		out[fmt.Sprintf("ZoomClusterHistory[%d]", i)] = q
	}

	// Both again on a Prometheus with no per-container series, where they fall
	// back to the pods' own cgroups. The first two queries of each are
	// the container sums already collected above; the two after them are the
	// fallback's, asked only on this path.
	for name, ask := range map[string]func(Metrics){
		"clusterHistory":     func(m Metrics) { clusterHistory(context.Background(), m, "any") },
		"ZoomClusterHistory": func(m Metrics) { ZoomClusterHistory(context.Background(), m, "any", WindowFor("6h")) },
	} {
		fallback := &recordingMetrics{answer: []byte(`{"status":"success","data":{"result":[]}}`), probe: exported(t)}
		ask(fallback)
		for i, q := range fallback.queries {
			if i >= 2 {
				out[fmt.Sprintf("%s, pod cgroups[%d]", name, i)] = q
			}
		}
	}

	// The "hottest pods" board's Prometheus fallback. Instant queries, so they
	// land in `probes` rather than `queries`.
	top := &recordingMetrics{answer: topVector(t)}
	promPodUsage(context.Background(), top, "any", topPods)
	for i, q := range top.probes {
		out[fmt.Sprintf("promPodUsage[%d]", i)] = q
	}

	return out
}

func TestEveryClusterAndTopPodQueryDeduplicatesBeforeSumming(t *testing.T) {
	queries := everyPrometheusQueryThisPackageSends(t)

	// Ten: two for the dashboard, two for its zoom, two for each of their
	// pod-cgroup fallbacks, two for the board. An unexpected count is not a
	// pass — a path that answered nothing would otherwise satisfy every
	// assertion below by having nothing to assert on, which is an empty
	// answer wearing a success.
	if len(queries) != 10 {
		t.Fatalf("collected %d queries, want 10 (cluster ×2, zoom ×2, their pod-cgroup fallbacks ×2 each, "+
			"top pods ×2): %v", len(queries), queries)
	}

	for name, q := range queries {
		if !strings.Contains(q, theWrapper()) {
			t.Errorf("%s sums series that were never deduplicated, so a cluster with two kubelet "+
				"scrape targets reads double:\n  %s", name, q)
			continue
		}
		// Inside the aggregation or it does nothing: `avg without (...)` applied
		// to an already-summed scalar has one series to average and returns it
		// unchanged, so the double survives a wrapper in the wrong place and the
		// query still *contains* the string above.
		agg := strings.Index(q, "sum")
		if agg < 0 || agg > strings.Index(q, theWrapper()) {
			t.Errorf("%s deduplicates outside the aggregation, which averages one series "+
				"and changes nothing:\n  %s", name, q)
		}
	}
}

// The board's two carry topk's `k`, and the wrapper is built by string
// concatenation around a `%d`. That is exactly the shape that silently acquires
// a second verb or loses the first, and `fmt.Sprintf` does not fail loudly when
// it does — it writes `%!d(MISSING)` into the PromQL and Prometheus rejects the
// whole query at runtime.
func TestTheTopPodQueriesStillTakeExactlyOneK(t *testing.T) {
	for name, format := range map[string]string{
		"queryTopPodCPU":    queryTopPodCPU,
		"queryTopPodMemory": queryTopPodMemory,
	} {
		if n := strings.Count(format, "%"); n != 1 {
			t.Errorf("%s carries %d format verbs, want exactly one:\n  %s", name, n, format)
		}
		filled := fmt.Sprintf(format, 5)
		if strings.Contains(filled, "%!") || strings.Contains(filled, "%") {
			t.Errorf("%s did not format cleanly:\n  %s", name, filled)
		}
		if !strings.HasPrefix(filled, "topk(5, sum by (namespace, pod) ("+theWrapper()) {
			t.Errorf("%s no longer ranks a deduplicated sum:\n  %s", name, filled)
		}
	}
}

// topVector is an instant answer shaped like the board's: a ranked list with the
// two labels `parseVector` keys on.
func topVector(t *testing.T) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"status": "success",
		"data": map[string]any{"result": []any{
			map[string]any{
				"metric": map[string]string{"namespace": "jellyfin", "pod": "jellyfin-67d6d9d48d-hl8vz"},
				"value":  []any{1.0, "5.356"},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

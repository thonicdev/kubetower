package metrics_test

import (
	"encoding/json"
	"slices"
	"strconv"
	"testing"
)

// A series of an answer: its labels and its points, by second.
type curve struct {
	labels map[string]string
	points map[int64]float64
}

// matrix is a query_range answer holding these series.
func matrix(t *testing.T, all ...curve) []byte {
	t.Helper()
	type one struct {
		Metric map[string]string `json:"metric"`
		Values [][2]any          `json:"values"`
	}
	result := []one{}
	for _, s := range all {
		o := one{Metric: s.labels, Values: [][2]any{}}
		at := make([]int64, 0, len(s.points))
		for second := range s.points {
			at = append(at, second)
		}
		slices.Sort(at)
		for _, second := range at {
			o.Values = append(o.Values, [2]any{second, strconv.FormatFloat(s.points[second], 'g', -1, 64)})
		}
		result = append(result, o)
	}
	body, err := json.Marshal(map[string]any{"status": "success", "data": map[string]any{"resultType": "matrix", "result": result}})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func at(values ...float64) map[int64]float64 {
	out := map[int64]float64{}
	for i, v := range values {
		out[int64(1000+15*i)] = v
	}
	return out
}

func labels(kv ...string) map[string]string {
	out := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i]] = kv[i+1]
	}
	return out
}

const byRcode = `sum by (rcode) (rate(coredns_dns_responses_total{namespace="kube-system"}[3m]))`

// #176: the rows of 2026-11-26's report that differ by extrapolation alone
// still pass, up to the 98% of a point a quantile reached.
func TestARateDifferingByExtrapolationAloneIsWithinTheBound(t *testing.T) {
	q := query{PromQL: byRcode, Range: true}
	// sum by (rcode): 56% of a point at NXDOMAIN, as on 2026-11-26.
	v := compareAnswers(q,
		matrix(t, curve{labels("rcode", "NOERROR"), at(0.14, 0.19)}, curve{labels("rcode", "NXDOMAIN"), at(0.0533, 0.04)}),
		matrix(t, curve{labels("rcode", "NOERROR"), at(0.15, 0.29)}, curve{labels("rcode", "NXDOMAIN"), at(0.1222, 0.05)}))
	if v.equal() || !v.withinRateBound() {
		t.Errorf("equal %v, within the bound %v (×%.2f, %d inverted), want a RATE row", v.equal(), v.withinRateBound(), v.magnitude, v.inverted)
	}

	// A bucket that moved once before the window: all of it on one side,
	// none on the other, but too small to weigh.
	v = compareAnswers(q,
		matrix(t, curve{labels("rcode", "NOERROR"), at(0.2, 0.2)}, curve{labels("rcode", "SERVFAIL"), at(0, 0)}),
		matrix(t, curve{labels("rcode", "NOERROR"), at(0.25, 0.22)}, curve{labels("rcode", "SERVFAIL"), at(0.005, 0)}))
	if !v.withinRateBound() {
		t.Errorf("a series under a tenth of the answer counted: ×%.2f", v.magnitude)
	}

	// histogram_quantile(0.99): ×15.6 on the quantile, its buckets close.
	quantile := query{PromQL: `histogram_quantile(0.99, sum by (le, instance) (rate(coredns_dns_request_duration_seconds_bucket{instance=~"(10\\.244\\.0\\.32):[0-9]+"}[3m])))`}
	v = compareAnswers(quantile,
		matrix(t, curve{labels("instance", "10.244.0.32:9153"), at(0.00182)}),
		matrix(t, curve{labels("instance", "10.244.0.32:9153"), at(0.02832)}))
	argument, _ := argumentOf(quantile.PromQL)
	inner := compareAnswers(query{PromQL: argument},
		matrix(t, curve{labels("le", "0.001", "instance", "a"), at(0.30)}, curve{labels("le", "+Inf", "instance", "a"), at(0.31)}),
		matrix(t, curve{labels("le", "0.001", "instance", "a"), at(0.27)}, curve{labels("le", "+Inf", "instance", "a"), at(0.30)}))
	v.inner = &inner
	if !v.withinRateBound() {
		t.Errorf("a quantile whose buckets are close failed: %s", boundOf(v))
	}
	inner = compareAnswers(inner.q,
		matrix(t, curve{labels("le", "0.001", "instance", "a"), at(0.30)}, curve{labels("le", "+Inf", "instance", "a"), at(0.31)}),
		matrix(t, curve{labels("le", "0.001", "instance", "a"), at(3.3)}, curve{labels("le", "+Inf", "instance", "a"), at(3.4)}))
	v.inner = &inner
	if v.withinRateBound() {
		t.Error("a quantile whose buckets are ×11 apart passed")
	}

	// topk(2): another series picked where two are close, as on 2026-11-27.
	top := query{PromQL: `topk(2, sum by (pod) (rate(cpu[5m])))`}
	v = compareAnswers(top,
		matrix(t, curve{labels("pod", "a"), at(0.5)}, curve{labels("pod", "b"), at(0.30)}),
		matrix(t, curve{labels("pod", "a"), at(0.6)}, curve{labels("pod", "c"), at(0.33)}))
	argument, _ = argumentOf(top.PromQL)
	inner = compareAnswers(query{PromQL: argument},
		matrix(t, curve{labels("pod", "a"), at(0.5)}, curve{labels("pod", "b"), at(0.30)}, curve{labels("pod", "c"), at(0.29)}),
		matrix(t, curve{labels("pod", "a"), at(0.6)}, curve{labels("pod", "b"), at(0.31)}, curve{labels("pod", "c"), at(0.33)}))
	v.inner, v.picks = &inner, true
	if !v.withinRateBound() {
		t.Errorf("a topk picking another of two close series failed: %s", boundOf(v))
	}
	inner = compareAnswers(inner.q,
		matrix(t, curve{labels("pod", "a"), at(0.5)}, curve{labels("pod", "b"), at(0.30)}, curve{labels("pod", "c"), at(0.29)}),
		matrix(t, curve{labels(), at(1.2)}))
	v.inner = &inner
	if v.withinRateBound() {
		t.Error("a topk whose argument lost its by (pod) passed")
	}
}

// #176 done-when 1: a `by` label dropped inside a rate fails, and so does an
// aggregation that sums what it should not, or two series swapped.
func TestAWrongRateFailsTheBound(t *testing.T) {
	q := query{PromQL: `sum by (instance) (rate(coredns_dns_requests_total[3m]))`, Range: true}
	prom := matrix(t, curve{labels("instance", "a"), at(0.18, 0.2)}, curve{labels("instance", "b"), at(0.1, 0.12)})
	for name, vm := range map[string][]byte{
		"by (instance) dropped": matrix(t, curve{labels(), at(0.28, 0.32)}),
		"by (instance, pod)":    matrix(t, curve{labels("instance", "a", "pod", "x"), at(0.18, 0.2)}, curve{labels("instance", "b", "pod", "y"), at(0.1, 0.12)}),
		"a series too many":     matrix(t, curve{labels("instance", "a"), at(0.18, 0.2)}, curve{labels("instance", "b"), at(0.1, 0.12)}, curve{labels("instance", "c"), at(0.1, 0.1)}),
		"×12, summed 12 times":  matrix(t, curve{labels("instance", "a"), at(2.16, 2.4)}, curve{labels("instance", "b"), at(1.2, 1.44)}),
		"one series at nothing": matrix(t, curve{labels("instance", "a"), at(0.18, 0.2)}, curve{labels("instance", "b"), at(0, 0)}),
	} {
		if v := compareAnswers(q, prom, vm); v.withinRateBound() {
			t.Errorf("%s passed the bound (×%.2f, %d inverted)", name, v.magnitude, v.inverted)
		}
	}

	// Two series swapped, each within ×10 of itself.
	v := compareAnswers(q,
		matrix(t, curve{labels("instance", "a"), at(1)}, curve{labels("instance", "b"), at(0.05)}),
		matrix(t, curve{labels("instance", "a"), at(0.11)}, curve{labels("instance", "b"), at(0.5)}))
	if v.magnitude > rateBound || v.inverted == 0 || v.withinRateBound() {
		t.Errorf("swapped series: ×%.2f, %d inverted, within %v", v.magnitude, v.inverted, v.withinRateBound())
	}

	// Not a rate: the tolerance holds, not the bound.
	gauge := query{PromQL: `sum by (instance) (container_memory_working_set_bytes)`, Range: true}
	if v := compareAnswers(gauge, prom, matrix(t, curve{labels("instance", "a"), at(0.2, 0.2)}, curve{labels("instance", "b"), at(0.1, 0.12)})); v.equal() || v.withinRateBound() {
		t.Errorf("a gauge 10%% off: equal %v, within the bound %v", v.equal(), v.withinRateBound())
	}
}

func TestEachByLabelIsDroppedInTurn(t *testing.T) {
	q := `histogram_quantile(0.99, sum by (le, instance) (rate(x{instance=~"(a|b) by (c)"}[3m]))) or sum by(job) (rate(y[1m]))`
	got := droppedBy(q)
	want := []string{
		`histogram_quantile(0.99, sum by (instance) (rate(x{instance=~"(a|b) by (c)"}[3m]))) or sum by(job) (rate(y[1m]))`,
		`histogram_quantile(0.99, sum by (le) (rate(x{instance=~"(a|b) by (c)"}[3m]))) or sum by(job) (rate(y[1m]))`,
		`histogram_quantile(0.99, sum by (le, instance) (rate(x{instance=~"(a|b) by (c)"}[3m]))) or sum by() (rate(y[1m]))`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("droppedBy:\n got %q\nwant %q", got, want)
	}
}

func TestAnArgumentIsReadPastParenthesesInStrings(t *testing.T) {
	for q, want := range map[string]string{
		`histogram_quantile(0.5, sum by (le) (rate(x{i=~"(10\\.0):[0-9]+"}[3m])))`:           `sum by (le) (rate(x{i=~"(10\\.0):[0-9]+"}[3m]))`,
		` histogram_quantile(0.9,sum by (le) (rate(x{i=")"}[3m])) ) `:                        `sum by (le) (rate(x{i=")"}[3m]))`,
		`histogram_quantile(0.5, sum by (le) (rate(x[3m]))) or vector(0)`:                    ``,
		`sum by (instance) (rate(x[3m]))`:                                                    ``,
		`max(histogram_quantile(0.5, sum by (le) (rate(x[3m]))))`:                            ``,
		`histogram_quantile(0.5, sum by (le) (rate(x{i=~"\"(,"}[3m])))`:                      `sum by (le) (rate(x{i=~"\"(,"}[3m]))`,
		"histogram_quantile(0.5, sum by (le) (rate(x{i=~`(`}[3m])))":                         "sum by (le) (rate(x{i=~`(`}[3m]))",
		`histogram_quantile(0.5, sum by (le) (rate(x{i='a(,b'}[3m])))`:                       `sum by (le) (rate(x{i='a(,b'}[3m]))`,
		`histogram_quantile(scalar(foo), sum by (le) (rate(x[3m])))`:                         `sum by (le) (rate(x[3m]))`,
		`histogram_quantile(0.5, sum by (le, instance) (increase(x[3m])))`:                   `sum by (le, instance) (increase(x[3m]))`,
		`histogram_quantile(0.5`:                                                             ``,
		`histogram_quantile(0.5 sum by (le) (rate(x[3m])))`:                                  ``,
		`histogram_quantile(0.5, sum by (le) (rate(x[3m])))  or  histogram_quantile(0.9, y)`: ``,
		`topk(5, sum by (namespace, pod) (rate(x[5m])))`:                                     `sum by (namespace, pod) (rate(x[5m]))`,
		`bottomk(3, rate(x{a=","}[5m]))`:                                                     `rate(x{a=","}[5m])`,
	} {
		if got, _ := argumentOf(q); got != want {
			t.Errorf("argumentOf(%s) = %q, want %q", q, got, want)
		}
	}
}

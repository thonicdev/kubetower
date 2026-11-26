package metrics_test

// The values VictoriaMetrics answers the console's queries with, against
// Prometheus's, on the same samples.
//
// Off by default, like vmreplay_integration_test.go, whose setup it shares:
//
//	KT_VM_CLUSTER=kind-kubetower go test ./internal/metrics/ -run VictoriaMetricsIntegrationValues -v
//
// Two scrapers never sample at the same instants, so comparing what each
// scraped would measure the scrapes. Here the raw samples every recorded
// query reads are copied out of the cluster's Prometheus into a tenant of
// vm-operator's VMCluster "main" of its own (KT_VM_VALUES_CLUSTER overrides
// `vm/vminsert-main:8480`, `vm/vmselect-main:8481` and `vm/vmstorage-main:8482`
// as `namespace/prefix`), and each query is then asked of both at the same
// instants. A difference left is the query language's.
//
// KT_VM_REPORT names a file the report is also written to.
//
// Two differences are expected, and neither is a query's:
//   - a counter's rate or increase: Prometheus extrapolates the increase to the
//     window's edges, VictoriaMetrics does not (RATE in the report, not failed
//     on; see verdict.extrapolated);
//   - a container that restarted in the window: cAdvisor samples carry their
//     own timestamps, for which Prometheus writes no staleness marker, so it
//     counts the old container for its 5-minute lookback beside the new one,
//     where VictoriaMetrics stops after about two scrape intervals. The test
//     fails on it; rerun when no container restarts.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// tolerance: a value within 5% of the other, or this close to it absolutely
// (values near 0, where a relative difference means nothing).
const (
	relTolerance = 0.05
	absTolerance = 1e-9
)

// The instants compared: [T0-compareFrom, T0-compareUntil], compareFrom
// being KT_VM_COMPARE (default 30m) plus the longest range selector the
// queries hold. The samples copied start a further 10 minutes before, so every
// range selector sees whole data on both sides and neither edge is compared.
// The reference must hold continuous samples over that span: around a gap the
// two engines look back differently, and that is the gap's, not a query's.
const compareUntil = 5 * time.Minute

var rangeSelector = regexp.MustCompile(`\[([0-9]+)([smhd])\]`)

// longestCopied is the longest range selector a compared query may hold,
// KT_VM_LONGEST overriding it. A longer one (the window's listing of every
// container over 7 days) reads further back than any copy, and is left to the
// replay of series counts.
var longestCopied = 2 * time.Hour

// rangeOf is the longest range selector in one query.
func rangeOf(q query) time.Duration {
	var longest time.Duration
	for _, m := range rangeSelector.FindAllStringSubmatch(q.PromQL, -1) {
		n, _ := strconv.Atoi(m[1])
		unit := map[string]time.Duration{"s": time.Second, "m": time.Minute, "h": time.Hour, "d": 24 * time.Hour}[m[2]]
		longest = max(longest, time.Duration(n)*unit)
	}
	return longest
}

func TestVictoriaMetricsIntegrationValues(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	s := open(ctx, t, os.Getenv("KT_VM_CLUSTER"), os.Getenv("KT_VM_TARGETS"))
	// Not the console's clients: they refuse any write that did not go through
	// its reviewed door, and the copy is a write.
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: s.Kubeconfig},
		&clientcmd.ConfigOverrides{CurrentContext: s.Cluster},
	).ClientConfig()
	if err != nil {
		t.Fatal(err)
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	namespace, prefix := "vm", "main"
	if v := os.Getenv("KT_VM_VALUES_CLUSTER"); v != "" {
		namespace, prefix, _ = strings.Cut(v, "/")
	}
	reference := found(ctx, t, s)
	prom := proxied{client: client, base: reference}
	queries := recordAll(ctx, t, s)
	window := 30 * time.Minute
	if v := os.Getenv("KT_VM_COMPARE"); v != "" {
		if window, err = time.ParseDuration(v); err != nil {
			t.Fatal(err)
		}
	}
	if v := os.Getenv("KT_VM_LONGEST"); v != "" {
		if longestCopied, err = time.ParseDuration(v); err != nil {
			t.Fatal(err)
		}
	}
	var longest time.Duration
	var compared, skipped []query
	for _, q := range queries {
		if r := rangeOf(q); r > longestCopied {
			skipped = append(skipped, q)
		} else {
			compared = append(compared, q)
			longest = max(longest, r)
		}
	}
	for _, q := range skipped {
		t.Logf("not compared, its range selector reads further back than the copy: %s", q.PromQL)
	}
	queries = compared
	compareFrom := compareUntil + window
	copied := compareFrom + longest + 10*time.Minute
	t.Logf("comparing %s, longest range selector %s: %s of continuous samples needed", window, longest, copied)

	// T0 a whole minute ago, so every scrape before it has landed.
	t0 := time.Now().Truncate(time.Minute).Add(-time.Minute)
	names := metricNames(ctx, t, prom, queries)
	tenant := 1000 + time.Now().Unix()%1_000_000
	vm := proxied{client: client, base: fmt.Sprintf("/api/v1/namespaces/%s/services/vmselect-%s:8481/proxy/select/%d/prometheus", namespace, prefix, tenant)}
	series := copySamples(ctx, t, prom, client, namespace, prefix, tenant, names, t0, copied)
	waitForSeries(ctx, t, vm, series)
	t.Logf("copied %d series of %d metrics into tenant %d, %s up to %s", series, len(names), tenant, copied, t0.Format(time.RFC3339))
	if series == 0 {
		t.Fatal("nothing was copied: the reference holds no sample in that span")
	}

	var report []verdict
	for _, q := range queries {
		report = append(report, compareValues(ctx, prom, vm, q, t0, compareFrom))
	}
	writeReport(t, report)
}

// found is the reference Prometheus's proxy path, from its discovery.
func found(ctx context.Context, t *testing.T, s setup) string {
	t.Helper()
	d := s.Reference.Discover(ctx, s.Cluster, false)
	if d.Prometheus == nil {
		t.Fatalf("no Prometheus: %s", d.Detail)
	}
	return fmt.Sprintf("/api/v1/namespaces/%s/services/%s:%d/proxy%s", d.Prometheus.Namespace, d.Prometheus.Service, d.Prometheus.Port, d.Prometheus.Path)
}

// proxied asks one Prometheus API through the API server's service proxy.
type proxied struct {
	client kubernetes.Interface
	base   string
}

func (p proxied) get(ctx context.Context, endpoint string, params url.Values) ([]byte, error) {
	req := p.client.CoreV1().RESTClient().Get().AbsPath(p.base + endpoint)
	for key, values := range params {
		for _, value := range values {
			req = req.Param(key, value)
		}
	}
	return req.DoRaw(ctx)
}

// answer is a Prometheus API answer: an instant vector, a matrix, or a scalar.
type answer struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string          `json:"resultType"`
		Result     json.RawMessage `json:"result"`
	} `json:"data"`
}

// sample is one series' points, keyed by milliseconds.
type sampled struct {
	labels string
	points map[int64]float64
}

func parse(body []byte) ([]sampled, error) {
	var a answer
	if err := json.Unmarshal(body, &a); err != nil {
		return nil, err
	}
	if a.Status != "success" {
		return nil, fmt.Errorf("status %q: %s", a.Status, firstBytes(body))
	}
	if a.Data.ResultType == "scalar" {
		var point []json.RawMessage
		if err := json.Unmarshal(a.Data.Result, &point); err != nil {
			return nil, err
		}
		one := sampled{labels: "{}", points: map[int64]float64{}}
		addPoint(one.points, point)
		return []sampled{one}, nil
	}
	var result []struct {
		Metric map[string]string   `json:"metric"`
		Value  []json.RawMessage   `json:"value"`
		Values [][]json.RawMessage `json:"values"`
	}
	if err := json.Unmarshal(a.Data.Result, &result); err != nil {
		return nil, err
	}
	out := make([]sampled, 0, len(result))
	for _, r := range result {
		one := sampled{labels: labelKey(r.Metric), points: map[int64]float64{}}
		if r.Value != nil {
			addPoint(one.points, r.Value)
		}
		for _, v := range r.Values {
			addPoint(one.points, v)
		}
		out = append(out, one)
	}
	return out, nil
}

func addPoint(points map[int64]float64, point []json.RawMessage) {
	if len(point) != 2 {
		return
	}
	var at float64
	var text string
	if json.Unmarshal(point[0], &at) != nil || json.Unmarshal(point[1], &text) != nil {
		return
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return
	}
	points[int64(math.Round(at*1000))] = value
}

func labelKey(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("{")
	for i, k := range keys {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, "%s=%q", k, labels[k])
	}
	b.WriteString("}")
	return b.String()
}

func firstBytes(body []byte) string {
	if len(body) > 200 {
		return string(body[:200])
	}
	return string(body)
}

var identifier = regexp.MustCompile(`[a-zA-Z_:][a-zA-Z0-9_:]*`)

// metricNames are the metric names the queries read: every identifier in them
// that the reference Prometheus holds a metric of.
func metricNames(ctx context.Context, t *testing.T, prom proxied, queries []query) []string {
	t.Helper()
	body, err := prom.get(ctx, "/api/v1/label/__name__/values", nil)
	if err != nil {
		t.Fatal(err)
	}
	var known struct {
		Data []string `json:"data"`
	}
	if err := json.Unmarshal(body, &known); err != nil {
		t.Fatal(err)
	}
	held := map[string]bool{}
	for _, name := range known.Data {
		held[name] = true
	}
	wanted := map[string]bool{}
	for _, q := range queries {
		for _, word := range identifier.FindAllString(q.PromQL, -1) {
			if held[word] {
				wanted[word] = true
			}
		}
	}
	names := make([]string, 0, len(wanted))
	for name := range wanted {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// copySamples reads each metric's raw samples over the copied window ending
// at t0 from Prometheus and imports them into the tenant, through vminsert.
// It answers how many series were imported.
func copySamples(ctx context.Context, t *testing.T, prom proxied, client kubernetes.Interface, namespace, prefix string, tenant int64, names []string, t0 time.Time, copied time.Duration) int {
	t.Helper()
	insert := fmt.Sprintf("/api/v1/namespaces/%s/services/vminsert-%s:8480/proxy/insert/%d/prometheus/api/v1/import", namespace, prefix, tenant)
	total := 0
	for _, name := range names {
		body, err := prom.get(ctx, "/api/v1/query", url.Values{
			"query": {fmt.Sprintf(`{__name__=%q}[%ds]`, name, int(copied.Seconds()))},
			"time":  {strconv.FormatInt(t0.Unix(), 10)},
		})
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		var a struct {
			Data struct {
				Result []struct {
					Metric map[string]string   `json:"metric"`
					Values [][]json.RawMessage `json:"values"`
				} `json:"result"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &a); err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		var lines bytes.Buffer
		for _, r := range a.Data.Result {
			points := map[int64]float64{}
			for _, v := range r.Values {
				addPoint(points, v)
			}
			stamps := make([]int64, 0, len(points))
			for at := range points {
				stamps = append(stamps, at)
			}
			sort.Slice(stamps, func(i, j int) bool { return stamps[i] < stamps[j] })
			values := make([]float64, len(stamps))
			for i, at := range stamps {
				values[i] = points[at]
			}
			line, err := json.Marshal(map[string]any{"metric": r.Metric, "values": values, "timestamps": stamps})
			if err != nil {
				t.Fatal(err)
			}
			lines.Write(line)
			lines.WriteByte('\n')
		}
		if lines.Len() == 0 {
			continue
		}
		if err := importLines(ctx, client, insert, lines.Bytes()); err != nil {
			t.Fatalf("importing %s: %v", name, err)
		}
		total += len(a.Data.Result)
	}
	flush := fmt.Sprintf("/api/v1/namespaces/%s/services/vmstorage-%s:8482/proxy/internal/force_flush", namespace, prefix)
	if _, err := client.CoreV1().RESTClient().Get().AbsPath(flush).DoRaw(ctx); err != nil {
		t.Logf("flush: %v (waiting for the data instead)", err)
	}
	return total
}

// importLines posts JSON lines to vminsert in pieces of at most 64 KiB, each
// tried three times: the API server's proxy answers 503 to a large body
// vminsert is slow to take.
func importLines(ctx context.Context, client kubernetes.Interface, insert string, lines []byte) error {
	const piece = 64 << 10
	for len(lines) > 0 {
		cut := len(lines)
		if cut > piece {
			cut = bytes.LastIndexByte(lines[:piece], '\n') + 1
			if cut == 0 {
				cut = bytes.IndexByte(lines, '\n') + 1
			}
		}
		var err error
		for try := 0; try < 3; try++ {
			_, err = client.CoreV1().RESTClient().Post().AbsPath(insert).
				SetHeader("Content-Type", "application/json").Body(lines[:cut]).DoRaw(ctx)
			if err == nil {
				break
			}
			time.Sleep(time.Duration(try+1) * time.Second)
		}
		if err != nil {
			return err
		}
		lines = lines[cut:]
	}
	return nil
}

// waitForSeries waits until the tenant answers with every imported series.
func waitForSeries(ctx context.Context, t *testing.T, vm proxied, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		body, err := vm.get(ctx, "/api/v1/series/count", nil)
		var count struct {
			Data []int `json:"data"`
		}
		if err == nil && json.Unmarshal(body, &count) == nil && len(count.Data) == 1 && count.Data[0] >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the tenant holds %v of %d series after two minutes (%v)", count.Data, want, err)
		}
		time.Sleep(2 * time.Second)
	}
}

// verdict is what one query came to.
type verdict struct {
	q        query
	compared int // points compared
	// differ is the points outside the tolerance, worst the largest
	// relative difference among them, at worstLabels and worstAt.
	differ      int
	worst       float64
	worstLabels string
	worstAt     int64
	worstProm   float64
	worstVM     float64
	// chart is the largest difference relative to the series' own largest
	// value, the scale a chart draws it at: how visible a difference is.
	chart float64
	// onlyProm and onlyVM are finite points one side has and the other not.
	onlyProm, onlyVM int
	err              string
}

func (v verdict) equal() bool {
	return v.err == "" && v.differ == 0 && v.onlyProm == 0 && v.onlyVM == 0
}

// extrapolated says the query reads a counter's rate or increase, whose
// value differs by design between the two engines, on the same samples:
// Prometheus extrapolates the increase to the window's edges, VictoriaMetrics
// takes it from the last sample before the window and does not extrapolate
// (nor does its rate_prometheus). No query reads alike on both without
// changing what Prometheus answers, so such a difference is reported, not
// failed on; its size is the report's to say.
func (v verdict) extrapolated() bool {
	return v.err == "" && v.onlyProm == 0 && v.onlyVM == 0 &&
		(strings.Contains(v.q.PromQL, "rate(") || strings.Contains(v.q.PromQL, "increase("))
}

// compareValues asks q of both at the same instants: a range query over the
// compared window at its own step (five minutes at most, so the window has
// points), an instant query at its end.
func compareValues(ctx context.Context, prom, vm proxied, q query, t0 time.Time, compareFrom time.Duration) verdict {
	v := verdict{q: q}
	end := t0.Add(-compareUntil)
	start := t0.Add(-compareFrom)
	endpoint := "/api/v1/query"
	params := url.Values{"query": {q.PromQL}, "time": {strconv.FormatInt(end.Unix(), 10)}}
	if q.Range {
		step := min(max(q.Step, 15*time.Second), 5*time.Minute)
		// Both ends on the step, so both engines evaluate the same instants.
		start = start.Truncate(step)
		end = end.Truncate(step)
		endpoint = "/api/v1/query_range"
		params = url.Values{
			"query": {q.PromQL}, "start": {strconv.FormatInt(start.Unix(), 10)},
			"end": {strconv.FormatInt(end.Unix(), 10)}, "step": {strconv.FormatInt(int64(step.Seconds()), 10)},
		}
	}
	pb, err := prom.get(ctx, endpoint, params)
	if err != nil {
		v.err = "Prometheus: " + err.Error()
		return v
	}
	vmParams := url.Values{"nocache": {"1"}}
	for k, values := range params {
		vmParams[k] = values
	}
	vb, err := vm.get(ctx, endpoint, vmParams)
	if err != nil {
		v.err = "VictoriaMetrics: " + err.Error()
		return v
	}
	ps, err := parse(pb)
	if err != nil {
		v.err = "Prometheus: " + err.Error()
		return v
	}
	vs, err := parse(vb)
	if err != nil {
		v.err = "VictoriaMetrics: " + err.Error()
		return v
	}
	byLabels := map[string]map[int64]float64{}
	for _, one := range vs {
		byLabels[one.labels] = one.points
	}
	seen := map[string]bool{}
	for _, one := range ps {
		seen[one.labels] = true
		other := byLabels[one.labels]
		scale := 0.0
		for _, a := range one.points {
			if finiteValue(a) {
				scale = math.Max(scale, math.Abs(a))
			}
		}
		for at, a := range one.points {
			b, ok := other[at]
			switch {
			case !finiteValue(a) && (!ok || !finiteValue(b)):
				// A gap on both sides.
			case !ok || !finiteValue(b):
				v.onlyProm++
			case !finiteValue(a):
				v.onlyVM++
			default:
				v.compared++
				if scale > 0 {
					v.chart = math.Max(v.chart, math.Abs(a-b)/scale)
				}
				if d := relative(a, b); d > relTolerance && math.Abs(a-b) > absTolerance {
					v.differ++
					if d > v.worst {
						v.worst, v.worstLabels, v.worstAt, v.worstProm, v.worstVM = d, one.labels, at, a, b
					}
				}
			}
		}
		for at, b := range other {
			if _, ok := one.points[at]; !ok && finiteValue(b) {
				v.onlyVM++
			}
		}
	}
	for labels, points := range byLabels {
		if seen[labels] {
			continue
		}
		for _, b := range points {
			if finiteValue(b) {
				v.onlyVM++
			}
		}
	}
	return v
}

func finiteValue(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

func relative(a, b float64) float64 {
	scale := math.Max(math.Abs(a), math.Abs(b))
	if scale == 0 {
		return 0
	}
	return math.Abs(a-b) / scale
}

// writeReport logs one line per query, fails the test on every difference,
// and writes the report to KT_VM_REPORT when it is set.
func writeReport(t *testing.T, report []verdict) {
	t.Helper()
	var b strings.Builder
	equal, rates := 0, 0
	for _, v := range report {
		switch {
		case v.equal():
			equal++
		case v.extrapolated():
			rates++
		}
	}
	fmt.Fprintf(&b, "%d of %d queries equal within %.0f%% (or %g absolute); %d differ by rate extrapolation (RATE); %d differ otherwise (DIFFER) or failed (ERROR)\n\n",
		equal, len(report), relTolerance*100, absTolerance, rates, len(report)-equal-rates)
	for _, v := range report {
		kind := "instant"
		if v.q.Range {
			kind = "range " + v.q.Step.String()
		}
		switch {
		case v.err != "":
			fmt.Fprintf(&b, "ERROR  %s  %s\n       %s\n", kind, v.q.PromQL, v.err)
		case v.equal():
			fmt.Fprintf(&b, "equal  %s  %d points  %s\n", kind, v.compared, v.q.PromQL)
		default:
			label := "DIFFER"
			if v.extrapolated() {
				label = "RATE  "
			}
			fmt.Fprintf(&b, "%s %s  %d of %d points, max %.2f%% of the point, %.2f%% of the chart, only Prometheus %d, only VictoriaMetrics %d  %s\n",
				label, kind, v.differ, v.compared, v.worst*100, v.chart*100, v.onlyProm, v.onlyVM, v.q.PromQL)
			if v.differ > 0 {
				fmt.Fprintf(&b, "       worst at %s %s: Prometheus %g, VictoriaMetrics %g\n",
					time.UnixMilli(v.worstAt).UTC().Format(time.RFC3339), v.worstLabels, v.worstProm, v.worstVM)
			}
		}
	}
	t.Log("\n" + b.String())
	if path := os.Getenv("KT_VM_REPORT"); path != "" {
		if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
			t.Errorf("writing the report: %v", err)
		}
	}
	points := 0
	for _, v := range report {
		points += v.compared
		if !v.equal() && !v.extrapolated() {
			t.Errorf("not equal: %s", v.q.PromQL)
		}
	}
	if points == 0 {
		t.Error("no point was compared: the reference held nothing in the compared span")
	}
}

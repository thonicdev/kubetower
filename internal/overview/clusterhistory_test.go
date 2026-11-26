package overview

// On docker-desktop, cAdvisor behind cri-dockerd exports the pods' cgroups and
// the machine's and no series with a `container` label, so the two container
// sums cannot match and the Overview's *Over time* would draw "No samples in
// this window." under CPU used and Memory used, captioned "Prometheus
// answered". These tests hold the cluster history to the object panel's probe,
// and to its fallback: the pods' own cgroups, each pod counted once, never the
// machine's root cgroup.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// cgroupSeries is one cAdvisor series: a metric name, its labels, and the value
// it holds over the whole window. For a counter the value is read as its rate.
type cgroupSeries struct {
	name   string
	labels map[string]string
	value  float64
}

// cadvisorProm is a Prometheus holding a fixed set of cAdvisor series, which
// evaluates the subset of PromQL this package sends for the cluster's history:
// `sum(…)`, `max by (…) (…)`, `avg without (…) (…)`, `rate(<selector>[…])`, a
// selector with `=`, `!=`, `=~` and `!~` matchers, and the probe's
// `label_replace(count(…), "kt_metric", …)` joined by `or`.
//
// Unlike recordingMetrics, which answers every question with the same body, this
// one answers each query from the series: whether a selector matches a pod's
// cgroup, its containers or the machine is exactly what is under test, so a fake
// that could not tell them apart could not test it. A query shape it does not
// know fails the test rather than answering empty.
type cadvisorProm struct {
	t      *testing.T
	series []cgroupSeries
	// failProbes makes every probe that asks about one of these counters fail,
	// as a Prometheus refusing or timing out on that one question would.
	failProbes map[string]bool

	mu      sync.Mutex
	queries []string
	probes  []string
}

type sample struct {
	labels map[string]string
	value  float64
}

func (p *cadvisorProm) Query(_ context.Context, _, promQL string) ([]byte, error) {
	p.mu.Lock()
	p.probes = append(p.probes, promQL)
	p.mu.Unlock()

	for metric := range p.failProbes {
		if strings.Contains(promQL, metric) {
			return nil, fmt.Errorf("probe for %s: context deadline exceeded", metric)
		}
	}

	rows := []any{}
	for _, part := range strings.Split(promQL, " or ") {
		inner, ok := strings.CutPrefix(part, "label_replace(count(")
		selector, label, found := strings.Cut(inner, "), ")
		if !ok || !found || !strings.HasPrefix(label, `"`+probeLabel+`", "`) {
			p.t.Errorf("the fake Prometheus does not understand this probe: %s", promQL)
			return nil, fmt.Errorf("unsupported probe")
		}
		name, matched := p.selectSeries(selector)
		if len(matched) == 0 {
			continue
		}
		rows = append(rows, map[string]any{
			"metric": map[string]string{probeLabel: name},
			"value":  []any{1.0, strconv.Itoa(len(matched))},
		})
	}
	return json.Marshal(map[string]any{"status": "success", "data": map[string]any{"result": rows}})
}

func (p *cadvisorProm) QueryRange(_ context.Context, _, promQL string, start, end time.Time, _ time.Duration) ([]byte, error) {
	p.mu.Lock()
	p.queries = append(p.queries, promQL)
	p.mu.Unlock()

	rows := []any{}
	for _, s := range p.evaluate(promQL) {
		v := strconv.FormatFloat(s.value, 'f', -1, 64)
		rows = append(rows, map[string]any{
			"metric": s.labels,
			"values": [][]any{{float64(start.Unix()), v}, {float64(end.Unix()), v}},
		})
	}
	return json.Marshal(map[string]any{"status": "success", "data": map[string]any{"result": rows}})
}

func (p *cadvisorProm) evaluate(expr string) []sample {
	expr = strings.TrimSpace(expr)
	switch {
	case strings.HasPrefix(expr, "sum(") && strings.HasSuffix(expr, ")"):
		in := p.evaluate(expr[len("sum(") : len(expr)-1])
		if len(in) == 0 {
			return nil
		}
		total := 0.0
		for _, s := range in {
			total += s.value
		}
		return []sample{{labels: map[string]string{}, value: total}}
	case strings.HasPrefix(expr, "max by ("):
		labels, inner := p.grouping(expr, "max by (")
		return group(p.evaluate(inner), func(l map[string]string) map[string]string { return keep(l, labels) },
			func(values []float64) float64 {
				m := values[0]
				for _, v := range values[1:] {
					if v > m {
						m = v
					}
				}
				return m
			})
	case strings.HasPrefix(expr, "avg without ("):
		labels, inner := p.grouping(expr, "avg without (")
		return group(p.evaluate(inner), func(l map[string]string) map[string]string { return drop(l, labels) },
			func(values []float64) float64 {
				sum := 0.0
				for _, v := range values {
					sum += v
				}
				return sum / float64(len(values))
			})
	case strings.HasPrefix(expr, "rate(") && strings.HasSuffix(expr, "])"):
		inner := expr[len("rate(") : len(expr)-1]
		open := strings.LastIndex(inner, "[")
		if open < 0 {
			break
		}
		_, matched := p.selectSeries(inner[:open])
		return matched
	case selectorShape.MatchString(expr):
		_, matched := p.selectSeries(expr)
		return matched
	}
	p.t.Errorf("the fake Prometheus does not understand this expression: %s", expr)
	return nil
}

// grouping splits `<head>a, b) (<inner>)` into the label list and the inner
// expression.
func (p *cadvisorProm) grouping(expr, head string) ([]string, string) {
	rest := expr[len(head):]
	closing := strings.Index(rest, ")")
	if closing < 0 {
		p.t.Errorf("no closing parenthesis in %s", expr)
		return nil, ""
	}
	labels := strings.Split(rest[:closing], ",")
	for i := range labels {
		labels[i] = strings.TrimSpace(labels[i])
	}
	inner := strings.TrimSpace(rest[closing+1:])
	if !strings.HasPrefix(inner, "(") || !strings.HasSuffix(inner, ")") {
		p.t.Errorf("the grouping in %s does not wrap a parenthesized expression", expr)
		return nil, ""
	}
	return labels, inner[1 : len(inner)-1]
}

var (
	selectorShape = regexp.MustCompile(`^([a-zA-Z_:][a-zA-Z0-9_:]*)\{(.*)\}$`)
	matcherShape  = regexp.MustCompile(`^([a-zA-Z_][a-zA-Z0-9_]*)(=~|!~|!=|=)"((?:[^"\\]|\\.)*)"$`)
)

// selectSeries is a selector's metric name and the series it matches. A label a
// series does not carry is the empty string, as in Prometheus.
func (p *cadvisorProm) selectSeries(selector string) (string, []sample) {
	parts := selectorShape.FindStringSubmatch(strings.TrimSpace(selector))
	if parts == nil {
		p.t.Errorf("the fake Prometheus does not understand this selector: %s", selector)
		return "", nil
	}
	name := parts[1]
	var matchers [][]string
	for _, raw := range splitMatchers(parts[2]) {
		m := matcherShape.FindStringSubmatch(strings.TrimSpace(raw))
		if m == nil {
			p.t.Errorf("the fake Prometheus does not understand this matcher: %s", raw)
			return name, nil
		}
		matchers = append(matchers, m[1:])
	}
	var out []sample
	for _, s := range p.series {
		if s.name != name || !matchesAll(s.labels, matchers) {
			continue
		}
		labels := map[string]string{}
		for k, v := range s.labels {
			labels[k] = v
		}
		out = append(out, sample{labels: labels, value: s.value})
	}
	return name, out
}

func splitMatchers(body string) []string {
	var out []string
	quoted, start := false, 0
	for i := 0; i < len(body); i++ {
		switch body[i] {
		case '\\':
			i++
		case '"':
			quoted = !quoted
		case ',':
			if !quoted {
				out = append(out, body[start:i])
				start = i + 1
			}
		}
	}
	if strings.TrimSpace(body[start:]) != "" {
		out = append(out, body[start:])
	}
	return out
}

func matchesAll(labels map[string]string, matchers [][]string) bool {
	for _, m := range matchers {
		have, op, want := labels[m[0]], m[1], m[2]
		var ok bool
		switch op {
		case "=":
			ok = have == want
		case "!=":
			ok = have != want
		case "=~", "!~":
			ok = regexp.MustCompile("^(?:" + want + ")$").MatchString(have)
			if op == "!~" {
				ok = !ok
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

func group(in []sample, key func(map[string]string) map[string]string, reduce func([]float64) float64) []sample {
	order := []string{}
	byKey := map[string][]float64{}
	labelsOf := map[string]map[string]string{}
	for _, s := range in {
		labels := key(s.labels)
		k := labelKey(labels)
		if _, seen := byKey[k]; !seen {
			order = append(order, k)
			labelsOf[k] = labels
		}
		byKey[k] = append(byKey[k], s.value)
	}
	out := make([]sample, 0, len(order))
	for _, k := range order {
		out = append(out, sample{labels: labelsOf[k], value: reduce(byKey[k])})
	}
	return out
}

func keep(labels map[string]string, names []string) map[string]string {
	out := map[string]string{}
	for _, n := range names {
		if v := labels[n]; v != "" {
			out[n] = v
		}
	}
	return out
}

func drop(labels map[string]string, names []string) map[string]string {
	out := map[string]string{}
	for k, v := range labels {
		out[k] = v
	}
	for _, n := range names {
		delete(out, n)
	}
	return out
}

func labelKey(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k + "=" + labels[k] + ";")
	}
	return b.String()
}

// --- the series ------------------------------------------------------------------

func cg(name string, value float64, labels ...string) cgroupSeries {
	l := map[string]string{"job": "kubelet", "instance": "192.168.65.3:10250", "service": "kps-kubelet"}
	for i := 0; i+1 < len(labels); i += 2 {
		l[labels[i]] = labels[i+1]
	}
	return cgroupSeries{name: name, labels: l, value: value}
}

// theMachine is what every cAdvisor exports whatever the runtime: the node's
// root cgroup, which counts the whole machine, and a system slice. Neither has a
// `pod` label, and neither may enter a figure that claims to be the pods'.
func theMachine() []cgroupSeries {
	return []cgroupSeries{
		cg(metricCPU, 4, "id", "/"),
		cg(metricCPU, 1, "id", "/system.slice/docker.service"),
		cg(metricMemory, 8e9, "id", "/"),
		cg(metricMemory, 1e9, "id", "/system.slice/docker.service"),
	}
}

// dockerDesktop is the shape measured on docker-desktop: the pods' cgroups and
// the machine's, and no series with a `container` label. The coredns pod is
// scraped twice, through two kubelet Services, as on a doubly scraped cluster.
func dockerDesktop() []cgroupSeries {
	coredns := []string{"id", "/kubepods/burstable/pod1a", "namespace", "kube-system", "pod", "coredns-1"}
	prom := []string{"id", "/kubepods/burstable/pod2b", "namespace", "monitoring", "pod", "prometheus-0"}
	return append(theMachine(),
		cg(metricCPU, 0.25, coredns...),
		cg(metricCPU, 0.25, append(coredns, "service", "kps-kubelet-stale")...),
		cg(metricCPU, 0.5, prom...),
		cg(metricMemory, 100e6, coredns...),
		cg(metricMemory, 100e6, append(coredns, "service", "kps-kubelet-stale")...),
		cg(metricMemory, 300e6, prom...),
	)
}

func valueOf(t *testing.T, h History) float64 {
	t.Helper()
	if len(h.Points) == 0 {
		t.Fatalf("%s drew nothing: %q", h.Title, h.Unavailable)
	}
	return h.Points[len(h.Points)-1].Value
}

// --- the tests the entry names ---------------------------------------------------

// A Prometheus holding only pod-level series: the panel draws the pods' cgroups,
// summed, and the caption names that source.
func TestTheClusterHistoryFallsBackToPodCgroupsWhenNoSeriesHasAContainer(t *testing.T) {
	prom := &cadvisorProm{t: t, series: dockerDesktop()}
	av, cpu, memory := clusterHistory(context.Background(), prom, "docker-desktop")

	if !av.OK {
		t.Fatalf("history unavailable: %s", av.Detail)
	}
	// 0.25 cores for coredns, counted once although scraped twice, and 0.5 for
	// prometheus: 750m. The machine's 4 cores and the system slice are not pods.
	if got := valueOf(t, cpu); got != 750 {
		t.Errorf("CPU used = %vm, want 750m - the pods' cgroups, each once, without the machine", got)
	}
	if got := valueOf(t, memory); got != 400e6 {
		t.Errorf("Memory used = %v, want 400e6", got)
	}
	if av.Detail != fromPodCgroups {
		t.Errorf("caption = %q, want %q: a sum of pod cgroups must not pass for the container sum", av.Detail, fromPodCgroups)
	}
	for _, line := range []History{cpu, memory} {
		if line.Unavailable != "" || line.Missing != MissingNone {
			t.Errorf("%s drew and still carries a reason: %q (%v)", line.Title, line.Unavailable, line.Missing)
		}
	}
	for _, q := range prom.queries {
		if strings.Contains(q, `{pod!="",container=""}`) && !strings.Contains(q, "max by (namespace, pod) (") {
			t.Errorf("a pod-cgroup query does not keep one series per pod:\n  %s", q)
		}
	}
}

// A pod with its own cgroup and its containers' under it is counted once,
// whichever of the two shapes the sum is read from.
func TestAPodWithItsOwnCgroupAndItsContainersIsCountedOnce(t *testing.T) {
	pod := []string{"namespace", "shop", "pod", "api-0", "id", "/kubepods/burstable/pod3c"}
	under := func(container, id string) []string {
		return []string{"namespace", "shop", "pod", "api-0", "container", container, "id", "/kubepods/burstable/pod3c/" + id}
	}

	t.Run("container series exist", func(t *testing.T) {
		prom := &cadvisorProm{t: t, series: append(theMachine(),
			cg(metricCPU, 0.5, pod...),
			cg(metricCPU, 0.3, under("app", "a1")...),
			cg(metricCPU, 0.2, under("sidecar", "b2")...),
			cg(metricCPU, 0.0, under("POD", "c3")...),
			cg(metricMemory, 500e6, pod...),
			cg(metricMemory, 300e6, under("app", "a1")...),
			cg(metricMemory, 200e6, under("sidecar", "b2")...),
		)}
		av, cpu, memory := clusterHistory(context.Background(), prom, "any")
		if got := valueOf(t, cpu); got != 500 {
			t.Errorf("CPU used = %vm, want 500m: the pod's cgroup was added to its containers'", got)
		}
		if got := valueOf(t, memory); got != 500e6 {
			t.Errorf("Memory used = %v, want 500e6", got)
		}
		if av.Detail != "Prometheus answered" {
			t.Errorf("caption = %q, want the container sum's own caption", av.Detail)
		}
		if len(prom.probes) != 0 {
			t.Errorf("a cluster whose lines drew was probed: %v", prom.probes)
		}
	})

	// Where no series has a container label, a pod's cgroup and a cgroup under
	// it can both carry the pod's labels. The pod's own is the parent and counts
	// everything under it, so it is the one kept.
	t.Run("only pod-level series", func(t *testing.T) {
		child := []string{"namespace", "shop", "pod", "api-0", "id", "/kubepods/burstable/pod3c/a1"}
		prom := &cadvisorProm{t: t, series: append(theMachine(),
			cg(metricCPU, 0.5, pod...),
			cg(metricCPU, 0.3, child...),
			cg(metricMemory, 500e6, pod...),
			cg(metricMemory, 300e6, child...),
		)}
		av, cpu, memory := clusterHistory(context.Background(), prom, "any")
		if got := valueOf(t, cpu); got != 500 {
			t.Errorf("CPU used = %vm, want 500m: a cgroup under the pod was added to the pod's", got)
		}
		if got := valueOf(t, memory); got != 500e6 {
			t.Errorf("Memory used = %v, want 500e6", got)
		}
		if av.Detail != fromPodCgroups {
			t.Errorf("caption = %q", av.Detail)
		}
	})
}

// Neither shape: the caption says why, and is not "Prometheus answered".
func TestAClusterWithNeitherContainerNorPodSeriesSaysWhy(t *testing.T) {
	prom := &cadvisorProm{t: t, series: theMachine()}
	av, cpu, memory := clusterHistory(context.Background(), prom, "any")

	if !av.OK {
		t.Fatalf("Prometheus answered every question and the history reads as failed: %s", av.Detail)
	}
	if av.Detail == "Prometheus answered" || av.Detail == "" {
		t.Fatalf("caption = %q over two empty charts, which says nothing about why", av.Detail)
	}
	for _, want := range []string{"no per-container", "pod cgroup", "not a claim that the cluster is idle"} {
		if !strings.Contains(av.Detail, want) {
			t.Errorf("caption %q does not say %q", av.Detail, want)
		}
	}
	for _, line := range []History{cpu, memory} {
		if len(line.Points) != 0 {
			t.Errorf("%s drew %d points from a Prometheus holding only the machine", line.Title, len(line.Points))
		}
		if line.Missing != MissingCounter {
			t.Errorf("%s: Missing = %v, want MissingCounter", line.Title, line.Missing)
		}
		if strings.Contains(line.Unavailable, "younger") {
			t.Errorf("%s blames the window for a series that cannot exist: %q", line.Title, line.Unavailable)
		}
	}
}

// --- and the other side of the probe ----------------------------------------------

// The container series exist and the window holds none of them: the empty
// window is the true sentence, and nothing is summed from the pods' cgroups.
func TestAnEmptyWindowOverContainerSeriesIsSaidAsOne(t *testing.T) {
	empty := []byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`)
	m := &recordingMetrics{answer: empty, probe: everyCounter(t)}
	av, cpu, memory := clusterHistory(context.Background(), m, "any")

	if !av.OK || av.Detail != emptyWindow {
		t.Errorf("history = %+v, want OK with the empty-window sentence", av)
	}
	for _, line := range []History{cpu, memory} {
		if line.Missing != MissingEmpty || line.Unavailable != emptyWindow {
			t.Errorf("%s = %q (%v), want the empty window", line.Title, line.Unavailable, line.Missing)
		}
	}
	if len(m.queries) != 2 {
		t.Errorf("asked %d range queries, want the two container sums only: %v", len(m.queries), m.queries)
	}
	// One probe per line, so one line's failed question cannot decide the
	// other's outcome.
	asked := strings.Join(m.probes, "\n")
	if len(m.probes) != 2 || !strings.Contains(asked, perContainer(metricCPU)) ||
		!strings.Contains(asked, perContainer(metricMemory)) {
		t.Errorf("probes = %v, want one each for per-container CPU and memory", m.probes)
	}
}

// The dashboard's zoom reads the same two lines and falls back the same way:
// the pattern is the cluster's, not one caller's.
func TestTheClusterZoomFallsBackToPodCgroupsToo(t *testing.T) {
	prom := &cadvisorProm{t: t, series: dockerDesktop()}
	out := ZoomClusterHistory(context.Background(), prom, "docker-desktop", WindowFor("6h"))
	if got := valueOf(t, out.CPU); got != 750 {
		t.Errorf("zoomed CPU = %vm, want 750m", got)
	}
	if out.Source != fromPodCgroups {
		t.Errorf("zoom source = %q, want %q", out.Source, fromPodCgroups)
	}
}

// --- one line drawn, the other not ---------------------------------------------

// containerCPU is a cluster whose CPU has per-container series: one pod, its own
// cgroup and its one container's.
func containerCPU() []cgroupSeries {
	return append(theMachine(),
		cg(metricCPU, 0.5, "namespace", "shop", "pod", "api-0", "id", "/kubepods/pod3c"),
		cg(metricCPU, 0.5, "namespace", "shop", "pod", "api-0", "container", "app", "id", "/kubepods/pod3c/a1"),
	)
}

// `fill` says "no samples in this window" only when no line drew, because its
// sentence is about the object's age and a drawn line disproves that. The
// cluster's lines are probed one by one all the same: CPU drawing from its
// containers must not leave Memory blank under "Prometheus answered".
func TestOneLineDrawnDoesNotSilenceTheOther(t *testing.T) {
	t.Run("the other falls back to pod cgroups", func(t *testing.T) {
		prom := &cadvisorProm{t: t, series: append(containerCPU(),
			cg(metricMemory, 300e6, "namespace", "shop", "pod", "api-0", "id", "/kubepods/pod3c"),
		)}
		av, cpu, memory := clusterHistory(context.Background(), prom, "any")
		if got := valueOf(t, cpu); got != 500 {
			t.Errorf("CPU used = %vm, want 500m from the container", got)
		}
		if got := valueOf(t, memory); got != 300e6 {
			t.Errorf("Memory used = %v, want 300e6 from the pod's cgroup", got)
		}
		// Honest about the one line only: CPU is the container sum, and this
		// Prometheus does have per-container CPU series.
		if av.Detail == fromPodCgroups || av.Detail == "Prometheus answered" {
			t.Fatalf("caption = %q over one container line and one pod-cgroup line", av.Detail)
		}
		for _, want := range []string{"Memory used", "from pod cgroups", metricMemory} {
			if !strings.Contains(av.Detail, want) {
				t.Errorf("caption %q does not say %q", av.Detail, want)
			}
		}
		if strings.Contains(av.Detail, "CPU used") {
			t.Errorf("caption %q speaks for the CPU line, which is the container sum", av.Detail)
		}
	})

	t.Run("the other has no series of either shape", func(t *testing.T) {
		prom := &cadvisorProm{t: t, series: containerCPU()}
		av, cpu, memory := clusterHistory(context.Background(), prom, "any")
		valueOf(t, cpu)
		if len(memory.Points) != 0 || memory.Missing != MissingCounter || memory.Unavailable == "" {
			t.Errorf("Memory = %d points, %q (%v), want the absent counter said", len(memory.Points), memory.Unavailable, memory.Missing)
		}
		if !strings.HasPrefix(av.Detail, "Memory used: ") || !strings.Contains(av.Detail, "no pod cgroup") {
			t.Errorf("caption = %q, want Memory's reason under its title", av.Detail)
		}
	})

	// Memory's container series exist and hold no sample in the window (NaN
	// throughout, which Prometheus writes for a gap and parseRange drops).
	t.Run("the other has its series and an empty window", func(t *testing.T) {
		prom := &cadvisorProm{t: t, series: append(containerCPU(),
			cg(metricMemory, math.NaN(), "namespace", "shop", "pod", "api-0", "container", "app", "id", "/kubepods/pod3c/a1"),
			cg(metricMemory, 300e6, "namespace", "shop", "pod", "api-0", "id", "/kubepods/pod3c"),
		)}
		av, cpu, memory := clusterHistory(context.Background(), prom, "any")
		valueOf(t, cpu)
		if len(memory.Points) != 0 || memory.Missing != MissingEmpty || memory.Unavailable == "" {
			t.Errorf("Memory = %d points, %q (%v), want an empty window said", len(memory.Points), memory.Unavailable, memory.Missing)
		}
		if strings.Contains(memory.Unavailable, "younger") {
			t.Errorf("Memory blames the cluster's age beside a CPU line that drew: %q", memory.Unavailable)
		}
		if !strings.HasPrefix(av.Detail, "Memory used: ") {
			t.Errorf("caption = %q, want Memory's reason under its title", av.Detail)
		}
		for _, q := range prom.queries {
			if strings.Contains(q, `container=""`) {
				t.Errorf("a line whose container series exist was read from the pods' cgroups:\n  %s", q)
			}
		}
	})
}

// --- every combination -------------------------------------------------------------

// lineState is what one cluster line is, and each has exactly one sentence.
type lineState string

const (
	drawnContainers lineState = "drawn from containers"
	drawnPods       lineState = "drawn from pod cgroups"
	emptyContainers lineState = "container series, empty window"
	emptyPods       lineState = "pod cgroup series, empty window"
	neitherShape    lineState = "neither shape"
	probeFailed     lineState = "probe failed"
)

var lineStates = []lineState{drawnContainers, drawnPods, emptyContainers, emptyPods, neitherShape, probeFailed}

// seriesIn builds one counter's series for a state. A pod cgroup worth 0.5 sits
// beside the container worth 0.3 wherever the container series exist, so a line
// read from the wrong shape draws the wrong figure, and beside an empty
// container window or a failed probe, so a fallback taken there draws at all.
// NaN is a window holding no sample: Prometheus writes a gap as NaN and
// parseRange drops it.
func seriesIn(state lineState, metric string, scale float64) []cgroupSeries {
	pod := []string{"namespace", "shop", "pod", "api-0", "id", "/kubepods/pod3c"}
	container := append([]string{"container", "app"}, "namespace", "shop", "pod", "api-0", "id", "/kubepods/pod3c/a1")
	switch state {
	case drawnContainers:
		return []cgroupSeries{cg(metric, 0.5*scale, pod...), cg(metric, 0.3*scale, container...)}
	case drawnPods:
		return []cgroupSeries{cg(metric, 0.5*scale, pod...)}
	case emptyContainers:
		return []cgroupSeries{cg(metric, 0.5*scale, pod...), cg(metric, math.NaN(), container...)}
	case emptyPods:
		return []cgroupSeries{cg(metric, math.NaN(), pod...)}
	case probeFailed:
		return []cgroupSeries{cg(metric, 0.5*scale, pod...)}
	}
	return nil
}

func TestEveryCombinationOfTheTwoClusterLinesSaysOneTrueThing(t *testing.T) {
	for _, cpuState := range lineStates {
		for _, memoryState := range lineStates {
			t.Run(string(cpuState)+" × "+string(memoryState), func(t *testing.T) {
				series := append(theMachine(), seriesIn(cpuState, metricCPU, 1)...)
				series = append(series, seriesIn(memoryState, metricMemory, 1e9)...)
				failing := map[string]bool{}
				if cpuState == probeFailed {
					failing[metricCPU] = true
				}
				if memoryState == probeFailed {
					failing[metricMemory] = true
				}
				prom := &cadvisorProm{t: t, series: series, failProbes: failing}
				av, cpu, memory := clusterHistory(context.Background(), prom, "any")

				if !av.OK {
					t.Fatalf("every range query answered and the history reads as failed: %q", av.Detail)
				}
				drew := map[lineState]bool{drawnContainers: true, drawnPods: true}
				anyDrew := drew[cpuState] || drew[memoryState]
				lines := []struct {
					state  lineState
					line   History
					metric string
					want   float64
				}{
					{cpuState, cpu, metricCPU, 0},
					{memoryState, memory, metricMemory, 0},
				}
				lines[0].want = map[lineState]float64{drawnContainers: 300, drawnPods: 500}[cpuState]
				lines[1].want = map[lineState]float64{drawnContainers: 0.3e9, drawnPods: 0.5e9}[memoryState]

				for _, l := range lines {
					checkLine(t, l.state, l.line, l.metric, l.want, anyDrew)
				}

				// The caption.
				switch {
				case cpuState == drawnContainers && memoryState == drawnContainers:
					if av.Detail != "Prometheus answered" {
						t.Errorf("caption = %q", av.Detail)
					}
					if len(prom.probes) != 0 || len(prom.queries) != 2 {
						t.Errorf("a healthy cluster asked %d range queries and %d probes, want 2 and 0",
							len(prom.queries), len(prom.probes))
					}
				case cpuState == drawnPods && memoryState == drawnPods:
					if av.Detail != fromPodCgroups {
						t.Errorf("caption = %q, want %q", av.Detail, fromPodCgroups)
					}
				default:
					if av.Detail == "Prometheus answered" || av.Detail == fromPodCgroups {
						t.Errorf("caption = %q over %s and %s", av.Detail, cpuState, memoryState)
					}
					for _, l := range lines {
						switch l.state {
						case drawnContainers:
							if strings.Contains(av.Detail, l.line.Title+":") {
								t.Errorf("caption %q speaks for %s, the container sum", av.Detail, l.line.Title)
							}
						case drawnPods:
							if !strings.Contains(av.Detail, l.line.Title+": from pod cgroups") {
								t.Errorf("caption %q does not name %s's source", av.Detail, l.line.Title)
							}
						default:
							if !strings.Contains(av.Detail, l.line.Unavailable) {
								t.Errorf("caption %q does not carry %s's reason %q", av.Detail, l.line.Title, l.line.Unavailable)
							}
						}
					}
				}
			})
		}
	}
}

// checkLine holds one line to its state's one outcome.
func checkLine(t *testing.T, state lineState, line History, metric string, want float64, anyDrew bool) {
	t.Helper()
	switch state {
	case drawnContainers, drawnPods:
		if len(line.Points) == 0 {
			t.Errorf("%s (%s) drew nothing: %q", line.Title, state, line.Unavailable)
			return
		}
		if got := line.Points[len(line.Points)-1].Value; got != want {
			t.Errorf("%s (%s) = %v, want %v", line.Title, state, got, want)
		}
		if line.Unavailable != "" || line.Missing != MissingNone {
			t.Errorf("%s drew and carries a reason: %q (%v)", line.Title, line.Unavailable, line.Missing)
		}
		return
	}
	if len(line.Points) != 0 {
		t.Errorf("%s (%s) drew %d points", line.Title, state, len(line.Points))
	}
	if line.Unavailable == "" {
		t.Errorf("%s (%s) is empty with no reason", line.Title, state)
	}
	if anyDrew && strings.Contains(line.Unavailable, "younger") {
		t.Errorf("%s (%s) blames the cluster's age beside a line that drew: %q", line.Title, state, line.Unavailable)
	}
	for _, contradiction := range []string{"holds its series — whether", "could not be established"} {
		if state != probeFailed && strings.Contains(line.Unavailable, contradiction) {
			t.Errorf("%s (%s): %q", line.Title, state, line.Unavailable)
		}
	}
	switch state {
	case emptyContainers:
		if line.Missing != MissingEmpty || strings.Contains(line.Unavailable, "pod cgroup") {
			t.Errorf("%s (%s) = %q (%v), want an empty window over the container series", line.Title, state, line.Unavailable, line.Missing)
		}
	case emptyPods:
		if line.Missing != MissingEmpty || !strings.HasPrefix(line.Unavailable, "from pod cgroups: ") {
			t.Errorf("%s (%s) = %q (%v), want an empty window over the pod cgroups", line.Title, state, line.Unavailable, line.Missing)
		}
	case neitherShape:
		if line.Missing != MissingCounter || !strings.Contains(line.Unavailable, "no pod cgroup") ||
			!strings.Contains(line.Unavailable, metric) {
			t.Errorf("%s (%s) = %q (%v), want neither shape said", line.Title, state, line.Unavailable, line.Missing)
		}
	case probeFailed:
		// Never an empty window: a probe that failed established nothing, and
		// `empty_scope` would draw a failure as a quiet cluster.
		if line.Missing != MissingFailed || line.Err == nil {
			t.Errorf("%s (%s) = %v (err %v), want a failure", line.Title, state, line.Missing, line.Err)
		}
		if !strings.Contains(line.Unavailable, "could not establish") || strings.Contains(line.Unavailable, "although") ||
			strings.Contains(line.Unavailable, "no samples") {
			t.Errorf("%s (%s) = %q", line.Title, state, line.Unavailable)
		}
	}
}

package overview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thonicdev/kubetower/internal/metrics"
)

// A fake Prometheus that records what it was asked and answers with whatever the
// test wants. The point of most of these is the *query*, because a query with a
// wrong selector returns a perfectly well-formed chart of the wrong thing — and
// there is nothing on screen to say so.
//
// mu is load-bearing rather than defensive: fill() asks every series' question
// in its own goroutine through gather(), so QueryRange is genuinely concurrent.
// Without it `go test -race ./...` fails in this package, which would hide a
// real race anywhere else in the tree behind this one. The fields are read
// only after the
// call under test has returned, so the readers need no lock.
type recordingMetrics struct {
	mu      sync.Mutex
	queries []string
	steps   []time.Duration
	windows []time.Duration
	answer  []byte
	err     error
	// probe answers the one *instant* query the availability check makes: does
	// this Prometheus hold these counter names at all. Kept apart from `answer`
	// because that is the entire point of the probe — a Prometheus with no
	// cAdvisor network counters and a pod that sent no packets return the same
	// empty range, and a fake that could not tell them apart could not test the
	// one thing here that matters.
	probes   []string
	probe    []byte
	probeErr error
}

func (m *recordingMetrics) Query(_ context.Context, _, promQL string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.probes = append(m.probes, promQL)
	if m.probe != nil || m.probeErr != nil {
		return m.probe, m.probeErr
	}
	return m.answer, m.err
}

func (m *recordingMetrics) QueryRange(_ context.Context, _, promQL string, start, end time.Time, step time.Duration) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.queries = append(m.queries, promQL)
	m.steps = append(m.steps, step)
	m.windows = append(m.windows, end.Sub(start))
	return m.answer, m.err
}

func matrix(t *testing.T, values ...[2]any) []byte {
	t.Helper()
	rows := make([][]any, 0, len(values))
	for _, v := range values {
		rows = append(rows, []any{v[0], v[1]})
	}
	raw, err := json.Marshal(map[string]any{
		"status": "success",
		"data":   map[string]any{"result": []any{map[string]any{"values": rows}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// exported builds the probe's answer: one count per counter this cluster
// holds, named in the probe's label. A name not listed is one its Prometheus
// has never seen.
func exported(t *testing.T, present ...string) []byte {
	t.Helper()
	rows := make([]any, 0, len(present))
	for _, name := range present {
		rows = append(rows, map[string]any{
			"metric": map[string]string{probeLabel: name},
			"value":  []any{1.0, "12"},
		})
	}
	raw, err := json.Marshal(map[string]any{
		"status": "success",
		"data":   map[string]any{"result": rows},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// everyCounter is a cluster whose Prometheus holds all of them.
func everyCounter(t *testing.T) []byte {
	t.Helper()
	return exported(t, metricCPU, metricMemory, metricNetIn, metricNetOut, metricDisk)
}

func TestAPodsQueriesNameThatPodAndNothingElse(t *testing.T) {
	m := &recordingMetrics{answer: matrix(t, [2]any{1.0, "0.25"}), probe: everyCounter(t)}

	PodHistory(context.Background(), m, "any", "support", "api-7d9f", WindowFor("1h"))

	// One per series, and the network chart is two of them.
	if len(m.queries) != len(AllCharts)+1 {
		t.Fatalf("asked %d range questions, want %d: %v", len(m.queries), len(AllCharts)+1, m.queries)
	}
	for _, q := range m.queries {
		if !strings.Contains(q, `namespace="support"`) {
			t.Errorf("the query does not name the namespace, so it charts the whole cluster:\n  %s", q)
		}
		if !strings.Contains(q, `pod="api-7d9f"`) {
			t.Errorf("the query does not name the pod:\n  %s", q)
		}
	}
}

// The selector that differs from the one three lines above it in the source, and
// the case this whole feature turns on.
//
// cAdvisor collects network counters from the *pod's* network namespace — every
// container in a pod shares one — so those series carry no `container` label at
// all. `container!=""`, which CPU and memory need in order to drop the pod-level
// rollup cAdvisor exports beside the per-container series, matches none of them.
//
// Verified against a real Prometheus on an EKS cluster:
// `container_network_receive_bytes_total{namespace="prometheus"}` returns 261
// series, and the same selector with `container!=""` returns zero.
//
// A test that still passed with `container!=""` on the network query would prove
// nothing, so every series is named and checked on its own.
func TestTheNetworkSelectorIsNotTheCPUOne(t *testing.T) {
	cases := []struct {
		chart         string
		metric        string
		wantContainer bool
		why           string
	}{
		{ChartCPU, metricCPU, true, "cAdvisor's pod-level rollup would double every CPU figure"},
		{ChartMemory, metricMemory, true, "cAdvisor's pod-level rollup would double every memory figure"},
		{ChartNetwork, metricNetIn, false, "network counters carry no container label, so this matches nothing at all"},
		{ChartNetwork, metricNetOut, false, "network counters carry no container label, so this matches nothing at all"},
		{ChartDisk, metricDisk, true, `the machine-level id="/" series would add a node's whole root disk to a pod`},
	}

	for _, c := range cases {
		m := &recordingMetrics{answer: matrix(t, [2]any{1.0, "1"}), probe: everyCounter(t)}
		PodHistory(context.Background(), m, "any", "ns", "pod", WindowFor("1h"), c.chart)

		query := ""
		for _, q := range m.queries {
			if strings.Contains(q, c.metric+"{") {
				query = q
			}
		}
		if query == "" {
			t.Errorf("%s: nothing was asked about %s; the queries were %v", c.chart, c.metric, m.queries)
			continue
		}
		if got := strings.Contains(query, `container!=""`); got != c.wantContainer {
			t.Errorf(`%s: container!="" present = %v, want %v — %s`+"\n  %s",
				c.chart, got, c.wantContainer, c.why, query)
		}
	}
}

// A counter charted raw is a monotonically rising line whose gradient is the
// answer, which is not a chart anybody reads.
func TestTheNetworkCountersAreChartedAsARate(t *testing.T) {
	m := &recordingMetrics{answer: matrix(t, [2]any{1.0, "1"}), probe: everyCounter(t)}
	got := PodHistory(context.Background(), m, "any", "ns", "pod", WindowFor("1h"), ChartNetwork)

	for _, q := range m.queries {
		if !strings.Contains(q, "rate(") {
			t.Errorf("a network counter is charted raw rather than as a rate:\n  %s", q)
		}
	}
	if got.NetIn.Unit != UnitBytesPerSecond || got.NetOut.Unit != UnitBytesPerSecond {
		t.Error("a network rate is not carrying a per-second unit; a rate labeled as a level " +
			"reads three orders of magnitude wrong over an hour")
	}
}

// The panel draws one chart at a time, so it has to be able to ask for one.
func TestAskingForOneChartAsksOnlyForThatChart(t *testing.T) {
	for _, c := range []struct {
		chart string
		want  int
	}{
		{ChartCPU, 1}, {ChartMemory, 1}, {ChartNetwork, 2}, {ChartDisk, 1},
	} {
		m := &recordingMetrics{answer: matrix(t, [2]any{1.0, "1"}), probe: everyCounter(t)}
		PodHistory(context.Background(), m, "any", "ns", "pod", WindowFor("1h"), c.chart)
		if len(m.queries) != c.want {
			t.Errorf("%s asked %d range queries, want %d: %v", c.chart, len(m.queries), c.want, m.queries)
		}
	}
}

// Narrowing the fetch must not narrow the selector.
//
// The panel draws one chart at a time and asks for that one, so a render of the
// CPU chart issues one range query. But the row of controls above it still has to
// say whether the *network* chart can be drawn — and a panel that grayed the
// network control out merely because this render did not fetch network would be
// refusing a chart that works. So the range queries follow the request and the
// probe covers every chart the object has.
func TestNarrowingTheFetchStillAnswersForTheOtherCharts(t *testing.T) {
	m := &recordingMetrics{answer: matrix(t, [2]any{1.0, "1"}), probe: everyCounter(t)}

	got := PodHistory(context.Background(), m, "any", "ns", "pod", WindowFor("1h"), ChartCPU)

	if len(m.queries) != 1 {
		t.Errorf("asking for CPU cost %d range queries: %v", len(m.queries), m.queries)
	}
	if !got.Support.Network.OK {
		t.Errorf("the network control was refused because this render drew CPU: %q", got.Support.Network.Detail)
	}
	byKey := map[string]ChartOption{}
	for _, o := range got.Options() {
		byKey[o.Key] = o
	}
	// And it is offered under its real name, not the fallback: the label comes
	// from the series' title, which is set whether or not the series was fetched.
	if byKey[ChartDisk].Label != "Container filesystem" {
		t.Errorf("the filesystem control is labeled %q; a narrowed fetch lost the honest name",
			byKey[ChartDisk].Label)
	}
	// The series that was not fetched says so, rather than looking like an
	// object with no traffic.
	if !strings.Contains(got.NetIn.Unavailable, "not asked for") {
		t.Errorf("an unfetched series says %q; drawn, that reads as an idle pod", got.NetIn.Unavailable)
	}
}

// A chart name nothing recognizes must not come back as a cluster with no
// metrics: the two are fixed by different people.
func TestAnUnknownChartSaysNothingWasAskedRatherThanNothingExists(t *testing.T) {
	m := &recordingMetrics{answer: matrix(t, [2]any{1.0, "1"}), probe: everyCounter(t)}
	got := PodHistory(context.Background(), m, "any", "ns", "pod", WindowFor("1h"), "netin")

	if len(m.queries) != 0 {
		t.Errorf("an unrecognized chart name still asked Prometheus something: %v", m.queries)
	}
	if !strings.Contains(got.Support.Network.Detail, "no chart was asked for") {
		t.Errorf("the network control says %q; it should say nothing was asked, "+
			"not describe the cluster", got.Support.Network.Detail)
	}
}

// Whatever the panel asks for, the cluster is asked about the optional counters
// exactly once, and with an instant query. Four extra range queries per render to
// decide whether to gray out two icons is load on somebody's production
// Prometheus for a picture nobody is looking at.
func TestAvailabilityCostsOneInstantQuery(t *testing.T) {
	m := &recordingMetrics{answer: matrix(t, [2]any{1.0, "1"}), probe: everyCounter(t)}
	PodHistory(context.Background(), m, "any", "ns", "pod", WindowFor("1h"))

	if len(m.probes) != 1 {
		t.Fatalf("establishing what this cluster can answer for cost %d instant queries, want 1: %v",
			len(m.probes), m.probes)
	}
	for _, name := range []string{metricNetIn, metricNetOut, metricDisk} {
		if !strings.Contains(m.probes[0], name) {
			t.Errorf("the probe does not ask about %s:\n  %s", name, m.probes[0])
		}
	}
	if strings.Contains(m.probes[0], "rate(") || strings.Contains(m.probes[0], "[5m]") {
		t.Errorf("the probe is a range question dressed as an instant one:\n  %s", m.probes[0])
	}
	// Set operators match on the label set *excluding* the metric name, so
	// sub-answers told apart by __name__ alone have the same empty signature
	// and `or` throws away every one after the first. Prometheus can be told
	// `or on(__name__)`; VictoriaMetrics ignores __name__ in on(). A cluster
	// holding 1 836 series of container_network_transmit_bytes_total would then
	// report it as not exported, graying the network chart on exactly the
	// clusters where it works. Each sub-answer names its counter in an ordinary
	// label, which both engines tell apart.
	parts := strings.Split(m.probes[0], " or ")
	if len(parts) < 2 {
		t.Fatalf("the probe asks one question:\n  %s", m.probes[0])
	}
	for _, part := range parts {
		if !strings.HasPrefix(part, "label_replace(count(") || !strings.Contains(part, `"`+probeLabel+`"`) ||
			strings.Contains(part, "__name__") {
			t.Errorf("a sub-question is not told apart by the %s label:\n  %s", probeLabel, part)
		}
	}
}

// The cluster dashboard's zoom has no series selector, so it must not pay for
// one.
func TestTheClusterZoomStillAsksTwoQuestionsAndNoProbe(t *testing.T) {
	m := &recordingMetrics{answer: matrix(t, [2]any{1.0, "1"})}
	ZoomClusterHistory(context.Background(), m, "any", ZoomWindowFor("1h"))

	if len(m.queries) != 2 {
		t.Errorf("the cluster zoom asked %d range queries, want 2: %v", len(m.queries), m.queries)
	}
	if len(m.probes) != 0 {
		t.Errorf("the cluster zoom probed for counters it has no control for: %v", m.probes)
	}
}

// --- the crux ---------------------------------------------------------------------

// A cluster whose Prometheus has no cAdvisor network counters must say so, and
// must never draw an empty chart that reads as "this pod does no network I/O".
//
// An empty answer and a failed one are different answers, at their sharpest:
// a pod moving 40 MB/s drawn as a flat line at zero is true-looking,
// reassuring, and wrong.
func TestAnAbsentCounterIsNotReportedAsAnIdleObject(t *testing.T) {
	// The range queries all come back empty — which is exactly what they do when
	// the counter does not exist.
	m := &recordingMetrics{answer: matrix(t), probe: exported(t, metricCPU, metricMemory)}

	got := PodHistory(context.Background(), m, "any", "ns", "pod", WindowFor("1h"))

	for _, series := range []History{got.NetIn, got.NetOut} {
		if !strings.Contains(series.Unavailable, metricNetIn) && !strings.Contains(series.Unavailable, metricNetOut) {
			t.Errorf("%s says %q; it must name the counter this cluster does not export, "+
				"because an empty chart here reads as an idle pod", series.Title, series.Unavailable)
		}
		if strings.Contains(series.Unavailable, "no samples in this window") {
			t.Errorf("%s blames the window for a counter the cluster does not have: %q",
				series.Title, series.Unavailable)
		}
	}
	if got.Support.Network.OK {
		t.Error("the network chart is offered on a cluster with no network counters")
	}
	if !strings.Contains(got.Support.Network.Detail, metricNetIn) {
		t.Errorf("the refusal does not name what is missing: %q", got.Support.Network.Detail)
	}
	if got.Support.Disk.OK {
		t.Error("the filesystem chart is offered on a cluster with no filesystem counter")
	}
}

// The probe has to ask for the counter *and* the dimension the query needs.
//
// Asking whether `container_fs_usage_bytes` exists is the wrong question for a
// pod. On an EKS cluster it does exist — as twelve machine-level series under
// id="/", each one a node's root disk — so a bare-name probe answers "yes", the
// filesystem control is offered, and the chart behind it comes back with
// nought samples: the "empty reads as idle" failure the probe exists to
// prevent, reintroduced one level above it.
func TestTheProbeAsksForTheDimensionAndNotJustTheName(t *testing.T) {
	pod := &recordingMetrics{answer: matrix(t), probe: everyCounter(t)}
	PodHistory(context.Background(), pod, "any", "ns", "pod", WindowFor("1h"), ChartDisk)
	if len(pod.probes) != 1 {
		t.Fatalf("probes = %v", pod.probes)
	}
	if !strings.Contains(pod.probes[0], metricDisk+`{container!=""}`) {
		t.Errorf("a pod's filesystem probe asks about the bare counter, so a cluster reporting only "+
			"machine filesystems answers yes and the chart is offered empty:\n  %s", pod.probes[0])
	}

	node := &recordingMetrics{answer: matrix(t), probe: everyCounter(t)}
	NodeHistory(context.Background(), node, "any", "ip-10-1", WindowFor("1h"), ChartDisk)
	if len(node.probes) != 1 {
		t.Fatalf("probes = %v", node.probes)
	}
	if !strings.Contains(node.probes[0], metricDisk+`{id="/"}`) {
		t.Errorf("a node's filesystem probe does not ask for the machine's own filesystem:\n  %s", node.probes[0])
	}
}

// And the behavior that follows: a counter present only in a shape this object
// cannot use is refused, with a sentence saying which shape is missing.
func TestACounterInTheWrongShapeIsRefusedRatherThanOffered(t *testing.T) {
	// The probe's filesystem sub-expression matches nothing, because the cluster
	// only has machine-level series. Its network sub-expressions match.
	m := &recordingMetrics{answer: matrix(t), probe: exported(t, metricNetIn, metricNetOut)}

	got := PodHistory(context.Background(), m, "any", "ns", "pod", WindowFor("1h"))

	if got.Support.Disk.OK {
		t.Error("the filesystem chart was offered to a pod on a cluster that only measures whole machines")
	}
	if !strings.Contains(got.Support.Disk.Detail, "per-container") {
		t.Errorf("the refusal is %q; it should say what is missing is the per-container series, "+
			"because the counter itself is there and the operator will go looking for it",
			got.Support.Disk.Detail)
	}
	if !strings.Contains(got.Disk.Unavailable, "per-container") {
		t.Errorf("the chart says %q rather than what happened", got.Disk.Unavailable)
	}
	if !got.Support.Network.OK {
		t.Errorf("the network chart was refused along with it: %q", got.Support.Network.Detail)
	}
}

// And the other half of the same rule: when the counter *is* there and the
// window is empty, that is a fact about the object and must not be dressed up as
// a missing feature.
func TestAnEmptyWindowOnACounterThatExistsBlamesTheWindow(t *testing.T) {
	m := &recordingMetrics{answer: matrix(t), probe: everyCounter(t)}

	got := PodHistory(context.Background(), m, "any", "ns", "pod", WindowFor("7d"))

	if !strings.Contains(got.NetIn.Unavailable, "no samples in this window") {
		t.Errorf("Network in says %q; the counter exists, so the window is the answer", got.NetIn.Unavailable)
	}
	if !got.Support.Network.OK {
		t.Errorf("the network chart was refused although the counters are there: %q", got.Support.Network.Detail)
	}
}

// A probe that could not be run is not a cluster without counters. Graying the
// control out over it would hide a chart that works.
func TestAProbeThatFailedIsNotAnAbsentCounter(t *testing.T) {
	m := &recordingMetrics{answer: matrix(t, [2]any{1.0, "5"}), probeErr: errors.New("Prometheus refused the probe")}

	got := PodHistory(context.Background(), m, "any", "ns", "pod", WindowFor("1h"))

	if got.Support.Network.OK {
		t.Error("a failed probe was read as a working cluster")
	}
	if !strings.Contains(got.Support.Network.Detail, "could not establish") {
		t.Errorf("the reason is %q; it should say the question could not be answered, not that the answer was no",
			got.Support.Network.Detail)
	}
	// And the series itself keeps its samples: the probe decides what to offer,
	// never what to draw.
	if len(got.NetIn.Points) == 0 {
		t.Error("a failed probe blanked a series that Prometheus had answered")
	}
}

// --- the failure matrix over the new fill -----------------------------------------

func TestOneSeriesFailingDoesNotBlankTheOthers(t *testing.T) {
	m := &failingFirst{answer: matrix(t, [2]any{1.0, "0.5"}), probe: everyCounter(t)}

	got := PodHistory(context.Background(), m, "any", "ns", "pod", WindowFor("1h"))

	drawn := 0
	for _, series := range []History{got.CPU, got.Memory, got.NetIn, got.NetOut, got.Disk} {
		if len(series.Points) > 0 {
			drawn++
		}
	}
	if drawn != len(AllCharts) {
		t.Errorf("%d of %d series drew; one query failing blanked the others",
			drawn, len(AllCharts)+1)
	}
}

func TestEverySeriesFailingIsOneSentenceBecauseItIsOneCause(t *testing.T) {
	m := &recordingMetrics{err: errors.New("dial tcp 10.0.0.1:9090: connect: connection refused"), probe: everyCounter(t)}

	got := PodHistory(context.Background(), m, "any", "ns", "pod", WindowFor("1h"))

	for _, series := range []History{got.CPU, got.Memory, got.NetIn, got.NetOut, got.Disk} {
		if !strings.Contains(series.Unavailable, "connection refused") {
			t.Errorf("%s says %q, want the one cause", series.Title, series.Unavailable)
		}
	}
}

// The failure travels with the line, unflattened,
// so internal/web classifies it with stateOf instead of calling every failed
// query `cluster_unreachable`. Both paths: every series failing on one cause,
// and one series failing among answers.
func TestAFailedSeriesCarriesItsError(t *testing.T) {
	cause := errors.New("dial tcp 10.0.0.1:9090: connect: connection refused")
	all := PodHistory(context.Background(), &recordingMetrics{err: cause, probe: everyCounter(t)},
		"any", "ns", "pod", WindowFor("1h"))
	for _, series := range []History{all.CPU, all.Memory, all.NetIn, all.NetOut, all.Disk} {
		if series.Missing != MissingFailed || !errors.Is(series.Err, cause) {
			t.Errorf("%s: missing %v, err %v; want MissingFailed carrying the cause", series.Title, series.Missing, series.Err)
		}
	}

	one := PodHistory(context.Background(), &failingFirst{answer: matrix(t, [2]any{1.0, "0.5"}), probe: everyCounter(t)},
		"any", "ns", "pod", WindowFor("1h"))
	failed := 0
	for _, series := range []History{one.CPU, one.Memory, one.NetIn, one.NetOut, one.Disk} {
		switch {
		case series.Missing == MissingFailed && series.Err != nil:
			failed++
		case series.Err != nil:
			t.Errorf("%s carries an error and is %v; only a failed line carries one", series.Title, series.Missing)
		}
	}
	if failed != 1 {
		t.Errorf("%d lines carry their failure; want the one whose query was refused", failed)
	}
}

// One series full and one empty, on a cluster holding both counters: the empty
// one says the window held nothing and the panel as a whole says nothing at all,
// because there is a chart to look at.
func TestOneFullAndOneEmptyKeepsThePanelSilent(t *testing.T) {
	m := &emptyNetwork{full: matrix(t, [2]any{1.0, "0.5"}), empty: matrix(t), probe: everyCounter(t)}

	got := PodHistory(context.Background(), m, "any", "ns", "pod", WindowFor("1h"))

	if len(got.CPU.Points) == 0 {
		t.Fatal("the series that answered did not draw")
	}
	// The empty one keeps `Unavailable` empty, which is the documented way this
	// package says "the backend answered and the window was genuinely empty" — a
	// caller that cannot tell that from a failure has lost the distinction the
	// whole field exists for. The sentence the reader gets comes from the chart.
	if got.NetIn.Unavailable != "" {
		t.Errorf("Network in says %q; the counter is there and Prometheus answered, so this is not "+
			"unavailable", got.NetIn.Unavailable)
	}
	if drawn := string(got.Chart(ChartNetwork)); !strings.Contains(drawn, StateNoMetrics) {
		t.Errorf("the network chart of a pod with no traffic does not say so:\n  %s", drawn)
	}
}

func TestEverySeriesEmptyIsExplainedRatherThanDrawn(t *testing.T) {
	m := &recordingMetrics{answer: matrix(t), probe: everyCounter(t)}

	got := PodHistory(context.Background(), m, "any", "ns", "pod", WindowFor("7d"))

	if got.Drawable() {
		t.Error("an empty answer produced points")
	}
	for _, series := range []History{got.CPU, got.Memory, got.NetIn, got.NetOut, got.Disk} {
		if !strings.Contains(series.Unavailable, "no samples") {
			t.Errorf("%s says %q; it should say the window held nothing", series.Title, series.Unavailable)
		}
	}
}

// failingFirst refuses the first range query and answers the rest, whichever
// order they arrive in.
//
// The counter is guarded for the same reason recordingMetrics' slice is: the
// range queries run in their own goroutines, so `calls++` is a genuine race
// under -race.
type failingFirst struct {
	mu     sync.Mutex
	answer []byte
	probe  []byte
	calls  int
}

func (f *failingFirst) Query(context.Context, string, string) ([]byte, error) { return f.probe, nil }
func (f *failingFirst) QueryRange(context.Context, string, string, time.Time, time.Time, time.Duration) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.calls == 1 {
		return nil, errors.New("Prometheus refused the first query")
	}
	return f.answer, nil
}

// emptyNetwork answers the network questions with an empty window and everything
// else with samples, which is the shape a pod with no traffic on a fully
// instrumented cluster has.
type emptyNetwork struct {
	mu    sync.Mutex
	full  []byte
	empty []byte
	probe []byte
}

func (e *emptyNetwork) Query(context.Context, string, string) ([]byte, error) { return e.probe, nil }
func (e *emptyNetwork) QueryRange(_ context.Context, _, promQL string, _, _ time.Time, _ time.Duration) ([]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if strings.Contains(promQL, "container_network_") {
		return e.empty, nil
	}
	return e.full, nil
}

// --- the node's own questions ------------------------------------------------------

// A node's usage is not the sum of its containers: everything running outside
// one — the kubelet, the runtime, the operating system — is invisible to that
// sum, and on a busy node that is not a rounding error. That limitation is
// inherent to cAdvisor and accepted; what this pins is that no
// node query narrows itself to one pod.
func TestANodesQueryIsAboutTheNodeAndNotOnePod(t *testing.T) {
	m := &recordingMetrics{answer: matrix(t, [2]any{1.0, "3"}), probe: everyCounter(t)}

	NodeHistory(context.Background(), m, "any", "ip-10-40-11-65", WindowFor("1h"))

	if len(m.queries) == 0 {
		t.Fatal("a node asked nothing")
	}
	for _, q := range m.queries {
		if !strings.Contains(q, `node="ip-10-40-11-65"`) {
			t.Errorf("the query does not name the node:\n  %s", q)
		}
		// `pod!=""` is how the machine-level rollup is kept out of a node's
		// network sum, so a node query may mention pods. It may never name one.
		if strings.Contains(q, `pod="`) {
			t.Errorf("a node query narrowed itself to a single pod:\n  %s", q)
		}
	}
}

// The machine's name is not in a label cAdvisor emits: `node` is added by
// whatever scrapes kubelet, and on some clusters it is not: on an EKS
// cluster `container_cpu_usage_seconds_total{node="<a real node>",container!=""}`
// answers zero series, so a node-label query alone draws "no samples in this
// window" over running machines. Such a cluster carries the name in
// `instance`.
//
// So every node query is asked both ways and joined with `or`, which keeps the
// left-hand answer and takes the right only where the left has none.
func TestANodeIsFoundByEitherLabelItsNameArrivesIn(t *testing.T) {
	m := &recordingMetrics{answer: matrix(t, [2]any{1.0, "3"}), probe: everyCounter(t)}

	NodeHistory(context.Background(), m, "any", "ip-10-40-11-65.eu-west-3.compute.internal", WindowFor("1h"))

	for _, q := range m.queries {
		if !strings.Contains(q, " or ") {
			t.Errorf("the query has no fallback, so it is empty on a cluster that labels cAdvisor "+
				"with instance rather than node:\n  %s", q)
		}
		if !strings.Contains(q, `instance=~`) {
			t.Errorf("the fallback does not match on instance:\n  %s", q)
		}
		// Every dot in a node's name is a regex wildcard until it is escaped, and
		// an unanchored near-match on a chart is not diagnosable.
		if !strings.Contains(q, `ip-10-40-11-65\\.eu-west-3\\.compute\\.internal`) {
			t.Errorf("the node's name is not escaped for a regex matcher:\n  %s", q)
		}
	}
}

// A node's filesystem series is the machine's root disk, and cAdvisor reports
// one series per mounted filesystem. The set observed on an EKS node was
// /dev/nvme0n1p1, /dev/shm, /run and /tmp — the last three memory-backed. Summing
// them puts tmpfs in a disk figure.
func TestANodesFilesystemQueryLeavesTmpfsOut(t *testing.T) {
	m := &recordingMetrics{answer: matrix(t, [2]any{1.0, "3"}), probe: everyCounter(t)}

	NodeHistory(context.Background(), m, "any", "ip-10-40-11-65", WindowFor("1h"), ChartDisk)

	if len(m.queries) != 1 {
		t.Fatalf("asked %d questions for one chart: %v", len(m.queries), m.queries)
	}
	q := m.queries[0]
	if !strings.Contains(q, `id="/"`) {
		t.Errorf("the node's filesystem query does not select the machine:\n  %s", q)
	}
	if !strings.Contains(q, `device!="/dev/shm"`) {
		t.Errorf("the node's filesystem query counts tmpfs as disk:\n  %s", q)
	}
}

// A node's network chart is the sum of its pods' traffic, and it is titled that
// way. The node's own uplink counter is node-exporter's and is not present on
// the k8s nodes of an EKS cluster; cAdvisor's host-namespace series counts one veth
// per pod alongside the uplink, so summing it counts most packets twice.
func TestANodesNetworkChartSaysItIsThePods(t *testing.T) {
	m := &recordingMetrics{answer: matrix(t, [2]any{1.0, "3"}), probe: everyCounter(t)}

	got := NodeHistory(context.Background(), m, "any", "ip-10-40-11-65", WindowFor("1h"), ChartNetwork)

	if !strings.Contains(strings.ToLower(got.NetIn.Title), "pod") {
		t.Errorf("the node's network series is titled %q; it is the sum of the pods on the node "+
			"and not the machine's interface, and a chart named something it is not is worse than no chart",
			got.NetIn.Title)
	}
	for _, q := range m.queries {
		if !strings.Contains(q, `pod!=""`) {
			t.Errorf("the machine-level rollup is not excluded, so most packets are counted twice:\n  %s", q)
		}
	}
}

// A pod's filesystem chart is the container filesystem and says so. What an
// operator means by a pod's disk usage is almost always a mounted volume, which
// this metric does not measure.
func TestAPodsFilesystemChartIsNotCalledDisk(t *testing.T) {
	m := &recordingMetrics{answer: matrix(t, [2]any{1.0, "3"}), probe: everyCounter(t)}

	got := PodHistory(context.Background(), m, "any", "ns", "pod", WindowFor("1h"), ChartDisk)

	if !strings.Contains(strings.ToLower(got.Disk.Title), "container") {
		t.Errorf("a pod's filesystem series is titled %q; cAdvisor measures the container filesystem "+
			"and not the volumes mounted into it", got.Disk.Title)
	}
}

// --- the selector's model ------------------------------------------------------------

func TestOptionsOffersEveryChartAndRefusesWithAReason(t *testing.T) {
	m := &recordingMetrics{answer: matrix(t, [2]any{1.0, "3"}), probe: exported(t, metricCPU, metricMemory, metricNetIn, metricNetOut)}

	options := PodHistory(context.Background(), m, "any", "ns", "pod", WindowFor("1h")).Options()

	if len(options) != len(AllCharts) {
		t.Fatalf("%d options for %d charts", len(options), len(AllCharts))
	}
	byKey := map[string]ChartOption{}
	for _, o := range options {
		byKey[o.Key] = o
		if o.Label == "" {
			t.Errorf("%s has no label", o.Key)
		}
		if !o.Available && o.Detail == "" {
			t.Errorf("%s is refused with nothing saying why; a grayed control with no reason is the "+
				"defect this struct exists to prevent", o.Key)
		}
	}
	if !byKey[ChartNetwork].Available {
		t.Errorf("the network chart was refused on a cluster exporting both counters: %q", byKey[ChartNetwork].Detail)
	}
	if byKey[ChartDisk].Available {
		t.Error("the filesystem chart was offered on a cluster with no filesystem counter")
	}
	if byKey[ChartNetwork].Label != "Network" {
		t.Errorf("the network chart is labeled %q, want the name of the chart rather than of one of its lines",
			byKey[ChartNetwork].Label)
	}
}

// Half a network chart is not a network chart: a cluster exporting receive and
// not transmit must not be shown a picture implying nothing was ever sent.
func TestHalfTheNetworkCountersIsNotANetworkChart(t *testing.T) {
	m := &recordingMetrics{answer: matrix(t, [2]any{1.0, "3"}), probe: exported(t, metricCPU, metricMemory, metricNetIn)}

	got := PodHistory(context.Background(), m, "any", "ns", "pod", WindowFor("1h"))

	if got.Support.Network.OK {
		t.Error("the network chart is offered with only one of its two counters")
	}
	if !strings.Contains(got.Support.Network.Detail, metricNetOut) {
		t.Errorf("the refusal does not name the missing counter: %q", got.Support.Network.Detail)
	}
}

func TestChartDrawsWhatItWasAskedForAndSaysSoWhenItCannot(t *testing.T) {
	m := &recordingMetrics{answer: matrix(t, [2]any{1.0, "3"}, [2]any{2.0, "4"}), probe: everyCounter(t)}
	got := PodHistory(context.Background(), m, "any", "ns", "pod", WindowFor("1h"))

	for _, c := range []struct{ key, want string }{
		{ChartCPU, "CPU"},
		{ChartMemory, "Memory"},
		{ChartNetwork, "Network in"},
		{ChartDisk, "Container filesystem"},
	} {
		svg := string(got.Chart(c.key))
		if !strings.Contains(svg, c.want) {
			t.Errorf("Chart(%q) does not mention %q:\n  %s", c.key, c.want, svg)
		}
		if !strings.Contains(svg, "<svg") {
			t.Errorf("Chart(%q) drew no chart", c.key)
		}
	}
	// The network chart is both lines, on one picture.
	network := string(got.Chart(ChartNetwork))
	if !strings.Contains(network, "Network out") {
		t.Error("the network chart draws only one of its two series")
	}

	unknown := string(got.Chart("bandwidth"))
	if !strings.Contains(unknown, "no chart called") {
		t.Errorf("an unrecognized chart key drew something rather than saying what happened:\n  %s", unknown)
	}
}

// --- the rest -------------------------------------------------

// The step is not a preference. A point per minute over seven days is ten
// thousand samples nobody can see and Prometheus has to compute.
func TestEveryWindowAsksForANumberOfPointsAChartCanShow(t *testing.T) {
	for _, w := range Windows {
		points := int(w.Range / w.Step)
		if points < 40 || points > 250 {
			t.Errorf("%s asks for %d points at a step of %s; a chart cannot show that many "+
				"and Prometheus has to compute them", w.Label, points, w.Step)
		}
	}
}

func TestTheWindowReachesTheQuery(t *testing.T) {
	for _, label := range []string{"15m", "6h", "7d"} {
		m := &recordingMetrics{answer: matrix(t, [2]any{1.0, "1"}), probe: everyCounter(t)}
		want := WindowFor(label)

		PodHistory(context.Background(), m, "any", "ns", "pod", want)

		if len(m.windows) == 0 {
			t.Fatalf("%s asked nothing", label)
		}
		// Allow a second of slack: the range is measured from time.Now().
		if diff := m.windows[0] - want.Range; diff > time.Second || diff < -time.Second {
			t.Errorf("%s asked for a %s window, want %s", label, m.windows[0], want.Range)
		}
		if m.steps[0] != want.Step {
			t.Errorf("%s asked for a step of %s, want %s", label, m.steps[0], want.Step)
		}
	}
}

func TestAnUnknownWindowFallsBackRatherThanFailing(t *testing.T) {
	// A stale bookmark or a hand-edited URL. Neither is worth an error page.
	for _, label := range []string{"", "42y", "; DROP TABLE", "1H"} {
		got := WindowFor(label)
		if got.Range == 0 || got.Step == 0 {
			t.Errorf("%q produced an unusable window %+v", label, got)
		}
	}
	if WindowFor("1H").Label != "1h" {
		t.Error("the window lookup is case-sensitive; a capital H should still be an hour")
	}
}

// Prometheus counts CPU in cores and every table in this console counts
// millicores. Mixing them is how a chart reads 0.4 next to a row saying 400m —
// and the scaling must reach CPU and nothing else, because bytes and bytes per
// second are bytes on both sides.
func TestCPUIsConvertedToTheUnitTheTablesUseAndNothingElseIs(t *testing.T) {
	m := &recordingMetrics{answer: matrix(t, [2]any{1.0, "0.25"}, [2]any{2.0, "0.5"}), probe: everyCounter(t)}

	got := PodHistory(context.Background(), m, "any", "ns", "pod", WindowFor("1h"))

	if len(got.CPU.Points) != 2 {
		t.Fatalf("got %d points", len(got.CPU.Points))
	}
	if got.CPU.Points[0].Value != 250 || got.CPU.Points[1].Value != 500 {
		t.Errorf("got %v millicores, want [250 500]",
			[]float64{got.CPU.Points[0].Value, got.CPU.Points[1].Value})
	}
	for _, series := range []History{got.Memory, got.NetIn, got.NetOut, got.Disk} {
		if len(series.Points) > 0 && series.Points[0].Value != 0.25 {
			t.Errorf("%s was scaled as though it were cores: got %v, want 0.25",
				series.Title, series.Points[0].Value)
		}
	}
}

// No metrics backend is the ordinary state of most clusters. It is a fact about
// the cluster, not a failure of this panel, and it must arrive as a sentence —
// on every series and on every control, not only on CPU and memory.
func TestNoBackendIsExplainedRatherThanFailing(t *testing.T) {
	got := PodHistory(context.Background(), nil, "any", "ns", "pod", WindowFor("1h"))

	if got.Drawable() {
		t.Error("a nil backend produced points")
	}
	if got.NoHistory == "" {
		t.Error("nothing explains the empty panel; a chart-shaped blank reads as a broken chart")
	}
	for _, series := range []History{got.CPU, got.Memory, got.NetIn, got.NetOut, got.Disk} {
		if series.Unavailable == "" {
			t.Errorf("%q carries no reason", series.Title)
		}
	}
	for _, o := range got.Options() {
		if o.Detail == "" {
			t.Errorf("%s carries no reason", o.Key)
		}
	}
}

func TestAValueIsQuotedIntoTheQueryRatherThanConcatenated(t *testing.T) {
	// Kubernetes names cannot contain these, so this is not defending against a
	// live injection — it is defending against the day a label selector or an
	// image name is passed in instead.
	got := promQuote(`we"ird\value`)
	if got != `"we\"ird\\value"` {
		t.Errorf("got %s", got)
	}
	if !strings.HasPrefix(promQuote("plain"), `"`) || !strings.HasSuffix(promQuote("plain"), `"`) {
		t.Error("a plain value came back unquoted")
	}
}

func TestARegexMatcherEscapesTheValueItMatches(t *testing.T) {
	if got := promRegexEscape("ip-10-1.eu-west-3.internal"); got != `ip-10-1\.eu-west-3\.internal` {
		t.Errorf("got %s; every dot in a node's name is a wildcard until it is escaped", got)
	}
	if got := promRegexEscape("plain-name"); got != "plain-name" {
		t.Errorf("got %s; a name with nothing to escape must come back as itself", got)
	}
}

func TestDrawableIsFalseUntilThereIsSomethingToDraw(t *testing.T) {
	if (ObjectHistory{}).Drawable() {
		t.Error("an empty history claimed to be drawable")
	}
	withPoints := ObjectHistory{CPU: History{Points: []Point{{Value: 1}}}}
	if !withPoints.Drawable() {
		t.Error("a history with points claimed not to be drawable")
	}
	// Still CPU-or-memory on purpose: a pod whose only readable series is its
	// container filesystem has no usage picture, and callers ask this whether
	// there is one.
	onlyDisk := ObjectHistory{Disk: History{Points: []Point{{Value: 1}}}}
	if onlyDisk.Drawable() {
		t.Error("Drawable changed meaning; the templates asking it want a usage picture")
	}
}

// Every aggregation this file performs deduplicates first.
//
// A cluster carrying two kube-prometheus-stack releases stores every cAdvisor
// series twice — same job, same instance, same cgroup id, differing only in
// `service` — so a bare `sum()` returns exactly double. Measured on such a
// cluster: a pod read 54m from metrics-server and 123.6m from the chart's own
// query, 61.8m once deduplicated.
//
// Checked per query rather than by counting occurrences, because the failure
// this guards against is one query being copied without the wrapper, and a
// count would still pass with the wrapper on the wrong nine.
func TestEveryObjectQueryDeduplicatesBeforeSumming(t *testing.T) {
	pod := &recordingMetrics{answer: matrix(t, [2]any{1.0, "1"}), probe: everyCounter(t)}
	PodHistory(context.Background(), pod, "any", "ns", "pod", WindowFor("1h"), AllCharts...)
	node := &recordingMetrics{answer: matrix(t, [2]any{1.0, "1"}), probe: everyCounter(t)}
	NodeHistory(context.Background(), node, "any", "kubeworker2", WindowFor("1h"), AllCharts...)

	queries := append(append([]string{}, pod.queries...), node.queries...)
	if len(queries) != 10 {
		t.Fatalf("expected 5 pod and 5 node queries, got %d: %v", len(queries), queries)
	}
	for _, q := range queries {
		if !strings.Contains(q, "avg without ("+scrapeLabels+") (") {
			t.Errorf("a query sums series that were never deduplicated, so a cluster with two "+
				"kubelet scrape targets reads double:\n  %s", q)
		}
		// The wrapper has to be *inside* the sum. Outside it, it averages
		// nothing and the double survives.
		if !strings.HasPrefix(q, "sum(avg without") && !strings.HasPrefix(q, "sum by") {
			t.Errorf("the deduplication is not inside the aggregation:\n  %s", q)
		}
	}
}

// The same rule is written here and in internal/metrics, because this package
// takes a Metrics interface rather than importing that one. Two statements can
// drift; this is what stops them doing it in silence. See the comment on
// scrapeLabels.
func TestDedupIsOneRule(t *testing.T) {
	if scrapeLabels != metrics.ScrapeLabels {
		t.Errorf("the chart queries and the column queries deduplicate on different labels,\n"+
			"  so a cluster can read right in one place and double in the other:\n"+
			"  internal/overview: %q\n  internal/metrics:  %q", scrapeLabels, metrics.ScrapeLabels)
	}
	if got, want := dedup("X"), metrics.Dedup("X"); got != want {
		t.Errorf("dedup wrappers differ:\n  internal/overview: %q\n  internal/metrics:  %q", got, want)
	}
}

// "There is no Prometheus here" and "Prometheus did
// not answer" both arrive as an error from every query, and only the first is a
// cluster on which no span of history can be chosen. The metrics layer marks it
// with a method, which this package recognizes without importing it.
type noPrometheus struct{}

func (noPrometheus) Error() string {
	return "prod has metrics-server and no Prometheus, so it holds no history to query"
}
func (noPrometheus) NoHistory() bool { return true }

func TestNoPrometheusIsToldApartFromAFailedOne(t *testing.T) {
	for _, tc := range []struct {
		name      string
		err       error
		missing   Missing
		noHistory bool
	}{
		{"no Prometheus", fmt.Errorf("wrapped: %w", noPrometheus{}), MissingBackend, true},
		{"a Prometheus that refused", errors.New("connection refused"), MissingFailed, false},
	} {
		got := PodHistory(context.Background(), &recordingMetrics{err: tc.err}, "prod", "ns", "pod", WindowFor("1h"))
		if (got.NoHistory != "") != tc.noHistory {
			t.Errorf("%s: NoHistory = %q", tc.name, got.NoHistory)
		}
		for _, series := range []History{got.CPU, got.Memory, got.NetIn, got.NetOut, got.Disk} {
			if series.Missing != tc.missing {
				t.Errorf("%s: %s is Missing=%d; want %d", tc.name, series.Title, series.Missing, tc.missing)
			}
		}
		// And CPU and memory are refused with the sentence, never offered empty.
		for _, option := range got.Options()[:2] {
			if option.Available || option.Detail == "" {
				t.Errorf("%s: %s is %+v; want refused with a reason", tc.name, option.Key, option)
			}
		}
	}

	if got := PodHistory(context.Background(), nil, "prod", "ns", "pod", WindowFor("1h")); got.NoHistory == "" || got.CPU.Missing != MissingBackend {
		t.Errorf("no backend configured: NoHistory=%q cpu.Missing=%d; want both to say there is no Prometheus", got.NoHistory, got.CPU.Missing)
	}
}

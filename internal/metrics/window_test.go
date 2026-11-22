package metrics

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// The usage window: what is asked for, how one answer is taken apart, and the
// states a page has to be able to tell apart. The engine's own use of the
// window - pooling, P95, the coverage sentence - is the extension's, and tested
// there.
//
// The fixtures are shaped on answers taken from a live kube-prometheus-stack:
// the per-pod aggregate row with no `container` label, the value quoted as a
// string, and the duplicate scrape are all things that cluster returned.

// fakeProm answers whatever it was given, and records what it was asked.
//
// The recording is under a mutex because ReadWindow sends its two range queries
// from two goroutines on purpose; an unguarded append in the double is a data
// race in the double.
type fakeProm struct {
	count    string
	cpu      string
	memory   string
	countErr error
	cpuErr   error
	memErr   error
	// failOn fails every range query whose selector names it: one batch.
	failOn string
	// hold, when set, keeps every range query waiting until it is closed.
	hold chan struct{}

	mu          sync.Mutex
	asked       []string
	steps       []time.Duration
	spans       []time.Duration
	inFlight    int
	maxInFlight int
}

// Query answers the listing: count when it was given, otherwise every
// container the two matrices hold, as Prometheus would list them.
func (f *fakeProm) Query(_ context.Context, _, promQL string) ([]byte, error) {
	f.mu.Lock()
	f.asked = append(f.asked, promQL)
	f.mu.Unlock()
	if f.countErr != nil {
		return nil, f.countErr
	}
	if f.count != "" {
		return []byte(f.count), nil
	}
	var keys []string
	for _, m := range []string{f.cpu, f.memory} {
		series, err := parseSeries([]byte(m), 1)
		if err != nil {
			continue
		}
		for k := range series {
			keys = append(keys, k)
		}
	}
	return []byte(listing(keys...)), nil
}

func (f *fakeProm) QueryRange(_ context.Context, _, promQL string, start, end time.Time, step time.Duration) ([]byte, error) {
	f.mu.Lock()
	f.asked = append(f.asked, promQL)
	f.steps = append(f.steps, step)
	f.spans = append(f.spans, end.Sub(start))
	f.inFlight++
	f.maxInFlight = max(f.maxInFlight, f.inFlight)
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.inFlight--
		f.mu.Unlock()
	}()
	time.Sleep(time.Millisecond) // long enough for the batches in flight to overlap
	if f.hold != nil {
		<-f.hold
	}
	if !end.After(start) {
		return nil, errors.New("the window ends before it starts")
	}
	if f.failOn != "" && strings.Contains(promQL, f.failOn) {
		return nil, errors.New("query timed out")
	}
	if strings.Contains(promQL, "container_cpu_usage_seconds_total") {
		if f.cpuErr != nil {
			return nil, f.cpuErr
		}
		return []byte(f.cpu), nil
	}
	if f.memErr != nil {
		return nil, f.memErr
	}
	return []byte(f.memory), nil
}

func (f *fakeProm) asking() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.asked...)
}

// countOf is a listing of n containers, prod/web-<i>/app.
func countOf(n int) string {
	keys := make([]string, n)
	for i := range keys {
		keys[i] = ContainerKey("prod", fmt.Sprintf("web-%d", i), "app")
	}
	return listing(keys...)
}

// listing is the answer to the container listing for these keys.
func listing(keys ...string) string {
	rows := make([]string, 0, len(keys))
	for _, k := range keys {
		parts := strings.SplitN(k, "/", 3)
		rows = append(rows, fmt.Sprintf(`{"metric":{"namespace":%q,"pod":%q,"container":%q},"value":[1788294085.360,"1"]}`,
			parts[0], parts[1], parts[2]))
	}
	return `{"status":"success","data":{"resultType":"vector","result":[` + strings.Join(rows, ",") + `]}}`
}

type promRow struct {
	namespace, pod, container string
	values                    []float64
}

func matrix(rows ...promRow) string {
	series := make([]string, 0, len(rows))
	for _, r := range rows {
		samples := make([]string, 0, len(r.values))
		for i, v := range r.values {
			samples = append(samples, fmt.Sprintf(`[%d,"%g"]`, 1788200000+i*120, v))
		}
		labels := fmt.Sprintf(`"namespace":%q,"pod":%q`, r.namespace, r.pod)
		if r.container != "" {
			labels += fmt.Sprintf(`,"container":%q`, r.container)
		}
		series = append(series, fmt.Sprintf(`{"metric":{%s},"values":[%s]}`, labels, strings.Join(samples, ",")))
	}
	return fmt.Sprintf(`{"status":"success","data":{"resultType":"matrix","result":[%s]}}`,
		strings.Join(series, ","))
}

func flat(n int, v float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func readWindow(t *testing.T, q RangeQuerier, namespace string) Window {
	t.Helper()
	w, err := ReadWindow(context.Background(), q, "c", namespace)
	if err != nil {
		t.Fatalf("ReadWindow(%q): %v", namespace, err)
	}
	return w
}

// --- what is asked for ------------------------------------------------------

// cAdvisor's per-pod aggregate row carries no `container` label and sums the
// pod's containers. Guarded twice on purpose: the selector asks Prometheus not to
// send it, and the parser drops one that arrives anyway.
func TestTheAggregateRowPerPodIsExcludedFromTheWindow(t *testing.T) {
	t.Run("every query asks Prometheus to leave it out", func(t *testing.T) {
		for name, q := range map[string]string{"CPU": queryCPUSeries, "memory": queryMemorySeries, "count": queryContainers} {
			if !strings.Contains(q, `container!=""`) {
				t.Errorf("the %s query does not exclude the per-pod aggregate row: %s", name, q)
			}
			if !strings.Contains(q, `container!="POD"`) {
				t.Errorf("the %s query does not exclude the pause container: %s", name, q)
			}
		}
	})

	t.Run("one that arrives anyway is dropped rather than filed under no name", func(t *testing.T) {
		got, err := parseSeries([]byte(matrix(
			promRow{"prod", "web-0", "", flat(3, 500)},
			promRow{"prod", "web-0", "app", flat(3, 300)},
			promRow{"prod", "web-0", "envoy", flat(3, 200)},
		)), 1)
		if err != nil {
			t.Fatalf("parseSeries: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("parsed %d series, want 2 - the per-pod aggregate was kept", len(got))
		}
		for key := range got {
			if strings.HasSuffix(key, "/") {
				t.Errorf("a series is filed under an empty container name (%q)", key)
			}
		}
	})
}

// The window and step are what the sentence names and what the contract
// publishes, so they have to be the ones actually asked for.
func TestTheWindowAskedForIsTheWindowTheNoteNames(t *testing.T) {
	prom := &fakeProm{
		count:  countOf(2),
		cpu:    matrix(promRow{"prod", "web-0", "app", flat(180, 0.1)}),
		memory: matrix(promRow{"prod", "web-0", "app", flat(180, 64<<20)}),
	}
	w := readWindow(t, prom, "")
	if w.State != WindowRead {
		t.Fatalf("no window was read from a Prometheus that answered: %s %s", w.State, w.Detail)
	}
	prom.mu.Lock()
	steps, spans := append([]time.Duration(nil), prom.steps...), append([]time.Duration(nil), prom.spans...)
	prom.mu.Unlock()
	if len(steps) != 2 {
		t.Fatalf("%d range queries, want 2 (CPU and memory)", len(steps))
	}
	for i := range steps {
		if steps[i] != WindowStep || spans[i] != WindowSpan {
			t.Errorf("a range query asked for %s at %s; the window is %s at %s", spans[i], steps[i], WindowSpan, WindowStep)
		}
	}
	if !strings.Contains(w.Detail, "7d") || !strings.Contains(w.Detail, "15m") {
		t.Errorf("the detail does not name the window and the step: %q", w.Detail)
	}
	rated := false
	for _, q := range prom.asking() {
		if strings.Contains(q, "["+windowRate+"]") {
			rated = true
		}
	}
	if !rated {
		t.Errorf("no query rates the CPU counter over %s: %v", windowRate, prom.asking())
	}
	// Memory is sized on its maximum, so a point is the largest working set of
	// its own step: a peak between two samples must not fall through.
	peaked := false
	for _, q := range prom.asking() {
		if strings.Contains(q, "container_memory_working_set_bytes") &&
			strings.Contains(q, "max_over_time(") && strings.Contains(q, "["+short(WindowStep)+"]") {
			peaked = true
		}
	}
	if !peaked {
		t.Errorf("no memory query takes the maximum over each step: %v", prom.asking())
	}
}

// --- taking one answer apart ------------------------------------------------

func TestOneAnswerIsSplitBackIntoOneWindowPerContainer(t *testing.T) {
	w := readWindow(t, &fakeProm{
		cpu: matrix(
			promRow{"prod", "web-0", "app", flat(180, 0.25)},
			promRow{"prod", "web-0", "envoy", flat(180, 0.05)},
			promRow{"staging", "web-0", "app", flat(180, 0.01)},
		),
		memory: matrix(
			promRow{"prod", "web-0", "app", flat(180, 512<<20)},
			promRow{"prod", "web-0", "envoy", flat(180, 32<<20)},
			promRow{"staging", "web-0", "app", flat(180, 8<<20)},
		),
	}, "")

	if len(w.CPU) != 3 || len(w.Memory) != 3 {
		t.Fatalf("got %d CPU and %d memory windows from three series each, want 3", len(w.CPU), len(w.Memory))
	}
	// Two namespaces, one pod name: the case where a key without the namespace
	// hands one workload the other's window.
	prod, staging := w.CPU[ContainerKey("prod", "web-0", "app")], w.CPU[ContainerKey("staging", "web-0", "app")]
	if len(prod) == 0 || len(staging) == 0 {
		t.Fatalf("one of the two web-0 windows is missing: prod=%d staging=%d", len(prod), len(staging))
	}
	if prod[0].V == staging[0].V {
		t.Errorf("prod and staging web-0 have the same CPU window (%g)", prod[0].V)
	}
	// Cores in, millicores out.
	if prod[0].V != 250 {
		t.Errorf("CPU = %g, want 250 millicores", prod[0].V)
	}
	// Memory is bytes and is not scaled.
	if got := w.Memory[ContainerKey("prod", "web-0", "app")][0].V; got != float64(512<<20) {
		t.Errorf("memory = %g, want %d bytes unscaled", got, 512<<20)
	}
}

// Two kubelet ServiceMonitors scrape every container twice. Two series for one
// container must not become one whose timestamps go backwards halfway through.
func TestADuplicateScrapeDoesNotBecomeOneWindowTwice(t *testing.T) {
	t.Run("the query collapses them", func(t *testing.T) {
		for name, q := range map[string]string{"CPU": queryCPUSeries, "memory": queryMemorySeries} {
			if !strings.Contains(q, "max by (namespace, pod, container)") {
				t.Errorf("the %s query does not aggregate away the scrape's own labels: %s", name, q)
			}
		}
	})
	t.Run("one that arrives anyway is not concatenated", func(t *testing.T) {
		got, err := parseSeries([]byte(matrix(
			promRow{"prod", "web-0", "app", flat(180, 100)},
			promRow{"prod", "web-0", "app", flat(90, 100)},
		)), 1)
		if err != nil {
			t.Fatalf("parseSeries: %v", err)
		}
		if n := len(got[ContainerKey("prod", "web-0", "app")]); n != 180 {
			t.Fatalf("the container's window is %d points, want 180", n)
		}
	})
}

// A gap is a gap. A NaN read as zero claims the container used nothing.
func TestAMissingSampleIsAGapAndNeverAZero(t *testing.T) {
	raw := `{"status":"success","data":{"resultType":"matrix","result":[{"metric":` +
		`{"namespace":"prod","pod":"web-0","container":"app"},"values":[` +
		`[1788200000,"100"],[1788200120,"NaN"],[1788200240,"+Inf"],[1788200360,"-Inf"],[1788200480,"120"]]}]}}`
	got, err := parseSeries([]byte(raw), 1)
	if err != nil {
		t.Fatalf("parseSeries: %v", err)
	}
	points := got[ContainerKey("prod", "web-0", "app")]
	if len(points) != 2 {
		t.Fatalf("kept %d points, want the 2 real ones: %v", len(points), points)
	}
	for _, p := range points {
		if p.V == 0 {
			t.Fatalf("a NaN or an infinity came through as a zero: %v", points)
		}
	}
}

// --- the states --------------------------------------------------------------

// "No Prometheus", "could not ask" and "Prometheus said nothing" are different
// failures, and none of them is a window: each is a state of its own rather
// than one "no window" sentence.
func TestTheHistoryNoteSaysWhichModeThePageIsIn(t *testing.T) {
	cases := []struct {
		name     string
		q        RangeQuerier
		ns       string
		want     WindowState
		contains string
	}{
		{"no Prometheus configured", nil, "", WindowNoPrometheus, "no Prometheus configured"},
		{"a cluster that runs none", &fakeProm{countErr: NoHistory{Sentence: "c has metrics-server and no Prometheus"}},
			"", WindowNoPrometheus, "no Prometheus"},
		{"a Prometheus that could not be asked", &fakeProm{countErr: errors.New("c could not be asked (unauthorized)")},
			"", WindowUnreadable, "could not be asked"},
		{"no container series at all", &fakeProm{count: countOf(0)}, "", WindowEmpty,
			"no series carried both a pod and a container label"},
		{"nothing in one namespace", &fakeProm{count: countOf(0)}, "argocd", WindowEmpty,
			"no container series for namespace argocd"},
		{"both window queries refused", &fakeProm{count: countOf(2), cpuErr: errors.New("boom"), memErr: errors.New("boom")},
			"", WindowUnreadable, "boom"},
		{"both window queries answered with nothing", &fakeProm{count: countOf(2), cpu: matrix(), memory: matrix()},
			"", WindowEmpty, "no usable series"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := readWindow(t, tc.q, tc.ns)
			if w.State != tc.want {
				t.Errorf("state = %s, want %s (%q)", w.State, tc.want, w.Detail)
			}
			if !strings.Contains(w.Detail, tc.contains) {
				t.Errorf("detail %q does not say %q", w.Detail, tc.contains)
			}
			if w.State != WindowRead && (len(w.CPU) != 0 || len(w.Memory) != 0) {
				t.Errorf("a %s answer carries series", w.State)
			}
		})
	}
}

// What the absence costs - "no P95, no burst ratio, no trend" - is the page's
// sentence, because the core does not know what the window is for. What the
// core owes for each of these inputs is a state that is not WindowRead, a
// reason that is not empty, and no series; a bad namespace is the request's
// error, not a state.
func TestEveryStateWithoutAWindowSaysWhatThatCosts(t *testing.T) {
	for name, q := range map[string]struct {
		q  RangeQuerier
		ns string
	}{
		"no Prometheus":              {nil, ""},
		"no series cluster-wide":     {&fakeProm{count: countOf(0)}, ""},
		"no series in one namespace": {&fakeProm{count: countOf(0)}, "argocd"},
		"count refused":              {&fakeProm{countErr: errors.New("refused")}, ""},
		"an empty answer":            {&fakeProm{count: countOf(2), cpu: matrix(), memory: matrix()}, ""},
	} {
		w := readWindow(t, q.q, q.ns)
		if w.State == WindowRead || w.Detail == "" || len(w.CPU) != 0 || len(w.Memory) != 0 {
			t.Errorf("%s: state %s, detail %q, %d+%d series - want a reason and no window",
				name, w.State, w.Detail, len(w.CPU), len(w.Memory))
		}
	}
	if _, err := ReadWindow(context.Background(), &fakeProm{count: countOf(2)}, "c", "Not A Namespace"); err == nil {
		t.Error("a namespace that cannot be one was answered as a state rather than refused")
	}
}

// --- batches ----------------------------------------------------------------

// many is n containers in one namespace, each with a short series of each half.
func many(namespace string, n int) (cpu, memory string) {
	rows := make([]promRow, n)
	for i := range rows {
		rows[i] = promRow{namespace, fmt.Sprintf("p-%04d", i), "app", flat(3, 0.1)}
	}
	cpu = matrix(rows...)
	for i := range rows {
		rows[i].values = flat(3, 1<<26)
	}
	return cpu, matrix(rows...)
}

// No scope is too large for a window any more: past WindowBatch containers it
// is read in batches, at most windowInFlight at once, and assembled.
func TestALargeScopeIsReadInBatchesAndAssembled(t *testing.T) {
	cpu, memory := many("prod", 1200)
	prom := &fakeProm{cpu: cpu, memory: memory}
	w := readWindow(t, prom, "")
	if w.State != WindowRead {
		t.Fatalf("state = %s %q, want a window", w.State, w.Detail)
	}
	if w.Batches != 3 || len(w.CPU) != 1200 || len(w.Memory) != 1200 {
		t.Errorf("%d batches, %d CPU and %d memory series; want 3 batches and 1200 of each", w.Batches, len(w.CPU), len(w.Memory))
	}
	if !strings.Contains(w.Detail, "1200 containers read in 3 batches") || w.Partial != 0 {
		t.Errorf("detail %q, partial %d", w.Detail, w.Partial)
	}
	ranged := 0
	for _, q := range prom.asking() {
		if strings.Contains(q, "pod=~") {
			ranged++
			if pods := strings.Count(q, "p-"); pods > WindowBatch {
				t.Errorf("one query names %d pods, past the batch of %d", pods, WindowBatch)
			}
		}
	}
	if ranged != 6 {
		t.Errorf("%d range queries, want two per batch", ranged)
	}
	if prom.maxInFlight > 2*windowInFlight {
		t.Errorf("%d range queries in flight at once, past %d", prom.maxInFlight, 2*windowInFlight)
	}

	t.Run("and the state that declined a scope is gone", func(t *testing.T) {
		cpu, memory := many("prod", 9000)
		w := readWindow(t, &fakeProm{cpu: cpu, memory: memory}, "")
		if w.State != WindowRead || w.Batches != 18 || len(w.CPU) != 9000 {
			t.Errorf("9000 containers: %s in %d batches, %d series", w.State, w.Batches, len(w.CPU))
		}
		if strings.Contains(w.Detail, "narrow") {
			t.Errorf("the detail still asks to narrow: %q", w.Detail)
		}
	})
}

// A batch that fails is a part of the window missing, said as such - never a
// window missing, and never an empty one.
func TestOneFailedBatchIsAPartialWindowThatSaysSo(t *testing.T) {
	cpu, memory := many("prod", 1200)
	prom := &fakeProm{cpu: cpu, memory: memory, failOn: `"p-0500|`}
	w := readWindow(t, prom, "")
	if w.State != WindowRead {
		t.Fatalf("one failed batch cost the whole window: %s %q", w.State, w.Detail)
	}
	if w.Partial != 1 || len(w.CPU) != 700 || len(w.Memory) != 700 {
		t.Errorf("partial %d, %d CPU and %d memory series; want 1 and 700", w.Partial, len(w.CPU), len(w.Memory))
	}
	if !strings.Contains(w.Detail, "1 of 3 batches could not be read") || !strings.Contains(w.PartialNote, "query timed out") {
		t.Errorf("the partial window is not said: %q / %q", w.Detail, w.PartialNote)
	}
	if w.CPUErr != nil || w.MemoryErr != nil {
		t.Error("one batch failing was read as a whole half failing")
	}
}

// keep narrows the listing before anything is read: the series of a namespace
// the person may not list are never asked for.
func TestANarrowedReadNeverAsksForAnotherNamespace(t *testing.T) {
	prom := &fakeProm{
		cpu:    matrix(promRow{"mine", "p-0000", "app", flat(3, 0.1)}, promRow{"theirs", "x-0", "app", flat(3, 9)}),
		memory: matrix(promRow{"mine", "p-0000", "app", flat(3, 1)}, promRow{"theirs", "x-0", "app", flat(3, 9)}),
	}
	keep := func(keys []string) (map[string]bool, error) {
		out := map[string]bool{}
		for _, k := range keys {
			out[k] = strings.HasPrefix(k, "mine/")
		}
		return out, nil
	}
	w, err := ReadWindowKept(context.Background(), prom, "c", "", keep)
	if err != nil || w.State != WindowRead {
		t.Fatalf("%v %s %q", err, w.State, w.Detail)
	}
	for _, q := range prom.asking() {
		if strings.Contains(q, "pod=~") && strings.Contains(q, "theirs") {
			t.Errorf("a range query names a namespace the person may not list: %s", q)
		}
	}
	for k := range w.Memory {
		if !strings.HasPrefix(k, "mine/") {
			t.Errorf("%s reached a narrowed person", k)
		}
	}

	nothing := func([]string) (map[string]bool, error) { return map[string]bool{}, nil }
	if w, _ := ReadWindowKept(context.Background(), prom, "c", "", nothing); w.State != WindowEmpty ||
		!strings.Contains(w.Detail, "namespaces where you may list pods") {
		t.Errorf("nothing of theirs: %s %q", w.State, w.Detail)
	}
}

// A name that is not a Kubernetes name is never written into a selector, and a
// dot is escaped so it matches only itself.
func TestABatchSelectorNamesOnlyKubernetesNames(t *testing.T) {
	got := batchesOf([]string{"prod/web.v2-0/app", `prod/x"} or up{/app`, "prod/web-1/app"})
	if len(got) != 1 || len(got[0].keys) != 2 {
		t.Fatalf("batches = %+v", got)
	}
	if want := `pod=~"web-1|web\\.v2-0"`; !strings.Contains(got[0].selector, want) {
		t.Errorf("selector %s, want it to hold %s", got[0].selector, want)
	}
}

// A namespace is a DNS-1123 label; anything else builds no query.
func TestANamespaceThatCannotBeOneBuildsNoQuery(t *testing.T) {
	for _, bad := range []string{`kube-system"} or up{`, "UPPER", "has space", "semi;colon", strings.Repeat("a", 254)} {
		prom := &fakeProm{count: countOf(2)}
		if _, err := ReadWindow(context.Background(), prom, "c", bad); !errors.Is(err, ErrBadNamespace) {
			t.Errorf("%q: err = %v, want ErrBadNamespace", bad, err)
		}
		if asked := prom.asking(); len(asked) != 0 {
			t.Errorf("%q reached a query: %v", bad, asked)
		}
	}
	prom := &fakeProm{count: countOf(0)}
	readWindow(t, prom, "kube-system")
	if asked := prom.asking(); len(asked) == 0 || !strings.Contains(asked[0], `namespace="kube-system"`) {
		t.Errorf("a valid namespace did not narrow the query: %v", asked)
	}
}

// --- half a window ---------------------------------------------------------

// One query failing is not the other's problem, and the reader has to see which
// half is which.
func TestOneHalfOfTheWindowFailingKeepsTheOther(t *testing.T) {
	for _, tc := range []struct {
		name         string
		prom         *fakeProm
		says         string
		cpu, memory  bool
		cpuFailed    bool
		memoryFailed bool
	}{
		{"CPU fails", &fakeProm{count: countOf(1), cpuErr: errors.New("query timed out"),
			memory: matrix(promRow{"prod", "web-0", "app", flat(180, 1<<26)})},
			"memory only", false, true, true, false},
		{"memory fails", &fakeProm{count: countOf(1), memErr: errors.New("query timed out"),
			cpu: matrix(promRow{"prod", "web-0", "app", flat(180, 0.1)})},
			"CPU only", true, false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := readWindow(t, tc.prom, "")
			if w.State != WindowRead {
				t.Fatalf("one half was thrown away because the other failed: %s %q", w.State, w.Detail)
			}
			if !strings.Contains(w.Detail, tc.says) || !strings.Contains(w.Detail, "query timed out") {
				t.Errorf("the detail does not say which half is windowed: %q", w.Detail)
			}
			key := ContainerKey("prod", "web-0", "app")
			if (len(w.CPU[key]) > 0) != tc.cpu || (len(w.Memory[key]) > 0) != tc.memory {
				t.Errorf("CPU=%d memory=%d points", len(w.CPU[key]), len(w.Memory[key]))
			}
			if (w.CPUErr != nil) != tc.cpuFailed || (w.MemoryErr != nil) != tc.memoryFailed {
				t.Errorf("CPUErr=%v MemoryErr=%v", w.CPUErr, w.MemoryErr)
			}
		})
	}
}

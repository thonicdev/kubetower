package metrics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"
)

// Reading a usage window per container out of the cluster's Prometheus.
//
// This serves `extension.Host.UsageWindow`. It lives in the core because
// **Prometheus has no RBAC**:
// an extension handed the raw `Query` and `QueryRange` could read any series for
// anyone, so the queries are fixed here, the answer is typed, and the caller
// narrows it to the pods the person may list (internal/web, hostwindow.go).
//
// What stays in the extension is what the window is *for* - pooling replicas, a
// P95, the burst guard, and the sentence saying whether the page is fully or
// partly windowed. That depends on how many containers the page shows, which
// only the page knows.

// RangeQuerier is the slice of the metrics layer a window needs: PromQL, instant
// and over a range. `*Metrics` satisfies it, and so does internal/web's Series.
// A nil value is valid and means this installation has no Prometheus at all.
type RangeQuerier interface {
	Query(ctx context.Context, cluster, promQL string) ([]byte, error)
	QueryRange(ctx context.Context, cluster, promQL string, start, end time.Time, step time.Duration) ([]byte, error)
}

// The window, the step, and what they cost. Each is a trade, and
// the reasoning is kept with the numbers.
const (
	// WindowSpan is seven days. A request sized on a few hours never saw a
	// workload busiest at night or on Mondays, and the reduction it proposed
	// was the next week's throttle or eviction. Seven days holds every daily
	// peak and one of each weekday. The cost is a workload resized during the
	// week still arguing partly from its old size, which the page's
	// confidence level says by naming how much of the week each figure covers.
	WindowSpan = 7 * 24 * time.Hour

	// WindowStep is fifteen minutes: 673 points per series. The step sets
	// the size of the answer, not the precision of what is kept:
	//
	//   - CPU is sampled, one five-minute rate per step. A P95 is a statement
	//     about a distribution, and 672 samples spread evenly over a week
	//     estimate it as well as ten times as many would;
	//   - memory is sized on its maximum, which sampling would miss between two
	//     steps, so each point is the maximum over its own fifteen minutes
	//     (max_over_time below) and no peak falls through the gaps.
	//
	// Five minutes would triple the answer for a P95 that barely moves; thirty
	// would leave a day with 48 points and a start excluded for half an hour.
	WindowStep = 15 * time.Minute

	// WindowBatch is how many containers one pair of range queries asks for.
	// A CPU point is about 34 bytes of JSON - a whole-second timestamp and a
	// rate printed to seventeen digits - so a series of 673 is about 23 kB,
	// and memory's are shorter: 500 containers is about 12 MB per answer,
	// inside this package's 16 MB answer cap with room for longer numbers. A
	// scope holding more is read in as many batches as it takes and the
	// answers assembled (batch.go): there is no size past which a page goes
	// without a window. It also bounds what Prometheus loads per query: a
	// week of raw samples at a 30 s scrape is about 20 000 per series, 10
	// million for a batch, under its default limit of 50 million.
	WindowBatch = 500

	// windowRate is what rate() averages a CPU counter over. Five minutes rather
	// than the step, because a fifteen-minute average flattens the bursts a P95
	// is there to see, and rate() over fewer than four scrape intervals returns
	// nothing on a cluster scraping every 60 s. A twenty-second spike is
	// smoothed to roughly a fifth of its height; bursts lasting minutes survive.
	windowRate = "5m"
)

// The three questions, written against cAdvisor's metric names because the
// kubelet exposes them everywhere - kube-state-metrics and node-exporter are not.
//
//   - container!="" excludes cAdvisor's per-pod aggregate row, which carries no
//     container label and sums the pod's containers: kept, it would file a pod's
//     whole usage under a container named "" and count multi-container pods
//     twice.
//   - container!="POD" excludes the pause container, which older cAdvisor labels
//     that way instead of dropping the label.
//   - max by (namespace, pod, container) collapses duplicate scrapes. Two Helm
//     releases of kube-prometheus-stack each install a kubelet ServiceMonitor, so
//     every container is scraped twice; without the aggregation one container
//     arrives as two series. max rather than avg, because a scrape that missed a
//     sample must not halve the value.
const (
	queryCPUSeries    = "max by (namespace, pod, container) (rate(container_cpu_usage_seconds_total{container!=\"\",container!=\"POD\"%s}[%s]))"
	queryMemorySeries = "max by (namespace, pod, container) (max_over_time(container_memory_working_set_bytes{container!=\"\",container!=\"POD\"%s}[%s]))"
	// queryContainers lists every container with a series in the window, not
	// only the running ones: a workload's former pods are read too, so its
	// week survives a rollout.
	queryContainers = "group by (namespace, pod, container) (last_over_time(container_memory_working_set_bytes{container!=\"\",container!=\"POD\"%s}[%s]))"
)

// WindowState is which of the states a window read landed in.
//
// Four and not two, because "no Prometheus", "a Prometheus that could not be
// asked" and "a Prometheus with nothing to say" are three different sentences
// for three different people, and rendering any of them as an empty window is
// the failure an empty answer and a failed one being different exists to
// prevent. A window read in part is a read, and says which part.
type WindowState string

// The window states. The public contract repeats them (extension.WindowState);
// internal/web translates one to the other.
const (
	WindowNoPrometheus WindowState = "no-prometheus"
	WindowUnreadable   WindowState = "unreadable"
	WindowEmpty        WindowState = "empty"
	WindowRead         WindowState = "read"
)

// Point is one sample: T in Unix seconds, V in the series' unit.
type Point struct {
	T float64
	V float64
}

// Window is one read. CPU and Memory are keyed ContainerKey and set only in
// WindowRead; CPU is in millicores, memory in bytes.
type Window struct {
	State WindowState
	// Detail is the sentence for the state: the reason when there is no window,
	// and "Prometheus, 7d at 15m" - with which half failed, if one did - when
	// there is one.
	Detail string
	// Count is how many containers the listing found and the read was for.
	Count  int
	CPU    map[string][]Point
	Memory map[string][]Point
	// CPUErr and MemoryErr are one half failing, in every batch, while the
	// other was read. Both failing is WindowUnreadable.
	CPUErr    error
	MemoryErr error
	// Batches is how many batches the window was read in. Partial is how many
	// of them could not be read in full, and PartialNote says so in one
	// sentence: the containers in them have no window, or half of one, while
	// the rest of the page has its own.
	Batches     int
	Partial     int
	PartialNote string
	// ReadAt is when Prometheus answered this window. A window kept by the
	// cache carries the time of its read, not of the display that was handed
	// it, so the page can say how old it is. Zero when nothing was asked, or
	// when the answer came from a kept listing alone.
	ReadAt time.Time
}

// ErrBadNamespace is a namespace that cannot be one. Refused rather than
// escaped: a namespace is a DNS-1123 label, so anything else did not come from a
// cluster and has no business being interpolated into a query.
var ErrBadNamespace = errors.New("not a namespace name")

// ReadWindow asks a cluster's Prometheus for seven days of CPU and memory per
// container, at a fifteen-minute step, or says why there is none.
//
// Its only error is a namespace that cannot be one, which is the caller's bug
// and builds no query. Everything about Prometheus is a state: no Prometheus,
// could not ask, nothing, or a window - whole, or in part and saying so.
func ReadWindow(ctx context.Context, q RangeQuerier, cluster, namespace string) (Window, error) {
	return ReadWindowKept(ctx, q, cluster, namespace, nil)
}

// ReadWindowKept is ReadWindow for a person who may not see every container
// the listing finds: keep answers which of the listed keys are theirs, and only
// those are read, so the series of a namespace they may not list are never
// asked for. Nil keeps them all. Its error is ReadWindow's, or keep's.
func ReadWindowKept(ctx context.Context, q RangeQuerier, cluster, namespace string,
	keep func(keys []string) (map[string]bool, error)) (Window, error) {
	if w, done, err := unasked(q, namespace); done {
		return w, err
	}
	// One budget for the listing and every batch: see windowBudget.
	ctx, cancel := context.WithTimeout(ctx, windowBudget)
	defer cancel()
	keys, err := listContainers(ctx, q, cluster, namespace)
	if err != nil {
		w := failedWindow(err)
		w.ReadAt = time.Now()
		return w, nil
	}
	kept, narrowed, err := narrow(keys, keep)
	if err != nil {
		return Window{}, err
	}
	if len(kept) == 0 {
		w := emptyWindow(namespace, narrowed)
		w.ReadAt = time.Now()
		return w, nil
	}
	w := readKeys(ctx, q, cluster, kept)
	w.ReadAt = time.Now()
	return w, nil
}

// unasked is the answer that needs no Prometheus: a namespace that cannot be
// one, which is the caller's error, or no Prometheus at all. done is false
// when there is something to ask.
func unasked(q RangeQuerier, namespace string) (w Window, done bool, err error) {
	if _, ok := namespaceSelector(namespace); !ok {
		return Window{}, true, fmt.Errorf("%w: %q, so no query was built from it", ErrBadNamespace, namespace)
	}
	if q == nil {
		return Window{State: WindowNoPrometheus, Detail: "this installation has no Prometheus configured"}, true, nil
	}
	return Window{}, false, nil
}

// listContainers is what there is to read, before it is paid for: every
// container with a series in the window, running or gone. The namespace has
// been checked (unasked).
func listContainers(ctx context.Context, q RangeQuerier, cluster, namespace string) ([]string, error) {
	selector, _ := namespaceSelector(namespace)
	return containerKeys(ctx, q, cluster, selector)
}

// narrow keeps the keys keep allows, in a new slice, and says whether any was
// left out. Nil keeps them all.
func narrow(keys []string, keep func(keys []string) (map[string]bool, error)) (kept []string, narrowed bool, err error) {
	if keep == nil || len(keys) == 0 {
		return keys, false, nil
	}
	allowed, err := keep(slices.Clone(keys))
	if err != nil {
		return nil, false, err
	}
	for _, k := range keys {
		if allowed[k] {
			kept = append(kept, k)
		}
	}
	return kept, len(kept) < len(keys), nil
}

// emptyWindow is a listing with nothing to read in it.
//
// Two different facts. Cluster-wide, "no container series at all" is a
// statement about the label schema; about one namespace, or the namespaces
// one person may list, it is far more likely that they hold nothing, and
// telling that operator their monitoring is broken would send them to fix
// something that is not.
func emptyWindow(namespace string, narrowed bool) Window {
	switch {
	case narrowed:
		return Window{State: WindowEmpty, Detail: "Prometheus has no container series in the namespaces where you may list pods"}
	case namespace != "":
		return Window{State: WindowEmpty, Detail: fmt.Sprintf(
			"Prometheus has no container series for namespace %s", namespace)}
	}
	return Window{State: WindowEmpty, Detail: "Prometheus answered, but no series carried both a pod " +
		"and a container label - this cluster's cAdvisor metrics are not where KubeTower looks for them"}
}

// readKeys reads the window of these containers.
//
// The end on a step boundary: every point then lands on a whole minute, so a
// timestamp prints without a fraction (a few bytes per point, over 673 points
// per series) and two reads in one quarter of an hour ask the same question.
func readKeys(ctx context.Context, q RangeQuerier, cluster string, keys []string) Window {
	end := time.Now().Truncate(WindowStep)
	return readBatches(ctx, q, cluster, batchesOf(slices.Clone(keys)), end.Add(-WindowSpan), end, len(keys))
}

// failedWindow tells a cluster with no Prometheus from one that could not be
// asked. NoHistory is the pool's own answer for the first (prom.go).
func failedWindow(err error) Window {
	var none NoHistory
	if errors.As(err, &none) {
		return Window{State: WindowNoPrometheus, Detail: shortError(err)}
	}
	return Window{State: WindowUnreadable, Detail: shortError(err)}
}

// short renders a duration the way the sentence reads it: 7d, 6h, 15m. It is
// also how the memory query writes its range, which PromQL reads the same way.
func short(d time.Duration) string {
	switch {
	case d <= 0:
		return d.String()
	case d%(24*time.Hour) == 0:
		return strconv.FormatInt(int64(d/(24*time.Hour)), 10) + "d"
	case d%time.Hour == 0:
		return strconv.FormatInt(int64(d/time.Hour), 10) + "h"
	case d%time.Minute == 0:
		return strconv.FormatInt(int64(d/time.Minute), 10) + "m"
	default:
		return d.String()
	}
}

// namespaceSelector renders the namespace half of the label matcher, or refuses.
// Empty means the whole cluster.
func namespaceSelector(namespace string) (string, bool) {
	if namespace == "" {
		return "", true
	}
	if len(namespace) > 253 {
		return "", false
	}
	for _, r := range namespace {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
		default:
			return "", false
		}
	}
	return ",namespace=\"" + namespace + "\"", true
}

func containerKeys(ctx context.Context, q RangeQuerier, cluster, selector string) ([]string, error) {
	raw, err := q.Query(ctx, cluster, fmt.Sprintf(queryContainers, selector, short(WindowSpan)))
	if err != nil {
		return nil, err
	}
	return parseKeys(raw)
}

// rangeSeries runs one range query and demultiplexes it per container. scale
// converts Prometheus's unit: CPU arrives in cores and the contract counts
// millicores.
func rangeSeries(ctx context.Context, q RangeQuerier, cluster, promQL string, start, end time.Time, scale float64) (map[string][]Point, error) {
	raw, err := q.QueryRange(ctx, cluster, promQL, start, end, WindowStep)
	if err != nil {
		return nil, err
	}
	return parseSeries(raw, scale)
}

type promMatrix struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Data   struct {
		Result []struct {
			Metric struct {
				Namespace string `json:"namespace"`
				Pod       string `json:"pod"`
				Container string `json:"container"`
			} `json:"metric"`
			Values samples `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

type promVector struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Data   struct {
		Result []struct {
			Value []any `json:"value"`
		} `json:"result"`
	} `json:"data"`
}

// parseSeries demultiplexes a matrix into points keyed ContainerKey.
//
// A series whose pod or container label is missing is dropped rather than filed
// under an empty name: the label schema is the cluster's, and a cluster whose
// cAdvisor carries the container somewhere else must degrade to "no window",
// never to a window filed against a container called "".
func parseSeries(raw []byte, scale float64) (map[string][]Point, error) {
	var resp promMatrix
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("Prometheus answered with something unreadable: %w", err)
	}
	if resp.Status != "success" {
		return nil, fmt.Errorf("Prometheus rejected the query: %s", resp.Error)
	}

	out := make(map[string][]Point, len(resp.Data.Result))
	for _, series := range resp.Data.Result {
		m := series.Metric
		if m.Pod == "" || m.Container == "" {
			continue
		}
		// The points are already in their own slice, gaps left out (samples).
		points := []Point(series.Values)
		for i := range points {
			points[i].V *= scale
		}
		if len(points) == 0 {
			continue
		}
		// Two series for one container is a duplicate scrape the max by already
		// collapses. If one arrives anyway the longer wins: concatenating them
		// would make timestamps go backwards halfway through.
		key := ContainerKey(m.Namespace, m.Pod, m.Container)
		if existing, seen := out[key]; seen && len(existing) >= len(points) {
			continue
		}
		out[key] = points
	}
	return out, nil
}

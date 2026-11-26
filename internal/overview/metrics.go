package overview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	restclient "k8s.io/client-go/rest"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/apiraw"
	"github.com/thonicdev/kubetower/internal/resources"
)

// The window the sparklines cover, and how finely: sixty points, about as
// many as a panel can distinguish.
const (
	historyWindow = time.Hour
	historyStep   = time.Minute
)

// The two questions the cluster page asks Prometheus, written against
// cAdvisor's metric names, which kubelet exposes everywhere.
//
// Both deduplicate before summing (dedup and scrapeLabels in object.go): a
// cluster whose kubelets are scraped twice by some targets reads up to double,
// and not by a clean factor at cluster scale, so nobody can spot it. A var
// because dedup is a function.
//
// The CPU query depends on the step: its rate looks back over rateRange(step),
// so a zoomed long window skips nothing between its points.
var (
	queryClusterCPU    = clusterCPUQuery(historyStep)
	queryClusterMemory = `sum(` + dedup(`container_memory_working_set_bytes{container!="",container!="POD"}`) + `)`
)

func clusterCPUQuery(step time.Duration) string {
	return `sum(` + dedup(`rate(container_cpu_usage_seconds_total{container!="",container!="POD"}[`+rateRange(step)+`])`) + `)`
}

// fromPodCgroups is the caption over a cluster history summed from the pods'
// own cgroups. It names the source because a pod's cgroup also counts what the
// runtime charges to the pod outside its containers: not the same figure.
const fromPodCgroups = "from pod cgroups: this Prometheus has no per-container series"

// The same two questions over the pods' own cgroups: the fallback where this
// Prometheus holds no series with a `container` label (cAdvisor behind
// cri-dockerd, as on docker-desktop, exports only pod and machine cgroups).
//
//   - `pod!=""` keeps out the machine's root cgroup and the system slices.
//   - `container=""` keeps out containers wherever they are labeled: the pod's
//     cgroup already counts them.
//   - `max by (namespace, pod)` keeps one series per pod where several cgroups
//     carry its labels: the pod's own is their parent and the largest. After
//     dedup, so a pod scraped twice is still one pod.
func clusterPodCgroupCPUQuery(step time.Duration) string {
	return `sum(max by (namespace, pod) (` + dedup(`rate(`+perPodCgroup(metricCPU)+`[`+rateRange(step)+`])`) + `))`
}

var queryPodCgroupMemory = `sum(max by (namespace, pod) (` + dedup(perPodCgroup(metricMemory)) + `))`

// perPodCgroup is the selector, and the probe, for a pod's own cgroup.
func perPodCgroup(metric string) string { return metric + `{pod!="",container=""}` }

// emptyClusterHistory is the two charts before anything has answered: their
// names, units and step, in one place so "CPU used" is spelled one way.
func emptyClusterHistory() (History, History) {
	return History{Title: "CPU used", Unit: UnitCores, Step: historyStep},
		History{Title: "Memory used", Unit: UnitBytes, Step: historyStep}
}

// clusterHistory fetches the two series the cluster page draws. metrics == nil
// is an ordinary configuration and returns no error: an unavailable
// Availability and two Histories carrying the reason.
//
// A backend that answered is available, and its Detail (the panel's caption)
// says what was read: the container sums, the pods' cgroups, or why a line has
// nothing to draw.
func clusterHistory(ctx context.Context, metrics Metrics, cluster string) (Availability, History, History) {
	cpu, memory := emptyClusterHistory()

	if metrics == nil {
		reason := "no metrics backend is configured, so there is no history to draw"
		cpu.Unavailable, memory.Unavailable = reason, reason
		return unavailable(reason), cpu, memory
	}

	out := clusterLines(ctx, metrics, cluster, Window{Label: "1h", Range: historyWindow, Step: historyStep}, cpu.Title, memory.Title)

	// Both failing is one cause, said once; one failing does not blank the
	// other.
	if failed(out.CPU) && failed(out.Memory) {
		return unavailable(out.Source), out.CPU, out.Memory
	}
	if out.Source == "" {
		return available("Prometheus answered"), out.CPU, out.Memory
	}
	return available(out.Source), out.CPU, out.Memory
}

// failed is a line whose range query did not answer, or a cluster with no
// Prometheus. A failed probe is not: its range query answered, empty.
func failed(h History) bool {
	var probe probeFailure
	if h.Missing == MissingFailed && errors.As(h.Err, &probe) {
		return false
	}
	return h.Missing == MissingFailed || h.Missing == MissingBackend
}

// probeFailure marks a line's error as its probe's rather than its range
// query's; it unwraps for internal/web's classifier.
type probeFailure struct{ error }

func (p probeFailure) Unwrap() error { return p.error }

// lineOutcome is what one cluster line turned out to be; each has exactly one
// sentence (see clusterLines).
type lineOutcome int

const (
	lineFromContainers lineOutcome = iota
	lineFromPods
	lineEmptyContainers
	lineEmptyPods
	lineNeither
	lineProbeFailed
	lineQueryFailed
)

// clusterLines is the cluster's CPU and memory over a window: the dashboard's
// hour, or the zoom's. Each line is resolved on its own, since a cluster can
// hold the two counters in different shapes. A line is exactly one of:
//
//   - drawn from the container sums, asked first and with no probe, so a
//     dashboard that draws costs Prometheus nothing extra per refresh;
//   - drawn from the pods' own cgroups, where the probe says there are no
//     per-container series;
//   - empty over container series that exist;
//   - empty over pod cgroup series that exist, where the container ones do not;
//   - neither shape present;
//   - a failed probe or a failed range query.
//
// The sentence is chosen once both outcomes are known: an empty window blames
// the object's age (emptyWindow) only where no line drew, and says
// loneEmptyWindow beside one that did; a failed probe is MissingFailed, never
// an empty window, since it established nothing.
//
// Source is "" where both lines drew from the container sums, fromPodCgroups
// where both drew from the pods, and otherwise each line's source or reason
// under its title.
func clusterLines(ctx context.Context, metrics Metrics, cluster string, window Window, cpuTitle, memoryTitle string) ObjectHistory {
	specs := []seriesSpec{
		{chart: ChartCPU, into: cpuOf, title: cpuTitle, unit: UnitCores, scale: 1000, metric: metricCPU, query: clusterCPUQuery(window.Step)},
		{chart: ChartMemory, into: memoryOf, title: memoryTitle, unit: UnitBytes, metric: metricMemory, query: queryClusterMemory},
	}
	// fill scales, titles and folds the range failures; what it says of an
	// empty line is replaced below.
	out := fill(ctx, metrics, cluster, ObjectHistory{Window: window}, specs, nil)

	outcomes := make([]lineOutcome, len(specs))
	errs := make([]error, len(specs))
	points := make([][]Point, len(specs))
	var tasks []task
	for i, spec := range specs {
		line := spec.into(&out)
		switch {
		case len(line.Points) > 0:
			outcomes[i] = lineFromContainers
		case line.Missing == MissingFailed || line.Missing == MissingBackend:
			outcomes[i] = lineQueryFailed
		default:
			// Until resolved: a panic in resolveEmptyLine leaves it here, with
			// the panic as its error.
			outcomes[i] = lineProbeFailed
			i, spec := i, spec
			tasks = append(tasks, guard(&errs[i], "resolving the "+spec.title+" line", func() {
				outcomes[i], points[i], errs[i] = resolveEmptyLine(ctx, metrics, cluster, window, spec)
			}))
		}
	}
	if len(tasks) == 0 {
		out.Source = clusterSource(out, specs, outcomes)
		return out
	}
	gather(tasks...)

	anyDrew := false
	for i := range specs {
		if outcomes[i] == lineFromContainers || outcomes[i] == lineFromPods {
			anyDrew = true
		}
	}

	for i, spec := range specs {
		line := spec.into(&out)
		if outcomes[i] == lineFromContainers || (outcomes[i] == lineQueryFailed && errs[i] == nil) {
			continue
		}
		line.Points, line.Unavailable, line.Missing, line.Err = nil, "", MissingNone, nil
		empty := emptyWindow
		if anyDrew {
			empty = loneEmptyWindow(spec.metric)
		}
		switch outcomes[i] {
		case lineFromPods:
			line.Points = points[i]
		case lineEmptyContainers:
			line.Unavailable, line.Missing = empty, MissingEmpty
		case lineEmptyPods:
			line.Unavailable, line.Missing = "from pod cgroups: "+empty, MissingEmpty
		case lineNeither:
			line.Unavailable, line.Missing = noShape(spec.metric), MissingCounter
		case lineProbeFailed:
			line.Unavailable, line.Missing, line.Err = "could not establish why this line is empty: "+
				firstLine(errs[i].Error()), MissingFailed, probeFailure{errs[i]}
		case lineQueryFailed:
			line.Unavailable, line.Missing, line.Err = firstLine(errs[i].Error()), MissingFailed, errs[i]
		}
	}
	out.Source = clusterSource(out, specs, outcomes)
	return out
}

// resolveEmptyLine establishes why a container sum came back empty, and reads
// the pods' cgroups where the container series do not exist. One probe per
// line, so one line's failed question does not decide the other's outcome.
func resolveEmptyLine(ctx context.Context, metrics Metrics, cluster string, window Window, spec seriesSpec) (lineOutcome, []Point, error) {
	known, err := probeMetrics(ctx, metrics, cluster, probeQuery([]seriesSpec{{probe: perContainer(spec.metric)}}))
	if err != nil {
		return lineProbeFailed, nil, fmt.Errorf("asking whether this Prometheus holds per-container %s series: %w", spec.metric, err)
	}
	if known[spec.metric] {
		return lineEmptyContainers, nil, nil
	}

	query := queryPodCgroupMemory
	if spec.chart == ChartCPU {
		query = clusterPodCgroupCPUQuery(window.Step)
	}
	end := time.Now()
	var points []Point
	var podKnown map[string]bool
	var rangeErr, probeErr error
	gather(
		guard(&rangeErr, "the pod cgroup "+spec.title+" query", func() {
			points, rangeErr = rangeQueryStep(ctx, metrics, cluster, query, end.Add(-window.Range), end, window.Step)
		}),
		guard(&probeErr, "the probe for pod cgroup "+spec.metric, func() {
			podKnown, probeErr = probeMetrics(ctx, metrics, cluster, probeQuery([]seriesSpec{{probe: perPodCgroup(spec.metric)}}))
		}),
	)
	if len(points) > 0 {
		if spec.scale != 0 && spec.scale != 1 {
			for i := range points {
				points[i].Value *= spec.scale
			}
		}
		return lineFromPods, points, nil
	}
	switch {
	case rangeErr != nil:
		return lineQueryFailed, nil, rangeErr
	case probeErr != nil:
		return lineProbeFailed, nil, fmt.Errorf("asking whether this Prometheus holds pod cgroup %s series: %w", spec.metric, probeErr)
	case podKnown[spec.metric]:
		return lineEmptyPods, nil, nil
	}
	return lineNeither, nil, nil
}

// loneEmptyWindow is an empty window said of one line where the other drew,
// which disproves emptyWindow's blaming of the object's age.
func loneEmptyWindow(metric string) string {
	return "no samples of " + metric + " in this window, although this Prometheus holds its series"
}

// noShape is a line with neither per-container nor pod cgroup series.
func noShape(metric string) string {
	return "this cluster's Prometheus has no per-container " + metric + " series and no pod cgroup's either — " +
		"cAdvisor here sees the machine, not its pods, so there is nothing to chart — " +
		"this is not a claim that the cluster is idle"
}

// clusterSource is the caption: each line's source where it is not the
// container sums, and each empty line's reason, under the line's title; said
// once where both lines share it.
func clusterSource(out ObjectHistory, specs []seriesSpec, outcomes []lineOutcome) string {
	if outcomes[0] == lineFromPods && outcomes[1] == lineFromPods {
		return fromPodCgroups
	}
	cpu, memory := specs[0].into(&out), specs[1].into(&out)
	if len(cpu.Points) == 0 && len(memory.Points) == 0 && cpu.Unavailable == memory.Unavailable {
		return cpu.Unavailable
	}
	var parts []string
	for i, spec := range specs {
		line := spec.into(&out)
		switch {
		case outcomes[i] == lineFromPods:
			parts = append(parts, line.Title+": from pod cgroups, this Prometheus has no per-container "+
				spec.metric+" series")
		case len(line.Points) == 0 && line.Unavailable != "":
			parts = append(parts, line.Title+": "+line.Unavailable)
		}
	}
	return strings.Join(parts, "; ")
}

// rangeQuery asks for a window at historyStep and returns the points as they
// came: never resampled, interpolated or smoothed, so six samples draw as six.
func rangeQuery(ctx context.Context, metrics Metrics, cluster, promQL string, start, end time.Time) ([]Point, error) {
	return rangeQueryStep(ctx, metrics, cluster, promQL, start, end, historyStep)
}

// rangeQueryStep is rangeQuery with the step chosen by the caller: windows run
// from fifteen minutes to a month, and one step cannot serve them all.
func rangeQueryStep(ctx context.Context, metrics Metrics, cluster, promQL string, start, end time.Time, step time.Duration) ([]Point, error) {
	raw, err := metrics.QueryRange(ctx, cluster, promQL, start, end, step)
	if err != nil {
		return nil, err
	}
	return parseRange(raw)
}

// promRange is the Prometheus range response, reduced to the points.
type promRange struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Data   struct {
		Result []struct {
			Values [][]any `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

// parseRange is split from the call so a Prometheus answer can be tested
// without a Prometheus.
func parseRange(raw []byte) ([]Point, error) {
	var resp promRange
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("Prometheus answered with something unreadable: %w", err)
	}
	if resp.Status != "success" {
		return nil, fmt.Errorf("Prometheus rejected the query: %s", resp.Error)
	}
	if len(resp.Data.Result) == 0 {
		// Not an error: "nothing matched" draws as "no data", which must
		// stay a different sentence from "Prometheus is unreachable".
		return nil, nil
	}

	// The queries aggregate with sum(), so there is one series; the first is
	// taken rather than merging several into a line no query reproduces.
	values := resp.Data.Result[0].Values
	points := make([]Point, 0, len(values))
	for _, v := range values {
		if len(v) < 2 {
			continue
		}
		at, ok := sampleTime(v[0])
		if !ok {
			continue
		}
		value, ok := sampleValue(v[1])
		if !ok {
			// NaN or ±Inf is dropped: a gap is honest, a zero is a claim.
			continue
		}
		points = append(points, Point{At: at, Value: value})
	}
	return points, nil
}

func sampleTime(v any) (time.Time, bool) {
	seconds, ok := v.(float64)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(int64(seconds), 0).UTC(), true
}

// sampleValue reads the quoted string Prometheus encodes every value as (so
// large counters survive), and rejects non-finite values: strconv.ParseFloat
// accepts "NaN" and "±Inf" without error, and Prometheus writes gaps and
// divisions by zero that way. Anywhere a sample becomes arithmetic, test
// math.IsNaN and math.IsInf explicitly.
func sampleValue(v any) (float64, bool) {
	s, ok := v.(string)
	if !ok {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

// --- instant usage, from metrics-server ----------------------------------------

// PodUsage is one pod's current consumption.
type PodUsage struct {
	Namespace   string
	Name        string
	CPUMilli    int64
	MemoryBytes int64
}

func (p PodUsage) CPUText() string    { return formatCores(p.CPUMilli) }
func (p PodUsage) MemoryText() string { return formatBytes(p.MemoryBytes) }

// Top is a leaderboard, or the reason there isn't one.
type Top struct {
	Pods []PodUsage
	// Unavailable is set when no backend could answer. Empty with no pods
	// means the backend answered and had nothing.
	Unavailable string
}

// topPods is how many rows a "hottest pods" panel carries.
const topPods = 5

// podUsage reads metrics-server through the cluster's raw REST client, which
// avoids a second client library for one aggregated API. It is reviewed as
// `list pods.metrics.k8s.io` (what `kubectl top pods` needs), narrowed to the
// namespaces the person may list.
func podUsage(ctx context.Context, r resources.Reader, namespace string) ([]PodUsage, access.Visible, Availability) {
	if r.Clients == nil {
		return nil, access.Visible{}, unavailable("this cluster's client cannot make raw API requests")
	}
	discovery := r.Clients.Discovery()
	if discovery == nil {
		return nil, access.Visible{}, unavailable("this cluster's client cannot make raw API requests")
	}
	rest := discovery.RESTClient()
	if rest == nil {
		// The tests' fake client, or a client with no REST config: no usage
		// figures, said so, not an error.
		return nil, access.Visible{}, unavailable("this cluster's client cannot make raw API requests, so metrics-server cannot be read")
	}

	path := "/apis/metrics.k8s.io/v1beta1/pods"
	if namespace != "" {
		path = "/apis/metrics.k8s.io/v1beta1/namespaces/" + namespace + "/pods"
	}

	question := access.Attributes{Verb: "list", Group: "metrics.k8s.io", Version: "v1beta1", Resource: "pods", Namespace: namespace}
	usage, visible, err := access.List(ctx, r.Gate, r.Cluster, question,
		func(p *PodUsage) string { return p.Namespace },
		func(ctx context.Context) ([]PodUsage, error) {
			raw, err := getRaw(ctx, rest, path)
			if err != nil {
				// Commonly metrics-server is not installed: say what did not
				// answer. %w, so the board is classified by client-go's status.
				return nil, fmt.Errorf("metrics-server did not answer: %w", err)
			}
			return parseUsage(raw)
		})
	if err != nil {
		return nil, visible, failedWith(err, "pods.metrics.k8s.io")
	}
	return usage, visible, available("metrics-server answered")
}

// maxRawRead bounds one raw read: a metrics.k8s.io pod entry is well under
// 1 KiB, so this holds tens of thousands of pods and still refuses a broken
// answer.
var maxRawRead int64 = 64 << 20

// errRawTooLarge is what an answer over maxRawRead is refused with.
var errRawTooLarge = fmt.Errorf("the answer is larger than %d MiB, which the console does not read", maxRawRead>>20)

// getRaw is one GET through the raw REST client, its body undecoded and capped
// at maxRawRead, its failure in the API server's own words (internal/apiraw),
// so a 403 reads the same as in internal/metrics and internal/kubeletstats.
func getRaw(ctx context.Context, rest restclient.Interface, path string) ([]byte, error) {
	raw, err := apiraw.DoCapped(ctx, rest.Get().AbsPath(path), maxRawRead, errRawTooLarge)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// usageList is what metrics.k8s.io answers with, reduced to what is read.
type usageList struct {
	Items []struct {
		Metadata struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"metadata"`
		Containers []struct {
			Usage map[string]string `json:"usage"`
		} `json:"containers"`
	} `json:"items"`
}

func parseUsage(raw []byte) ([]PodUsage, error) {
	var list usageList
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("metrics-server answered with something unreadable: %w", err)
	}

	out := make([]PodUsage, 0, len(list.Items))
	for _, item := range list.Items {
		// A pod's usage is the sum of its containers'.
		var pod PodUsage
		pod.Namespace, pod.Name = item.Metadata.Namespace, item.Metadata.Name
		for _, c := range item.Containers {
			pod.CPUMilli += milliCores(c.Usage["cpu"])
			pod.MemoryBytes += byteCount(c.Usage["memory"])
		}
		out = append(out, pod)
	}
	return out, nil
}

// --- the machines' usage now, from metrics-server --------------------------------

// Current is what a cluster's machines are using at this moment: metrics-server's
// per-node readings, summed. It is not history: a cluster with metrics-server
// and no Prometheus still has a figure for now.
type Current struct {
	CPUMilli    int64
	MemoryBytes int64
	// Nodes is how many machines the sums are over. Never zero on an OK
	// answer: a sum over nothing would read as an idle cluster.
	Nodes int
	// Node is the machine one reading is of, empty on a sum. Rows look their
	// figure up by name, never by position: the orders differ.
	Node string
}

// CPUText and MemoryText spell the sums the way the rest of the console does.
func (c Current) CPUText() string    { return formatCores(c.CPUMilli) }
func (c Current) MemoryText() string { return formatBytes(c.MemoryBytes) }

// nodeUsageNow reads metrics-server's node figures: one reading per machine,
// keyed by node name, and their sum.
//
// The per-machine readings are what the node table draws as CPU and Memory,
// the same endpoint the Nodes list reads, so a node reads the same on both
// pages. The sum is the Capacity tile's `used` only where there is no history;
// see also NowElsewhere for clusters without metrics-server.
//
// The kind of answer travels in NodesNow: the readings when metrics-server
// answered (empty in its first minute), the unflattened error otherwise, for
// internal/web's stateOf to tell a 503, a 401, a timeout and a refusal apart.
//
// Reviewed as `list nodes.metrics.k8s.io` (what `kubectl top nodes` needs).
// Nodes are cluster-scoped, so there is nothing to narrow: a person who may not
// list them is told why and never shown a sum.
func nodeUsageNow(ctx context.Context, r resources.Reader) (Current, Availability, NodesNow) {
	notRead := func(why string) (Current, Availability, NodesNow) {
		return Current{}, unavailable(why), NodesNow{Err: errors.New(why)}
	}
	if r.Clients == nil {
		return notRead("this cluster's client cannot make raw API requests")
	}
	discovery := r.Clients.Discovery()
	if discovery == nil {
		return notRead("this cluster's client cannot make raw API requests")
	}
	rest := discovery.RESTClient()
	if rest == nil {
		// The tests' fake client lands here: no figure, said so, as podUsage
		// does.
		return notRead("this cluster's client cannot make raw API requests, so metrics-server cannot be read")
	}

	question := access.Attributes{Verb: "list", Group: "metrics.k8s.io", Version: "v1beta1", Resource: "nodes"}
	readings, _, err := access.List(ctx, r.Gate, r.Cluster, question, nil,
		func(ctx context.Context) ([]Current, error) {
			raw, err := getRaw(ctx, rest, "/apis/metrics.k8s.io/v1beta1/nodes")
			if err != nil {
				// %w: stateOf classifies by client-go's status (404, 503, 401).
				return nil, fmt.Errorf("metrics-server did not answer: %w", err)
			}
			return parseNodeUsage(raw)
		})
	if err != nil {
		// A refusal and the API server's answer arrive unflattened, for the
		// one classifier.
		return Current{}, unavailable(firstLine(err.Error())), NodesNow{Err: err}
	}
	// metrics-server answered. The tile refuses to call an empty answer a
	// zero (sumCurrent); a node missing from the map is "not measured yet".
	total, from := sumCurrent(readings)
	return total, from, NodesNow{ByNode: readingsByNode(readings), From: fromMetricsServer}
}

// fromMetricsServer is NodesNow.From for metrics-server's own answer.
const fromMetricsServer = "metrics-server"

// NodesNow is a reading of now per machine, and what kind of answer it was:
// metrics-server's, or the fallback's (NowElsewhere).
type NodesNow struct {
	// ByNode is keyed by node name. Non-nil exactly when the source answered;
	// empty when it has measured nothing yet.
	ByNode map[string]Current
	// From names the source that answered ("metrics-server", "Prometheus");
	// empty when ByNode is nil.
	From string
	// Err is why ByNode is nil: the error itself, for internal/web's stateOf.
	Err error
}

// nodeUsageList is metrics.k8s.io's NodeMetricsList, reduced to what is read.
type nodeUsageList struct {
	Items []struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Usage map[string]string `json:"usage"`
	} `json:"items"`
}

// parseNodeUsage is one Current per node, split from the call for testing.
func parseNodeUsage(raw []byte) ([]Current, error) {
	var list nodeUsageList
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("metrics-server answered with something unreadable: %w", err)
	}
	out := make([]Current, 0, len(list.Items))
	for _, item := range list.Items {
		out = append(out, Current{
			CPUMilli:    milliCores(item.Usage["cpu"]),
			MemoryBytes: byteCount(item.Usage["memory"]),
			Nodes:       1,
			Node:        item.Metadata.Name,
		})
	}
	return out, nil
}

// readingsByNode keys the readings by node name. A reading with no name is
// left out rather than risk the wrong row.
func readingsByNode(readings []Current) map[string]Current {
	out := make(map[string]Current, len(readings))
	for _, one := range readings {
		if one.Node != "" {
			out[one.Node] = one
		}
	}
	return out
}

// sumCurrent adds the machines up and refuses to call an empty answer a zero.
// It is not yet the tile's figure: wholeNow settles it against the node list.
func sumCurrent(readings []Current) (Current, Availability) {
	var total Current
	for _, one := range readings {
		total.CPUMilli += one.CPUMilli
		total.MemoryBytes += one.MemoryBytes
		total.Nodes += one.Nodes
	}
	if total.Nodes == 0 {
		// Up but in its first scrape window, answering an empty list: "not
		// measured", never "0m used".
		return Current{}, unavailable("metrics-server answered and has not measured any node yet")
	}
	return total, available("metrics-server answered")
}

// leaders sorts a copy by the figure asked for and keeps the head: the caller
// ranks the same slice twice.
func leaders(usage []PodUsage, by func(PodUsage) int64, limit int) []PodUsage {
	ranked := make([]PodUsage, len(usage))
	copy(ranked, usage)
	sort.SliceStable(ranked, func(i, j int) bool {
		if a, b := by(ranked[i]), by(ranked[j]); a != b {
			return a > b
		}
		return ranked[i].Namespace+"/"+ranked[i].Name < ranked[j].Namespace+"/"+ranked[j].Name
	})
	// A pod using nothing measurable is not a leader: rows of zeroes read
	// as a broken panel.
	trimmed := ranked[:0]
	for _, p := range ranked {
		if by(p) > 0 {
			trimmed = append(trimmed, p)
		}
	}
	if len(trimmed) > limit {
		trimmed = trimmed[:limit]
	}
	return trimmed
}

// --- instant usage, from Prometheus ---------------------------------------------

// The leaderboard's two questions asked of Prometheus: the fallback for a
// cluster with Prometheus and no metrics-server (as kube-prometheus-stack
// installs it).
//
// Deduplicated like the cluster pair: topk ranks, so a pod scraped twice would
// push a real one off the board. dedup goes inside the `sum by`, since outside
// it there is nothing left to average over. The %d is topk's k.
var (
	queryTopPodCPU = `topk(%d, sum by (namespace, pod) (` +
		dedup(`rate(container_cpu_usage_seconds_total{container!="",container!="POD"}[5m])`) + `))`
	queryTopPodMemory = `topk(%d, sum by (namespace, pod) (` +
		dedup(`container_memory_working_set_bytes{container!="",container!="POD"}`) + `))`
)

// promPodUsage ranks pods through Prometheus; topk ranks server-side, so no
// sort here can disagree with it.
func promPodUsage(ctx context.Context, metrics Metrics, cluster string, limit int) (Top, Top, Availability) {
	if metrics == nil {
		return Top{}, Top{}, unavailable("no metrics backend is configured")
	}

	var (
		cpuRaw, memoryRaw   []byte
		cpuErr, memoryErr   error
		cpuPods, memoryPods []PodUsage
	)
	gather(
		guard(&cpuErr, "the CPU leaderboard query", func() {
			cpuRaw, cpuErr = metrics.Query(ctx, cluster, fmt.Sprintf(queryTopPodCPU, limit))
		}),
		guard(&memoryErr, "the memory leaderboard query", func() {
			memoryRaw, memoryErr = metrics.Query(ctx, cluster, fmt.Sprintf(queryTopPodMemory, limit))
		}),
	)
	if cpuErr != nil {
		return Top{}, Top{}, unavailable("Prometheus did not answer: " + firstLine(cpuErr.Error()))
	}
	if memoryErr != nil {
		return Top{}, Top{}, unavailable("Prometheus did not answer: " + firstLine(memoryErr.Error()))
	}

	cpuPods, cpuErr = parseVector(cpuRaw, func(v float64) PodUsage {
		// Prometheus counts CPU in cores; this package counts millicores.
		return PodUsage{CPUMilli: int64(v * 1000)}
	})
	if cpuErr != nil {
		return Top{}, Top{}, unavailable(firstLine(cpuErr.Error()))
	}
	memoryPods, memoryErr = parseVector(memoryRaw, func(v float64) PodUsage {
		return PodUsage{MemoryBytes: int64(v)}
	})
	if memoryErr != nil {
		return Top{}, Top{}, unavailable(firstLine(memoryErr.Error()))
	}

	return Top{Pods: cpuPods}, Top{Pods: memoryPods}, available("Prometheus answered")
}

// promVector is an instant query's answer, reduced to the labels and the value.
type promVector struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Data   struct {
		Result []struct {
			Metric map[string]string `json:"metric"`
			Value  []any             `json:"value"`
		} `json:"result"`
	} `json:"data"`
}

func parseVector(raw []byte, build func(float64) PodUsage) ([]PodUsage, error) {
	var resp promVector
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("Prometheus answered with something unreadable: %w", err)
	}
	if resp.Status != "success" {
		return nil, fmt.Errorf("Prometheus rejected the query: %s", resp.Error)
	}

	out := make([]PodUsage, 0, len(resp.Data.Result))
	for _, series := range resp.Data.Result {
		if len(series.Value) < 2 {
			continue
		}
		value, ok := sampleValue(series.Value[1])
		if !ok {
			continue
		}
		pod := build(value)
		pod.Namespace, pod.Name = series.Metric["namespace"], series.Metric["pod"]
		if pod.Name == "" {
			// A series with no pod label is an aggregate that escaped the
			// `by`; it would be a row pointing at nothing.
			continue
		}
		out = append(out, pod)
	}
	return out, nil
}

// parseMetricNames reads the answer to the probe in object.go: which of a list
// of metric names this Prometheus holds. A name counts only when its count is
// above zero, since a rule or federation can leave a name with nothing under
// it.
func parseMetricNames(raw []byte) (map[string]bool, error) {
	var resp promVector
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("Prometheus answered with something unreadable: %w", err)
	}
	if resp.Status != "success" {
		return nil, fmt.Errorf("Prometheus rejected the query: %s", resp.Error)
	}

	// Never nil on success: that is how a caller tells "absent" from "the
	// probe did not run".
	names := make(map[string]bool, len(resp.Data.Result))
	for _, series := range resp.Data.Result {
		name := series.Metric[probeLabel]
		if name == "" || len(series.Value) < 2 {
			continue
		}
		count, ok := sampleValue(series.Value[1])
		if !ok || count <= 0 {
			continue
		}
		names[name] = true
	}
	return names, nil
}

func milliCores(q string) int64 {
	parsed, err := resource.ParseQuantity(q)
	if err != nil {
		return 0
	}
	return parsed.MilliValue()
}

func byteCount(q string) int64 {
	parsed, err := resource.ParseQuantity(q)
	if err != nil {
		return 0
	}
	return parsed.Value()
}

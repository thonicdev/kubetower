package overview

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"strings"
	"time"
)

// History for one object over a window the reader chose, sharing the cluster
// page's parsing, point type and charts.

// Window is a period the reader can pick, and the step that suits it: each
// lands between 60 and 200 points, about what a chart can show, so a long
// window never asks Prometheus for samples nobody can see.
type Window struct {
	Label string
	Range time.Duration
	Step  time.Duration
}

// Windows are the offered ranges, shortest first. One hour is the default.
var Windows = []Window{
	{Label: "15m", Range: 15 * time.Minute, Step: 15 * time.Second},
	{Label: "1h", Range: time.Hour, Step: 30 * time.Second},
	{Label: "6h", Range: 6 * time.Hour, Step: 3 * time.Minute},
	{Label: "24h", Range: 24 * time.Hour, Step: 10 * time.Minute},
	{Label: "7d", Range: 7 * 24 * time.Hour, Step: time.Hour},
}

// WindowFor resolves a label to a window, falling back to one hour: an unknown
// label is a stale bookmark or an edited URL, not worth an error.
func WindowFor(label string) Window {
	for _, w := range Windows {
		if strings.EqualFold(w.Label, label) {
			return w
		}
	}
	return Windows[1]
}

// Chart draws this series.
func (h History) Chart() template.HTML { return HistorySparkline(h) }

// --- which charts a panel can offer ------------------------------------------------

// The charts a resource panel can draw, as keys that travel through a query
// parameter. `network` is one key and two series, on one pair of axes; see
// HistoryLines.
const (
	ChartCPU     = "cpu"
	ChartMemory  = "memory"
	ChartNetwork = "network"
	ChartDisk    = "disk"
)

// AllCharts is what a caller naming none gets, in selector order: the two
// every cluster can draw, then the two that depend on what Prometheus exports.
var AllCharts = []string{ChartCPU, ChartMemory, ChartNetwork, ChartDisk}

// SeriesSupport is whether this cluster's Prometheus holds the counters the
// optional charts are drawn from at all.
//
// A range query over a pod that did no I/O and one against a Prometheus that
// lacks the counter return the same empty result, so the metric names are
// probed separately, and an empty series is only "nothing happened" when the
// counter demonstrably exists. CPU and memory are always offered and are not
// in here, though a pod's lines are probed too: some cAdvisors (cri-dockerd)
// export no per-container series, and their empty lines say so.
type SeriesSupport struct {
	Network Availability
	Disk    Availability
}

// ChartOption is one entry in the selector: its name, whether this cluster can
// answer for it, and if not the sentence saying why, so a grayed control tells
// the reader what to fix.
type ChartOption struct {
	Key       string
	Label     string
	Available bool
	Detail    string
}

// ObjectHistory is what a resource panel draws: the cluster page's series
// narrowed to one object, plus those only some clusters can answer for.
type ObjectHistory struct {
	CPU    History
	Memory History
	// NetIn and NetOut are two queries drawn as one chart by
	// Chart(ChartNetwork).
	NetIn  History
	NetOut History
	// Disk is the container filesystem (cAdvisor's container_fs_usage_bytes:
	// the writable layer and the emptyDirs the runtime accounts to it), and
	// its Title says so: it does not measure mounted volumes.
	Disk   History
	Window Window
	// Source is the cluster overview's caption, set by clusterLines; an
	// object's lines carry their reasons in Unavailable instead.
	Source string
	// Support is what the cluster can answer for at all, established by one
	// instant query.
	Support SeriesSupport
	// NoHistory is set, with the reason, when there is no Prometheus to ask
	// (no backend, or metrics-server alone). Every series then carries
	// MissingBackend, and the panel's period buttons are grayed.
	NoHistory string
}

// Drawable reports whether CPU or memory has anything in it, to choose between
// a chart and a sentence: there is a usage picture for this object. The other
// series carry their own reasons, read through Options.
func (o ObjectHistory) Drawable() bool { return len(o.CPU.Points) > 0 || len(o.Memory.Points) > 0 }

// Options is the selector's model: every chart, in order, with whether this
// cluster can answer for it.
//
// CPU and memory are available when their series has something to draw, and
// otherwise refused with the series' own sentence; internal/web makes them
// available again from a reading of now, which this package does not take.
// Network and disk answer from the probe, the only thing that tells "no
// traffic" from "no such counter".
func (o ObjectHistory) Options() []ChartOption {
	network := o.Support.Network
	disk := o.Support.Disk
	return []ChartOption{
		{Key: ChartCPU, Label: chartLabel(o.CPU.Title, "CPU"), Available: len(o.CPU.Points) > 0, Detail: seriesDetail(o.CPU)},
		{Key: ChartMemory, Label: chartLabel(o.Memory.Title, "Memory"), Available: len(o.Memory.Points) > 0, Detail: seriesDetail(o.Memory)},
		{Key: ChartNetwork, Label: networkLabel(o.NetIn.Title), Available: network.OK, Detail: optionDetail(network)},
		{Key: ChartDisk, Label: chartLabel(o.Disk.Title, "Filesystem"), Available: disk.OK, Detail: optionDetail(disk)},
	}
}

// seriesDetail is a CPU or memory control's reason, never empty on a refused
// control. Empty where it is drawn.
func seriesDetail(h History) string {
	switch {
	case len(h.Points) > 0:
		return ""
	case h.Unavailable != "":
		return h.Unavailable
	default:
		return "no samples came back for this object, so there is nothing to chart"
	}
}

// optionDetail makes sure a refused control always carries a reason.
func optionDetail(a Availability) string {
	switch {
	case a.Detail != "":
		return a.Detail
	case a.OK:
		return ""
	default:
		return "not established — this history was fetched without asking about this chart"
	}
}

// Chart draws one chart by key, the same string the selector uses. An unknown
// key draws a sentence, not a chart quietly showing CPU.
func (o ObjectHistory) Chart(key string) template.HTML {
	switch key {
	case ChartCPU:
		return o.CPU.Chart()
	case ChartMemory:
		return o.Memory.Chart()
	case ChartNetwork:
		return HistoryLines(o.NetIn, o.NetOut)
	case ChartDisk:
		return o.Disk.Chart()
	default:
		return noData(linesWidth, sparkHeight,
			"there is no chart called "+key, "Unknown chart")
	}
}

func chartLabel(title, fallback string) string {
	if title == "" {
		return fallback
	}
	return title
}

// networkLabel turns the "in" series' title into the chart's name: "Network
// in" becomes "Network", and on a node "Pod network", which is what it
// measures.
func networkLabel(title string) string {
	if trimmed := strings.TrimSuffix(title, " in"); trimmed != "" && trimmed != title {
		return trimmed
	}
	return "Network"
}

// --- the metric names ---------------------------------------------------------------

// The cAdvisor counters everything here is drawn from. cAdvisor because
// kubelet exposes it everywhere; the consequence is that it knows containers
// and machines but not nodes (see NodeHistory).
const (
	metricCPU    = "container_cpu_usage_seconds_total"
	metricMemory = "container_memory_working_set_bytes"
	metricNetIn  = "container_network_receive_bytes_total"
	metricNetOut = "container_network_transmit_bytes_total"
	metricDisk   = "container_fs_usage_bytes"
)

// rateWindow is the shortest range a counter is rated over. One value for
// every chart in a panel, since charts smoothed differently suggest a spike in
// one of them; a short burst draws as a bump.
const rateWindow = "5m"

// rateFloor is rateWindow as a duration; TestRateFloorIsRateWindow pins them.
const rateFloor = 5 * time.Minute

// rateRange is the range a counter is rated over at a step: rateWindow, or the
// step where it is longer, as Grafana's $__rate_interval does. A rate is
// evaluated once per step and looks back over its range, so a range shorter
// than the step would skip what happens between points (a short run, a
// spike). Every chart in a panel shares one window and so one range.
func rateRange(step time.Duration) string {
	r := rateFloor
	if step > r {
		r = step
	}
	switch {
	case r%time.Hour == 0:
		return fmt.Sprintf("%dh", r/time.Hour)
	case r%time.Minute == 0:
		return fmt.Sprintf("%dm", r/time.Minute)
	default:
		return fmt.Sprintf("%ds", r/time.Second)
	}
}

// scrapeLabels and dedup make one container scraped by several Prometheus
// targets count once before anything is summed: a cluster with two
// kube-prometheus-stack releases stores every cAdvisor series twice, and sum()
// reads exactly double. The rule, avg without these labels, is the same as
// internal/metrics.ScrapeLabels; this package does not import internal/metrics,
// so TestDedupIsOneRule pins the two strings together.
const scrapeLabels = "job, instance, endpoint, service, metrics_path"

func dedup(inner string) string { return "avg without (" + scrapeLabels + ") (" + inner + ")" }

// PodHistory fetches one pod's series over a window. Naming charts asks for
// only those, since the panel draws one chart at a time; naming none asks for
// all.
//
// metrics == nil, or a cluster with only metrics-server, returns empty series
// carrying the reason and no error: an ordinary state, not a failure.
func PodHistory(ctx context.Context, metrics Metrics, cluster, namespace, pod string, window Window, charts ...string) ObjectHistory {
	out := ObjectHistory{Window: window}
	if metrics == nil {
		return noBackend(out)
	}

	// container!="" drops the pod-level rollup cAdvisor exports beside the
	// per-container series, which would double every figure.
	selector := fmt.Sprintf(`namespace=%s,pod=%s`, promQuote(namespace), promQuote(pod))
	byContainer := selector + `,container!=""`
	rate := rateRange(window.Step)

	// Every probe asks for the dimension its query selects on, not the bare
	// counter: where cAdvisor exports only pod and machine cgroups, or only the
	// machine's interfaces (cri-dockerd), a bare-name probe would say yes and
	// every pod would claim to be younger than the window.
	specs := []seriesSpec{
		{
			chart: ChartCPU, into: cpuOf, title: "CPU", unit: UnitCores, scale: 1000,
			metric: metricCPU,
			probe:  perContainer(metricCPU),
			absent: noPerContainer(metricCPU),
			query:  fmt.Sprintf(`sum(%s)`, dedup(fmt.Sprintf(`rate(%s{%s}[%s])`, metricCPU, byContainer, rate))),
		},
		{
			chart: ChartMemory, into: memoryOf, title: "Memory", unit: UnitBytes,
			metric: metricMemory,
			probe:  perContainer(metricMemory),
			absent: noPerContainer(metricMemory),
			query:  fmt.Sprintf(`sum(%s)`, dedup(fmt.Sprintf(`%s{%s}`, metricMemory, byContainer))),
		},
		// Network is collected per pod network namespace, shared by the pod's
		// containers, so these series carry no `container` label and
		// container!="" would match none of them: the selector differs from the
		// two above. sum() is over the pod's interfaces. The probe asks for
		// pod!="" because some cAdvisors report only the machine's interfaces.
		{
			chart: ChartNetwork, into: netInOf, title: "Network in", unit: UnitBytesPerSecond,
			metric: metricNetIn,
			probe:  fmt.Sprintf(`%s{pod!=""}`, metricNetIn),
			absent: "this cluster's Prometheus has no per-pod " + metricNetIn + " series — cAdvisor " +
				"here reports only each machine's own interfaces, which are not this pod's",
			query: fmt.Sprintf(`sum(%s)`, dedup(fmt.Sprintf(`rate(%s{%s}[%s])`, metricNetIn, selector, rate))),
		},
		{
			chart: ChartNetwork, into: netOutOf, title: "Network out", unit: UnitBytesPerSecond,
			metric: metricNetOut,
			probe:  fmt.Sprintf(`%s{pod!=""}`, metricNetOut),
			absent: "this cluster's Prometheus has no per-pod " + metricNetOut + " series — cAdvisor " +
				"here reports only each machine's own interfaces, which are not this pod's",
			query: fmt.Sprintf(`sum(%s)`, dedup(fmt.Sprintf(`rate(%s{%s}[%s])`, metricNetOut, selector, rate))),
		},
		// container!="" as for CPU: beside per-container stats cAdvisor exports a
		// machine-level series under id="/" that would add the node's root disk.
		// Some runtimes (containerd on EKS) give no per-container filesystem
		// stats at all, and the probe then reports the chart unavailable.
		{
			chart: ChartDisk, into: diskOf, title: "Container filesystem", unit: UnitBytes,
			metric: metricDisk,
			probe:  fmt.Sprintf(`%s{container!=""}`, metricDisk),
			absent: "this cluster's Prometheus has no per-container " + metricDisk + " series — cAdvisor " +
				"here reports only each machine's own filesystem, which is not this pod's",
			query: fmt.Sprintf(`sum(%s)`, dedup(fmt.Sprintf(`%s{%s}`, metricDisk, byContainer))),
		},
	}

	return fill(ctx, metrics, cluster, out, specs, charts)
}

// NodeHistory is the same for a machine. cAdvisor has no concept of a node, so:
//
//   - CPU and memory sum the containers on the machine, missing the kubelet,
//     the runtime and the OS. That limitation is accepted.
//   - The node's name is not a label cAdvisor emits: scrapers add `node` on
//     some clusters and only `instance` on others. Each query is asked with
//     `node` first, then `instance`, joined by `or`, which takes the right side
//     only where the left has no series, so nothing is counted twice.
func NodeHistory(ctx context.Context, metrics Metrics, cluster, node string, window Window, charts ...string) ObjectHistory {
	out := ObjectHistory{Window: window}
	if metrics == nil {
		return noBackend(out)
	}

	rate := rateRange(window.Step)
	specs := []seriesSpec{
		{
			chart: ChartCPU, into: cpuOf, title: "CPU", unit: UnitCores, scale: 1000,
			query: eitherNodeLabel(node, func(match string) string {
				return fmt.Sprintf(`sum(%s)`, dedup(fmt.Sprintf(`rate(%s{%s,container!=""}[%s])`, metricCPU, match, rate)))
			}),
		},
		{
			chart: ChartMemory, into: memoryOf, title: "Memory", unit: UnitBytes,
			query: eitherNodeLabel(node, func(match string) string {
				return fmt.Sprintf(`sum(%s)`, dedup(fmt.Sprintf(`%s{%s,container!=""}`, metricMemory, match)))
			}),
		},
		// A node's network is the sum of its pods' traffic, named so. The host
		// netns under id="/" holds the uplink, overlay, ipvs and every veth, so
		// summing it counts packets several times, and node-exporter is not
		// always present. pod!="" keeps the machine-level rollup out.
		{
			chart: ChartNetwork, into: netInOf, title: "Pod network in", unit: UnitBytesPerSecond,
			metric: metricNetIn, probe: fmt.Sprintf(`%s{pod!=""}`, metricNetIn),
			query: eitherNodeLabel(node, func(match string) string {
				return fmt.Sprintf(`sum(%s)`, dedup(fmt.Sprintf(`rate(%s{%s,pod!=""}[%s])`, metricNetIn, match, rate)))
			}),
		},
		{
			chart: ChartNetwork, into: netOutOf, title: "Pod network out", unit: UnitBytesPerSecond,
			metric: metricNetOut, probe: fmt.Sprintf(`%s{pod!=""}`, metricNetOut),
			query: eitherNodeLabel(node, func(match string) string {
				return fmt.Sprintf(`sum(%s)`, dedup(fmt.Sprintf(`rate(%s{%s,pod!=""}[%s])`, metricNetOut, match, rate)))
			}),
		},
		// The node's root filesystem (id="/"). cAdvisor reports one series per
		// mount, some memory-backed (/dev/shm, /run, /tmp): only /dev/* block
		// devices, less /dev/shm, are kept.
		{
			chart: ChartDisk, into: diskOf, title: "Node filesystem", unit: UnitBytes,
			metric: metricDisk, probe: fmt.Sprintf(`%s{id="/"}`, metricDisk),
			query: eitherNodeLabel(node, func(match string) string {
				return fmt.Sprintf(`sum(%s)`, dedup(fmt.Sprintf(`%s{%s,id="/",device=~"/dev/.*",device!="/dev/shm"}`, metricDisk, match)))
			}),
		},
	}

	return fill(ctx, metrics, cluster, out, specs, charts)
}

// eitherNodeLabel writes the query once per label a machine's name can arrive
// in, joined with `or`; see NodeHistory.
func eitherNodeLabel(node string, build func(match string) string) string {
	byNode := build(`node=` + promQuote(node))
	// PromQL anchors the regex, so the only slack is kubelet's optional port
	// on the scrape target.
	byInstance := build(`instance=~` + promQuote(promRegexEscape(node)+`(:[0-9]+)?`))
	return byNode + " or " + byInstance
}

// perContainer is the probe for a CPU or memory line summed over containers,
// and noPerContainer what that line says when the probe answers no; shared by
// a pod's panel and the cluster's history so both say it in the same words.
func perContainer(metric string) string { return metric + `{container!=""}` }

func noPerContainer(metric string) string {
	return "this cluster's Prometheus has no per-container " + metric + " series — cAdvisor " +
		"here sees the pods and the machine, not their containers"
}

// --- fetching them ---------------------------------------------------------------

// seriesSpec is one line on one chart: where it goes, what to ask for it, and
// what makes an empty answer explicable.
type seriesSpec struct {
	// chart is which ChartX key this series belongs to; two share
	// ChartNetwork.
	chart string
	// into points at the field this series fills, as a function so the
	// compiler checks it.
	into  func(*ObjectHistory) *History
	title string
	unit  Unit
	query string
	// scale multiplies every sample; zero means one. CPU's is 1000: Prometheus
	// counts cores, this console millicores.
	scale float64
	// metric names the counter, for the sentence a missing one gets.
	metric string
	// probe is the instant selector establishing whether this series can be
	// answered at all; empty means no probe. It names the dimension the query
	// needs as well as the counter: a bare container_fs_usage_bytes exists on
	// clusters where every series is a node's root disk, and the chart would
	// then come back empty and read as idle.
	probe string
	// absent is what to say when the probe answers no; empty generates a
	// sentence naming the counter.
	absent string
}

func cpuOf(o *ObjectHistory) *History    { return &o.CPU }
func memoryOf(o *ObjectHistory) *History { return &o.Memory }
func netInOf(o *ObjectHistory) *History  { return &o.NetIn }
func netOutOf(o *ObjectHistory) *History { return &o.NetOut }
func diskOf(o *ObjectHistory) *History   { return &o.Disk }

// chartSet is the charts asked for, as a set that answers true for everything
// when nothing was named. Unknown names are dropped; a request left with
// nothing is reported by fill as "nothing was asked", not as no metrics.
func chartSet(charts []string) func(string) bool {
	if len(charts) == 0 {
		return func(string) bool { return true }
	}
	wanted := make(map[string]bool, len(charts))
	for _, c := range charts {
		wanted[c] = true
	}
	return func(chart string) bool { return wanted[chart] }
}

// noBackend is the answer when there is no Prometheus at all: every series and
// every option carries the same sentence, and nothing was asked of anyone.
func noBackend(out ObjectHistory) ObjectHistory {
	reason := "no metrics backend is configured, so there is no history to draw"
	for _, into := range []func(*ObjectHistory) *History{cpuOf, memoryOf, netInOf, netOutOf, diskOf} {
		series := into(&out)
		series.Unavailable, series.Missing = reason, MissingBackend
	}
	out.NoHistory = reason
	out.CPU.Title, out.CPU.Unit = "CPU", UnitCores
	out.Memory.Title, out.Memory.Unit = "Memory", UnitBytes
	out.NetIn.Title, out.NetIn.Unit = "Network in", UnitBytesPerSecond
	out.NetOut.Title, out.NetOut.Unit = "Network out", UnitBytesPerSecond
	out.Disk.Title, out.Disk.Unit = "Filesystem", UnitBytes
	out.Support = SeriesSupport{Network: unavailable(reason), Disk: unavailable(reason)}
	return out
}

// fill runs one range query per series and folds the failures in: one series
// failing must not blank the others, and all failing is one sentence for one
// cause. The probe runs in the same wave, so the panel costs one round trip.
//
// The range queries cover only the charts asked for; the probe covers every
// series the object has, because the selector must still say whether the
// other charts can be drawn.
func fill(ctx context.Context, metrics Metrics, cluster string, out ObjectHistory, all []seriesSpec, charts []string) ObjectHistory {
	// Every series gets its title and unit, so the selector can name charts
	// this render does not fetch; those are marked not asked for, so no path
	// draws an empty chart out of a series nobody requested.
	wanted := chartSet(charts)
	specs := make([]seriesSpec, 0, len(all))
	for _, spec := range all {
		series := spec.into(&out)
		series.Title, series.Unit = spec.title, spec.unit
		if !wanted(spec.chart) {
			series.Unavailable = "this chart was not asked for in this request"
			series.Missing = MissingNotAsked
			continue
		}
		series.Step = out.Window.Step
		specs = append(specs, spec)
	}

	if len(specs) == 0 {
		// Only charts this package does not know: nothing was asked, so nothing
		// is missing.
		reason := "no chart was asked for, so nothing was asked of Prometheus"
		out.Support = SeriesSupport{Network: unavailable(reason), Disk: unavailable(reason)}
		return out
	}

	end := time.Now()
	start := end.Add(-out.Window.Range)

	// Each task reports through its error, so a panic fails one series; see
	// gather.
	errs := make([]error, len(specs))
	tasks := make([]task, 0, len(specs)+1)
	for i, spec := range specs {
		i, spec := i, spec
		tasks = append(tasks, guard(&errs[i], "the "+spec.title+" query", func() {
			series := spec.into(&out)
			series.Points, errs[i] = rangeQueryStep(ctx, metrics, cluster, spec.query, start, end, out.Window.Step)
		}))
	}

	var known map[string]bool
	var probeErr error
	if probe := probeQuery(all); probe != "" {
		tasks = append(tasks, guard(&probeErr, "the probe for which series this object has", func() {
			known, probeErr = probeMetrics(ctx, metrics, cluster, probe)
		}))
	}
	gather(tasks...)

	out.Support = support(all, known, probeErr)

	// All failing is one cause, reported from the first failure.
	failed := 0
	for _, err := range errs {
		if err != nil {
			failed++
		}
	}
	if failed == len(specs) {
		reason := firstLine(errs[0].Error())
		// "No Prometheus here" is a fact about the cluster; "Prometheus did
		// not answer" may work next time. The period buttons treat them
		// differently.
		missing := MissingFailed
		if noHistory(errs[0]) {
			missing = MissingBackend
			out.NoHistory = reason
		}
		for _, spec := range specs {
			series := spec.into(&out)
			series.Unavailable, series.Missing = reason, missing
			if missing == MissingFailed {
				series.Err = errs[0]
			}
		}
		return out
	}
	for i, spec := range specs {
		if errs[i] != nil {
			series := spec.into(&out)
			series.Unavailable, series.Missing, series.Err = firstLine(errs[i].Error()), MissingFailed, errs[i]
		}
	}

	// Prometheus counts CPU in cores, the tables beside the chart millicores;
	// only a series with a scale is touched.
	for _, spec := range specs {
		if spec.scale == 0 || spec.scale == 1 {
			continue
		}
		points := spec.into(&out).Points
		for i := range points {
			points[i].Value *= spec.scale
		}
	}

	// An empty window is not a failure (a young pod has nothing that far
	// back), and "the counter does not exist here" is a different sentence:
	// telling them apart is the probe's job.
	drawn := 0
	for i, spec := range specs {
		series := spec.into(&out)
		if len(series.Points) > 0 {
			drawn++
			continue
		}
		if errs[i] != nil {
			continue
		}
		if missing := absentReason(spec, known); missing != "" {
			series.Unavailable, series.Missing = missing, MissingCounter
		}
	}
	if drawn == 0 {
		for _, spec := range specs {
			// Only where nothing more specific was established, such as a
			// missing counter.
			if series := spec.into(&out); series.Unavailable == "" {
				series.Unavailable, series.Missing = emptyWindow, MissingEmpty
			}
		}
	}
	return out
}

// noHistory recognizes the metrics layer's "this cluster has no Prometheus"
// error by its method: this package does not import internal/metrics, and the
// sentence may be reworded.
func noHistory(err error) bool {
	var marked interface{ NoHistory() bool }
	return errors.As(err, &marked) && marked.NoHistory()
}

// --- the probe ------------------------------------------------------------------

// probeQuery asks in one instant query whether this Prometheus holds the
// series the charts depend on, in the shape each query needs: one
// `count by (__name__) (<probe>)` per series, joined with `or on(__name__)`.
//
// The on(__name__) matters: set operators match on labels excluding the metric
// name, so a plain `or` would see every sub-answer as the same empty signature
// and keep only the first, graying charts that work.
//
// It returns "" when no series declared a probe, so the cluster's CPU and
// memory cost no extra query.
func probeQuery(specs []seriesSpec) string {
	seen := map[string]bool{}
	parts := make([]string, 0, len(specs))
	for _, spec := range specs {
		if spec.probe == "" || seen[spec.probe] {
			continue
		}
		seen[spec.probe] = true
		parts = append(parts, fmt.Sprintf(`count by (__name__) (%s)`, spec.probe))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " or on(__name__) ")
}

// probeMetrics runs the probe and returns the names that came back with a
// non-zero count.
func probeMetrics(ctx context.Context, metrics Metrics, cluster, promQL string) (map[string]bool, error) {
	raw, err := metrics.Query(ctx, cluster, promQL)
	if err != nil {
		return nil, err
	}
	return parseMetricNames(raw)
}

// absentReason is the sentence for a series this Prometheus cannot answer for,
// and "" when it can, or when the probe did not run and guessing would be
// worse than the empty-window sentence.
func absentReason(spec seriesSpec, known map[string]bool) string {
	if spec.probe == "" || known == nil || known[spec.metric] {
		return ""
	}
	if spec.absent != "" {
		return spec.absent + ", so there is nothing to chart — this is not a claim that the object is idle"
	}
	return "this cluster's Prometheus does not export " + spec.metric + ", so there is nothing to chart — " +
		"this is not a claim that the object is idle"
}

// support turns the probe's answer into the per-chart availability the selector
// reads. A chart is offered only when every series it needs can be answered:
// half of the network chart would imply nothing was ever sent.
func support(specs []seriesSpec, known map[string]bool, probeErr error) SeriesSupport {
	var out SeriesSupport
	for _, chart := range []struct {
		key  string
		into *Availability
	}{
		{ChartNetwork, &out.Network},
		{ChartDisk, &out.Disk},
	} {
		probed := probedSpecs(specs, chart.key)
		if len(probed) == 0 {
			// Not asked for: nothing established. Options words the zero
			// Availability.
			continue
		}
		names := make([]string, 0, len(probed))
		for _, spec := range probed {
			names = append(names, spec.metric)
		}
		switch {
		case probeErr != nil:
			// Not established, said so rather than absent: a refused probe
			// does not mean the counters are missing.
			*chart.into = unavailable("could not establish whether this cluster exports " +
				strings.Join(names, " and ") + ": " + firstLine(probeErr.Error()))
			continue
		case known == nil:
			*chart.into = unavailable("nothing was asked about " + strings.Join(names, " and "))
			continue
		}

		var absent []string
		detail := ""
		for _, spec := range probed {
			if known[spec.metric] {
				continue
			}
			absent = append(absent, spec.metric)
			if detail == "" && spec.absent != "" {
				detail = spec.absent
			}
		}
		switch {
		case len(absent) == 0:
			*chart.into = available("this cluster's Prometheus exports " + strings.Join(names, " and "))
		case detail != "":
			*chart.into = unavailable(detail)
		default:
			*chart.into = unavailable("this cluster's Prometheus does not export " + strings.Join(absent, " or "))
		}
	}
	return out
}

// probedSpecs is the series one chart depends on that carry a probe, without
// repeats.
func probedSpecs(specs []seriesSpec, chart string) []seriesSpec {
	seen := map[string]bool{}
	var kept []seriesSpec
	for _, spec := range specs {
		if spec.chart != chart || spec.probe == "" || seen[spec.metric] {
			continue
		}
		seen[spec.metric] = true
		kept = append(kept, spec)
	}
	return kept
}

// promQuote turns a value into a quoted PromQL string literal. Kubernetes names
// cannot contain a quote or backslash, but nothing assembled by concatenation
// should rely on what it is handed.
func promQuote(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return `"` + replacer.Replace(value) + `"`
}

// promRegexEscape makes a value match itself inside a `=~` matcher: every dot
// in a node name is otherwise a wildcard. The result goes through promQuote,
// which doubles the backslashes.
func promRegexEscape(value string) string {
	var b strings.Builder
	for _, r := range value {
		if strings.ContainsRune(`\.+*?()|[]{}^$`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

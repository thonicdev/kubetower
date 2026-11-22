package web

import (
	"context"
	"sync"
	"time"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/metrics"
	"github.com/thonicdev/kubetower/internal/overview"
	"github.com/thonicdev/kubetower/safego"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// Overview is four panels and four envelopes, so one missing metrics source makes
// one panel absent rather than the dashboard unreadable, and never draws a usage
// of zero. The machines (overview.Cluster), the leaderboards (a metrics backend)
// and the workloads are read concurrently.
func (r apiReads) Overview(ctx context.Context, name string) (api.Overview, *api.Failure) {
	// A cluster the probe found unreachable moments ago is answered from that
	// verdict, without dialling it.
	if fail := freshRefusal(r.s, name, time.Now()); fail != nil {
		return api.Overview{}, fail
	}
	client, fail := r.client(name)
	if fail != nil {
		return api.Overview{}, fail
	}
	metrics := r.s.history()

	var (
		machines  overview.ClusterOverview
		machErr   error
		topCPU    overview.Top
		topMemory overview.Top
		topSeen   access.Visible
		usage     overview.Availability
		workloads overview.WorkloadsOverview
		workErr   error
		wg        sync.WaitGroup
	)
	// Each read through safego: overview.gather confines a panic to its panel, but
	// the assembly after it does not, and a panic on this request's goroutine would
	// end the process (under `--incluster`, everybody's console). `done` runs after
	// the recovery and before wg.Done, so wg.Wait never returns before the error is
	// set.
	wg.Add(3)
	safego.Go("overview: the machines", func() error {
		var err error
		machines, err = overview.ClusterWithFallback(ctx, r.s.gate, client, metrics, r.nodesNowElsewhere(name))
		return err
	}, func(err error) { machErr = err; wg.Done() })
	safego.Go("overview: the hottest pods", func() error {
		// Asked, not assumed: `ok` with an empty list would claim a backend answered
		// that nothing had queried.
		topCPU, topMemory, usage, topSeen = overview.TopPods(ctx, r.s.gate, client, "", metrics)
		return nil
	}, func(err error) {
		if err != nil {
			// The boards' unavailable shape: a sentence and no Cause, as
			// overview.Availability describes a panic.
			topCPU, topMemory, topSeen = overview.Top{}, overview.Top{}, access.Visible{}
			usage = overview.Availability{Detail: err.Error()}
		}
		wg.Done()
	})
	safego.Go("overview: the workloads", func() error {
		// The other half of the page: trouble, restarting containers, the phase bar and
		// the kind tiles. It fails only when the pod list fails; everything else
		// degrades into `WorkloadsOverview.Degraded`, which reaches the page as
		// `notShown`.
		var err error
		workloads, err = overview.Workloads(ctx, r.s.gate, client, "", metrics)
		return err
	}, func(err error) { workErr = err; wg.Done() })
	wg.Wait()

	if machErr != nil {
		// The node list proves the cluster answers at all. A refusal of it is not this
		// error: overview.Cluster answers it as NodesRefused and the page is drawn
		// without the machines.
		return api.Overview{}, stateOf(machErr, api.Scope{Cluster: name, Verb: "list"})
	}
	// A workloads fetch that failed on its own does not take the page: the workload
	// panels carry the refusal in their envelopes.
	var filled *overview.WorkloadsOverview
	if workErr == nil {
		filled = &workloads
	}
	out := overviewOf(name, machines, topCPU, topMemory, usage, metrics != nil)
	overviewPanels(&out, name, machines, filled, workErr, metrics)
	seen := machines.Access.Merge(topSeen)
	if filled != nil {
		seen = seen.Merge(filled.Access)
	}
	out.AccessNote = overviewAccessNote(seen)
	return out, nil
}

// nodesNowElsewhere is the Overview's reading of now per machine where
// metrics-server is not installed: the Nodes list's own read, out of the cache
// that list fills, so the Capacity tile and the Nodes list agree. nil where this
// build has no usage door, and the tile keeps the 404. It waits for a first read
// (usageAnswer); a disk-only entry is left out (ComputeUnmeasured).
//
//   - figures: the machines measured, and who measured them;
//   - IsNoMetrics: nothing to ask there either, an absence, and the 404 stands
//     with this sentence beside it;
//   - any other error: the fallback failed, said as a failure.
func (r apiReads) nodesNowElsewhere(cluster string) overview.NowElsewhere {
	if r.s.usage == nil {
		return nil
	}
	return func(ctx context.Context) overview.Elsewhere {
		figures, err := r.s.usageAnswer(ctx, cluster, "nodes", "")
		var byNode map[string]overview.Current
		from := ""
		for node, figure := range figures {
			if figure.ComputeUnmeasured {
				continue
			}
			if byNode == nil {
				byNode = make(map[string]overview.Current, len(figures))
			}
			byNode[node] = overview.Current{CPUMilli: figure.CPUMillicores, MemoryBytes: figure.MemoryBytes, Nodes: 1, Node: node}
			from = figure.From
		}
		switch {
		case byNode != nil:
			return overview.Elsewhere{ByNode: byNode, From: backendName(from)}
		case err == nil:
			// Asked, and no machine's CPU or memory in the answer: not a failure either.
			return overview.Elsewhere{Absent: "the Nodes list's own read has no reading of any node either"}
		case metrics.IsNoMetrics(err):
			return overview.Elsewhere{Absent: firstLine(err.Error())}
		}
		return overview.Elsewhere{Err: err}
	}
}

// backendName is a metrics backend as a sentence names it.
func backendName(kind string) string {
	if kind == metrics.KindPrometheus {
		return "Prometheus"
	}
	if kind == "" {
		return "the metrics backend"
	}
	return kind
}

// overviewAccessNote is what a dashboard narrowed by the access review says about
// itself, or "", built from the questions and never from the rows.
func overviewAccessNote(seen access.Visible) string {
	switch {
	case seen.Narrowed:
		return "You may not list everything this page counts in every namespace, so its pods, events and " +
			"workloads are those of the namespaces where you may, and what the pods request of each node is not shown."
	case seen.Unreviewed:
		return "A namespace whose access review could not be made is left out of what this page counts."
	}
	return ""
}

// overviewOf assembles the four panels from what the two reads answered, apart
// from the call so a test can drive it with a domain value: requested against
// used, asked against answered, empty against absent are all decided here.
func overviewOf(name string, machines overview.ClusterOverview,
	topCPU, topMemory overview.Top, usage overview.Availability, configured bool) api.Overview {

	out := api.Overview{Cluster: name}

	// The other panels are filled by overviewPanels (apioverview.go); this is the
	// machine half alone: the capacity cells and the leaderboards. Node figures are
	// the head's `nodesSummary` pill, not `counts`.
	out.Capacity = api.Items(capacityCells(name, machines))
	if refusal := nodesRefusal(name, machines); refusal != nil {
		// Capacity is a sum over the machines: unknown to a person refused them.
		out.Capacity = api.ListFailed[api.CapacityCell](refusal.State, refusal.Reason)
	}
	out.Top = topPanel(name, topCPU, topMemory, usage, configured)
	return out
}

// capacityCells is the three resources, each as requested, allocatable and used.
// Requested is what the scheduler committed, the sum of live pods' requests,
// which internal/overview carries in `Capacity.Used` despite the name. Used is
// actual consumption, which no Kubernetes call answers: the metrics backend's
// newest history sample or metrics-server's reading of now (usedNow), or a state
// where neither answers. The gap between the two is what the panel exists to
// show.
func capacityCells(name string, machines overview.ClusterOverview) []api.CapacityCell {
	// Paired by field, not by matching `Capacity.Name`.
	panels := []struct {
		capacity overview.Capacity
		used     api.Reading
	}{
		{machines.CPU, usedNow(machines.CPUHistory, machines, machines.CPU, machines.Now.CPUMilli)},
		{machines.Memory, usedNow(machines.MemoryHistory, machines, machines.Memory, machines.Now.MemoryBytes)},
		// A pod slot is claimed, not consumed: there is no separate usage figure.
		{machines.Pods, api.NoReading("a pod slot is claimed rather than consumed, so there is no separate usage figure")},
	}

	// The node list for all three: these are sums over the machines' allocatable.
	// The pod figure counts the slots kubelets allow, not pods.
	nodeList := listLink(name, "Node", "", "", "Every node in this cluster")

	cells := make([]api.CapacityCell, 0, len(panels))
	for _, panel := range panels {
		capacity := panel.capacity
		cell := api.CapacityCell{Label: capacity.Name}
		// The link is on the caption, never on a 6px bar.
		cell.Ref = refPtr(nodeList.Say(capacity.Name + " requested"))
		cell.Tone = toneOf(capacity.Class())
		if capacity.Known() {
			cell.Capacity = api.ReadingOf(float64(capacity.Total), true, resourceFigure(capacity.Unit, capacity.Total))
			cell.Requested = api.ReadingOf(float64(capacity.Used), true, resourceFigure(capacity.Unit, capacity.Used))
			cell.RequestedOf = resourceFraction(capacity.Unit, float64(capacity.Used), capacity.Total)
		} else {
			// No capacity reported: null and untrustworthy, never 0, which reads as idle.
			cell.Capacity = api.NoReading("the cluster reported no capacity")
			cell.Requested = api.NoReading(whyNoRequests(machines))
		}
		used := panel.used
		cell.Used = &used
		if capacity.Known() && used.Trustworthy && used.Value != nil {
			cell.UsedOf = resourceFraction(capacity.Unit, *used.Value, capacity.Total)
		}
		cells = append(cells, cell)
	}
	return cells
}

// requestedPanel is the name internal/overview degrades when the pod list fails,
// zeroing the requested figures and their totals. Matching it lets the cell say
// the pod list was refused instead of "no capacity"; a miss costs only the
// sentence.
const requestedPanel = "Requested CPU and memory"

func whyNoRequests(machines overview.ClusterOverview) string {
	for _, panel := range machines.Degraded {
		if panel.Panel == requestedPanel {
			return "the pod list could not be read, so nothing was summed: " + panel.Reason
		}
	}
	return "the cluster reported no capacity"
}

// usedNow is what a resource is using now: the newest history sample where there
// is one, so the line beside the tile ends at the tile's figure, and
// metrics-server's current reading where there is not (a metrics-server-only
// cluster still has a figure; the series panel says there is no history). Where
// neither has a figure, the tile names what stopped the reading of now
// (metrics-server refused, failing, or with no machine measured yet); only when
// metrics-server is not installed do the series' reasons stand.
func usedNow(series overview.History, machines overview.ClusterOverview, shape overview.Capacity, now int64) api.Reading {
	if len(series.Points) == 0 && machines.NowFrom.OK {
		text := resourceFigure(shape.Unit, now)
		return api.ReadingOf(float64(now), true, text)
	}
	if len(series.Points) == 0 {
		if why, ok := nowUnmeasured(machines); ok {
			return api.NoReading(why)
		}
	}
	return latestUsed(series, machines.History, shape)
}

// latestUsed is the most recent sample of one series, or why there is none: this
// series failed while the other worked, no backend answered, or an empty window
// (a working cluster). None of them is 0.
func latestUsed(series overview.History, source overview.Availability, shape overview.Capacity) api.Reading {
	switch {
	case series.Unavailable != "":
		return api.NoReading(series.Unavailable)
	case !source.OK:
		return api.NoReading(source.Detail)
	case len(series.Points) == 0:
		return api.NoReading("the metrics backend answered and had no samples in this window")
	}
	point := series.Points[len(series.Points)-1]
	// Spelled by the Nodes list's functions.
	text := resourceFigure(shape.Unit, int64(point.Value))
	return api.ReadingOf(point.Value, true, text)
}

// topPanel is the hottest-pods leaderboards. `usage` says whether a backend
// answered, so an empty board is never claimed for a backend nothing queried.
//
//	ok           a backend answered and named some pods
//	empty        a backend answered and no pod is using anything measurable
//	absent       there is nothing installed to ask
//	unreachable  something is configured and it did not answer
//	(any)        metrics-server was asked and failed: stateOf's answer
//
// A failure carrying its error is classified by stateOf (`credentials_expired`,
// `forbidden`, `unreachable`, `timeout`): `configured` only says whether
// Prometheus is configured and would misread a 403 as not installed. A 404
// (metrics-server not installed), or a failure with no error (no client, a
// panic), falls to `configured`: absent, or `unreachable` and retryable with the
// backends' words in `detail`.
func topPanel(cluster string, cpu, memory overview.Top,
	usage overview.Availability, configured bool) api.List[api.TopBoard] {

	scope := api.Scope{Cluster: cluster, Resource: "pods", Verb: "list"}
	if !usage.OK {
		if usage.Cause != nil && !apierrors.IsNotFound(usage.Cause) {
			// Scoped to what failed (pods.metrics.k8s.io for metrics-server), so a refusal
			// names the missing permission.
			failed := scope
			if usage.Resource != "" {
				failed.Resource = usage.Resource
			}
			failure := stateOf(usage.Cause, failed)
			reason := failure.Reason
			// Both backends' words where Prometheus was asked too.
			if usage.Detail != reason.Message {
				reason = reason.WithDetail(usage.Detail)
			}
			return api.ListFailed[api.TopBoard](failure.State, reason)
		}
		if configured {
			return api.ListFailed[api.TopBoard](api.StateUnreachable,
				api.Because(api.CodeClusterUnreachable,
					"this cluster's metrics backend did not answer").
					WithDetail(usage.Detail).In(scope).Retry(true))
		}
		return api.ListFailed[api.TopBoard](api.StateAbsent,
			api.Because(api.CodeNotInstalled,
				"no metrics backend was found on this cluster").
				WithDetail(usage.Detail).In(scope).Retry(true))
	}

	boards := make([]api.TopBoard, 0, 2)
	// A board with no rows is left out: TopBoard has no state, and an empty one would
	// be an `ok` claiming nothing uses memory.
	if len(cpu.Pods) > 0 {
		boards = append(boards, topBoardOf(cluster, "Hottest pods, CPU", cpu,
			func(p overview.PodUsage) (float64, string) { return float64(p.CPUMilli), p.CPUText() }))
	}
	if len(memory.Pods) > 0 {
		boards = append(boards, topBoardOf(cluster, "Hottest pods, memory", memory,
			func(p overview.PodUsage) (float64, string) { return float64(p.MemoryBytes), p.MemoryText() }))
	}
	if len(boards) == 0 {
		return api.NothingBecause[api.TopBoard](api.Because(api.CodeEmptyScope,
			"the metrics backend answered and no pod on this cluster is using anything measurable").
			WithDetail(usage.Detail).In(scope))
	}
	return api.Items(boards)
}

func topBoardOf(cluster, label string, top overview.Top,
	value func(overview.PodUsage) (float64, string)) api.TopBoard {

	board := api.TopBoard{Label: label, Entries: make([]api.TopEntry, 0, len(top.Pods))}
	for _, p := range top.Pods {
		number, text := value(p)
		// The board's link, built by the function that knows whether a kind has a
		// namespace.
		to := objectLink(cluster, "Pod", p.Namespace, p.Name, p.Namespace+"/"+p.Name, "Open "+p.Name)
		board.Entries = append(board.Entries, api.TopEntry{
			Ref:     api.Ref{Text: to.Text, Href: to.Href, Hint: to.Hint},
			Reading: api.ReadingOf(number, true, text),
		})
	}
	return board
}

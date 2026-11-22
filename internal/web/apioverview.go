package web

import (
	"math"
	"slices"
	"strconv"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/overview"
	"github.com/thonicdev/kubetower/internal/resources"
)

// The dashboard's panels beyond the four `overviewOf` fills: `alerts`,
// `trouble`, `restarting`, `nodes`, `phases` and `history`, as `api/openapi.yaml`
// declares them; the panels an operator acts on. Everything here is a mapping:
// `internal/overview` decides what counts as healthy, the thresholds and what an
// unmeasured reading looks like.

// overviewPanels fills everything on the dashboard that `overviewOf` did not. It
// takes both halves because `allWell` is a judgment over both. `workloads` is
// nil when that fetch failed on its own: the machine panels are still drawn, and
// the workload panels carry the failure.
func overviewPanels(out *api.Overview, name string,
	machines overview.ClusterOverview, workloads *overview.WorkloadsOverview,
	workloadsErr error, metrics overview.Metrics) {

	fillMachinePanels(out, name, machines)

	if workloads == nil {
		fillWorkloadsFailure(out, name, workloadsErr)
	} else {
		fillWorkloadPanels(out, name, *workloads)
	}

	// One panel with one answer, so a value envelope.
	out.History = clusterHistory(machines, metrics)
	if refusal := nodesRefusal(name, machines); refusal != nil {
		out.History = api.ValueFailed[api.MetricsPanel](refusal.State, refusal.Reason)
	}

	// Last: it reads both halves' Degraded slices, and AllWell depends on it.
	fillNotShown(out, machines, workloads)
	fillAllWell(out, machines, workloads)
}

// nodesRefusal is the node list's refusal as every panel that needs the
// machines answers it (capacity, node alerts, history), or nil; one failure for
// the three, so a client draws it once.
func nodesRefusal(name string, machines overview.ClusterOverview) *api.Failure {
	return stateOf(machines.NodesRefused, api.Scope{Cluster: name, Resource: "nodes", Verb: "list"})
}

// fillMachinePanels is everything that comes from `overview.Cluster`.
func fillMachinePanels(out *api.Overview, name string, machines overview.ClusterOverview) {
	out.Version = machines.Version
	if !machines.Collected.IsZero() {
		collected := machines.Collected
		out.CollectedAt = &collected
	}
	fillEvents(out, name, machines)

	// A person refused the nodes gets no node sentence (a count nobody made) and
	// node alerts that are unknown, not none.
	if refusal := nodesRefusal(name, machines); refusal != nil {
		out.Alerts = api.ListFailed[api.AlertEntry](refusal.State, refusal.Reason)
		return
	}

	// The head's node sentence and its tone. Healthy is not "Ready": a Ready node
	// can be under memory pressure or cordoned.
	nodeList := listLink(name, "Node", "", "", "Every node in this cluster")
	line := strconv.Itoa(machines.Healthy) + " of " + strconv.Itoa(machines.Total) + " nodes healthy"
	tone := overview.ClassWarn
	switch {
	case machines.Total == 0:
		line, tone = "no nodes reported", overview.ClassNone
	case machines.Healthy == machines.Total:
		tone = overview.ClassOK
	case machines.Healthy == 0:
		tone = overview.ClassBad
	}
	summary := refOf(nodeList.Say(line))
	out.NodesSummary = &summary
	out.NodesTone = toneOf(tone)

	alerts := make([]api.AlertEntry, 0, len(machines.Alerts))
	for _, alert := range machines.Alerts {
		// Kind and Name, never a split of the printed Subject, so a node's name cannot
		// be passed as a namespace.
		alerts = append(alerts, api.AlertEntry{
			Subject: refOf(objectLink(name, alert.Kind, "", alert.Name, alert.Subject, "Open "+alert.Name)),
			Summary: alert.Summary,
			Detail:  alert.Detail,
			Tone:    toneOf(alert.Class),
		})
	}
	out.Alerts = api.Items(alerts)
}

// fillEvents is the warning feed, read whether or not the nodes were.
func fillEvents(out *api.Overview, name string, machines overview.ClusterOverview) {
	events := make([]api.EventEntry, 0, len(machines.Events))
	for _, line := range machines.Events {
		entry := api.EventEntry{
			// Text is the message; the other members are what a message alone loses (the
			// object to open, the count of a repeated BackOff).
			Text:    line.Message,
			Hint:    line.Reason,
			Age:     line.Age,
			Reason:  line.Reason,
			Message: line.Message,
			Count:   int(line.Count),
			Tone:    toneOf(line.Class),
		}
		if !line.When.IsZero() {
			at := line.When
			entry.At = &at
		}
		// scopeLink: an event about a cluster-scoped object has no namespace and leads
		// nowhere.
		scope := refOf(scopeLink(name, "Event", line.Namespace))
		entry.Scope = &scope
		object := refOf(objectLink(name, line.Kind, line.Namespace, line.Name, line.Object, "Open "+line.Object))
		entry.Object = &object
		entry.Href = object.Href
		events = append(events, entry)
	}
	out.Events = api.Items(events)
	// How many there were before the cut, never fewer than carried (zero means
	// nothing was cut). Absent when the event read failed: the panel is then
	// `empty` with `notShown` naming it, and a 0 would read as a count.
	if slices.ContainsFunc(machines.Degraded, func(p overview.PanelError) bool {
		return p.Panel == overview.PanelEvents
	}) {
		return
	}
	eventsTotal := max(machines.EventsTotal, len(events))
	out.EventsTotal = &eventsTotal
}

// fillWorkloadPanels is everything that comes from `overview.Workloads`.
func fillWorkloadPanels(out *api.Overview, name string, w overview.WorkloadsOverview) {
	podList := listLink(name, "Pod", "", strconv.Itoa(w.Pods)+" pods", "Every pod in this cluster")
	summary := refOf(podList)
	out.WorkloadsSummary = &summary

	// The counts panel is the workload kind tiles; the node figures are the head's
	// `nodesSummary` pill, which leads to the Nodes page.
	counts := make([]api.CountEntry, 0, len(w.Kinds))
	for _, kind := range w.Kinds {
		healthy := kind.Healthy
		counts = append(counts, api.CountEntry{
			Kind:    kind.Slug,
			Label:   kind.Kind,
			Count:   kind.Total,
			Healthy: &healthy,
			Note:    kind.Note,
			Tone:    toneOf(kind.Class),
			Ref:     refPtr(listLink(name, kind.Kind, "", kind.Kind, "Every "+kind.Kind+" in this cluster")),
		})
	}
	out.Counts = api.Items(counts)

	phases := make([]api.PhaseEntry, 0, len(w.Phases))
	for _, segment := range w.Phases {
		// No per-segment destination: the list cannot be filtered by phase, and a
		// "Failed 3" landing on the unfiltered list would lie.
		phases = append(phases, api.PhaseEntry{
			Phase: segment.Label,
			Count: segment.Value,
			Tone:  toneOf(segment.Class),
		})
	}
	out.Phases = api.Items(phases)

	trouble := make([]api.TroubleEntry, 0, len(w.Trouble))
	for _, item := range w.Trouble {
		entry := api.TroubleEntry{
			Kind:   refPtr(listLink(name, item.Kind, "", item.Kind, "Every "+item.Kind+" in this cluster")),
			Scope:  refPtr(scopeLink(name, item.Kind, item.Namespace)),
			Object: refOf(objectLink(name, item.Kind, item.Namespace, item.Name, item.Name, "Open "+item.Name)),
			Detail: item.Detail,
			// The server's wording; the instant is the client's to show on hover.
			Age:  item.Age,
			Tone: toneOf(item.Class),
		}
		// A zero time travels as an absent member, not as year one.
		if !item.Since.IsZero() {
			since := item.Since
			entry.Since = &since
		}
		trouble = append(trouble, entry)
	}
	out.Trouble = api.Items(trouble)

	restarting := make([]api.RestartEntry, 0, len(w.Restarting))
	for _, item := range w.Restarting {
		entry := api.RestartEntry{
			Scope:     refPtr(scopeLink(name, "Pod", item.Namespace)),
			Object:    refOf(objectLink(name, "Pod", item.Namespace, item.Name, item.Name, "Open "+item.Name)),
			Container: item.Container,
			// Without the init mark a crash-looping init container and app container look
			// alike and send the reader to different logs. Always carried.
			Init:     item.Init,
			Restarts: int(item.Restarts),
			Reason:   item.Reason,
			// Relative in the cell, exact on hover from `lastExitAt`. `overview.Restarting`
			// writes `AgoTitle` when the kubelet no longer holds the exit, so an absent
			// `lastExitAt` must not draw a dash.
			Age:  item.Ago,
			Tone: toneOf(item.Class),
		}
		// A container whose exit the kubelet does not hold, or holds without an instant:
		// an absent member, as TroubleEntry.Since.
		if !item.LastExit.IsZero() {
			at := item.LastExit
			entry.LastExitAt = &at
		}
		restarting = append(restarting, entry)
	}
	out.Restarting = api.Items(restarting)
	// How many there were before the cut.
	restartingTotal := max(w.RestartingTotal, len(restarting))
	out.RestartingTotal = &restartingTotal
}

// fillWorkloadsFailure is the arm where the pod list itself could not be read.
// The machine half is drawn anyway, and the four workload panels say why rather
// than being missing, which the client would draw as "Not answered", a fault in
// the product.
func fillWorkloadsFailure(out *api.Overview, name string, err error) {
	message := "the workloads on this cluster could not be read"
	detail := ""
	if err != nil {
		detail = err.Error()
	}
	scope := api.Scope{Cluster: name, Resource: "pods", Verb: "list"}
	reason := api.Because(api.CodeReadDenied, message).WithDetail(detail).In(scope).Retry(true)

	out.Counts = api.ListFailed[api.CountEntry](api.StateUnreadable, reason)
	out.Phases = api.ListFailed[api.PhaseEntry](api.StateUnreadable, reason)
	out.Trouble = api.ListFailed[api.TroubleEntry](api.StateUnreadable, reason)
	out.Restarting = api.ListFailed[api.RestartEntry](api.StateUnreadable, reason)
}

// clusterHistory is the two sparklines, as numbers, in the same `MetricsPanel`
// an object's panel uses. Three answers: metrics-server and no Prometheus is a
// normal cluster, `absent` with the source's sentence; no metrics backend in the
// build is a different absence; a backend that answered with an empty window is
// `ok` with two empty series, each carrying why.
func clusterHistory(machines overview.ClusterOverview, metrics overview.Metrics) api.Value[api.MetricsPanel] {
	if metrics == nil {
		return api.ValueFailed[api.MetricsPanel](api.StateAbsent,
			api.Because(api.CodeNotInstalled, "this build has no metrics backend").Retry(false))
	}
	if !machines.History.OK {
		if failure := historyFailure(machines); failure != nil {
			return api.ValueFailed[api.MetricsPanel](failure.State, failure.Reason)
		}
		return api.ValueFailed[api.MetricsPanel](api.StateAbsent,
			api.Because(api.CodeNotInstalled, "this cluster keeps no series").
				WithDetail(machines.History.Detail).Retry(true))
	}

	panel := api.MetricsPanel{}
	for _, series := range []overview.History{machines.CPUHistory, machines.MemoryHistory} {
		if series.Title == "" && len(series.Points) == 0 && series.Unavailable == "" {
			continue
		}
		line := api.MetricsSeries{
			Key:   seriesKey(series.Title),
			Label: series.Title,
			Data:  seriesOf(series),
		}
		// Why a line has no samples, with an object line's code: `not_installed` for
		// series this Prometheus does not hold, `empty_scope` for an empty window.
		if len(series.Points) == 0 && series.Unavailable != "" {
			reason := lineReason(series)
			line.Reason = &reason
		}
		panel.Series = append(panel.Series, line)
	}
	if len(panel.Series) == 0 {
		return api.ValueFailed[api.MetricsPanel](api.StateAbsent,
			api.Because(api.CodeNotInstalled, "this cluster keeps no series").
				WithDetail(machines.History.Detail).Retry(true))
	}

	// No icon row and no span buttons: both series are drawn side by side.
	// `zoomable` is false because `getMetricsPanel` requires a kind and a name, so
	// the cluster's series cannot be re-asked at another window.
	zoomable := false
	panel.Zoomable = &zoomable
	panel.Caption = clusterHistoryCaption(machines)
	return api.Data(panel)
}

// historyFailure is the failure a history that could not be read carries, or nil
// where it is an absence. No Prometheus to ask (`MissingBackend`) is
// `absent`/`not_installed`, which the Overview may hide; a Prometheus that did
// not answer either range query (`MissingFailed`) is a failure, classified as
// `lineReason` does, and stays drawn. A panic carries neither and stays
// `absent`.
func historyFailure(machines overview.ClusterOverview) *api.Failure {
	for _, line := range []overview.History{machines.CPUHistory, machines.MemoryHistory} {
		if line.Missing != overview.MissingFailed {
			continue
		}
		reason := lineReason(line)
		state := api.StateUnreachable
		if failure := stateOf(line.Err, api.Scope{}); failure != nil {
			state = failure.State
		}
		if state == api.StateEmpty || state == api.StateOK {
			state = api.StateUnreachable
		}
		if machines.History.Detail != "" && machines.History.Detail != reason.Message {
			reason = reason.WithDetail(machines.History.Detail)
		}
		return &api.Failure{State: state, Reason: reason}
	}
	return nil
}

// clusterHistoryCaption is what was read, so a flat line has an attribution:
// Prometheus's container sums, the pods' cgroups where it fell back, and why a
// line is empty.
func clusterHistoryCaption(machines overview.ClusterOverview) string {
	if machines.History.Detail == "" {
		return ""
	}
	return machines.History.Detail
}

// seriesKey is the stable key a client keys a line by: lower case, no spaces
// (`cpu-used`).
func seriesKey(title string) string {
	out := make([]rune, 0, len(title))
	for _, r := range title {
		switch {
		case r >= 'A' && r <= 'Z':
			out = append(out, r+('a'-'A'))
		case r == ' ':
			out = append(out, '-')
		default:
			out = append(out, r)
		}
	}
	return string(out)
}

// fillNotShown is the page-level list of what was never looked at, machines
// first. A `PanelError` is a panel skipped for a missing prerequisite (events
// this account may not list, the pod list figures are summed from), not a failed
// envelope: without this list "the cluster is quiet" and "we were not allowed to
// look" would look the same. A panicked task's panel even answers `empty`.
//
// A failed envelope per spoilt panel would repeat one failed read in every box,
// so the reason is said here once, and each entry names the members it spoilt
// (`affects`), which then carry a mark pointing back here: never a count, never
// "none". The server names them, knowing which read feeds which panel.
func fillNotShown(out *api.Overview, machines overview.ClusterOverview, workloads *overview.WorkloadsOverview) {
	for _, panel := range machines.Degraded {
		out.NotShown = append(out.NotShown, skipped(panel))
	}
	if workloads != nil {
		for _, panel := range workloads.Degraded {
			out.NotShown = append(out.NotShown, skipped(panel))
		}
	}
}

func skipped(panel overview.PanelError) api.SkippedPanel {
	// Never nil, which marshals to `null` where the contract says array. An unmapped
	// panel is a defect the tests catch.
	affects := spoiltBy[panel.Panel]
	if affects == nil {
		affects = []string{}
	}
	return api.SkippedPanel{Panel: panel.Panel, Reason: panel.Reason, Affects: affects}
}

// spoiltBy is which payload members each failed task left incomplete. One read
// can feed several panels (the Deployments list is behind a `counts` tile and
// the `trouble` rows). Keys are `internal/overview`'s constants and values are
// `api.Overview`'s property names:
//   - `Kubernetes version`: `version`.
//   - `Warning events`: `events`.
//   - `Requested CPU and memory` is the pod list: `capacity`.
//   - the four workload kinds each drop a tile from `Kinds` and their rows
//     from `Trouble`, rather than being counted as zero.
//   - `CronJobs` drops a tile only.
//
// `top`, `phases`, `restarting`, `alerts` and `history` are absent on purpose:
// their pod list is fatal on the workloads side, and the history reports its own
// absence.
var spoiltBy = map[string][]string{
	overview.PanelVersion:      {"version"},
	overview.PanelEvents:       {"events"},
	overview.PanelRequests:     {"capacity"},
	overview.PanelDeployments:  {"counts", "trouble"},
	overview.PanelStatefulSets: {"counts", "trouble"},
	overview.PanelDaemonSets:   {"counts", "trouble"},
	overview.PanelJobs:         {"counts", "trouble"},
	overview.PanelCronJobs:     {"counts"},
}

// fillAllWell is the badge, a server judgment so a page missing panels never
// raises it. `ClusterOverview.AllWell` is the machine half and consults
// `Degraded`; the workload half is added here, and a workloads fetch that failed
// outright is not all well (TestAPageThatCouldNotBeReadIsNotAllWell).
func fillAllWell(out *api.Overview, machines overview.ClusterOverview, workloads *overview.WorkloadsOverview) {
	well := machines.AllWell()
	if workloads == nil {
		well = false
	} else {
		well = well &&
			len(workloads.Trouble) == 0 &&
			len(workloads.Restarting) == 0 &&
			len(workloads.Degraded) == 0
	}
	out.AllWell = &well
}

// nowUnmeasured is why metrics-server gave the Capacity tile no figure of now,
// where that is the reason to show: it answered with no machine measured yet, or
// was refused or failed (a panic included). False when metrics-server is not
// installed (a 404), where the history's reasons stand.
func nowUnmeasured(machines overview.ClusterOverview) (string, bool) {
	why := machines.NowFrom.Detail
	switch {
	case why == "":
		return "", false
	case machines.NowNodes.ByNode != nil:
		return why, true
	case machines.NowNodes.Err != nil && !apierrors.IsNotFound(machines.NowNodes.Err):
		return why, true
	}
	return "", false
}

// resourceFigure spells a CPU or memory figure with the Nodes list's functions.
func resourceFigure(unit overview.Unit, value int64) string {
	switch unit {
	case overview.UnitCores:
		return resources.CPUSize(value)
	case overview.UnitBytes:
		return resources.MemorySize(value)
	default:
		return strconv.FormatInt(value, 10)
	}
}

// resourceFraction is a capacity line's hover, `used / total` in the total's
// unit (resources.Fraction). Empty where there is no whole.
func resourceFraction(unit overview.Unit, used float64, total int64) string {
	if total <= 0 || math.IsNaN(used) || math.IsInf(used, 0) {
		return ""
	}
	switch unit {
	case overview.UnitCores:
		// The unrounded sample, as the chooser divides it (fleet.ts), so the two agree.
		return resources.CPUFraction(used, total)
	case overview.UnitBytes:
		return resources.BytesFraction(used, total)
	default:
		return resources.Fraction(used, float64(total), "")
	}
}

// refOf and refPtr turn a `link` into a contract `Ref`: required and optional.
// `refPtr` is nil for a link that leads nowhere and says nothing.
func refOf(l link) api.Ref {
	return api.Ref{Text: l.Text, Href: l.Href, Hint: l.Hint}
}

func refPtr(l link) *api.Ref {
	out := refOf(l)
	if out.Text == "" && out.Href == "" {
		return nil
	}
	return &out
}

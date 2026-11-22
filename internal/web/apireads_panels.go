package web

import (
	"context"
	"fmt"
	"log"
	"math"
	"time"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/api"
	"github.com/thonicdev/kubetower/internal/audit"
	"github.com/thonicdev/kubetower/internal/changes"
	"github.com/thonicdev/kubetower/internal/detail"
	"github.com/thonicdev/kubetower/internal/identity"
	"github.com/thonicdev/kubetower/internal/metrics"
	"github.com/thonicdev/kubetower/internal/overview"
	"github.com/thonicdev/kubetower/textutil"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (r apiReads) Metrics(ctx context.Context, q api.MetricsQuery) (api.MetricsPanel, *api.Failure) {
	scope := api.Scope{Cluster: q.Cluster, Resource: q.Kind, Namespace: q.Namespace, Name: q.Name}
	source := r.s.history()
	if source == nil {
		return api.MetricsPanel{}, api.Absent("this build has no metrics backend").Within(scope)
	}
	window := overview.WindowFor(q.Range)
	if isVolumeClaimKind(q.Kind) {
		return r.volumeMetrics(ctx, q, scope, source, window)
	}
	// The series come from the console's own monitoring backend, as the console, and
	// Prometheus has no RBAC. So the person is reviewed with the object page's own
	// question, `get` on that pod or node by name: who may not read the object is not
	// shown how it behaves.
	if fail := r.mayReadObject(ctx, q, scope); fail != nil {
		return api.MetricsPanel{}, fail
	}
	var object overview.ObjectHistory
	if isNodeKind(q.Kind) {
		object = overview.NodeHistory(ctx, source, q.Cluster, q.Name, window)
	} else {
		object = overview.PodHistory(ctx, source, q.Cluster, q.Namespace, q.Name, window)
		// The pod is read only when a line came back empty, the one case its run can
		// explain.
		if anyEmpty(object) {
			run := r.finishedRun(ctx, q)
			object = object.ExplainRun(run, time.Now()).
				CountRun(ctx, source, q.Cluster, q.Namespace, q.Name, run)
		}
	}
	panel := api.MetricsPanel{}
	// Only when a usage line has nothing to draw: otherwise its newest sample is
	// already the figure.
	var now *metrics.Usage
	if len(object.CPU.Points) == 0 || len(object.Memory.Points) == 0 {
		now = r.currentUsage(ctx, q)
	}
	// Every line the object has, keyed by a constant chart key (the icon row's, not
	// the lowered title, which differs for a pod's disk line) so a client groups on a
	// string both sides were handed. Five lines, the network's two included, or the
	// panel would find nothing under the Network icon. A line that could not be read
	// is sent empty with its `History.Unavailable` sentence: a missing exporter and
	// an empty window are fixed differently.
	for _, line := range []struct {
		key, chart string
		history    overview.History
	}{
		{overview.ChartCPU, overview.ChartCPU, object.CPU},
		{overview.ChartMemory, overview.ChartMemory, object.Memory},
		{seriesNetworkRx, overview.ChartNetwork, object.NetIn},
		{seriesNetworkTx, overview.ChartNetwork, object.NetOut},
		{overview.ChartDisk, overview.ChartDisk, object.Disk},
	} {
		if line.history.Title == "" && len(line.history.Points) == 0 && line.history.Unavailable == "" {
			continue
		}
		series := api.MetricsSeries{
			Key:   line.key,
			Chart: line.chart,
			Label: line.history.Title,
			Unit:  unitOf(line.history.Unit),
			Data:  seriesOf(line.history),
		}
		if len(line.history.Points) == 0 && line.history.Unavailable != "" {
			reason := lineReason(line.history)
			series.Reason = &reason
		}
		if len(line.history.Points) == 0 {
			series.Current = currentReading(line.key, line.history, now)
			series.Period = line.history.Period
			series.RunTotal = runTotalReading(line.history)
		}
		panel.Series = append(panel.Series, series)
	}
	if len(panel.Series) == 0 {
		return api.MetricsPanel{}, api.Absent("no series was available for this object").Within(scope)
	}
	// The icon row, the span buttons, and whether the overlay can open.
	// `ObjectHistory.Options()` already carries the sentence for a measurement this
	// cluster cannot answer. The selected chart is not a parameter of this
	// operation: it defaults to CPU where readable, else the first readable one.
	panel.Metrics = metricChoices(measurable(object.Options(), panel.Series), overview.ChartCPU)
	panel.Windows = metricWindows(overview.Windows, object.Window.Label)
	_, zoomable := overview.ZoomSeriesFor(object.CPU.Title)
	panel.Zoomable = boolOf(zoomable)
	panel.Caption = metricsCaptionOf(object, panel.Series)
	return panel, nil
}

// volumeMetrics is a claim's panel: used and capacity bytes from the kubelet's
// volume counters. The person is reviewed with `get` on the claim, by name, like
// the pod and node panels. Where nothing can be drawn - no backend, no series
// for this claim, a failed read - the answer is `absent` whatever the cause,
// because the claim's panel draws nothing at all in each of those cases; the
// cause is kept in the process log only, never sent.
func (r apiReads) volumeMetrics(ctx context.Context, q api.MetricsQuery, scope api.Scope, source overview.Metrics, window overview.Window) (api.MetricsPanel, *api.Failure) {
	if fail := r.mayReadObject(ctx, q, scope); fail != nil {
		return api.MetricsPanel{}, fail
	}
	history := overview.ReadVolumeHistory(ctx, source, q.Cluster, q.Namespace, q.Name, window)
	if history.Err != nil {
		log.Printf("volume metrics for %s/%s: %v", q.Namespace, q.Name, history.Err)
	}
	if !history.Drawable() {
		return api.MetricsPanel{}, api.Absent("no volume series was available for this claim").Within(scope)
	}
	panel := api.MetricsPanel{}
	for _, line := range []struct {
		key     string
		history overview.History
	}{
		{"volume_used", history.Used},
		{"volume_capacity", history.Capacity},
	} {
		if len(line.history.Points) == 0 {
			continue
		}
		panel.Series = append(panel.Series, api.MetricsSeries{
			Key:   line.key,
			Chart: volumeChart,
			Label: line.history.Title,
			Unit:  unitOf(line.history.Unit),
			Data:  seriesOf(line.history),
		})
	}
	panel.Metrics = []api.MetricsChoice{{Key: volumeChart, Label: "Storage", Available: true, Selected: true}}
	panel.Windows = metricWindows(overview.Windows, window.Label)
	return panel, nil
}

// currentUsage is the object's reading of now from the instant source, out of
// the same cache that fills the Pods and Nodes lists' CPU and memory columns.
// nil when there is no source or it holds no figure for this object. Through the
// cache so a Prometheus-only cluster does not pay a 404 and a tunnel per mount;
// usageAnswer waits for a first read, since this panel has no next tick.
//
// A pod's namespace is its own, so the entry is keyed by bare name; a node's is
// the Nodes list's entry. An entry present only for its disk (ComputeUnmeasured)
// is no reading of CPU or memory. No second review: `mayReadObject` already
// asked for `get` on this object.
func (r apiReads) currentUsage(ctx context.Context, q api.MetricsQuery) *metrics.Usage {
	if r.s.usage == nil {
		return nil
	}
	kind, namespace := "pods", q.Namespace
	if isNodeKind(q.Kind) {
		kind, namespace = "nodes", ""
	}
	figures, _ := r.s.usageAnswer(ctx, q.Cluster, kind, namespace)
	figure, ok := figures[q.Name]
	if !ok || figure.ComputeUnmeasured {
		return nil
	}
	return &metrics.Usage{CPUMillicores: figure.CPUMillicores, MemoryBytes: figure.MemoryBytes, From: figure.From}
}

// currentReading is one line's figure out of the object's reading of now, or nil:
// only CPU and memory are read that way.
func currentReading(key string, history overview.History, now *metrics.Usage) *api.Reading {
	if now == nil {
		return nil
	}
	var value int64
	switch key {
	case overview.ChartCPU:
		value = now.CPUMillicores
	case overview.ChartMemory:
		value = now.MemoryBytes
	default:
		return nil
	}
	// Formatted by the type that owns the unit, as every table spells it.
	reading := api.ReadingOf(float64(value), true, overview.Capacity{Unit: history.Unit, Used: value}.UsedText())
	return &reading
}

// runTotalReading is a finished pod's network line's whole run, in bytes.
func runTotalReading(history overview.History) *api.Reading {
	if history.RunTotal == nil {
		return nil
	}
	value := history.RunTotal.Value
	reading := api.ReadingOf(value, true,
		overview.Capacity{Unit: overview.UnitBytes, Used: int64(math.Round(value))}.UsedText())
	return &reading
}

// metricsCaptionOf is the panel's `caption`: the span, once a line is drawn.
func metricsCaptionOf(object overview.ObjectHistory, lines []api.MetricsSeries) string {
	for _, line := range lines {
		for _, value := range line.Data.Values {
			if value != nil {
				return metricsCaption(object)
			}
		}
	}
	return ""
}

// mayReadObject reviews the person behind ctx for the object a metrics panel is
// about: `get nodes`, `get persistentvolumeclaims` or `get pods`, by name. Any other kind is reviewed as a pod,
// which is what PodHistory reads it as.
func (r apiReads) mayReadObject(ctx context.Context, q api.MetricsQuery, scope api.Scope) *api.Failure {
	question := access.Attributes{Verb: "get", Version: "v1", Resource: "pods", Namespace: q.Namespace, Name: q.Name}
	if isNodeKind(q.Kind) {
		question = access.Attributes{Verb: "get", Version: "v1", Resource: "nodes", Name: q.Name}
	}
	if isVolumeClaimKind(q.Kind) {
		question = access.Attributes{Verb: "get", Version: "v1", Resource: "persistentvolumeclaims", Namespace: q.Namespace, Name: q.Name}
	}
	cluster := q.Cluster
	if r.s.clusters != nil {
		if resolved, ok := r.s.clusters.Resolve(q.Cluster); ok {
			cluster = resolved
		}
	}
	if err := r.s.gate.Check(ctx, cluster, question); err != nil {
		return stateOf(err, scope)
	}
	return nil
}

// finishedRun is the pod's run when it has finished; nil otherwise or when the
// pod cannot be read. The read is under the same `get pods` question
// mayReadObject asked, and a failure leaves the panel's own sentence in place.
func (r apiReads) finishedRun(ctx context.Context, q api.MetricsQuery) *overview.Run {
	if r.s.clusters == nil || r.s.gate == nil {
		return nil
	}
	client, err := r.s.clusters.Client(q.Cluster)
	if err != nil || client == nil || client.Clients == nil {
		return nil
	}
	cluster := q.Cluster
	if resolved, ok := r.s.clusters.Resolve(q.Cluster); ok {
		cluster = resolved
	}
	question := access.Attributes{Verb: "get", Version: "v1", Resource: "pods", Namespace: q.Namespace, Name: q.Name}
	var pod *corev1.Pod
	if _, err := r.s.gate.Read(ctx, cluster, question, func(ctx context.Context) error {
		var err error
		pod, err = client.Clients.CoreV1().Pods(q.Namespace).Get(ctx, q.Name, metav1.GetOptions{})
		return err
	}); err != nil {
		return nil
	}
	return overview.RunOf(pod)
}

// anyEmpty says whether a line came back empty from a backend that answered, the
// one state a finished pod's run can explain.
func anyEmpty(object overview.ObjectHistory) bool {
	for _, line := range []overview.History{object.CPU, object.Memory, object.NetIn, object.NetOut} {
		if line.Missing == overview.MissingEmpty {
			return true
		}
	}
	return false
}

// The network chart's two lines as the contract names them: the API's words for
// the wire, so not in internal/overview.
const (
	seriesNetworkRx = "network_rx"
	seriesNetworkTx = "network_tx"
)

// unitOf is the unit a line's samples are counted in, as the contract spells it;
// the client never invents one. CPU is millicores because `overview.fill` scales
// it to match every table; both network lines are `bytes/s`, so they share an
// axis.
func unitOf(unit overview.Unit) string {
	switch unit {
	case overview.UnitCores:
		return "mcores"
	case overview.UnitBytes:
		return "bytes"
	case overview.UnitBytesPerSecond:
		return "bytes/s"
	default:
		return ""
	}
}

// metricsCaption is what was read and over what span (from `ObjectHistory`), so
// a flat line has an attribution.
func metricsCaption(object overview.ObjectHistory) string {
	span := object.Window.Label
	if span == "" {
		return ""
	}
	return "the last " + span
}

// lineReason is why one line has no samples, with a code a client can branch on.
// The period buttons gray on the first case and must not on the second:
//
//   - `not_installed`: no Prometheus, or a counter it does not export in the
//     shape the line selects on;
//   - `empty_scope`: the backend answered and the window holds no sample;
//   - a failed query, classified by `stateOf` from the error itself
//     (`credentials_expired`, `verb_denied`, `timeout`, or retryable
//     `cluster_unreachable`), so a cause in credentials is never told "ask
//     again", and every surface names one failure one way.
func lineReason(history overview.History) api.Reason {
	switch history.Missing {
	case overview.MissingEmpty:
		return api.Because(api.CodeEmptyScope, history.Unavailable)
	case overview.MissingFailed:
		if failure := stateOf(history.Err, api.Scope{}); failure != nil {
			// The classifier's code and retry, the line's own sentence, no added scope.
			reason := failure.Reason
			reason.Message, reason.Scope = history.Unavailable, nil
			return reason
		}
		return api.Because(api.CodeClusterUnreachable, history.Unavailable).Retry(true)
	default:
		return api.Because(api.CodeNotInstalled, history.Unavailable)
	}
}

// measurable offers CPU and memory where the line carries a reading of now.
// `Options` knows only the series, so with metrics-server and no Prometheus it
// would gray the one measurement the panel can show (`kubectl top`'s). Such a
// line turns its control back on and drops the refusal's sentence; the period
// buttons say why there is no history.
func measurable(options []overview.ChartOption, lines []api.MetricsSeries) []overview.ChartOption {
	now := map[string]bool{}
	for _, line := range lines {
		if line.Current != nil {
			now[line.Chart] = true
		}
	}
	out := make([]overview.ChartOption, len(options))
	for i, option := range options {
		if !option.Available && now[option.Key] {
			option.Available, option.Detail = true, ""
		}
		out[i] = option
	}
	return out
}

// metricChoices is the icon row. `selected` is the measurement opened first when
// readable, otherwise the first that is; with nothing readable nothing is
// selected.
func metricChoices(options []overview.ChartOption, selected string) []api.MetricsChoice {
	if selected == "" {
		selected = overview.ChartCPU
	}
	first := ""
	for _, option := range options {
		if option.Available && (first == "" || option.Key == selected) {
			first = option.Key
		}
	}
	selected = first
	out := make([]api.MetricsChoice, 0, len(options))
	for _, option := range options {
		choice := api.MetricsChoice{
			Key:       option.Key,
			Label:     option.Label,
			Available: option.Available,
			Selected:  option.Key == selected,
		}
		// A reason only where the control is unavailable.
		if !option.Available && option.Detail != "" {
			reason := api.Because(api.CodeNotInstalled, option.Detail)
			choice.Reason = &reason
		}
		out = append(out, choice)
	}
	return out
}

// metricWindows is the span buttons.
func metricWindows(windows []overview.Window, selected string) []api.MetricsWindow {
	out := make([]api.MetricsWindow, 0, len(windows))
	for _, window := range windows {
		out = append(out, api.MetricsWindow{
			Key:      window.Label,
			Label:    window.Label,
			Selected: window.Label == selected,
		})
	}
	return out
}

// seriesOf keeps the gaps: a missing sample is null, never omitted, or every
// later sample shifts onto the wrong moment and the chart looks right while
// being wrong.
func seriesOf(history overview.History) api.Series {
	out := api.Series{StepSeconds: history.Step.Seconds(), Values: []*float64{}}
	if out.StepSeconds <= 0 {
		out.StepSeconds = 60
	}
	if len(history.Points) > 0 {
		out.StartedAt = history.Points[0].At
	}
	for _, point := range history.Points {
		out.Values = append(out.Values, api.Sample(point.Value))
	}
	if history.Unavailable != "" {
		out.Trustworthy = boolOf(false)
	}
	return out
}

func (r apiReads) Changes(ctx context.Context, ref api.ObjectRef) (api.ChangesPanel, *api.Failure) {
	scope := api.Scope{Cluster: ref.Cluster, Resource: ref.Kind, Namespace: ref.Namespace, Name: ref.Name}
	if r.s.changes == nil {
		return api.ChangesPanel{}, api.Absent("this build has no change reader").Within(scope)
	}
	report, err := r.s.changes.Read(ctx, ref.Cluster, changes.Request{
		Kind: ref.Kind, Namespace: ref.Namespace, Name: ref.Name,
	})
	if err != nil {
		return api.ChangesPanel{}, stateOf(err, scope)
	}
	panel := api.ChangesPanel{Entries: make([]api.HistoryEntry, 0, len(report.Changes))}
	for _, change := range report.Changes {
		panel.Entries = append(panel.Entries, api.HistoryEntry{
			At:      change.At,
			Summary: change.Manager + " " + change.Operation,
		})
	}
	// What shipped and when, beside who wrote which field: which image was running is
	// what someone choosing a rollback reads.
	for _, rollout := range report.Rollouts {
		panel.Rollouts = append(panel.Rollouts, api.Rollout{
			Revision: rollout.Revision,
			At:       rollout.At,
			Name:     rollout.Name,
			Replicas: rollout.Replicas,
			Image:    rollout.Image,
			Current:  rollout.Current,
			Cause:    rollout.Cause,
		})
	}

	// Whether either list is an answer.
	panel.EntriesState, panel.EntriesReason = entriesAnswer(report)
	panel.RolloutsState, panel.RolloutsReason = rolloutsAnswer(report, ref)
	return panel, nil
}

// entriesAnswer is the entries half's own state and reason, so an object that
// records no history does not read as one that never changed: `absent` when the
// object carries no `managedFields` (created before server-side apply, or by a
// controller that sets none), with `report.Note` verbatim; `ok` with entries. A
// failed read is not here: the entries come off the object, so that failure
// fails the whole panel. A pure function of the report.
func entriesAnswer(report changes.Report) (api.State, *api.Reason) {
	if len(report.Changes) > 0 {
		return api.StateOK, nil
	}
	if report.Note == "" {
		// Records changes and there are none. `noteFor` fills the note whenever the list
		// is empty, so this is unreachable today; kept rather than `ok` with nothing in
		// it.
		return api.StateEmpty, nil
	}
	return api.StateAbsent, reasonPtr(api.Because(api.CodeNotSupportedHere, report.Note))
}

// rolloutsAnswer is the rollout half's own state and reason. It reads a different
// object, the ReplicaSets, and can fail on its own (a Forbidden on
// `replicasets` is ordinary under tight RBAC): an unreadable history must not
// read as a workload that never shipped. A kind with no revisions at all gets an
// empty state and an absent member, which is why the report has `Revisioned`. A
// pure function of the report.
func rolloutsAnswer(report changes.Report, ref api.ObjectRef) (api.State, *api.Reason) {
	if !report.Revisioned {
		return "", nil
	}
	if report.RolloutsErr != nil {
		// Classified by `stateOf`, like every failed read on this route. The scope names
		// `replicasets`, the read that was refused, not the object's own kind.
		fail := stateOf(report.RolloutsErr, api.Scope{
			Cluster: ref.Cluster, Resource: "replicasets",
			Namespace: ref.Namespace, Name: ref.Name,
		})
		return fail.State, &fail.Reason
	}
	if len(report.Rollouts) == 0 {
		return api.StateEmpty, nil
	}
	return api.StateOK, nil
}

// RevealEnv is the one door a masked value reaches a view through. The contract
// names the workload; the console addresses the source object, the ConfigMap or
// Secret, which refuses any kind with no data block and so keeps the route from
// becoming a general object reader. `container` is accepted and unused. What it
// answers is `revealedValueOf`, a pure function so it is testable.
//
// The value is read with the console's credentials, which under the server
// profile can read every Secret, so the person is first reviewed for `get` on
// that object by name (a review by kind would over-grant a Role naming one
// Secret). A denial is `forbidden`; a review that could not be made is
// `unreadable` with its own code; neither reads anything.
//
// Then it is recorded like a write (internal/access/record.go): the attempt,
// naming the key, before the read, and the outcome after; an attempt that cannot
// be written refuses the reveal. A `reveal:` process line in the `write:` shape
// is written on every outcome, refusals included, with the key and never the
// value. A reveal whose kind could not be resolved was never reviewed and is not
// an act; the process line still says it.
func (r apiReads) RevealEnv(ctx context.Context, q api.EnvQuery) (api.EnvValue, *api.Failure) {
	scope := api.Scope{Cluster: q.Cluster, Resource: q.Kind, Namespace: q.Namespace, Name: q.Name}
	where, located, decision, fail := r.s.mayReveal(ctx, q, scope)
	if !located {
		r.s.log("%s", revealLine(ctx, q, decision, fail))
		return api.EnvValue{}, fail
	}
	record := r.s.gate.Record(ctx, audit.Reveal, where.Cluster, revealAttributes(where, q), decision)
	record.Key = q.Key
	pending, unrecorded := r.s.gate.Begin(ctx, record)
	if fail != nil {
		r.s.gate.End(ctx, pending, audit.Refused, "")
		r.s.log("%s", revealLine(ctx, q, decision, fail))
		return api.EnvValue{}, fail
	}
	if unrecorded != nil {
		fail := api.Fail(api.StateUnreadable, api.CodeAccessUnreviewable, unrecorded.Error()).Within(scope).Retryable()
		r.s.log("%s", revealLine(ctx, q, decision, fail))
		return api.EnvValue{}, fail
	}
	set, err := r.s.objects.Data(ctx, q.Cluster, detail.Request{
		Kind: q.Kind, Namespace: q.Namespace, Name: q.Name,
	}, q.Key)
	var value api.EnvValue
	if err != nil {
		fail = stateOf(err, scope)
	} else {
		value, fail = revealedValueOf(set, q.Key, scope)
	}
	// One line and one outcome record, on the path every answer leaves by, written
	// from the failure so a new branch cannot forget it. The reason is the failure's
	// code, never its message.
	if fail != nil {
		r.s.gate.End(ctx, pending, audit.Failed, string(fail.Reason.Code))
	} else {
		r.s.gate.End(ctx, pending, audit.Applied, "")
	}
	r.s.log("%s", revealLine(ctx, q, access.Allowed, fail))
	if fail != nil {
		return api.EnvValue{}, fail
	}
	return value, nil
}

// revealedValueOf is what the door says about one key of an object it has read:
// the value, the absence, or the refusal, never an answer made out of a failure
// to decode. A pure function, since the package holds a concrete
// `*detail.Reader` no test can replace.
func revealedValueOf(set detail.DataSet, key string, scope api.Scope) (api.EnvValue, *api.Failure) {
	entry, ok := set.Entry(key)
	if !ok {
		return api.EnvValue{}, api.Absent("no key called " + key + " on this object").Within(scope)
	}

	// A key whose value is not text is refused. `revealInto` skips it, so
	// `entry.Value` is "", and `ok` with "" would mean a revealed empty value: a
	// keystore drawn as an emptied key. The door reveals text only, as
	// `internal/edit` writes text only; base64 would hand bytes to an editor that
	// must never write them back. `entry.Binary` is the data block's own definition
	// of binary.
	if entry.Binary {
		refusal := &api.Failure{State: api.StateUnreadable, Reason: api.Because(api.CodeUndecodable,
			fmt.Sprintf("%q is not text — %s that does not decode to UTF-8. This console reveals "+
				"text; read it with kubectl and a pipe.", key, textutil.Bytes(int64(entry.Stored)))).Retry(false)}
		return api.EnvValue{}, refusal.Within(scope)
	}

	// `entry.Value`, not `set.Revealed`, which is the name of the revealed key: sent
	// as the value it would answer `{"key":"password","value":"password"}`. An empty
	// `entry.Value` is a revealed empty value, told from "no such key" by `Entry`.
	return api.EnvValue{Key: key, Value: entry.Value, Source: set.Kind}, nil
}

// revealLine is the record of one reveal: who asked for which key of which
// object, and what happened. Built from the query and the failure's code, which
// has no field for a value, never from the message, which could quote anything.
// `reason` sits beside `outcome` because `unreadable` covers both a non-text key
// (the door working) and a cluster that stopped answering (an incident).
// `container` is there for the audit line only.
//
// Every name on it comes from the query string, so it goes through
// identity.Line, which escapes the whole line and ends it with `who=` and `via=`:
// a key holding a newline cannot forge a `write:` line. The API already refuses
// such names (`internal/api/revealnames.go`); this holds without that door.
func revealLine(ctx context.Context, q api.EnvQuery, decision access.Decision, fail *api.Failure) string {
	outcome, reason := string(api.StateOK), api.ReasonCode("")
	if fail != nil {
		outcome, reason = string(fail.State), fail.Reason.Code
	}
	line := fmt.Sprintf("reveal: cluster=%q kind=%s namespace=%s name=%s key=%s decision=%s outcome=%s",
		q.Cluster, q.Kind, q.Namespace, q.Name, q.Key, decision, outcome)
	if reason != "" {
		line += " reason=" + string(reason)
	}
	if q.Container != "" {
		line += " container=" + q.Container
	}
	return identity.Line(ctx, "%s", line)
}

func (r apiReads) DataPanel(ctx context.Context, ref api.ObjectRef) (api.DataPanel, *api.Failure) {
	scope := api.Scope{Cluster: ref.Cluster, Resource: ref.Kind, Namespace: ref.Namespace, Name: ref.Name}
	set, err := r.s.objects.Data(ctx, ref.Cluster, detail.Request{
		Kind: ref.Kind, Namespace: ref.Namespace, Name: ref.Name,
	}, "")
	if err != nil {
		return api.DataPanel{}, stateOf(err, scope)
	}
	panel := api.DataPanel{
		Kind:            ref.Kind,
		Namespace:       ref.Namespace,
		Name:            ref.Name,
		ResourceVersion: set.ResourceVersion,
		Entries:         make([]api.DataEntry, 0, len(set.Entries)),
	}

	// The causes are told apart, being different people's to fix: immutability
	// first, the narrowest; then the guardrail and the review of the write
	// (editableFor), whose refusal to the person draws no edit control.
	if set.Immutable {
		panel.Editable = false
		panel.EditableNote = "this object is marked immutable, so its values cannot be changed; " +
			"a new object has to replace it"
	} else {
		panel.Editable, panel.EditableNote = r.s.editableFor(ctx, ref)
	}
	for _, entry := range set.Entries {
		panel.Entries = append(panel.Entries, dataEntryOf(entry))
	}
	return panel, nil
}

// dataEntryOf adapts one measured key to the contract's entry, as its own
// function so `dataemptyvalue_test.go` can drive it with one entry: an adapter
// that re-derives one of internal/detail's decisions is consistent and false.
func dataEntryOf(entry detail.DataEntry) api.DataEntry {
	out := api.DataEntry{Key: entry.Key, Masked: entry.Secret}
	if entry.Binary {
		out.Binary = boolOf(true)
	}
	// `Drawn`, not `!entry.Secret`: internal/detail has decided whether the panel may
	// show this value, and a narrower re-derivation would send a ConfigMap's empty
	// string as "", which `omitempty` drops and the client reads as withheld.
	if entry.Drawn {
		value := entry.Inline
		out.Value = &value
	}
	return out
}

// Manifest returns the manifest as tokens (yamlcolour.go), never markup.
func (r apiReads) Manifest(ctx context.Context, ref api.ObjectRef) (api.ManifestPanel, *api.Failure) {
	object, fail := r.object(ctx, ref)
	if fail != nil {
		return api.ManifestPanel{}, fail
	}
	// Editable only where the cluster is and the write would be allowed
	// (editableFor): a read-only cluster or a console that may not patch says which
	// refuses, and a person who may not patch gets no editor. Immutability is not
	// part of it: an immutable Secret's labels are still editable, and
	// `internal/edit` refuses the one change that cannot land.
	editable, note := r.s.editableFor(ctx, ref)
	return manifestPanelOf(object, editable, note), nil
}

// manifestPanelOf maps the object internal/detail read to the panel on the wire,
// a pure function so a test can plant a value in a Secret and check none leaks
// (TestASecretsManifestPanelCarriesNoValueAndNamesWhatItMasked).
func manifestPanelOf(object detail.Object, editable bool, note string) api.ManifestPanel {
	lines := tokeniseYAML(object.YAML)
	panel := api.ManifestPanel{
		Lines:           make([]api.ManifestLine, 0, len(lines)),
		Raw:             object.YAML,
		ResourceVersion: object.ResourceVersion,
		Editable:        editable,
		EditableNote:    note,
		MaskedKeys:      maskedKeysOf(object.Data),
	}
	for i, tokens := range lines {
		out := api.ManifestLine{Number: i + 1, Tokens: make([]api.ManifestToken, 0, len(tokens))}
		for _, token := range tokens {
			out.Tokens = append(out.Tokens, api.ManifestToken{Text: token.Text, Class: token.Class})
		}
		panel.Lines = append(panel.Lines, out)
	}
	return panel
}

// maskedKeysOf is which keys `detail.Trimmed` masked in the document, told to the
// editor so it need not parse the YAML for masks: it fetches each value through
// the reveal door, one request and one record per key. A binary entry is left
// out: the door refuses it, and writing an empty answer back would destroy the
// value; its mask stays. nil for everything that is not a Secret, so the member
// is absent rather than `[]`.
func maskedKeysOf(set *detail.DataSet) []string {
	if set == nil || !set.Secret {
		return nil
	}
	var keys []string
	for _, entry := range set.Entries {
		if entry.Secret && !entry.Binary {
			keys = append(keys, entry.Key)
		}
	}
	return keys
}

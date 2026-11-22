// Package pending is Pending: KubeTower's page that answers why a pod is
// Pending, and what it would take to start it. It is an extension of the
// console, part of the core and behind its own per-cluster switch, off by
// default.
//
// # Where the answer comes from
//
//   - **What the scheduler said**, which is already in the pod: its
//     PodScheduled condition carries the scheduler's last message ("0/5 nodes
//     are available: 2 Insufficient cpu, 3 node(s) had untolerated taint …"),
//     split here into reasons with their node counts and filed into
//     categories (reasons.go). No event is read for the list.
//   - **What can be computed honestly from the specs**, node by node (fit.go):
//     cordon, taints against tolerations, the node selector and required node
//     affinity, every resource the pod asks for against the node's
//     allocatable less the requests of the pods already placed there, the pod
//     count, host ports, and the zone of a volume already bound. Required pod
//     affinity, pod anti-affinity (the pod's own and that of the pods already
//     placed) and hard topology spread are computed for the node and zone
//     topologies, from the placed pods read once (affinity.go); where a case
//     is not handled, or the pods were not read whole, their cells say "per
//     the scheduler" and are never a pass.
//   - **What the scheduler said about preemption**: the `preemption:` half of
//     its message, counted (not helpful, no victims, policy Never), with the
//     pod's PriorityClass and the node it nominated (preemption.go). Only a
//     nominated node says eviction helps; nothing is evicted or simulated.
//   - **What the pod is waiting on once placed**: its first waiting
//     container, as the kubelet reports it.
//   - **Workloads that cannot create their pods**: a quota refuses a pod
//     before it exists, so it is never Pending; the refusal is a FailedCreate
//     event on its owner, read with one field selector.
//
// # Who sees what
//
// Every read goes through Host.List, reviewed for the person behind the
// request; Host.Client is never called and nothing here writes. A section
// that could not be read - the nodes, for a person who may list pods only -
// says so on its own, and the rest of the answer stands.
//
// # What it costs
//
// No cache between requests: each one is the person's own read, and a cache
// shared between people would serve one what another read. Every list is
// capped (read.go) and an answer cut short says so; placed pods are summed as
// they are read and never held.
package pending

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/thonicdev/kubetower/extension"
)

// Key is `pending`: the page's URL segment, the `KT_EXTRAS` name and the key
// its per-cluster switch is stored under, so it is not free to change.
const Key = "pending"

// Label is the menu entry.
const Label = "Pod Debugging"

// Pending is the extension `cmd/kubetower` registers.
type Pending struct {
	now func() time.Time
}

// New is the constructor `cmd/kubetower` calls.
func New() Pending { return Pending{now: time.Now} }

func (Pending) Key() string      { return Key }
func (Pending) Label() string    { return Label }
func (Pending) Legacy() []string { return nil }

func (Pending) Summary() string {
	return "Why each Pending pod waits - resources, taints, affinity, volumes, quota - which node comes closest, and what it would take to start it."
}

func (Pending) Needs() string {
	return "Nothing beyond the cluster itself; the node checks need list on nodes"
}

// Routes are five reads, all GETs: nothing here changes a cluster.
func (p Pending) Routes(h extension.Host) []extension.Route {
	return []extension.Route{
		{Method: http.MethodGet, Pattern: "/{$}", Handler: p.page(h)},
		{Method: http.MethodGet, Pattern: "/pod/{namespace}/{name}", Handler: p.podView(h)},
		{Method: http.MethodGet, Pattern: "/brief/{namespace}/{name}", Handler: p.brief(h)},
		{Method: http.MethodGet, Pattern: "/cells", Handler: p.cells(h)},
		{Method: http.MethodGet, Pattern: "/workload/{kind}/{namespace}/{name}", Handler: p.workload(h)},
	}
}

// CategoryLabels is each category in a few words, for a list's pill.
var CategoryLabels = map[Category]string{
	CatResources:    "Resources",
	CatTaints:       "Taints",
	CatNodeAffinity: "Node selector",
	CatPodAffinity:  "Pod affinity",
	CatSpread:       "Topology spread",
	CatVolumes:      "Volumes",
	CatCordon:       "Cordoned",
	CatPorts:        "Host ports",
	CatGated:        "Gated",
	CatNoScheduler:  "No scheduler",
	CatStarting:     "Starting",
	CatOther:        "Other",
}

// Gauge is an amount against what there is of it.
type Gauge struct {
	Allocatable int64 `json:"allocatable"`
	Requested   int64 `json:"requested"`
}

// Bar is one node's requested against allocatable.
type Bar struct {
	Name     string `json:"name"`
	Ready    bool   `json:"ready"`
	Cordoned bool   `json:"cordoned"`
	Taints   int    `json:"taints"`
	CPU      Gauge  `json:"cpu"`
	Memory   Gauge  `json:"memory"`
	Pods     Gauge  `json:"pods"`
}

func barOf(n *Node) Bar {
	taints := 0
	for _, t := range n.Taints {
		if t.Effect != corev1.TaintEffectPreferNoSchedule && t.Key != unschedulableKey {
			taints++
		}
	}
	return Bar{
		Name: n.Name, Ready: n.Ready, Cordoned: n.Unschedulable, Taints: taints,
		CPU:    Gauge{Allocatable: n.Allocatable["cpu"], Requested: n.Requested["cpu"]},
		Memory: Gauge{Allocatable: n.Allocatable["memory"], Requested: n.Requested["memory"]},
		Pods:   Gauge{Allocatable: n.Allocatable["pods"], Requested: n.Pods},
	}
}

// PageAnswer is `GET /`: every pending pod, the nodes' bars, and the
// workloads that cannot create theirs.
type PageAnswer struct {
	Cluster   string    `json:"cluster"`
	Namespace string    `json:"namespace"`
	Read      time.Time `json:"read"`
	// Pods is the pending pods' read; Capped says MaxPending was reached.
	Pods   Section `json:"pods"`
	Capped bool    `json:"capped"`
	// Total is how many pending pods were read; Counts how many fall under
	// each category (a pod counts once, under its main one).
	Total  int              `json:"total"`
	Counts map[Category]int `json:"counts"`
	// Rows is the pods, longest waiting first, at most MaxRows.
	Rows     []Row     `json:"rows"`
	Nodes    Section   `json:"nodes"`
	Placed   Section   `json:"placed"`
	Bars     []Bar     `json:"bars"`
	Failures []Failure `json:"failures"`
	// FailuresRead is the FailedCreate events' read.
	FailuresRead Section `json:"failuresRead"`
}

func (p Pending) page(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster := r.PathValue("cluster")
		namespace := r.URL.Query().Get("ns")
		if namespace != "" && len(validation.IsDNS1123Label(namespace)) > 0 {
			writeProblem(w, http.StatusBadRequest, CodeBadNamespace, quote(namespace)+" is not a namespace name.", nil)
			return
		}
		answer, err := p.read(r, h, cluster, namespace)
		if err != nil {
			writeReadFailure(w, cluster, err)
			return
		}
		writeJSON(w, http.StatusOK, answer)
	})
}

func (p Pending) read(r *http.Request, h extension.Host, cluster, namespace string) (*PageAnswer, error) {
	now := p.now()
	pods, page, err := readPending(r, h, cluster, namespace, "", MaxPending, now)
	if err != nil {
		return nil, err
	}
	a := &PageAnswer{
		Cluster: cluster, Namespace: namespace, Read: now,
		Pods:   sectionOf(page, nil),
		Capped: int64(len(pods)) >= MaxPending,
		Total:  len(pods),
		Counts: map[Category]int{},
	}
	for _, c := range Categories {
		a.Counts[c] = 0
	}
	keep := false
	for i := range pods {
		if pods[i].row.Stage == StageUnschedulable && declaresPlacement(pods[i].pod.Spec) {
			keep = true
			break
		}
	}
	fleet, err := readFleet(r, h, cluster, keep)
	if err != nil {
		return nil, err
	}
	a.Nodes, a.Placed = fleet.section, fleet.placed
	cl, err := readClaims(r, h, cluster, pods)
	if err != nil {
		return nil, err
	}
	placer := &placer{r: r, h: h, cluster: cluster, fleet: fleet, cache: map[string]*Placed{}, budget: maxPlacedWork}
	for i := range pods {
		row := &pods[i].row
		a.Counts[row.Main]++
		if row.Stage == StageUnschedulable && len(fleet.nodes) > 0 {
			want := wantOf(pods[i], cl.terms)
			want.Place = placer.placed(pods[i], false)
			_, fits := fitAll(want, fleet.nodes)
			row.Closest = &Closest{Node: fits[0].Node, Failing: fits[0].Failing}
		}
		pods[i].pod = nil
	}
	sort.SliceStable(pods, func(i, j int) bool {
		if !pods[i].row.Created.Equal(pods[j].row.Created) {
			return pods[i].row.Created.Before(pods[j].row.Created)
		}
		return pods[i].row.Namespace+"/"+pods[i].row.Name < pods[j].row.Namespace+"/"+pods[j].row.Name
	})
	for i := range pods {
		if i >= MaxRows {
			break
		}
		a.Rows = append(a.Rows, pods[i].row)
	}
	if a.Rows == nil {
		a.Rows = []Row{}
	}
	a.Bars = make([]Bar, 0, len(fleet.nodes))
	for _, n := range fleet.nodes {
		a.Bars = append(a.Bars, barOf(n))
	}
	sort.SliceStable(a.Bars, func(i, j int) bool { return a.Bars[i].Name < a.Bars[j].Name })
	a.Failures, a.FailuresRead, err = readFailures(r, h, cluster, namespace)
	if err != nil {
		return nil, err
	}
	if a.Failures == nil {
		a.Failures = []Failure{}
	}
	return a, nil
}

// Column is one of the matrix's columns.
type Column struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	// Computed is false for a column only the scheduler's word fills.
	Computed bool `json:"computed"`
	// Scheduler is, for such a column, how many nodes the scheduler said it
	// refused; -1 when it gave no count, 0 when it named none.
	Scheduler int `json:"scheduler"`
	// Why is, for a column that is not computed although it could have been,
	// what stopped it: a case not handled, or a read that was not whole.
	Why string `json:"why,omitempty"`
}

func placeWhy(p *Placed, key string) string {
	switch {
	case p == nil:
		return ""
	case key == PredPodAffinity:
		return p.Affinity.Why
	}
	return p.Spread.Why
}

func columnLabel(key string) string {
	switch key {
	case PredCordon:
		return "Cordon"
	case PredTaints:
		return "Taints"
	case PredAffinity:
		return "Selector"
	case PredPods:
		return "Pod count"
	case PredPorts:
		return "Host ports"
	case PredVolumes:
		return "Volume zone"
	case PredPodAffinity:
		return "Pod affinity"
	case PredSpread:
		return "Spread"
	}
	name := strings.TrimPrefix(key, resourcePrefix)
	switch name {
	case "cpu":
		return "CPU"
	case "memory":
		return "Memory"
	case "ephemeral-storage":
		return "Ephemeral"
	}
	return name
}

// Event is one line of a pod's timeline.
type Event struct {
	Type    string    `json:"type"`
	Reason  string    `json:"reason"`
	Message string    `json:"message"`
	First   time.Time `json:"first"`
	Last    time.Time `json:"last"`
	Count   int32     `json:"count"`
	Source  string    `json:"source"`
}

// PodView is `GET /pod/{namespace}/{name}`: one pod, its fit on every node,
// what it would take, and its timeline.
type PodView struct {
	Cluster string `json:"cluster"`
	// Pending is false for a pod that is not (any longer) Pending: Row is
	// then absent.
	Pending bool      `json:"pending"`
	Phase   string    `json:"phase"`
	Read    time.Time `json:"read"`
	Row     *Row      `json:"row,omitempty"`
	Columns []Column  `json:"columns"`
	// Fits is the nodes closest first, at most MaxMatrix of NodesTotal.
	Fits       []Fit `json:"fits"`
	NodesTotal int   `json:"nodesTotal"`
	// Nodes and Placed are absent where no node was read: a pod that is not
	// waiting for a node.
	Nodes  *Section `json:"nodes,omitempty"`
	Placed *Section `json:"placed,omitempty"`
	// Bars is the nodes of Fits that come first, for the bars with the pod's
	// request laid over them.
	Bars      []Bar    `json:"bars"`
	WouldTake []string `json:"wouldTake"`
	// Claims is each claim that holds the pod, and why.
	Claims       []ClaimNote `json:"claims"`
	ClaimsRead   Section     `json:"claimsRead"`
	Events       []Event     `json:"events"`
	EventsRead   Section     `json:"eventsRead"`
	EventsCapped bool        `json:"eventsCapped"`
}

type ClaimNote struct {
	Claim   string `json:"claim"`
	Problem string `json:"problem"`
}

// maxBars is how many nodes the pod view draws as bars.
const maxBars = 12

func (p Pending) podView(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster, namespace, name := r.PathValue("cluster"), r.PathValue("namespace"), r.PathValue("name")
		if !validObject(w, namespace, name) {
			return
		}
		view, err := p.view(r, h, cluster, namespace, name)
		switch {
		case errors.Is(err, errNoSuchPod):
			writeProblem(w, http.StatusNotFound, CodeNoSuchPod, "There is no pod "+namespace+"/"+name+" in this cluster any more.", nil)
		case err != nil:
			writeReadFailure(w, cluster, err)
		default:
			writeJSON(w, http.StatusOK, view)
		}
	})
}

var errNoSuchPod = errors.New("no such pod")

// onePod reads one pod by name, through the reviewed list.
func onePod(r *http.Request, h extension.Host, cluster, namespace, name string) (*corev1.Pod, error) {
	page, err := h.List(r, cluster, extension.ListQuery{GVR: podsGVR, Namespace: namespace, FieldSelector: "metadata.name=" + name, Limit: 1})
	if err != nil {
		return nil, err
	}
	if len(page.Items) == 0 {
		return nil, errNoSuchPod
	}
	pod := &corev1.Pod{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(page.Items[0].Object, pod); err != nil {
		return nil, fmt.Errorf("the pod the cluster returned could not be read: %w", err)
	}
	return pod, nil
}

func (p Pending) view(r *http.Request, h extension.Host, cluster, namespace, name string) (*PodView, error) {
	now := p.now()
	pod, err := onePod(r, h, cluster, namespace, name)
	if err != nil {
		return nil, err
	}
	v := &PodView{Cluster: cluster, Phase: string(pod.Status.Phase), Read: now, Columns: []Column{}, Fits: []Fit{}, Bars: []Bar{}, WouldTake: []string{}, Claims: []ClaimNote{}, Events: []Event{}}
	v.Events, v.EventsRead, v.EventsCapped, err = readEvents(r, h, cluster, namespace, name, string(pod.UID))
	if err != nil {
		return nil, err
	}
	if pod.Status.Phase != corev1.PodPending {
		return v, nil
	}
	pp := pendingPod{pod: pod, row: classify(pod, now)}
	v.Pending, v.Row = true, &pp.row
	cl, err := readClaims(r, h, cluster, []pendingPod{pp})
	if err != nil {
		return nil, err
	}
	v.ClaimsRead = cl.section
	for _, vol := range pod.Spec.Volumes {
		if vol.PersistentVolumeClaim == nil {
			continue
		}
		if why, ok := cl.problems[namespace+"/"+vol.PersistentVolumeClaim.ClaimName]; ok {
			v.Claims = append(v.Claims, ClaimNote{Claim: vol.PersistentVolumeClaim.ClaimName, Problem: why})
		}
	}
	if pp.row.Stage != StageUnschedulable {
		v.WouldTake = wouldTakeOther(pp.row)
		return v, nil
	}
	fleet, err := readFleet(r, h, cluster, declaresPlacement(pod.Spec))
	if err != nil {
		return nil, err
	}
	v.Nodes, v.Placed = &fleet.section, &fleet.placed
	want := wantOf(pp, cl.terms)
	placer := &placer{r: r, h: h, cluster: cluster, fleet: fleet, cache: map[string]*Placed{}, budget: 1}
	want.Place = placer.placed(pp, true)
	cols, fits := fitAll(want, fleet.nodes)
	counts := map[Category]int{}
	for _, reason := range pp.row.Verdict.Reasons {
		n := reason.Nodes
		if n < 0 {
			n = -1
		}
		if counts[reason.Category] >= 0 {
			if n < 0 {
				counts[reason.Category] = -1
			} else {
				counts[reason.Category] += n
			}
		}
	}
	for _, c := range cols {
		col := Column{Key: c, Label: columnLabel(c), Computed: true}
		switch c {
		case PredPodAffinity:
			col.Computed, col.Scheduler, col.Why = false, counts[CatPodAffinity], placeWhy(want.Place, c)
			if r := want.rule(c); r != nil {
				col.Computed = r.Computed
			}
		case PredSpread:
			col.Computed, col.Scheduler, col.Why = false, counts[CatSpread], placeWhy(want.Place, c)
			if r := want.rule(c); r != nil {
				col.Computed = r.Computed
			}
		}
		v.Columns = append(v.Columns, col)
	}
	v.NodesTotal = len(fits)
	if len(fits) > MaxMatrix {
		fits = fits[:MaxMatrix]
	}
	v.Fits = fits
	for i := 0; i < len(fits) && i < maxBars; i++ {
		v.Bars = append(v.Bars, barOf(fleet.byName[fits[i].Node]))
	}
	if len(fits) > 0 {
		pp.row.Closest = &Closest{Node: fits[0].Node, Failing: fits[0].Failing}
	}
	v.WouldTake = wouldTake(pp.row, v.Columns, fits, v.Claims, fleet.section)
	return v, nil
}

// wouldTake is what it would take to start an unschedulable pod, in
// sentences: on the closest node, for each computed check it fails, and for
// what only the scheduler saw, its own words.
func wouldTake(row Row, cols []Column, fits []Fit, claimNotes []ClaimNote, nodes Section) []string {
	out := []string{}
	for _, c := range claimNotes {
		out = append(out, "Claim "+c.Claim+": "+c.Problem+".")
	}
	if nodes.State != "read" {
		out = append(out, "The nodes could not be read for you, so only the scheduler's reasons are shown: "+row.Message)
		return out
	}
	if len(fits) == 0 {
		return append(out, "The cluster has no node.")
	}
	// A check no node passes is the pod's to change, not one node's.
	nowhere := map[string]bool{}
	for i, col := range cols {
		if !col.Computed {
			continue
		}
		passes := false
		for _, f := range fits {
			if ok := f.Cells[i].OK; ok != nil && *ok {
				passes = true
				break
			}
		}
		if !passes {
			nowhere[col.Key] = true
			out = append(out, nowhereSentence(col, fits[0].Cells[i]))
		}
	}
	best := fits[0]
	for i, cell := range best.Cells {
		col := cols[i]
		if !col.Computed || nowhere[col.Key] || cell.OK == nil || *cell.OK {
			continue
		}
		out = append(out, onNodeSentence(best.Node, col, cell))
	}
	for _, col := range cols {
		if col.Computed {
			continue
		}
		for _, reason := range row.Verdict.Reasons {
			if (col.Key == PredPodAffinity && reason.Category == CatPodAffinity) || (col.Key == PredSpread && reason.Category == CatSpread) {
				out = append(out, "Per the scheduler, "+nodesPhrase(reason.Nodes)+" "+strings.TrimPrefix(reason.Text, "node(s) ")+". This is not computed here: look at the pod's "+strings.ToLower(col.Label)+" rules and at the pods they match.")
			}
		}
	}
	if best.Failing == 0 && len(out) == 0 {
		out = append(out, "Every check computed here passes on "+best.Node+". What holds the pod is something only the scheduler sees, or the cluster has changed since it last tried: "+row.Message)
	}
	return out
}

func nodesPhrase(n int) string {
	switch {
	case n < 0:
		return "every node"
	case n == 1:
		return "1 node"
	}
	return fmt.Sprintf("%d nodes", n)
}

func nowhereSentence(col Column, cell Cell) string {
	switch {
	case col.Key == PredAffinity:
		return "No node matches the pod's node selector or required affinity (" + cell.Detail + "): label a node to match, or change the selector."
	case col.Key == PredTaints:
		return "Every node has a taint the pod does not tolerate (closest: " + cell.Detail + "): add a toleration, or remove the taint from a node."
	case col.Key == PredCordon:
		return "Every node is cordoned: uncordon one."
	case strings.HasPrefix(col.Key, resourcePrefix):
		if cell.Detail == "the node has none" {
			return "No node offers " + col.Label + ": add a node that does, or remove the request."
		}
		return "No node has enough " + col.Label + " free (closest: " + cell.Detail + "): free some, add a node, or ask for less."
	case col.Key == PredPods:
		return "Every node holds as many pods as it may: add a node."
	case col.Key == PredPorts:
		return "Every node already uses the pod's host port (" + cell.Detail + ")."
	case col.Key == PredVolumes:
		return "No node can reach the pod's volume (" + cell.Detail + ")."
	case col.Key == PredPodAffinity:
		return "Every node is ruled out by pod affinity (closest: " + cell.Detail + "): change the rule, or free the node it names."
	case col.Key == PredSpread:
		return "Every node would break the topology spread (closest: " + cell.Detail + "): loosen maxSkew, or add capacity to the emptiest domain."
	}
	return col.Label + ": no node passes."
}

func onNodeSentence(node string, col Column, cell Cell) string {
	switch {
	case col.Key == PredCordon:
		return "On " + node + ", the closest node: it is cordoned. Uncordon it."
	case col.Key == PredTaints:
		return "On " + node + ", the closest node: tolerate " + cell.Detail + "."
	case col.Key == PredAffinity:
		return "On " + node + ", the closest node: " + cell.Detail + "."
	case strings.HasPrefix(col.Key, resourcePrefix):
		return "On " + node + ", the closest node: " + col.Label + " " + cell.Detail + ". Free some there, or ask for less."
	case col.Key == PredPodAffinity:
		return "On " + node + ", the closest node: " + cell.Detail + "."
	}
	return "On " + node + ", the closest node: " + col.Label + ": " + cell.Detail + "."
}

// wouldTakeOther is the sentence for a pod not waiting on a node.
func wouldTakeOther(row Row) []string {
	switch row.Stage {
	case StageGated:
		return []string{"Wait for the controller that set the scheduling gates to remove them, or remove them yourself if nothing will."}
	case StageUnseen:
		if row.Main == CatNoScheduler {
			return []string{"Check that the scheduler " + quote(orDefault(row.Scheduler)) + " is running, or set schedulerName to one that is."}
		}
		return []string{"Wait a moment: the scheduler has not tried yet."}
	case StageStarting:
		m := row.Message
		switch {
		case strings.Contains(m, "ImagePull"), strings.Contains(m, "ErrImagePull"), strings.Contains(m, "InvalidImageName"):
			return []string{"The image cannot be pulled on " + row.Node + ": check its name and tag, the registry's reachability, and the pull secret."}
		case strings.Contains(m, "CreateContainerConfigError"):
			return []string{"A ConfigMap or Secret the container reads is missing, or a key in it: create it."}
		case strings.Contains(m, "ContainerCreating"), strings.Contains(m, "PodInitializing"):
			return []string{"The pod is placed and starting: if it stays here, its events say what it waits on (a volume to mount, a network to attach)."}
		}
		return []string{"The pod is placed on " + row.Node + "; its events say what it waits on."}
	}
	return []string{}
}

func orDefault(s string) string {
	if s == "" {
		return corev1.DefaultSchedulerName
	}
	return s
}

func readEvents(r *http.Request, h extension.Host, cluster, namespace, name, uid string) ([]Event, Section, bool, error) {
	page, err := h.List(r, cluster, extension.ListQuery{
		GVR: eventsGVR, Namespace: namespace,
		FieldSelector: "involvedObject.kind=Pod,involvedObject.name=" + name,
		Limit:         MaxEvents,
	})
	if err != nil {
		if ends(err) {
			return nil, Section{}, false, err
		}
		return []Event{}, sectionOf(page, err), false, nil
	}
	out := make([]Event, 0, len(page.Items))
	for i := range page.Items {
		ev := &corev1.Event{}
		if runtime.DefaultUnstructuredConverter.FromUnstructured(page.Items[i].Object, ev) != nil {
			continue
		}
		if uid != "" && ev.InvolvedObject.UID != "" && string(ev.InvolvedObject.UID) != uid {
			continue // an earlier pod of the same name
		}
		source := ev.ReportingController
		if source == "" {
			source = ev.Source.Component
		}
		out = append(out, Event{Type: ev.Type, Reason: ev.Reason, Message: firstLine(ev.Message), First: eventFirst(ev), Last: eventLast(ev), Count: eventCount(ev), Source: source})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].First.Before(out[j].First) })
	return out, sectionOf(page, nil), int64(len(page.Items)) >= MaxEvents, nil
}

// Brief is `GET /brief/{namespace}/{name}`: whether the pod is Pending, from
// the pod alone, for the panel's tab and shortcut to ask without the cost of
// the whole view.
type Brief struct {
	Pending bool `json:"pending"`
	Row     *Row `json:"row,omitempty"`
}

func (p Pending) brief(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster, namespace, name := r.PathValue("cluster"), r.PathValue("namespace"), r.PathValue("name")
		if !validObject(w, namespace, name) {
			return
		}
		pod, err := onePod(r, h, cluster, namespace, name)
		switch {
		case errors.Is(err, errNoSuchPod):
			writeJSON(w, http.StatusOK, Brief{})
			return
		case err != nil:
			writeReadFailure(w, cluster, err)
			return
		}
		if pod.Status.Phase != corev1.PodPending {
			writeJSON(w, http.StatusOK, Brief{})
			return
		}
		row := classify(pod, p.now())
		writeJSON(w, http.StatusOK, Brief{Pending: true, Row: &row})
	})
}

// Cell is one answer of `GET /cells`, the Pods list's column.
type CellAnswer struct {
	Text string `json:"text"`
	Tone string `json:"tone,omitempty"`
	Hint string `json:"hint,omitempty"`
	Href string `json:"href,omitempty"`
}

func toneOf(row Row) string {
	switch row.Main {
	case CatStarting, CatGated:
		return "warn"
	case CatOther:
		if row.Stage == StageUnseen {
			return ""
		}
	}
	return "bad"
}

func (p Pending) cells(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster := r.PathValue("cluster")
		out := map[string]CellAnswer{}
		kind := r.URL.Query().Get("kind")
		if _, ok := workloadKinds[kind]; ok {
			cells, err := p.workloadCells(r, h, cluster, kind)
			if err != nil {
				writeReadFailure(w, cluster, err)
				return
			}
			writeJSON(w, http.StatusOK, cells)
			return
		}
		if kind != "pods" {
			writeJSON(w, http.StatusOK, out)
			return
		}
		pods, _, err := readPending(r, h, cluster, "", "", MaxPending, p.now())
		if err != nil {
			writeReadFailure(w, cluster, err)
			return
		}
		for _, pp := range pods {
			row := pp.row
			out[row.Namespace+"/"+row.Name] = CellAnswer{
				Text: CategoryLabels[row.Main],
				Tone: toneOf(row),
				Hint: firstLine(row.Message),
				Href: "/c/" + cluster + "/x/" + Key + "/" + row.Namespace + "/" + row.Name,
			}
		}
		writeJSON(w, http.StatusOK, out)
	})
}

// suggestLimit is how many unscheduled pods the suggestion reads at most: it
// runs whenever a cluster's pages are opened.
const suggestLimit = 50

// Suggest is whether pods have been waiting for a node for a while, for the
// banner that offers to turn the page on (extension.Suggester).
func (p Pending) Suggest(r *http.Request, h extension.Host, cluster string) (string, error) {
	now := p.now()
	pods, page, err := readPending(r, h, cluster, "", "spec.nodeName=", suggestLimit, now)
	if err != nil {
		return "", err
	}
	waiting := 0
	for _, pp := range pods {
		since := pp.row.Created
		if pp.row.Since != nil {
			since = *pp.row.Since
		}
		if pp.row.Stage == StageUnschedulable && now.Sub(since) >= unseenAfter {
			waiting++
		}
	}
	if waiting == 0 {
		return "", nil
	}
	floor := ""
	if !page.Complete || len(pods) >= suggestLimit {
		floor = "At least "
	}
	noun := "pods have"
	if waiting == 1 {
		noun = "pod has"
	}
	return fmt.Sprintf("%s%d %s been waiting for a node for more than two minutes. Pending says why, and what it would take to start them.", floor, waiting, noun), nil
}

func validObject(w http.ResponseWriter, namespace, name string) bool {
	if len(validation.IsDNS1123Label(namespace)) > 0 {
		writeProblem(w, http.StatusBadRequest, CodeBadNamespace, quote(namespace)+" is not a namespace name.", nil)
		return false
	}
	if len(validation.IsDNS1123Subdomain(name)) > 0 {
		writeProblem(w, http.StatusBadRequest, CodeBadName, quote(name)+" is not a pod name.", nil)
		return false
	}
	return true
}

func quote(s string) string { return "\"" + s + "\"" }

type problem struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
}

// The codes these routes answer with. Protocol: the page names them too.
const (
	CodeUnknownCluster = "unknown_cluster"
	CodeBadNamespace   = "bad_namespace"
	CodeBadName        = "bad_name"
	CodeNoSuchPod      = "no_such_pod"
	CodeListFailed     = "list_failed"
)

func writeReadFailure(w http.ResponseWriter, cluster string, err error) {
	switch {
	case errors.Is(err, extension.ErrUnknownCluster):
		writeProblem(w, http.StatusNotFound, CodeUnknownCluster, extension.UnknownClusterText(cluster), err)
	case errors.Is(err, errCanceled), errors.Is(err, context.Canceled):
		// The page went away.
	default:
		if refused, ok := extension.RefusedRead(err,
			"The cluster says you may not list pods here, so no pending pod can be shown."); ok {
			writeProblem(w, refused.Status, refused.Code, refused.Message, err)
			return
		}
		writeProblem(w, http.StatusBadGateway, CodeListFailed,
			"This cluster's pods could not be listed, so nothing about them can be said.", err)
	}
}

func writeProblem(w http.ResponseWriter, status int, code, message string, err error) {
	body := problem{Code: code, Message: message}
	if err != nil {
		body.Detail = firstLine(err.Error())
	}
	writeJSON(w, status, body)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	data, err := json.Marshal(body)
	if err != nil {
		http.Error(w, "the answer could not be encoded", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(append(data, '\n'))
}

// placer computes the pod affinity and spread answers of the pods of one
// request, from the placed pods read once. Pods whose rules and labels are the
// same (the replicas of one workload) share an answer, and a page computes at
// most maxPlacedWork distinct ones: past that the cells stay gray and say so.
type placer struct {
	r       *http.Request
	h       extension.Host
	cluster string
	fleet   *fleet
	cache   map[string]*Placed
	budget  int

	nsDone   bool
	nsLabels nsLabels
}

// namespaces reads the namespaces' labels, once, and only for a pod whose
// answer needs them; a read that fails leaves them unknown, and the answer
// says so rather than guessing.
func (pr *placer) namespaces() nsLabels {
	if pr.nsDone {
		return pr.nsLabels
	}
	pr.nsDone = true
	page, err := pr.h.List(pr.r, pr.cluster, extension.ListQuery{GVR: namespacesGVR, Limit: MaxNodes})
	if err != nil || !page.Complete {
		return nil
	}
	out := nsLabels{}
	for i := range page.Items {
		out[page.Items[i].GetName()] = page.Items[i].GetLabels()
	}
	pr.nsLabels = out
	return out
}

// placed answers for one pending pod; nil where the pod has no rule of its
// own that placement reads and the scheduler did not name one.
func (pr *placer) placed(p pendingPod, detail bool) *Placed {
	set := map[Category]bool{}
	for _, c := range p.row.Verdict.Set() {
		set[c] = true
	}
	if !declaresPlacement(p.pod.Spec) && !set[CatPodAffinity] && !set[CatSpread] {
		return nil
	}
	rules := Rules{Namespace: p.pod.Namespace, Labels: p.pod.Labels, Spec: p.pod.Spec}
	key := placementKey(rules)
	if detail {
		key = "detail\x00" + key
	}
	if got, ok := pr.cache[key]; ok {
		return got
	}
	if pr.budget <= 0 {
		why := "not computed for so many distinct pods on one page: open the pod"
		return &Placed{Affinity: Rule{Why: why}, Spread: Rule{Why: why}}
	}
	pr.budget--
	var nsl nsLabels
	if pr.fleet.place.needsNamespaceLabels(p.pod.Spec) {
		nsl = pr.namespaces()
	}
	out := pr.fleet.place.Compute(rules, pr.fleet.nodes, pr.fleet.section.State == "read" && pr.fleet.section.Complete, nsl, detail)
	pr.cache[key] = &out
	return &out
}

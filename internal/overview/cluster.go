package overview

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes"

	"github.com/thonicdev/kubetower/internal/access"
	"github.com/thonicdev/kubetower/internal/cluster"

	"github.com/thonicdev/kubetower/internal/resources"
	"github.com/thonicdev/kubetower/safego"
)

// ClusterOverview is the machines: what they are, whether they answer, and how
// much of them is already spoken for.
type ClusterOverview struct {
	Cluster   string
	Collected time.Time

	// Version is the API server's, as it states it. Empty when discovery did
	// not answer.
	Version string

	// Healthy and Total count nodes. Healthy is not "Ready": a Ready node under
	// memory pressure, or cordoned, does not count, so 10/10 is never a false
	// reassurance.
	Healthy int
	Total   int

	// CPU and Memory are requests against allocatable: the figure that decides
	// whether the next pod schedules, available without any metrics backend.
	CPU    Capacity
	Memory Capacity
	// Pods is how many pod slots are taken; kubelet's pod limit often runs out
	// before memory does.
	Pods Capacity

	// Alerts are node conditions that must be impossible to miss: NotReady, or
	// under memory or disk pressure.
	Alerts []Alert
	// Events is the warning feed, newest first.
	Events []EventLine
	// EventsTotal is how many warning events there were before Events was cut
	// to maxEvents, so the page can say the feed is not whole.
	EventsTotal int

	// History says whether the two series below could be had at all; no
	// Prometheus is a normal cluster, drawn as an em dash, not an alarm.
	History       Availability
	CPUHistory    History
	MemoryHistory History

	// Now is what the machines use at this moment, from metrics-server, and
	// NowFrom says whether it could be had. Independent of History because the
	// backends are: metrics-server holds the present, Prometheus the past.
	Now     Current
	NowFrom Availability
	// NowNodes is the same read per node name; the tile's figure is summed
	// from it against the node list (wholeNow). It can hold an answer where
	// NowFrom does not: an empty answer is no figure for the tile.
	NowNodes NodesNow

	// Degraded names the panels above that could not be filled, and why.
	Degraded []PanelError

	// Access is what the access review did to the lists this page is built
	// from: Narrowed when the person may not list pods or events everywhere.
	Access access.Visible

	// NodesRefused is the cluster refusing this person the node list.
	// Everything summed over the machines is then unknown to them; the
	// version, the pods and the warning feed are still read. Nil when the
	// nodes were listed.
	NodesRefused error
}

// AllWell reports whether nothing on this page needs acting on. Degraded and
// NodesRefused count as well as Alerts: a panel that could not be filled is not
// a panel with nothing in it, and the badge must not go green over a page that
// failed to load. Alerts is node conditions only; the caller adds the workload
// half, which comes from a separate fetch.
func (o ClusterOverview) AllWell() bool {
	return len(o.Alerts) == 0 && len(o.Degraded) == 0 && o.NodesRefused == nil
}

// NodeCondition is one condition a node reports that is saying something:
// what alertsFor lifts to the top of the page.
type NodeCondition struct {
	Type    string
	Reason  string
	Message string
	Class   string
}

// NowElsewhere reads now per machine from somewhere other than metrics-server,
// asked only when metrics-server answered 404. On a cluster with Prometheus and
// no metrics-server, internal/web hands in the Nodes list's own read so the
// tile and that list agree by construction, without a second PromQL spelling
// here.
//
// Only on a 404: a refusal, a timeout or a 503 is a fault and is said as one,
// and a fallback past a 403 would show figures the person was refused.
type NowElsewhere func(ctx context.Context) Elsewhere

// Elsewhere is what NowElsewhere answers: exactly one of three.
type Elsewhere struct {
	// ByNode is each machine's reading by node name when the source answered
	// (empty when it measured nothing), and From names that source.
	ByNode map[string]Current
	From   string
	// Absent says there is nothing to ask there either: metrics-server's 404
	// stands, with this sentence beside it.
	Absent string
	// Err is the source failing when asked; it replaces the 404 as the
	// reason, so a failure is not drawn as an absence.
	Err error
}

// Cluster gathers the machine-side dashboard, with metrics-server as the only
// reading of now; see ClusterWithFallback.
func Cluster(ctx context.Context, gate *access.Gate, client *cluster.Client, metrics Metrics) (ClusterOverview, error) {
	return ClusterWithFallback(ctx, gate, client, metrics, nil)
}

// ClusterWithFallback gathers the machine-side dashboard.
//
// metrics may be nil: it only feeds the two history charts. elsewhere may be
// nil; see NowElsewhere.
//
// It returns an error only when the node list fails other than by a refusal:
// that call is the proof the cluster answers, and a dashboard of zeroes for an
// unreachable cluster is what this package must never draw. Every list is
// reviewed for the person behind ctx; a refused node list only empties what is
// summed over the machines (NodesRefused), and pods and events are narrowed to
// the namespaces the person may list.
func ClusterWithFallback(ctx context.Context, gate *access.Gate, client *cluster.Client, metrics Metrics,
	elsewhere NowElsewhere) (ClusterOverview, error) {
	if client == nil || client.Clients == nil {
		return ClusterOverview{}, fmt.Errorf("no client for this cluster")
	}
	api := client.Clients
	name := client.Context
	reader := resources.ReaderFor(gate, client)

	out := ClusterOverview{Cluster: name, Collected: time.Now()}

	var (
		nodes      []corev1.Node
		nodeErr    error
		pods       []corev1.Pod
		podSeen    access.Visible
		podErr     error
		events     []corev1.Event
		eventSeen  access.Visible
		eventErr   error
		version    string
		versionErr error
	)

	// Every task names where its failure goes, in words an operator reads in
	// the PanelError; see gather.
	gather(
		guard(&nodeErr, "the node list", func() { nodes, nodeErr = listNodes(ctx, reader) }),
		guard(&podErr, "the pod list", func() { pods, podSeen, podErr = listPods(ctx, reader, "") }),
		guard(&eventErr, "the warning event feed", func() { events, eventSeen, eventErr = listWarningEvents(ctx, reader, "") }),
		guard(&versionErr, "the version call", func() { version, versionErr = serverVersion(ctx, api) }),
		// The history panels report their own absence, so a panic is written
		// into all three or the charts come back blank with no reason.
		attempt(
			func() { out.History, out.CPUHistory, out.MemoryHistory = clusterHistory(ctx, metrics, name) },
			func(crash any) { out.History, out.CPUHistory, out.MemoryHistory = crashedHistory(crash) },
		),
		// Beside the history so the page costs the slower of the two, not their
		// sum.
		degrading(&out.NowFrom, "the current usage read", func() { out.Now, out.NowFrom, out.NowNodes = nodeUsageNow(ctx, reader) }),
	)

	if nodeErr != nil && !apierrors.IsForbidden(nodeErr) {
		// Wrapped: the caller tells a lapsed session from an unreachable
		// cluster by the original error.
		return ClusterOverview{}, fmt.Errorf("listing the nodes of %s: %w", name, nodeErr)
	}

	out.Version = version
	degrade(&out.Degraded, PanelVersion, versionErr)
	degrade(&out.Degraded, PanelEvents, eventErr)
	if nodeErr != nil {
		// A refusal, of this person or of the console's credentials. The
		// cluster answered, so the page is drawn, with the machines unknown
		// rather than drawn as a cluster of no nodes.
		out.NodesRefused = nodeErr
		out.Access = podSeen.Merge(eventSeen)
		out.Events, out.EventsTotal = eventLines(events, out.Collected, maxEvents)
		return withoutMachines(out), nil
	}
	// A failed pod list is not fatal, but the requests would then read zero;
	// say so instead.
	degrade(&out.Degraded, PanelRequests, podErr)
	out.Access = podSeen.Merge(eventSeen)
	if podErr == nil && podSeen.Filtered() {
		// The requests are a sum over every pod; a narrowed list makes it
		// smaller than it is, wrong in the reassuring direction. So it is
		// unknown to this person, as when the pod list fails.
		podErr = ErrNarrowedRequests
		degrade(&out.Degraded, PanelRequests, podErr)
	}

	// A panic in the usage read leaves NowNodes empty and its reason on
	// NowFrom; copied to NowNodes.Err so the Capacity tile says the read
	// failed rather than "no metrics backend".
	if out.NowNodes.ByNode == nil && out.NowNodes.Err == nil && out.NowFrom.Detail != "" {
		out.NowNodes.Err = errors.New(out.NowFrom.Detail)
	}
	if elsewhere != nil && apierrors.IsNotFound(out.NowNodes.Err) {
		out.NowFrom, out.NowNodes = nowFromElsewhere(ctx, elsewhere, out.NowFrom, out.NowNodes)
	}
	nowNodes := out.NowNodes

	requests := aggregateRequests(pods)

	// Allocatable, not capacity: allocatable is what the scheduler may hand out,
	// after kubelet's own reservation. Sizing against capacity makes a cluster
	// read "40% used" and unable to take a pod.
	out.Total = len(nodes)
	var allocCPU, allocMemory, allocPods int64
	for i := range nodes {
		node := &nodes[i]
		if nodeHealthy(node) {
			out.Healthy++
		}
		out.Alerts = append(out.Alerts, alertsFor(node)...)

		alloc := node.Status.Allocatable
		allocCPU += quantityMilli(alloc, corev1.ResourceCPU)
		allocMemory += quantityValue(alloc, corev1.ResourceMemory)
		allocPods += quantityValue(alloc, corev1.ResourcePods)
	}

	// The tile's reading of now, settled against the nodes counted below.
	out.Now, out.NowFrom = wholeNow(nodes, nowNodes, out.Now, out.NowFrom)

	out.CPU = Capacity{Name: "CPU", Unit: UnitCores, Used: requests.cpuMilli, Total: allocCPU}
	out.Memory = Capacity{Name: "Memory", Unit: UnitBytes, Used: requests.memoryBytes, Total: allocMemory}
	out.Pods = Capacity{Name: "Pods", Unit: UnitCount, Used: requests.pods, Total: allocPods}

	// When the pod list failed the total is zeroed too, not because the
	// allocatable is in doubt but because Capacity.Known() (Total > 0) is the
	// only "could not be computed" channel: a real total beside a zero Used
	// would draw a confident, green "0%".
	if podErr != nil {
		out.CPU.Used, out.Memory.Used, out.Pods.Used = 0, 0, 0
		out.CPU.Total, out.Memory.Total, out.Pods.Total = 0, 0, 0
	}

	out.Events, out.EventsTotal = eventLines(events, out.Collected, maxEvents)
	return out, nil
}

// withoutMachines is the page of a person refused the node list: everything
// summed over the machines is unknown (zero totals, which Capacity.Known reads
// as not computed), and usage now and over time is cleared even if its read
// answered, since the person was refused the machines.
func withoutMachines(out ClusterOverview) ClusterOverview {
	reason := "the node list was refused, so the machines' usage is not shown"
	out.Total, out.Healthy = 0, 0
	out.Alerts = nil
	out.CPU = Capacity{Name: "CPU", Unit: UnitCores}
	out.Memory = Capacity{Name: "Memory", Unit: UnitBytes}
	out.Pods = Capacity{Name: "Pods", Unit: UnitCount}
	out.Now, out.NowFrom, out.NowNodes = Current{}, unavailable(reason), NodesNow{Err: errors.New(reason)}
	out.History = unavailable(reason)
	out.CPUHistory, out.MemoryHistory = emptyClusterHistory()
	out.CPUHistory.Unavailable, out.MemoryHistory.Unavailable = reason, reason
	return out
}

// nowFromElsewhere asks NowElsewhere after metrics-server's 404 and puts its
// answer where metrics-server's would have gone. A panic is a failed read.
func nowFromElsewhere(ctx context.Context, elsewhere NowElsewhere, from Availability, now NodesNow) (Availability, NodesNow) {
	answer, crash := askElsewhere(ctx, elsewhere)
	if crash != nil {
		answer = Elsewhere{Err: fmt.Errorf("the fallback usage read panicked: %s", firstLine(fmt.Sprint(crash)))}
	}
	switch {
	case answer.Err != nil:
		return failedWith(answer.Err, ""), NodesNow{Err: answer.Err}
	case answer.ByNode != nil:
		// Settled into the tile's figure by wholeNow, as metrics-server's are.
		return available(answer.From + " answered"), NodesNow{ByNode: answer.ByNode, From: answer.From}
	case answer.Absent != "":
		// %w, so the tile still reads the 404 as "not installed".
		return from, NodesNow{Err: fmt.Errorf("%w; %s", now.Err, answer.Absent)}
	}
	return from, now
}

// askElsewhere runs the fallback with its panic caught, for nowFromElsewhere.
func askElsewhere(ctx context.Context, elsewhere NowElsewhere) (answer Elsewhere, crash any) {
	defer func() { crash = recover() }()
	return elsewhere(ctx), nil
}

// wholeNow is the Capacity tile's reading of now: the sum over the nodes the
// capacity counts, or no figure where the source left any of them out. A sum
// over some machines against a capacity over all of them is low, wrong in the
// reassuring direction, so it is unknown with the missing nodes named. It is
// summed over the node list, so a reading for a node the list no longer has is
// not counted.
func wholeNow(nodes []corev1.Node, now NodesNow, total Current, from Availability) (Current, Availability) {
	if now.ByNode == nil {
		// No answer at all; NowFrom already says why.
		return total, from
	}
	source := now.From
	if source == "" {
		source = fromMetricsServer
	}
	var sum Current
	var missing []string
	for i := range nodes {
		reading, ok := now.ByNode[nodes[i].Name]
		if !ok {
			missing = append(missing, nodes[i].Name)
			continue
		}
		sum.CPUMilli += reading.CPUMilli
		sum.MemoryBytes += reading.MemoryBytes
		sum.Nodes++
	}
	switch {
	case sum.Nodes == 0:
		// It answered about nobody yet: not measured, never "0m used".
		return Current{}, unavailable(source + " answered and has not measured any node yet")
	case len(missing) > 0:
		sort.Strings(missing)
		return Current{}, unavailable(fmt.Sprintf(
			"%s measured %d of the %d nodes, so a sum would understate what this cluster uses; not measured: %s",
			source, sum.Nodes, len(nodes), strings.Join(missing, ", ")))
	}
	return sum, available(source + " answered")
}

// maxEvents caps the warning feed, which the page draws 20 at a time: a cluster
// in trouble emits thousands, mostly the same few, so the newest are kept.
const maxEvents = 200

func rank(class string) int {
	switch class {
	case ClassBad:
		return 0
	case ClassWarn:
		return 1
	default:
		return 2
	}
}

// --- nodes ---------------------------------------------------------------------

// nodeHealthy is whether a node counts towards Healthy: its status reads OK
// and none of its conditions is notable. A Ready node under pressure is about
// to evict something, so it does not count.
func nodeHealthy(node *corev1.Node) bool {
	_, class := nodeStatus(node)
	return class == ClassOK && len(notableConditions(node)) == 0
}

// nodeStatus is resources.NodeStatus, so every page reads a node the same way
// (a cordoned node reads cordoned, as in kubectl). The rank is dropped because
// this page sorts on its own summary.
func nodeStatus(node *corev1.Node) (string, string) {
	status, class, _ := resources.NodeStatus(node)
	return status, class
}

// pressures are the conditions whose healthy value is False, the inverse of
// Ready: MemoryPressure=True is the bad one.
var pressures = []corev1.NodeConditionType{
	corev1.NodeMemoryPressure,
	corev1.NodeDiskPressure,
	corev1.NodePIDPressure,
	corev1.NodeNetworkUnavailable,
}

func notableConditions(node *corev1.Node) []NodeCondition {
	var out []NodeCondition
	for _, c := range node.Status.Conditions {
		notable := false
		switch {
		case c.Type == corev1.NodeReady && c.Status != corev1.ConditionTrue:
			notable = true
		case isPressure(c.Type) && c.Status == corev1.ConditionTrue:
			notable = true
		}
		if !notable {
			continue
		}
		out = append(out, NodeCondition{
			Type:    string(c.Type),
			Reason:  c.Reason,
			Message: c.Message,
			Class:   ClassBad,
		})
	}
	return out
}

func isPressure(t corev1.NodeConditionType) bool {
	for _, p := range pressures {
		if p == t {
			return true
		}
	}
	return false
}

// alertsFor turns a node's notable conditions into the alerts at the top of
// the page, the page's only per-node sign: a red pill deep in a table is
// missable.
func alertsFor(node *corev1.Node) []Alert {
	var out []Alert
	for _, c := range notableConditions(node) {
		summary := c.Type
		switch c.Type {
		case string(corev1.NodeReady):
			summary = "is not Ready"
		case string(corev1.NodeMemoryPressure):
			summary = "is under memory pressure"
		case string(corev1.NodeDiskPressure):
			summary = "is under disk pressure"
		case string(corev1.NodePIDPressure):
			summary = "is out of process IDs"
		case string(corev1.NodeNetworkUnavailable):
			summary = "has no working network"
		}
		out = append(out, Alert{
			Class:   ClassBad,
			Subject: node.Name,
			Summary: summary,
			Detail:  strings.TrimSpace(c.Reason + " " + c.Message),
			// Stated, not recovered from Subject, so a link builder knows the
			// kind is cluster-scoped.
			Kind: "Node",
			Name: node.Name,
		})
	}
	return out
}

// --- requests ------------------------------------------------------------------

type nodeRequests struct {
	cpuMilli    int64
	memoryBytes int64
	pods        int64
}

func (r *nodeRequests) add(o nodeRequests) {
	r.cpuMilli += o.cpuMilli
	r.memoryBytes += o.memoryBytes
	r.pods += o.pods
}

func aggregateRequests(pods []corev1.Pod) nodeRequests {
	var total nodeRequests
	for i := range pods {
		if !holdsCapacity(&pods[i]) {
			continue
		}
		total.add(podRequests(&pods[i]))
	}
	return total
}

// holdsCapacity reports whether a pod still costs its node anything. Finished
// pods linger in Succeeded or Failed, and counting them makes an idle cluster
// read as fully requested; an unscheduled pod costs no node.
func holdsCapacity(pod *corev1.Pod) bool {
	if pod.Spec.NodeName == "" {
		return false
	}
	return pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed
}

// podRequests is what the scheduler reserves for a pod, which is not the sum
// of its containers. It mirrors k8s.io/component-helpers/resource's
// PodRequests with default options (spec requests, overhead included):
//
//   - Init containers run one at a time before the rest, so the pod needs the
//     larger of the biggest init step and the regular containers' sum.
//   - A native sidecar (restartPolicy: Always) keeps running and adds to both.
//     Order matters: an ordinary init container shares the node only with the
//     sidecars declared before it.
//   - Each resource is its own maximum, so a pod can reserve its init's CPU
//     peak and its run's memory peak.
//   - Spec.Overhead (the RuntimeClass's sandbox cost) is added on top.
//
// Not handled: pod-level Spec.Resources, resized requests in the status,
// defaults for containers that set none, and DRA claims; such a pod reads its
// containers' spec.
func podRequests(pod *corev1.Pod) nodeRequests {
	var running nodeRequests
	for i := range pod.Spec.Containers {
		running.add(containerRequests(&pod.Spec.Containers[i]))
	}

	// In declaration order, because the order is the semantics. sidecars is
	// the sidecars started so far; initPeak the largest single init step.
	var sidecars, initPeak nodeRequests
	for i := range pod.Spec.InitContainers {
		init := &pod.Spec.InitContainers[i]
		req := containerRequests(init)

		// A sidecar's step is every sidecar started so far, itself included;
		// an ordinary init container's is itself plus those same sidecars.
		var step nodeRequests
		// resources.IsNativeSidecar keeps this answer the same as the pod
		// status and the Ready and Containers columns.
		if resources.IsNativeSidecar(init) {
			running.add(req)
			sidecars.add(req)
			step = sidecars
		} else {
			step = req
			step.add(sidecars)
		}
		initPeak.cpuMilli = max(initPeak.cpuMilli, step.cpuMilli)
		initPeak.memoryBytes = max(initPeak.memoryBytes, step.memoryBytes)
	}

	out := nodeRequests{
		cpuMilli:    max(running.cpuMilli, initPeak.cpuMilli),
		memoryBytes: max(running.memoryBytes, initPeak.memoryBytes),
		pods:        1,
	}
	// After the maximum: the overhead is the sandbox's, paid in every phase.
	out.cpuMilli += quantityMilli(pod.Spec.Overhead, corev1.ResourceCPU)
	out.memoryBytes += quantityValue(pod.Spec.Overhead, corev1.ResourceMemory)
	return out
}

func containerRequests(c *corev1.Container) nodeRequests {
	return nodeRequests{
		cpuMilli:    quantityMilli(c.Resources.Requests, corev1.ResourceCPU),
		memoryBytes: quantityValue(c.Resources.Requests, corev1.ResourceMemory),
	}
}

func quantityMilli(list corev1.ResourceList, name corev1.ResourceName) int64 {
	if q, ok := list[name]; ok {
		return q.MilliValue()
	}
	return 0
}

func quantityValue(list corev1.ResourceList, name corev1.ResourceName) int64 {
	if q, ok := list[name]; ok {
		return q.Value()
	}
	return 0
}

// --- events --------------------------------------------------------------------

// badReasons are the warning reasons that mean something stopped rather than
// something is slow; they get the strongest color.
var badReasons = []string{"Failed", "BackOff", "OOM", "Evicted", "NotReady", "Unhealthy", "Killing"}

// eventLines is the warning feed, newest first and cut to limit, with the
// count before the cut so the page can say the feed is not whole.
func eventLines(events []corev1.Event, now time.Time, limit int) ([]EventLine, int) {
	lines := make([]EventLine, 0, len(events))
	for i := range events {
		e := &events[i]
		when := resources.EventTime(e)
		lines = append(lines, EventLine{
			When:      when,
			Age:       resources.ShortDuration(now.Sub(when)),
			Namespace: e.Namespace,
			Object:    strings.TrimSpace(e.InvolvedObject.Kind + "/" + e.InvolvedObject.Name),
			Kind:      e.InvolvedObject.Kind,
			Name:      e.InvolvedObject.Name,
			Reason:    e.Reason,
			Message:   resources.Clip(e.Message, 160),
			Count:     max(resources.EventCount(e), 1),
			Class:     gradeReason(e.Reason),
		})
	}

	// Newest first: an event list answers "what just happened".
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].When.After(lines[j].When) })
	total := len(lines)
	if total > limit {
		lines = lines[:limit]
	}
	return lines, total
}

func gradeReason(reason string) string {
	for _, bad := range badReasons {
		if strings.Contains(reason, bad) {
			return ClassBad
		}
	}
	return ClassWarn
}

// --- the API calls -------------------------------------------------------------

// all is what every list on this page asks with: the API server's watch cache
// rather than a quorum read, in chunks followed to the end (see
// resources.Cached and resources.Paged). The page redraws every few seconds.
func all() metav1.ListOptions { return resources.Cached() }

// ErrNarrowedRequests is why the requested figures are unknown to a person
// whose pod list was narrowed. See ClusterWithFallback.
var ErrNarrowedRequests = fmt.Errorf("you may not list pods in every namespace, so what the pods in this cluster request is not known to you")

func listNodes(ctx context.Context, r resources.Reader) ([]corev1.Node, error) {
	nodes, _, err := resources.Listed(ctx, r, resources.ClusterScoped("", "v1", "nodes"), all(),
		r.Clients.CoreV1().Nodes().List,
		func(l *corev1.NodeList) []corev1.Node { return l.Items })
	return nodes, err
}

// listPods is shared between the two dashboards; see podShare. The share is of
// the read, never of the answer: each caller's access review narrows the one
// list for them.
func listPods(ctx context.Context, r resources.Reader, namespace string) ([]corev1.Pod, access.Visible, error) {
	question := resources.InNamespace("", "v1", "pods", namespace)
	return access.List(ctx, r.Gate, r.Cluster, question.Attrs, podNamespace, func(ctx context.Context) ([]corev1.Pod, error) {
		return podLists.list(ctx, r.Cluster, namespace, func(ctx context.Context) ([]corev1.Pod, error) {
			return resources.Paged(ctx, all(), r.Clients.CoreV1().Pods(namespace).List,
				func(l *corev1.PodList) []corev1.Pod { return l.Items })
		})
	})
}

func podNamespace(pod *corev1.Pod) string { return pod.Namespace }

// listWarningEvents asks the server to filter, and filters again: a field
// selector is a hint some API servers and every fake client ignore.
//
// It is not capped: a limited list comes back in key order, not time order, so
// a cap would keep arbitrary events under a heading that says "latest".
func listWarningEvents(ctx context.Context, r resources.Reader, namespace string) ([]corev1.Event, access.Visible, error) {
	opts := all()
	opts.FieldSelector = "type=Warning"
	items, visible, err := resources.Listed(ctx, r, resources.InNamespace("", "v1", "events", namespace), opts,
		r.Clients.CoreV1().Events(namespace).List,
		func(l *corev1.EventList) []corev1.Event { return l.Items })
	if err != nil {
		return nil, visible, err
	}
	out := make([]corev1.Event, 0, len(items))
	for _, e := range items {
		if e.Type == corev1.EventTypeWarning {
			out = append(out, e)
		}
	}
	return out, visible, nil
}

// versionTimeout bounds the one decorative call on this page: the version's
// absence is already reported as a degraded panel, so it is not worth waiting
// on. A var only so a test can shrink it.
var versionTimeout = 5 * time.Second

// serverVersion asks the API server what it is, and gives up after
// versionTimeout. The context must reach the request, or a black-holed route
// would hold it to client-go's own timeout: Discovery()'s ServerVersion takes
// none, so the contextual interface client-go's *DiscoveryClient satisfies is
// asserted, with a goroutine as the fallback for other implementations.
func serverVersion(ctx context.Context, api kubernetes.Interface) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()

	info, err := discoverServerVersion(ctx, api.Discovery())
	if err != nil {
		// Said in this page's words rather than as "context deadline
		// exceeded"; a caller draws it into a degraded panel.
		if ctx.Err() != nil {
			return "", fmt.Errorf("the API server did not state its version within %s", versionTimeout)
		}
		return "", err
	}
	// An answer that carries no version is not a failure: the server
	// replied, and the page prints missing.
	if info == nil || strings.TrimSpace(info.GitVersion) == "" {
		return missing, nil
	}
	return info.GitVersion, nil
}

// discoverServerVersion asserts the contextual interface, else falls back to a
// goroutine. Its channel is buffered because that goroutine cannot be stopped
// and must be able to post with nobody left to read.
func discoverServerVersion(ctx context.Context, d discovery.DiscoveryInterface) (*version.Info, error) {
	if withCtx, ok := d.(discovery.ServerVersionInterfaceWithContext); ok {
		return withCtx.ServerVersionWithContext(ctx)
	}

	type answer struct {
		info *version.Info
		err  error
	}
	done := make(chan answer, 1)
	// Through safego: a panic is this read's error, still posted.
	safego.Go("overview: the server version", func() error {
		info, err := d.ServerVersion()
		done <- answer{info: info, err: err}
		return nil
	}, func(err error) {
		if err != nil {
			done <- answer{err: err}
		}
	})

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case a := <-done:
		return a.info, a.err
	}
}

// crashedHistory is the history whose read panicked: both lines a failed read
// with the crash as their error, so internal/web sends a failure rather than
// "no Prometheus".
func crashedHistory(crash any) (Availability, History, History) {
	failure := Crash{What: "reading this cluster's history", Value: firstLine(fmt.Sprint(crash))}
	cpu, memory := emptyClusterHistory()
	for _, line := range []*History{&cpu, &memory} {
		line.Unavailable, line.Missing, line.Err = failure.Error(), MissingFailed, failure
	}
	return Availability{Detail: failure.Error(), Cause: failure}, cpu, memory
}

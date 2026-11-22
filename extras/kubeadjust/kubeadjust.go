// Package kubeadjust is KubeAdjust, right-sizing: KubeTower's page that answers
// whether a cluster's workloads ask for what they use, and if not, what they
// should ask for. It is an extension of the console, part of the core and
// behind its own per-cluster switch, off by default.
//
// # What is here
//
// Four read-only routes under the extension's prefix: `GET /`, answering the
// whole page as JSON for `?ns=`, `GET /node/{name}`, the tab in a Node's panel
// (nodetab.go), `GET /workload/{kind}/{namespace}/{name}`, the tab in a
// workload's panel (workload.go), and `GET /window?ns=`, only the state of the
// scope's usage window, which a page drawn before its week was read polls
// instead of reading itself again. A React page has one load and its refetch,
// so there is no separate "check again" route: the page route serves both.
//
// One answer per scope. `ns` is the scope, and changes what is read; the
// page's other two address parameters, `view` (the overview or the Nodes view)
// and `cat` (the category filter), are drawn by the page from the scope's one
// answer, so switching either reads nothing. The route accepts them and
// ignores them, so an older address still answers.
//
// The arithmetic is the engine's (`rightsizing`). This package reads the
// cluster for it and shapes the answer for the page, including the bars' and
// the gauges' geometry, which the client draws and never rescales.
//
// # Who sees what
//
// Every read goes through a door reviewed for the person behind the request:
// pods, ReplicaSets, Jobs, ControllerRevisions, nodes, claims and namespaces
// through Host.List, each container's CPU and memory through
// Host.ContainerUsage, the usage window through Host.UsageWindow, and the
// kubelets' disk figures through Host.Usage.
// **Host.Client is never called**: nothing here needs the console's own
// credentials, and nothing here writes. A recommendation is a
// `kubectl set resources` the operator reads, copies and runs; there is no
// Apply, because applying one restarts every pod of the workload and that is
// the operator's deployment to run, not this page's button.
//
// # An empty page is a claim, and it has to be earned
//
// "No suggestions" is what a well-tuned cluster looks like, and it is also what
// a cluster nobody could measure looks like. So the answer always carries four
// notes - Usage, Node capacity, Disk and History - each saying what it was
// built from or why it could not be, and every measurement carries absent,
// zero and unknown as three different values.
package kubeadjust

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/extras/kubeadjust/rightsizing"
)

// Key is `kubeadjust`. It is the page's URL segment, the `KT_EXTRAS` name and
// the key the per-cluster switch is stored under, so it is not free to change.
const Key = "kubeadjust"

// legacyKey is the page's key when it was called "Right-sizing". Kept in Legacy
// so a switch stored under it, or a KT_EXTRAS line naming it, still reads on
// (`internal/extras/legacykey_test.go` holds the store's side).
const legacyKey = "rightsizing"

// Label is the menu entry: the tool's name.
const Label = "KubeAdjust"

var (
	podsGVR        = schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	nodesGVR       = schema.GroupVersionResource{Version: "v1", Resource: "nodes"}
	namespacesGVR  = schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}
	claimsGVR      = schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}
	replicaSetsGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}
	jobsGVR        = schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}
	// revisionsGVR is where a StatefulSet and a DaemonSet keep the templates
	// they rolled out - see rightsizing/rollout.go.
	revisionsGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "controllerrevisions"}
	hpasGVR      = schema.GroupVersionResource{Group: "autoscaling", Version: "v2", Resource: "horizontalpodautoscalers"}
	// vpasGVR is a custom resource many clusters do not have; its absence is
	// an answer (nothing sizes these workloads vertically), not a failure.
	vpasGVR = schema.GroupVersionResource{Group: "autoscaling.k8s.io", Version: "v1", Resource: "verticalpodautoscalers"}
)

// KubeAdjust is the extension `cmd/kubetower` registers.
type KubeAdjust struct {
	// now is the clock, a field so a test can stand at a chosen instant.
	now func() time.Time
}

// New is the constructor `cmd/kubetower` calls.
func New() KubeAdjust { return KubeAdjust{now: time.Now} }

func (KubeAdjust) Key() string   { return Key }
func (KubeAdjust) Label() string { return Label }

func (KubeAdjust) Summary() string {
	return "Whether each workload's requests and limits are the right size, and the kubectl commands that would fix them, to review and run by hand."
}

// Needs is drawn beside the switch: what the page measures with, and what each
// absence costs.
func (KubeAdjust) Needs() string {
	return "metrics-server for usage; a Prometheus for a seven-day window (otherwise one sample per pod); " +
		"the nodes/proxy permission for disk figures"
}

func (KubeAdjust) Legacy() []string { return []string{legacyKey} }

// Routes are four reads, all GETs: nothing here changes a cluster. The page,
// one node for the tab in a Node's panel, one workload for the tab in a
// Deployment's, StatefulSet's or DaemonSet's, and the state of a scope's window.
func (k KubeAdjust) Routes(h extension.Host) []extension.Route {
	return []extension.Route{
		{Method: http.MethodGet, Pattern: "/{$}", Handler: k.page(h)},
		{Method: http.MethodGet, Pattern: "/node/{name}", Handler: k.node(h)},
		{Method: http.MethodGet, Pattern: "/workload/{kind}/{namespace}/{name}", Handler: k.workload(h)},
		{Method: http.MethodGet, Pattern: "/window", Handler: k.windowState(h)},
	}
}

// windowAnswer is what `GET /window` answers: the state the page's History
// would have now, and nothing else.
type windowAnswer struct {
	Cluster   string `json:"cluster"`
	Namespace string `json:"namespace"`
	Window    string `json:"window"`
	// WindowReadAt is the page's: when the window was read, absent while it
	// is pending.
	WindowReadAt string `json:"windowReadAt,omitempty"`
}

// windowState answers only where the scope's usage window stands: kept,
// still being read (pending, after the same wait as a page), or why there is
// none. A page drawn before its week polls this, and reads itself again once
// it is no longer pending: a poll of the page would list the pods, every other
// kind and metrics-server each time, for the same figures.
func (k KubeAdjust) windowState(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster := r.PathValue("cluster")
		namespace := r.URL.Query().Get("ns")
		if namespace != "" && len(validation.IsDNS1123Label(namespace)) > 0 {
			writeProblem(w, http.StatusBadRequest, CodeBadNamespace,
				"\""+namespace+"\" is not a namespace name.", nil)
			return
		}
		wait, waited := context.WithTimeout(r.Context(), windowWait)
		window, err := h.UsageWindow(r.WithContext(wait), cluster, namespace)
		late := errors.Is(wait.Err(), context.DeadlineExceeded) && r.Context().Err() == nil
		waited()
		history := historyOf(window, err)
		state, readAt := string(history.Window), readAtText(history.ReadAt)
		switch {
		case late:
			state, readAt = string(WindowPending), ""
		case ends(err):
			writeReadFailure(w, cluster, err)
			return
		}
		writeJSON(w, http.StatusOK, windowAnswer{Cluster: cluster, Namespace: namespace, Window: state,
			WindowReadAt: readAt})
	})
}

func (k KubeAdjust) page(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster := r.PathValue("cluster")
		namespace := r.URL.Query().Get("ns")
		if namespace != "" && len(validation.IsDNS1123Label(namespace)) > 0 {
			// Refused rather than passed on: a namespace is a DNS-1123 label, so
			// anything else did not come from a cluster.
			writeProblem(w, http.StatusBadRequest, CodeBadNamespace,
				"\""+namespace+"\" is not a namespace name.", nil)
			return
		}
		read, err := k.read(r, h, cluster, namespace, "")
		if err != nil {
			writeReadFailure(w, cluster, err)
			return
		}
		mark := time.Now()
		answer := shape(read, cluster, namespace)
		read.timing.since("shape", mark)
		writeTimedJSON(w, http.StatusOK, answer, &read.timing)
	})
}

// reading is what one request read, before it is shaped.
type reading struct {
	report rightsizing.Report
	// pods says whether the pods were read at all (false when usage failed
	// first and the page stopped there).
	podsRead bool
	// namespaces is the selector's list, and namespacesNote why it is partial.
	namespaces     []string
	namespacesNote string
	// pods and usage are what the report was built from, kept for the node
	// tab, which draws every pod of one node rather than the busiest few.
	pods  []corev1.Pod
	usage map[string]rightsizing.Usage
	// podsComplete is false when the pods were narrowed to the namespaces the
	// person may list: every node total is then a sum over some of its pods.
	podsComplete bool
	// nodes are the nodes the display read, nil when it read none: the node
	// tab takes a node's runtime and architecture from them.
	nodes []corev1.Node
	// nodesErr is why the nodes were not read, nil when they were.
	nodesErr error
	// timing is where the read's time went, for the Server-Timing header.
	timing timing
}

// read asks the cluster, through the person's doors, for everything one page
// needs. Its error is the request's: an unknown cluster, or pods that could not
// be listed at all. Everything else is a note on the page.
//
// node, when set, is the one node the node tab draws: its kubelet is the only
// one asked, where the page asks every node hosting a pod in scope: a kubelet
// read is a request per node.
//
// Once the usage says there is something to size, the reads go out together
// (gather.go), so a display waits for its slowest read and not for their sum.
func (k KubeAdjust) read(r *http.Request, h extension.Host, cluster, namespace, node string) (reading, error) {
	var out reading
	t := &out.timing
	mark := time.Now()

	// The usage, first: without it nothing else is read, and a
	// cluster with no metrics-server gets its sentence rather than a wait.
	live, usageErr := h.ContainerUsage(r, cluster, namespace)
	mark = t.since("usage", mark)
	if ends(usageErr) {
		return out, usageErr
	}
	usage := make(map[string]rightsizing.Usage, len(live.Containers))
	for key, u := range live.Containers {
		usage[key] = rightsizing.Usage{CPUMillicores: u.CPUMillicores, MemoryBytes: u.MemoryBytes}
	}

	in := rightsizing.Inputs{Namespace: namespace, Now: k.now(), Usage: usage, UsageErr: usageErr}
	if !rightsizing.Measurable(usage, usageErr) {
		// The window, only when there is usage to size: without it nothing on
		// the page is sized, and seven days of Prometheus series would be read
		// for nothing. The History note says why it was not asked for.
		in.History = rightsizing.HistoryFromWindow(extension.Window{State: extension.WindowUnreadable,
			Detail: "the window was not asked for, because no per-container usage could be read"})
		// Not asked is not unreadable: nothing is known about a Prometheus.
		in.History.Window = ""
		out.report = rightsizing.Assemble(in)
		out.namespaces, out.namespacesNote = k.namespaces(r, h, cluster, nil)
		return out, nil
	}

	g := gather(r, h, cluster, namespace, node)
	g.timings(t)
	mark = t.since("reads", mark)
	if err := g.failure(); err != nil {
		return out, err
	}

	pods := g.pods
	in.History = g.history
	in.Pods = pods
	out.podsRead = true
	out.pods, out.usage = pods, usage
	out.podsComplete = g.podsPage.Complete
	in.Owners, in.ReplicaSets, in.ReplicaSetsErr, in.OwnersErr = g.owners, g.replicaSets, g.replicaSetsErr, g.ownersErr
	in.ControllerRevisions, in.ControllerRevisionsErr = g.revisions, g.revisionsErr
	in.Autoscalers, in.AutoscalersErr = g.autoscalers, g.autoscalersErr
	in.Nodes, in.NodesErr = g.nodes, g.nodesErr
	out.nodes, out.nodesErr = g.nodes, g.nodesErr
	in.Claims, in.ClaimsErr = g.claims, g.claimsErr
	in.Storage, in.StorageState = g.storage, g.storageState

	out.report = rightsizing.Assemble(in)
	mark = t.since("engine", mark)
	if node != "" {
		// The coverage sentence over this node's containers: the window was
		// read for its namespaces only, so the page's denominator - every
		// container in scope - would call a whole window partial.
		out.report.History = nodeHistoryNote(g.history, node, pods, usage)
	}

	// What the doors narrowed, said where the reader looks. A narrowed answer
	// drawn as complete is the failure an empty one is, one step along.
	podsPage := g.podsPage
	if out.report.Usage.OK {
		if !live.Complete && live.Note != "" {
			out.report.Usage.Detail += " · " + live.Note
		}
		if !podsPage.Complete && podsPage.Note != "" {
			out.report.Usage.Detail += " · " + podsPage.Note
		}
		if g.badPods > 0 {
			out.report.Usage.Detail += " · " + plural(g.badPods, "pod") + " could not be read, so left out"
		}
	}
	if out.report.Capacity.OK && g.badNodes > 0 {
		// Counted and said, never dropped in silence: "3 node(s)" over a list
		// that held four is a capacity read about three machines.
		out.report.Capacity.Detail += " · " + plural(g.badNodes, "node") + " could not be read, so left out"
	}
	if out.report.Capacity.OK && !podsPage.Complete && namespace == "" {
		out.report.Capacity.Detail += " · node totals count only the pods in the namespaces you may list"
	}
	out.namespaces, out.namespacesNote = namespacesFrom(g.namespacesPage, g.namespacesErr, pods)
	t.since("notes", mark)
	return out, nil
}

// ends is whether err ends the request rather than becoming a note: the
// cluster is not one this console knows, or the request was canceled.
func ends(err error) bool {
	return errors.Is(err, extension.ErrUnknownCluster) || errors.Is(err, context.Canceled)
}

// readAtText is a window's read time as the answers carry it, RFC 3339 in UTC,
// or "" when it is not known.
func readAtText(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.UTC().Format(time.RFC3339)
}

// historyOf is the History for what one UsageWindow answered. Its error, past
// the two the caller returns, is a window that could not be read: said as one,
// never drawn as no load.
func historyOf(window extension.Window, err error) rightsizing.History {
	if err != nil {
		return rightsizing.HistoryFromWindow(extension.Window{
			State: extension.WindowUnreadable, Detail: rightsizing.ShortError(err)})
	}
	return rightsizing.HistoryFromWindow(window)
}

// namespaces is the selector's list. When the namespaces cannot be listed - a
// person allowed into some namespaces is often not allowed to list them - it
// falls back to the namespaces of the pods that were read, and says so.
func (KubeAdjust) namespaces(r *http.Request, h extension.Host, cluster string, pods []corev1.Pod) ([]string, string) {
	page, err := h.List(r, cluster, extension.ListQuery{GVR: namespacesGVR})
	return namespacesFrom(page, err, pods)
}

// namespacesFrom is the selector's list out of the namespaces' read, or the
// namespaces of the pods when that read failed.
func namespacesFrom(page extension.Page, err error, pods []corev1.Pod) ([]string, string) {
	if err == nil {
		names := make([]string, 0, len(page.Items))
		for _, item := range page.Items {
			names = append(names, item.GetName())
		}
		return names, page.Note
	}
	seen := map[string]bool{}
	var names []string
	for i := range pods {
		if !seen[pods[i].Namespace] {
			seen[pods[i].Namespace] = true
			names = append(names, pods[i].Namespace)
		}
	}
	return names, "the namespaces could not be listed (" + rightsizing.ShortError(err) +
		"), so this offers the ones the pods on this page are in"
}

// owners maps a ReplicaSet or a Job onto the controller that made it, so a pod
// can be attributed to the Deployment or CronJob whose template would be
// edited. Two hops and not one: stopping at the first would file a suggestion
// under `web-7d9f8b6c5`, a name that changes on every rollout and that no
// `kubectl set resources` can address.
//
// The ReplicaSets are also returned typed: they are a Deployment's kept
// revisions, which remember a memory limit raised after a kill
// (rightsizing/rollout.go), with their own error: why they could not be used.
// A ReplicaSet that would not convert fails them all, because a missing
// revision is a history that may be wrong rather than one that is short - and
// it does not make the workload names partial, which the last error says.
func owners(r *http.Request, h extension.Host, cluster, namespace string) (
	index map[string]metav1.OwnerReference, replicaSets []appsv1.ReplicaSet, replicaSetsErr, err error) {
	index = map[string]metav1.OwnerReference{}
	for _, kind := range []struct {
		gvr  schema.GroupVersionResource
		kind string
	}{{replicaSetsGVR, "ReplicaSet"}, {jobsGVR, "Job"}} {
		page, listErr := h.List(r, cluster, extension.ListQuery{GVR: kind.gvr, Namespace: namespace})
		if listErr != nil {
			err = errors.New("listing " + kind.gvr.Resource + ": " + rightsizing.ShortError(listErr))
			if kind.kind == "ReplicaSet" {
				replicaSetsErr = err
			}
			return index, replicaSets, replicaSetsErr, err
		}
		if kind.kind == "ReplicaSet" {
			items, bad := typed[appsv1.ReplicaSet](page.Items)
			if bad > 0 {
				replicaSetsErr = errors.New(plural(bad, "replicaset") + " could not be read")
			} else {
				replicaSets = items
			}
		}
		for i := range page.Items {
			item := &page.Items[i]
			if owner := metav1.GetControllerOfNoCopy(item); owner != nil {
				index[kind.kind+"/"+item.GetNamespace()+"/"+item.GetName()] = *owner
			}
		}
	}
	return index, replicaSets, replicaSetsErr, nil
}

// controllerRevisions is every StatefulSet's and DaemonSet's kept revisions in
// scope. A revision that would not convert fails them all, for the reason
// owners gives.
func controllerRevisions(r *http.Request, h extension.Host, cluster, namespace string) ([]appsv1.ControllerRevision, error) {
	page, err := h.List(r, cluster, extension.ListQuery{GVR: revisionsGVR, Namespace: namespace})
	if err != nil {
		return nil, errors.New("listing controllerrevisions: " + rightsizing.ShortError(err))
	}
	items, bad := typed[appsv1.ControllerRevision](page.Items)
	if bad > 0 {
		return nil, errors.New(plural(bad, "controllerrevision") + " could not be read")
	}
	return items, nil
}

// autoscalers reads the HPAs and the VPAs in scope, so a workload one of them
// already sizes is not argued with (rightsizing/autoscale.go).
//
// A cluster without the VPA custom resource answers its list with "not
// found", and that is the ordinary case: no VPA, nothing to say. Any other
// failure of either list is reported.
func autoscalers(r *http.Request, h extension.Host, cluster, namespace string) (map[string][]rightsizing.Autoscaler, error) {
	hpaPage, err := h.List(r, cluster, extension.ListQuery{GVR: hpasGVR, Namespace: namespace})
	if err != nil {
		return nil, errors.New("listing horizontalpodautoscalers: " + rightsizing.ShortError(err))
	}
	hpas, _ := typed[autoscalingv2.HorizontalPodAutoscaler](hpaPage.Items)
	vpaPage, err := h.List(r, cluster, extension.ListQuery{GVR: vpasGVR, Namespace: namespace})
	switch {
	case err == nil:
	case apierrors.IsNotFound(err) || meta.IsNoMatchError(err) || notServed(err):
		vpaPage = extension.Page{}
	default:
		return rightsizing.Autoscalers(hpas, nil), errors.New("listing verticalpodautoscalers: " + rightsizing.ShortError(err))
	}
	return rightsizing.Autoscalers(hpas, vpaPage.Items), nil
}

// notServed recognizes the console's own way of saying a resource does not
// exist on this cluster, which is not always a typed API error by the time it
// has crossed the cache.
func notServed(err error) bool {
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "could not find the requested resource") ||
		strings.Contains(text, "not served") || strings.Contains(text, "no matches for")
}

// claims is each PVC's capacity, keyed `namespace/name`: what the claim was
// given, which is the ceiling its volume is judged against.
func claims(r *http.Request, h extension.Host, cluster, namespace string) (map[string]int64, error) {
	page, err := h.List(r, cluster, extension.ListQuery{GVR: claimsGVR, Namespace: namespace})
	if err != nil {
		return nil, errors.New("listing persistentvolumeclaims: " + rightsizing.ShortError(err))
	}
	items, _ := typed[corev1.PersistentVolumeClaim](page.Items)
	out := make(map[string]int64, len(items))
	for i := range items {
		if size, ok := items[i].Status.Capacity[corev1.ResourceStorage]; ok {
			out[items[i].Namespace+"/"+items[i].Name] = size.Value()
		}
	}
	return out, nil
}

// typed converts what List answered into the API's own types, and counts what
// would not convert rather than dropping it in silence.
func typed[T any](items []unstructured.Unstructured) ([]T, int) {
	out := make([]T, 0, len(items))
	bad := 0
	for i := range items {
		var v T
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(items[i].Object, &v); err != nil {
			bad++
			continue
		}
		out = append(out, v)
	}
	return out, bad
}

// --- failures ----------------------------------------------------------------

// problem is the failure body: Code is what the page branches on, Message what
// it prints.
type problem struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
}

// The codes this route answers with. Protocol: the page names them too.
const (
	CodeUnknownCluster = "unknown_cluster"
	CodeListFailed     = "list_failed"
	CodeBadNamespace   = "bad_namespace"
	// A read refused for the person: 403, and 503 when it could not be asked.
	CodeListForbidden  = extension.CodeListForbidden
	CodeListUnreviewed = extension.CodeListUnreviewed
)

// writeReadFailure answers a read that failed as a whole: 404 for a cluster that
// is not in the kubeconfig, 403 or 503 for a read refused for the person, 502
// for one whose pods could not be listed.
func writeReadFailure(w http.ResponseWriter, cluster string, err error) {
	switch {
	case errors.Is(err, extension.ErrUnknownCluster):
		writeProblem(w, http.StatusNotFound, CodeUnknownCluster, extension.UnknownClusterText(cluster), err)
	case errors.Is(err, context.Canceled):
		// The page went away, or a newer request replaced this one.
	default:
		// A refusal for the person is an answer about them, never a 502: 403
		// when the cluster says they may not, 503 when that could not be asked.
		if refused, ok := extension.RefusedRead(err,
			"The cluster says you may not read what sizing needs here, so nothing could be sized."); ok {
			writeProblem(w, refused.Status, refused.Code, refused.Message, err)
			return
		}
		writeProblem(w, http.StatusBadGateway, CodeListFailed,
			"This cluster's pods could not be listed, so nothing on it could be sized.", err)
	}
}

func writeProblem(w http.ResponseWriter, status int, code, message string, err error) {
	body := problem{Code: code, Message: message}
	if err != nil {
		body.Detail = rightsizing.ShortError(err)
	}
	writeJSON(w, status, body)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	writeTimedJSON(w, status, body, nil)
}

// writeTimedJSON is writeJSON with the request's Server-Timing, the encoding
// included: the body is encoded before the header is sent, so its time can be.
func writeTimedJSON(w http.ResponseWriter, status int, body any, t *timing) {
	mark := time.Now()
	data, err := json.Marshal(body)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "encode_failed", "The answer could not be encoded.", err)
		return
	}
	if t != nil {
		t.since("encode", mark)
		t.set(w)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(append(data, '\n'))
}

// only is nodes kept to the one named, or nothing when it is not among them: a
// node hosting no pod in scope has no kubelet worth asking.
func only(nodes []string, node string) []string {
	for _, n := range nodes {
		if n == node {
			return []string{node}
		}
	}
	return nil
}

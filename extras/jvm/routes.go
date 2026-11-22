package jvm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/safego"
	"github.com/thonicdev/kubetower/textutil"
)

// The JSON the page reads. web/app/extras/jvm/data.ts declares the same
// shapes, and every failure is a problem with a code the page branches on,
// never a sentence it parses.

// Container is one container of a pod, read from its spec.
type Container struct {
	Name  string `json:"name"`
	Image string `json:"image"`
	// Java is whether anything in the spec says it runs a JVM: Hints is not
	// empty. False is "not seen from the spec", never "not Java".
	Java  bool   `json:"java"`
	Hints []Hint `json:"hints"`
	// Request and Limit are its memory request and limit, null when unset.
	Request *Memory `json:"request"`
	Limit   *Memory `json:"limit"`
	// Heap and Risk are null for a container nothing says is Java: a heap
	// estimate for a process that has none would be invented.
	Heap *Heap `json:"heap"`
	Risk *Risk `json:"risk"`
	// Notes are what the reading could not take into account.
	Notes []string `json:"notes"`
	// WorkingSet is the memory metrics-server measured for it, on the
	// inventory only: null when it was not measured - no metrics-server, a
	// read that failed (the answer's Usage says which), or a container
	// metrics-server has no figure for yet. Never a zero standing for none.
	WorkingSet *Memory `json:"workingSet"`
}

// Pod is one pod's containers.
type Pod struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Phase     string `json:"phase"`
	// Java is whether any of its containers is probably Java.
	Java       bool        `json:"java"`
	Containers []Container `json:"containers"`
	// Owner is the workload that runs it, null for a pod nothing controls.
	Owner *Owner `json:"owner"`
}

// Owner is the workload a pod belongs to, read from its controller reference.
//
// A ReplicaSet's pod is named after its Deployment when the pod's
// pod-template-hash label is the ReplicaSet's suffix, which is how the
// Deployment controller names them; ReplicaSet then keeps the set's own name.
// A ReplicaSet created by anything else stays a ReplicaSet.
type Owner struct {
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	ReplicaSet string `json:"replicaSet,omitempty"`
}

// ownerOf is p's workload, nil without a controller reference.
func ownerOf(p *corev1.Pod) *Owner {
	ref := metav1.GetControllerOf(p)
	if ref == nil {
		return nil
	}
	o := &Owner{Kind: ref.Kind, Name: ref.Name}
	if ref.Kind == "ReplicaSet" && strings.HasPrefix(ref.APIVersion, "apps/") {
		if hash := p.Labels["pod-template-hash"]; hash != "" && strings.HasSuffix(ref.Name, "-"+hash) &&
			len(ref.Name) > len(hash)+1 {
			o.Kind, o.Name, o.ReplicaSet = "Deployment", strings.TrimSuffix(ref.Name, "-"+hash), ref.Name
		}
	}
	return o
}

// inventoryAnswer is `GET /`: the pods of the scope that are probably Java,
// each with its Java containers only.
type inventoryAnswer struct {
	Cluster string `json:"cluster"`
	// Namespace is the `?ns=` it was read for, empty for the whole cluster.
	Namespace string `json:"namespace"`
	// Complete is false when the pod list was narrowed for the person or cut
	// short: an empty list is then not "no Java here", and Note says why.
	Complete bool   `json:"complete"`
	Note     string `json:"note,omitempty"`
	// Read is how many pods were read, running or pending ones only:
	// "none of 40 pods" and "none of 0" are different answers.
	Read int `json:"read"`
	// Found is how many Java pods the read held, before MaxJavaPods cut the
	// list: more than len(Pods) when the answer was cut.
	Found int   `json:"found"`
	Pods  []Pod `json:"pods"`
	// Unreadable is how many pods the cluster returned that could not be
	// decoded, said rather than dropped.
	Unreadable int `json:"unreadable,omitempty"`
	// Usage is whether the working sets could be read: an absent figure in a
	// container is "not measured", and this says why.
	Usage Usage `json:"usage"`
}

// The states of Usage. "read" with a container's WorkingSet null is a
// container metrics-server has no figure for; every other state means no
// container has one, and says why - an unread figure is never drawn as none.
const (
	usageRead        = "read"
	usageAbsent      = string(extension.MetricsAbsent)      // the cluster has no metrics-server
	usageRefused     = string(extension.MetricsRefused)     // the API server refused the console
	usageUnreachable = string(extension.MetricsUnreachable) // the API server did not answer
	usageForbidden   = "forbidden"                          // the person may not list pods there
	usageFailed      = "failed"                             // any other failure
)

// Usage is how the working sets were read, from metrics-server.
type Usage struct {
	State string `json:"state"`
	// Detail is one line on why nothing was read.
	Detail string `json:"detail,omitempty"`
	// Complete is false when the figures were narrowed to the namespaces the
	// person may list pods in; Note says so.
	Complete bool   `json:"complete"`
	Note     string `json:"note,omitempty"`
}

// usageOf is the Usage of a ContainerUsage answer.
func usageOf(live extension.LiveUsage, err error) Usage {
	if err == nil {
		return Usage{State: usageRead, Complete: live.Complete, Note: live.Note}
	}
	var missing *extension.MetricsUnavailable
	if errors.As(err, &missing) {
		return Usage{State: string(missing.Reason), Detail: textutil.FirstLineN(missing.Detail, 300)}
	}
	if refused, ok := extension.RefusedRead(err, "You may not read the pods' usage here."); ok {
		return Usage{State: usageForbidden, Detail: refused.Message}
	}
	return Usage{State: usageFailed, Detail: textutil.FirstLineN(err.Error(), 300)}
}

// measure puts each Java container's working set from live into pods.
func measure(pods []Pod, live map[string]extension.Resources) {
	for i := range pods {
		p := &pods[i]
		for j := range p.Containers {
			c := &p.Containers[j]
			if u, ok := live[extension.ContainerKey(p.Namespace, p.Name, c.Name)]; ok && u.MemoryBytes >= 0 {
				c.WorkingSet = &Memory{Bytes: u.MemoryBytes, Text: textutil.Bytes(u.MemoryBytes)}
			}
		}
	}
}

// podAnswer is `GET /pod/{namespace}/{name}`: every container of one pod.
type podAnswer struct {
	Cluster string `json:"cluster"`
	Pod
}

// The caps that keep one answer's memory bounded, whatever the cluster's
// size. There is no cache here: every answer is read for its request and
// dropped with it, so these two are the whole bound.
const (
	// MaxPodsRead is the most pods one read asks the cluster for
	// (ListQuery.Limit). A larger cluster's read is cut by the Host, which
	// then answers Complete false with its own note.
	MaxPodsRead = 20000
	// MaxJavaPods is the most Java pods one inventory answers with, the worst
	// first: past it the answer is Complete false and its Note says how many
	// were found.
	MaxJavaPods = 2000
)

// cut keeps the max worst pods of pods (worst risk first, then namespace and
// name), in namespace and name order, and says whether any was dropped.
func cut(pods []Pod, max int) ([]Pod, bool) {
	if len(pods) <= max {
		return pods, false
	}
	worst := func(p Pod) int {
		w := 0
		for _, c := range p.Containers {
			if c.Risk != nil && severity(c.Risk.Level) > w {
				w = severity(c.Risk.Level)
			}
		}
		return w
	}
	sort.SliceStable(pods, func(i, j int) bool { return worst(pods[i]) > worst(pods[j]) })
	kept := append([]Pod(nil), pods[:max]...)
	sort.Slice(kept, func(i, j int) bool {
		if kept[i].Namespace != kept[j].Namespace {
			return kept[i].Namespace < kept[j].Namespace
		}
		return kept[i].Name < kept[j].Name
	})
	return kept, true
}

// read is c's reading. Nothing is run: it is the spec alone.
func read(c corev1.Container) Container {
	out := Container{
		Name: c.Name, Image: c.Image, Hints: hints(c), Notes: []string{},
		Request: memoryOf(c.Resources.Requests), Limit: memoryOf(c.Resources.Limits),
	}
	if out.Hints == nil {
		out.Hints = []Hint{}
	}
	out.Java = len(out.Hints) > 0
	if !out.Java {
		return out
	}
	o := readOptions(c)
	heap, risk := estimate(o, out.Limit)
	out.Heap, out.Risk = &heap, &risk
	if o.notes != nil {
		out.Notes = o.notes
	}
	return out
}

// readPod is every container of p, in the spec's order.
func readPod(p *corev1.Pod) Pod {
	out := Pod{Namespace: p.Namespace, Name: p.Name, Phase: string(p.Status.Phase), Containers: []Container{},
		Owner: ownerOf(p)}
	for _, c := range p.Spec.Containers {
		row := read(c)
		out.Java = out.Java || row.Java
		out.Containers = append(out.Containers, row)
	}
	return out
}

// finished is a pod that runs nothing any more: a Job's completed pod.
func finished(p *corev1.Pod) bool {
	return p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed
}

// scan is the probably-Java pods among items that still run, each with its
// Java containers only, how many pods it read, and how many could not be
// decoded.
func scan(items []unstructured.Unstructured) (pods []Pod, read, bad int) {
	for i := range items {
		var p corev1.Pod
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(items[i].Object, &p); err != nil {
			bad++
			continue
		}
		if finished(&p) {
			continue
		}
		read++
		row := readPod(&p)
		if !row.Java {
			continue
		}
		java := row.Containers[:0]
		for _, c := range row.Containers {
			if c.Java {
				java = append(java, c)
			}
		}
		row.Containers = java
		pods = append(pods, row)
	}
	sort.Slice(pods, func(i, j int) bool {
		if pods[i].Namespace != pods[j].Namespace {
			return pods[i].Namespace < pods[j].Namespace
		}
		return pods[i].Name < pods[j].Name
	})
	return pods, read, bad
}

func (JVM) inventory(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster := r.PathValue("cluster")
		ns := strings.TrimSpace(r.URL.Query().Get("ns"))
		if ns != "" && len(validation.IsDNS1123Label(ns)) > 0 {
			writeProblem(w, http.StatusBadRequest, CodeBadRequest, quote(ns)+" is not a namespace name.", nil)
			return
		}
		// The working sets are asked beside the list, so the page waits for
		// the slower of the two reads and not for their sum. Both are
		// reviewed for the person; neither is the console's own read.
		// Through safego, so a panic in the read is this request's missing
		// working sets and not the end of the process.
		var m struct {
			live extension.LiveUsage
			err  error
		}
		usage := make(chan struct{})
		safego.Go("jvm: the working sets", func() error {
			m.live, m.err = h.ContainerUsage(r, cluster, ns)
			return nil
		}, func(crash error) {
			if crash != nil {
				m.live, m.err = extension.LiveUsage{}, crash
			}
			close(usage)
		})
		page, err := h.List(r, cluster, extension.ListQuery{GVR: podsGVR, Namespace: ns, Limit: MaxPodsRead})
		// Waited for whatever happened to the list: the goroutine above
		// never outlives its request.
		<-usage
		if err != nil {
			writeReadFailure(w, cluster, err)
			return
		}
		if r.Context().Err() != nil {
			// The page went away while the reads ran: nobody reads an answer.
			return
		}
		pods, n, bad := scan(page.Items)
		page.Items = nil // the spec's reading is all that is kept
		if pods == nil {
			pods = []Pod{}
		}
		found := len(pods)
		complete, note := page.Complete, page.Note
		if kept, dropped := cut(pods, MaxJavaPods); dropped {
			pods, complete = kept, false
			note = strings.TrimSpace(note + " " + strconv.Itoa(found) + " Java pods were found; the " +
				strconv.Itoa(MaxJavaPods) + " with the worst risk are listed. Narrow the page to a namespace to see the rest.")
		}
		if m.err == nil {
			measure(pods, m.live.Containers)
		}
		writeJSON(w, http.StatusOK, inventoryAnswer{Cluster: cluster, Namespace: ns, Complete: complete,
			Note: note, Read: n, Found: found, Pods: pods, Unreadable: bad, Usage: usageOf(m.live, m.err)})
	})
}

func (JVM) pod(h extension.Host) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cluster, namespace, name := r.PathValue("cluster"), r.PathValue("namespace"), r.PathValue("name")
		p, ok := lookupPod(w, r, h, cluster, namespace, name)
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, podAnswer{Cluster: cluster, Pod: readPod(p)})
	})
}

// lookupPod is the pod alone, by name, through the read reviewed for the
// person: never the console's own client. It answers the failure itself and
// says false when there is no pod to go on with.
func lookupPod(w http.ResponseWriter, r *http.Request, h extension.Host, cluster, namespace, name string) (*corev1.Pod, bool) {
	if len(validation.IsDNS1123Label(namespace)) > 0 {
		writeProblem(w, http.StatusBadRequest, CodeBadRequest, quote(namespace)+" is not a namespace name.", nil)
		return nil, false
	}
	if len(validation.IsDNS1123Subdomain(name)) > 0 {
		writeProblem(w, http.StatusBadRequest, CodeBadRequest, quote(name)+" is not a pod name.", nil)
		return nil, false
	}
	page, err := h.List(r, cluster, extension.ListQuery{GVR: podsGVR, Namespace: namespace,
		FieldSelector: "metadata.name=" + name})
	if err != nil {
		writeReadFailure(w, cluster, err)
		return nil, false
	}
	for i := range page.Items {
		item := &page.Items[i]
		if item.GetName() != name || item.GetNamespace() != namespace {
			continue
		}
		var p corev1.Pod
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(item.Object, &p); err != nil {
			writeProblem(w, http.StatusBadGateway, CodeUnreadablePod,
				"The cluster returned the pod, and its spec could not be read.", err)
			return nil, false
		}
		return &p, true
	}
	writeProblem(w, http.StatusNotFound, CodeUnknownPod,
		"There is no pod "+quote(name)+" in namespace "+quote(namespace)+": it may have been deleted or replaced.", nil)
	return nil, false
}

// --- failures ----------------------------------------------------------------

// problem is the failure body: Code is what the page branches on, Message what
// it prints.
type problem struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
}

// The codes these routes answer with. Protocol: the page names them too.
const (
	CodeUnknownCluster = "unknown_cluster"
	CodeUnknownPod     = "unknown_pod"
	CodeUnreadablePod  = "unreadable_pod"
	CodeListFailed     = "list_failed"
	CodeBadRequest     = "bad_request"
	// A read refused for the person: 403, and 503 when it could not be asked.
	CodeListForbidden  = extension.CodeListForbidden
	CodeListUnreviewed = extension.CodeListUnreviewed
)

// writeReadFailure answers a pod list that did not give an answer: 404 for a
// cluster that is not in the kubeconfig, 403 or 503 for a read refused for the
// person, 502 for one that failed. Never an empty answer.
func writeReadFailure(w http.ResponseWriter, cluster string, err error) {
	switch {
	case errors.Is(err, extension.ErrUnknownCluster):
		writeProblem(w, http.StatusNotFound, CodeUnknownCluster, extension.UnknownClusterText(cluster), err)
	case errors.Is(err, context.Canceled):
		// The page went away, or a newer request replaced this one.
	default:
		if refused, ok := extension.RefusedRead(err,
			"The cluster says you may not list pods here, so which of them run Java is not shown."); ok {
			writeProblem(w, refused.Status, refused.Code, refused.Message, err)
			return
		}
		writeProblem(w, http.StatusBadGateway, CodeListFailed,
			"The pods could not be read, so which of them run Java is unknown.", err)
	}
}

func writeProblem(w http.ResponseWriter, status int, code, message string, err error) {
	body := problem{Code: code, Message: message}
	if err != nil {
		body.Detail = textutil.FirstLineN(err.Error(), 300)
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

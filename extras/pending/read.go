package pending

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/thonicdev/kubetower/extension"
	"github.com/thonicdev/kubetower/textutil"
)

var (
	podsGVR       = schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	nodesGVR      = schema.GroupVersionResource{Version: "v1", Resource: "nodes"}
	eventsGVR     = schema.GroupVersionResource{Version: "v1", Resource: "events"}
	quotasGVR     = schema.GroupVersionResource{Version: "v1", Resource: "resourcequotas"}
	claimsGVR     = schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}
	volumesGVR    = schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumes"}
	namespacesGVR = schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}
	classesGVR    = schema.GroupVersionResource{Group: "storage.k8s.io", Version: "v1", Resource: "storageclasses"}
)

// The ceilings on what one request reads, so a large cluster costs a bounded
// amount: past them the answer says it is incomplete.
const (
	// MaxPending is the most pending pods one answer reads.
	MaxPending = 2000
	// MaxPlaced is the most placed pods counted against the nodes.
	MaxPlaced = 20000
	// MaxNodes is the most nodes read.
	MaxNodes = 5000
	// MaxFailures is the most FailedCreate events read.
	MaxFailures = 500
	// MaxEvents is the most events one pod's timeline holds.
	MaxEvents = 200
	// MaxMatrix is the most nodes one pod's matrix carries, closest first.
	MaxMatrix = 200
	// MaxRows is the most pods the page's list carries; the counts in the
	// tiles are over every pod read.
	MaxRows = 2000
)

// Section is a part of the answer that was read, refused or failed apart from
// the rest: a person who may list pods but not nodes still gets the
// scheduler's reasons, with the node half saying why it is missing.
type Section struct {
	// State is "read", "denied" (the cluster refused this person),
	// "unreviewable", or "failed".
	State string `json:"state"`
	// Complete is false when the read was narrowed or cut short; Note says why.
	Complete bool   `json:"complete"`
	Note     string `json:"note,omitempty"`
}

func sectionOf(page extension.Page, err error) Section {
	switch {
	case err == nil:
		return Section{State: "read", Complete: page.Complete, Note: page.Note}
	case errors.Is(err, extension.ErrDenied):
		return Section{State: "denied", Note: "The cluster says you may not make this read."}
	case errors.Is(err, extension.ErrUnreviewable):
		return Section{State: "unreviewable", Note: extension.UnreviewedText}
	}
	return Section{State: "failed", Note: firstLine(err.Error())}
}

func firstLine(s string) string { return textutil.FirstLineN(s, 300) }

// ends is an error that answers the whole request rather than one section:
// the cluster is unknown, or the request went away.
func ends(err error) bool {
	return errors.Is(err, extension.ErrUnknownCluster) || errors.Is(err, errCanceled) || errors.Is(err, context.Canceled)
}

var errCanceled = errors.New("the request was canceled")

// fleet is the nodes, with the placed pods counted against them.
type fleet struct {
	nodes []*Node
	// place is the placed pods, as pod affinity and topology spread read them.
	place   *Placement
	byName  map[string]*Node
	section Section
	// placed is the section of the placed pods' read: narrowed, the free
	// capacity it leaves is overstated, and the note says so.
	placed Section
}

// readFleet reads the nodes and the pods placed on them. keep says some
// pending pod's own affinity or spread rules need every placed pod's labels,
// which are then kept (a map per pod); without it only the pods that declare
// required anti-affinity are, as they can exclude any pod.
func readFleet(r *http.Request, h extension.Host, cluster string, keep bool) (*fleet, error) {
	f := &fleet{byName: map[string]*Node{}, place: &Placement{Kept: keep}}
	page, err := h.List(r, cluster, extension.ListQuery{GVR: nodesGVR, Limit: MaxNodes})
	if err != nil && ends(err) {
		return nil, err
	}
	if r.Context().Err() != nil {
		return nil, errCanceled
	}
	f.section = sectionOf(page, err)
	if err != nil {
		// No nodes, so no placed pods to count against them: the same refusal.
		f.placed = f.section
		return f, nil
	}
	for i := range page.Items {
		var n corev1.Node
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(page.Items[i].Object, &n); err != nil {
			continue
		}
		node := nodeOf(&n)
		node.Index = len(f.nodes)
		f.nodes = append(f.nodes, node)
		f.byName[node.Name] = node
	}
	page.Items = nil
	placed, err := h.List(r, cluster, extension.ListQuery{
		GVR:           podsGVR,
		FieldSelector: "spec.nodeName!=,status.phase!=Succeeded,status.phase!=Failed",
		Limit:         MaxPlaced,
	})
	if err != nil && ends(err) {
		return nil, err
	}
	f.placed = sectionOf(placed, err)
	f.place.Whole = err == nil && placed.Complete
	if err == nil && !placed.Complete {
		f.placed.Note = "Requests on the nodes are counted only from the pods you may read, so the free capacity shown may be more than there is. " + placed.Note
	}
	for i := range placed.Items {
		countPlaced(placed.Items[i].Object, f.byName, f.place)
		placed.Items[i].Object = nil // let the read go as it is counted
	}
	return f, nil
}

// countPlaced adds one placed pod to its node, reading the unstructured
// object directly: converting several thousand pods to their typed form only
// to sum their requests is most of a large read's cost.
func countPlaced(obj map[string]any, nodes map[string]*Node, place *Placement) {
	spec, _ := obj["spec"].(map[string]any)
	if spec == nil {
		return
	}
	nodeName, _ := spec["nodeName"].(string)
	n := nodes[nodeName]
	if n == nil {
		return
	}
	notePlaced(obj, spec, nodeName, place)
	var ps corev1.PodSpec
	for _, field := range []string{"containers", "initContainers"} {
		list, _ := spec[field].([]any)
		for _, item := range list {
			c, _ := item.(map[string]any)
			if c == nil {
				continue
			}
			var out corev1.Container
			if res, ok := c["resources"].(map[string]any); ok {
				out.Resources.Requests = quantityList(res["requests"])
				out.Resources.Limits = quantityList(res["limits"])
			}
			if policy, ok := c["restartPolicy"].(string); ok {
				p := corev1.ContainerRestartPolicy(policy)
				out.RestartPolicy = &p
			}
			if ports, ok := c["ports"].([]any); ok {
				for _, p := range ports {
					pm, _ := p.(map[string]any)
					hp, _ := pm["hostPort"].(int64)
					if hp == 0 {
						if f, ok := pm["hostPort"].(float64); ok {
							hp = int64(f)
						}
					}
					if hp > 0 {
						proto, _ := pm["protocol"].(string)
						ip, _ := pm["hostIP"].(string)
						n.Ports[portKey(corev1.ContainerPort{HostPort: int32(hp), Protocol: corev1.Protocol(proto), HostIP: ip})] = true
					}
				}
			}
			if field == "containers" {
				ps.Containers = append(ps.Containers, out)
			} else {
				ps.InitContainers = append(ps.InitContainers, out)
			}
		}
	}
	ps.Overhead = quantityList(spec["overhead"])
	n.Requested.add(PodRequest(ps))
	n.Pods++
}

// notePlaced keeps what placement rules read of one placed pod: its labels,
// when every pod is kept, and its required anti-affinity, always.
func notePlaced(obj, spec map[string]any, nodeName string, place *Placement) {
	required := nestedList(spec, "affinity", "podAntiAffinity", "requiredDuringSchedulingIgnoredDuringExecution")
	if !place.Kept && len(required) == 0 {
		return
	}
	meta, _ := obj["metadata"].(map[string]any)
	p := &placedPod{Node: nodeName}
	p.Namespace, _ = meta["namespace"].(string)
	p.Name, _ = meta["name"].(string)
	if _, ok := meta["deletionTimestamp"]; ok {
		p.Terminating = true
	}
	if raw, ok := meta["labels"].(map[string]any); ok {
		p.Labels = make(map[string]string, len(raw))
		for k, v := range raw {
			if s, ok := v.(string); ok {
				p.Labels[k] = s
			}
		}
	}
	for _, item := range required {
		var term corev1.PodAffinityTerm
		m, _ := item.(map[string]any)
		if runtime.DefaultUnstructuredConverter.FromUnstructured(m, &term) != nil {
			p.BadAnti = true
			continue
		}
		t, err := newTerm(term, p.Namespace, p.Labels)
		if err != nil {
			p.BadAnti = true
			continue
		}
		p.Anti = append(p.Anti, t)
	}
	if place.Kept {
		place.Pods = append(place.Pods, p)
	}
	if len(required) > 0 {
		place.Anti = append(place.Anti, p)
	}
}

// nestedList is the list at a path of an unstructured object, nil where any
// step is absent.
func nestedList(obj map[string]any, path ...string) []any {
	cur := obj
	for i, key := range path {
		next, ok := cur[key]
		if !ok {
			return nil
		}
		if i == len(path)-1 {
			list, _ := next.([]any)
			return list
		}
		cur, _ = next.(map[string]any)
		if cur == nil {
			return nil
		}
	}
	return nil
}

func quantityList(v any) corev1.ResourceList {
	m, _ := v.(map[string]any)
	if len(m) == 0 {
		return nil
	}
	out := corev1.ResourceList{}
	for k, raw := range m {
		var s string
		switch x := raw.(type) {
		case string:
			s = x
		case int64:
			s = fmt.Sprint(x)
		case float64:
			s = fmt.Sprint(x)
		default:
			continue
		}
		if q, err := resource.ParseQuantity(s); err == nil {
			out[corev1.ResourceName(k)] = q
		}
	}
	return out
}

// Stage is where a pending pod is.
type Stage string

const (
	StageUnschedulable Stage = "unschedulable"
	StageGated         Stage = "gated"
	StageUnseen        Stage = "unseen"
	StageStarting      Stage = "starting"
)

// unseenAfter is how long a pod may wait with no word from any scheduler
// before the page says no scheduler is looking at it.
const unseenAfter = 2 * time.Minute

// Row is one pending pod, as the list draws it.
type Row struct {
	Namespace string    `json:"namespace"`
	Name      string    `json:"name"`
	Created   time.Time `json:"created"`
	Stage     Stage     `json:"stage"`
	// Main is the category the tiles file it under; Categories every one it
	// is under.
	Main       Category   `json:"main"`
	Categories []Category `json:"categories"`
	// Message is the scheduler's or the kubelet's own sentence.
	Message string  `json:"message"`
	Verdict Verdict `json:"verdict"`
	// Since is when the pod entered its stage, where the condition says.
	Since *time.Time `json:"since,omitempty"`
	// Closest is the node that fails the fewest computed predicates, and how
	// many it fails; absent where the nodes could not be read or the pod is
	// not waiting for a node.
	Closest *Closest `json:"closest,omitempty"`
	Owner   *Owner   `json:"owner,omitempty"`
	// Preemption is the scheduler's word on evicting lower-priority pods, the
	// node it nominated and the pod's priority: absent for a pod that is not
	// waiting for a node.
	Preemption *Preemption `json:"preemption,omitempty"`
	// Request is what the scheduler reserves for it, CPU in millicores and
	// bytes for memory.
	Request Amounts `json:"request"`
	// Node is where a starting pod was placed.
	Node      string `json:"node,omitempty"`
	Scheduler string `json:"scheduler,omitempty"`
}

type Closest struct {
	Node    string `json:"node"`
	Failing int    `json:"failing"`
}

type Owner struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// classify reads one pending pod.
func classify(pod *corev1.Pod, now time.Time) Row {
	row := Row{
		Namespace: pod.Namespace,
		Name:      pod.Name,
		Created:   pod.CreationTimestamp.Time,
		Request:   PodRequest(pod.Spec),
		Node:      pod.Spec.NodeName,
		Scheduler: pod.Spec.SchedulerName,
	}
	for _, o := range pod.OwnerReferences {
		if o.Controller != nil && *o.Controller {
			row.Owner = &Owner{Kind: o.Kind, Name: o.Name}
		}
	}
	var cond *corev1.PodCondition
	for i := range pod.Status.Conditions {
		if pod.Status.Conditions[i].Type == corev1.PodScheduled {
			cond = &pod.Status.Conditions[i]
		}
	}
	switch {
	case pod.Spec.NodeName != "":
		row.Stage, row.Main = StageStarting, CatStarting
		row.Message = startingMessage(pod)
		row.Verdict = Verdict{Total: -1, Reasons: []Reason{{Text: row.Message, Nodes: -1, Category: CatStarting}}}
	case len(pod.Spec.SchedulingGates) > 0 || (cond != nil && cond.Reason == corev1.PodReasonSchedulingGated):
		row.Stage, row.Main = StageGated, CatGated
		var gates []string
		for _, g := range pod.Spec.SchedulingGates {
			gates = append(gates, g.Name)
		}
		row.Message = "Held back by its scheduling gates: " + strings.Join(gates, ", ") + ". The controller that set them removes them when it is ready."
		row.Verdict = Verdict{Total: -1, Reasons: []Reason{{Text: row.Message, Nodes: -1, Category: CatGated}}}
	case cond != nil && cond.Status == corev1.ConditionFalse && cond.Message != "":
		row.Stage = StageUnschedulable
		row.Message = cond.Message
		row.Verdict = ParseMessage(cond.Message)
		row.Main = row.Verdict.Main()
	default:
		row.Stage = StageUnseen
		name := pod.Spec.SchedulerName
		if name == "" {
			name = corev1.DefaultSchedulerName
		}
		if now.Sub(pod.CreationTimestamp.Time) >= unseenAfter {
			row.Main = CatNoScheduler
			row.Message = fmt.Sprintf("No scheduler has answered for this pod in %s. It asks for the scheduler %q: is it running?", now.Sub(pod.CreationTimestamp.Time).Round(time.Second), name)
		} else {
			row.Main = CatOther
			row.Message = "Just created: the scheduler " + name + " has not answered yet."
		}
		row.Verdict = Verdict{Total: -1, Reasons: []Reason{{Text: row.Message, Nodes: -1, Category: row.Main}}}
	}
	if cond != nil && !cond.LastTransitionTime.IsZero() {
		t := cond.LastTransitionTime.Time
		row.Since = &t
	}
	row.Preemption = preemptionOf(pod, row.Verdict, row.Stage)
	row.Categories = row.Verdict.Set()
	if len(row.Categories) == 0 {
		row.Categories = []Category{row.Main}
	}
	return row
}

// startingMessage is why a placed pod has not started: the first container
// waiting, init containers first, as the kubelet says it.
func startingMessage(pod *corev1.Pod) string {
	for _, list := range [][]corev1.ContainerStatus{pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses} {
		for _, s := range list {
			if w := s.State.Waiting; w != nil && w.Reason != "" {
				msg := s.Name + ": " + w.Reason
				if w.Message != "" {
					msg += ": " + firstLine(w.Message)
				}
				return msg
			}
		}
	}
	return "Placed on " + pod.Spec.NodeName + "; its containers have not been created yet."
}

// pendingPod is a pending pod read, typed.
type pendingPod struct {
	pod *corev1.Pod
	row Row
}

func readPending(r *http.Request, h extension.Host, cluster, namespace, fieldSelector string, limit int64, now time.Time) ([]pendingPod, extension.Page, error) {
	sel := "status.phase=Pending"
	if fieldSelector != "" {
		sel += "," + fieldSelector
	}
	page, err := h.List(r, cluster, extension.ListQuery{GVR: podsGVR, Namespace: namespace, FieldSelector: sel, Limit: limit})
	if err != nil {
		return nil, page, err
	}
	out := make([]pendingPod, 0, len(page.Items))
	for i := range page.Items {
		pod := &corev1.Pod{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(page.Items[i].Object, pod); err != nil {
			continue
		}
		out = append(out, pendingPod{pod: pod, row: classify(pod, now)})
	}
	page.Items = nil
	return out, page, nil
}

// wantOf is what fitting the pod needs, its bound volumes' node terms
// included where they were read.
func wantOf(p pendingPod, volumeTerms map[string]*corev1.NodeSelector) *Want {
	w := &Want{Spec: p.pod.Spec, Request: p.row.Request, Verdict: p.row.Verdict}
	for _, v := range p.pod.Spec.Volumes {
		if v.PersistentVolumeClaim == nil {
			continue
		}
		if terms, ok := volumeTerms[p.pod.Namespace+"/"+v.PersistentVolumeClaim.ClaimName]; ok && terms != nil {
			w.VolumeZones = append(w.VolumeZones, volumeTerm{Claim: v.PersistentVolumeClaim.ClaimName, Terms: terms})
		}
	}
	return w
}

// fitAll holds the pod to every node and returns them closest first.
func fitAll(w *Want, nodes []*Node) ([]string, []Fit) {
	cols := w.Columns()
	fits := make([]Fit, 0, len(nodes))
	for _, n := range nodes {
		fits = append(fits, w.FitOn(n, cols))
	}
	sort.SliceStable(fits, func(i, j int) bool {
		if fits[i].Failing != fits[j].Failing {
			return fits[i].Failing < fits[j].Failing
		}
		if fits[i].Excluded != fits[j].Excluded {
			return fits[i].Excluded < fits[j].Excluded
		}
		if fits[i].Deficit != fits[j].Deficit {
			return fits[i].Deficit < fits[j].Deficit
		}
		return fits[i].Node < fits[j].Node
	})
	return cols, fits
}

// claims is the persistent volume claims the pending pods mount, and what
// stands in their way.
type claims struct {
	// terms is, per namespace/claim bound to a volume restricted to some
	// nodes, the volume's node terms.
	terms map[string]*corev1.NodeSelector
	// problems is, per namespace/claim, why it is not bound.
	problems map[string]string
	section  Section
}

func readClaims(r *http.Request, h extension.Host, cluster string, pods []pendingPod) (*claims, error) {
	c := &claims{terms: map[string]*corev1.NodeSelector{}, problems: map[string]string{}, section: Section{State: "read", Complete: true}}
	wanted := map[string]bool{}
	namespaces := map[string]bool{}
	for _, p := range pods {
		if p.row.Stage != StageUnschedulable {
			continue
		}
		for _, v := range p.pod.Spec.Volumes {
			if v.PersistentVolumeClaim != nil {
				wanted[p.pod.Namespace+"/"+v.PersistentVolumeClaim.ClaimName] = true
				namespaces[p.pod.Namespace] = true
			}
		}
	}
	if len(wanted) == 0 {
		return c, nil
	}
	ns := ""
	if len(namespaces) == 1 {
		for n := range namespaces {
			ns = n
		}
	}
	page, err := h.List(r, cluster, extension.ListQuery{GVR: claimsGVR, Namespace: ns, Limit: MaxPlaced})
	if err != nil {
		if ends(err) {
			return nil, err
		}
		c.section = sectionOf(page, err)
		return c, nil
	}
	found := map[string]*corev1.PersistentVolumeClaim{}
	for i := range page.Items {
		key := page.Items[i].GetNamespace() + "/" + page.Items[i].GetName()
		if !wanted[key] {
			continue
		}
		pvc := &corev1.PersistentVolumeClaim{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(page.Items[i].Object, pvc); err == nil {
			found[key] = pvc
		}
	}
	bound := map[string]string{} // volume name -> claim key
	var classes []string
	for key := range wanted {
		pvc, ok := found[key]
		switch {
		case !ok && page.Complete:
			c.problems[key] = "the claim does not exist"
		case !ok:
		case pvc.Status.Phase == corev1.ClaimBound && pvc.Spec.VolumeName != "":
			bound[pvc.Spec.VolumeName] = key
		case pvc.Status.Phase == corev1.ClaimPending:
			class := ""
			if pvc.Spec.StorageClassName != nil {
				class = *pvc.Spec.StorageClassName
			}
			c.problems[key] = "not bound yet (storage class " + quoted(class) + ")"
			classes = append(classes, class)
		case pvc.Status.Phase == corev1.ClaimLost:
			c.problems[key] = "its volume is lost"
		}
	}
	if len(classes) > 0 {
		cp, err := h.List(r, cluster, extension.ListQuery{GVR: classesGVR})
		if err != nil && ends(err) {
			return nil, err
		}
		if err == nil {
			have := map[string]*storagev1.StorageClass{}
			for i := range cp.Items {
				sc := &storagev1.StorageClass{}
				if runtime.DefaultUnstructuredConverter.FromUnstructured(cp.Items[i].Object, sc) == nil {
					have[sc.Name] = sc
				}
			}
			for key, why := range c.problems {
				for _, class := range classes {
					if !strings.Contains(why, quoted(class)) {
						continue
					}
					sc, ok := have[class]
					switch {
					case class != "" && !ok:
						c.problems[key] = "not bound: the storage class " + quoted(class) + " does not exist"
					case ok && sc.VolumeBindingMode != nil && *sc.VolumeBindingMode == storagev1.VolumeBindingWaitForFirstConsumer:
						// Bound when the pod is placed: not what holds it.
						delete(c.problems, key)
					}
				}
			}
		}
	}
	if len(bound) > 0 {
		vp, err := h.List(r, cluster, extension.ListQuery{GVR: volumesGVR, Limit: MaxPlaced})
		if err != nil && ends(err) {
			return nil, err
		}
		if err == nil {
			for i := range vp.Items {
				key, ok := bound[vp.Items[i].GetName()]
				if !ok {
					continue
				}
				pv := &corev1.PersistentVolume{}
				if runtime.DefaultUnstructuredConverter.FromUnstructured(vp.Items[i].Object, pv) == nil && pv.Spec.NodeAffinity != nil {
					c.terms[key] = pv.Spec.NodeAffinity.Required
				}
			}
		} else {
			c.section = sectionOf(vp, err)
		}
	}
	return c, nil
}

func quoted(s string) string {
	if s == "" {
		return "(default)"
	}
	return s
}

// Failure is a workload that could not create its pods: the pods never
// exist, so they are never Pending, and this is the only place the page can
// say so. Quota is the usual cause.
type Failure struct {
	Namespace string    `json:"namespace"`
	Kind      string    `json:"kind"`
	Name      string    `json:"name"`
	Category  string    `json:"category"` // quota, policy, other
	Message   string    `json:"message"`
	Count     int32     `json:"count"`
	Last      time.Time `json:"last"`
	Quota     *Quota    `json:"quota,omitempty"`
}

// Quota is one resource of a ResourceQuota: what the refused pod asked for,
// and what the quota holds now.
type Quota struct {
	Name      string `json:"name"`
	Resource  string `json:"resource"`
	Requested string `json:"requested"`
	// Used and Hard are the quota's figures as it stands now, in the
	// resource's base units (millicores for a CPU resource); absent when the
	// quota could not be read.
	Used *int64 `json:"used,omitempty"`
	Hard *int64 `json:"hard,omitempty"`
	// Room says whether the refused request would fit now.
	Room *bool `json:"room,omitempty"`
}

var quotaRE = regexp.MustCompile(`exceeded quota: ([^,]+), requested: ([^=,]+)=([^,]+),`)

func readFailures(r *http.Request, h extension.Host, cluster, namespace string) ([]Failure, Section, error) {
	page, err := h.List(r, cluster, extension.ListQuery{GVR: eventsGVR, Namespace: namespace, FieldSelector: "reason=FailedCreate", Limit: MaxFailures})
	if err != nil {
		if ends(err) {
			return nil, Section{}, err
		}
		return nil, sectionOf(page, err), nil
	}
	section := sectionOf(page, nil)
	latest := map[string]*Failure{}
	for i := range page.Items {
		ev := &corev1.Event{}
		if runtime.DefaultUnstructuredConverter.FromUnstructured(page.Items[i].Object, ev) != nil {
			continue
		}
		o := ev.InvolvedObject
		key := o.Namespace + "/" + o.Kind + "/" + o.Name
		f := Failure{Namespace: o.Namespace, Kind: o.Kind, Name: o.Name, Message: createMessage(ev.Message), Count: eventCount(ev), Last: eventLast(ev)}
		f.Category = createCategory(f.Message)
		if prev, ok := latest[key]; ok {
			f.Count += prev.Count
			if prev.Last.After(f.Last) {
				f.Last, f.Message, f.Category = prev.Last, prev.Message, prev.Category
			}
		}
		latest[key] = &f
	}
	page.Items = nil
	out := make([]Failure, 0, len(latest))
	needQuota := map[string]bool{}
	for _, f := range latest {
		if m := quotaRE.FindStringSubmatch(f.Message); m != nil {
			f.Quota = &Quota{Name: strings.TrimSpace(m[1]), Resource: strings.TrimSpace(m[2]), Requested: strings.TrimSpace(m[3])}
			needQuota[f.Namespace] = true
		}
		out = append(out, *f)
	}
	if len(needQuota) > 0 {
		qp, err := h.List(r, cluster, extension.ListQuery{GVR: quotasGVR, Namespace: namespace})
		if err != nil && ends(err) {
			return nil, Section{}, err
		}
		if err == nil {
			quotas := map[string]*corev1.ResourceQuota{}
			for i := range qp.Items {
				q := &corev1.ResourceQuota{}
				if runtime.DefaultUnstructuredConverter.FromUnstructured(qp.Items[i].Object, q) == nil {
					quotas[q.Namespace+"/"+q.Name] = q
				}
			}
			for i := range out {
				fillQuota(&out[i], quotas)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Last.Equal(out[j].Last) {
			return out[i].Last.After(out[j].Last)
		}
		return out[i].Namespace+out[i].Name < out[j].Namespace+out[j].Name
	})
	return out, section, nil
}

func fillQuota(f *Failure, quotas map[string]*corev1.ResourceQuota) {
	if f.Quota == nil {
		return
	}
	q, ok := quotas[f.Namespace+"/"+f.Quota.Name]
	if !ok {
		return
	}
	name := corev1.ResourceName(f.Quota.Resource)
	hard, hok := q.Status.Hard[name]
	used, uok := q.Status.Used[name]
	if !hok || !uok {
		return
	}
	unit := baseName(f.Quota.Resource)
	h, u := amount(unit, hard), amount(unit, used)
	f.Quota.Hard, f.Quota.Used = &h, &u
	if req, err := resource.ParseQuantity(f.Quota.Requested); err == nil {
		room := u+amount(unit, req) <= h
		f.Quota.Room = &room
	}
}

// baseName is the resource a quota line measures: requests.cpu and
// limits.cpu are CPU.
func baseName(quotaResource string) string {
	if i := strings.LastIndexByte(quotaResource, '.'); i >= 0 {
		switch quotaResource[i+1:] {
		case "cpu", "memory":
			return quotaResource[i+1:]
		}
	}
	return quotaResource
}

func createMessage(msg string) string {
	msg = strings.TrimPrefix(msg, "(combined from similar events): ")
	msg = strings.TrimPrefix(msg, "Error creating: ")
	return firstLine(msg)
}

func createCategory(msg string) string {
	m := strings.ToLower(msg)
	switch {
	case strings.Contains(m, "exceeded quota"):
		return "quota"
	case strings.Contains(m, "admission webhook"), strings.Contains(m, "violates podsecurity"), strings.Contains(m, "denied"), strings.Contains(m, "limitrange"), strings.Contains(m, "maximum"), strings.Contains(m, "minimum"):
		return "policy"
	}
	return "other"
}

func eventCount(ev *corev1.Event) int32 {
	switch {
	case ev.Series != nil && ev.Series.Count > 0:
		return ev.Series.Count
	case ev.Count > 0:
		return ev.Count
	}
	return 1
}

// eventLast is when an event was last seen. A recent event written through
// events.k8s.io has no first or last timestamp, only an eventTime and its
// series' last observation (measured on v1.34).
func eventLast(ev *corev1.Event) time.Time {
	switch {
	case ev.Series != nil && !ev.Series.LastObservedTime.IsZero():
		return ev.Series.LastObservedTime.Time
	case !ev.LastTimestamp.IsZero():
		return ev.LastTimestamp.Time
	case !ev.EventTime.IsZero():
		return ev.EventTime.Time
	}
	return ev.CreationTimestamp.Time
}

func eventFirst(ev *corev1.Event) time.Time {
	switch {
	case !ev.FirstTimestamp.IsZero():
		return ev.FirstTimestamp.Time
	case !ev.EventTime.IsZero():
		return ev.EventTime.Time
	}
	return ev.CreationTimestamp.Time
}
